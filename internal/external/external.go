package external

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dappermint/ratatouille/internal/exitcode"
	"github.com/dappermint/ratatouille/internal/plist"
	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

const (
	defaultVolumesRoot = "/Volumes"
	inspectTimeout     = 5 * time.Second
	maxMetadataItems   = 100000
	maxDepth           = 64
)

type Identity struct {
	Path       string `json:"path"`
	Device     string `json:"device"`
	VolumeUUID string `json:"volume_uuid,omitempty"`
}

type Item struct {
	Path     string    `json:"path"`
	Kind     string    `json:"kind"`
	Bytes    int64     `json:"bytes"`
	Device   uint64    `json:"-"`
	Inode    uint64    `json:"-"`
	Modified time.Time `json:"-"`
}

type Plan struct {
	Mount Identity `json:"mount"`
	Items []Item   `json:"items"`
}

func (p Plan) Bytes() int64 {
	var total int64
	for _, item := range p.Items {
		total += item.Bytes
	}
	return total
}

type Inspector func(context.Context, string) (Identity, error)

type Options struct {
	VolumesRoot string
	Inspect     Inspector
}

func Find(ctx context.Context, path string, options Options) (Plan, error) {
	root := options.VolumesRoot
	if root == "" {
		root = defaultVolumesRoot
	}
	inspector := options.Inspect
	if inspector == nil {
		inspector = inspectDisk
	}
	mount, err := validate(ctx, path, root, inspector)
	if err != nil {
		return Plan{}, err
	}
	items, err := findItems(ctx, mount.Path)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Mount: mount, Items: items}, nil
}

func validate(ctx context.Context, path, volumesRoot string, inspector Inspector) (Identity, error) {
	if !filepath.IsAbs(path) {
		return Identity{}, refused("external volume path must be absolute: %s", path)
	}
	cleaned := filepath.Clean(path)
	root, err := filepath.EvalSymlinks(filepath.Clean(volumesRoot))
	if err != nil {
		return Identity{}, refused("cannot resolve the volumes root: %v", err)
	}
	info, err := os.Lstat(cleaned)
	if err != nil {
		return Identity{}, refused("external volume path is unavailable: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Identity{}, refused("external volume path is a symlink: %s", cleaned)
	}
	if !info.IsDir() {
		return Identity{}, refused("external volume path is not a directory: %s", cleaned)
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return Identity{}, refused("cannot resolve external volume path: %v", err)
	}
	if filepath.Dir(resolved) != root {
		return Identity{}, refused("external cleanup only accepts a direct mount under %s", root)
	}
	identity, err := inspector(ctx, resolved)
	if err != nil {
		return Identity{}, refused("cannot verify external volume identity: %v", err)
	}
	if identity.Path == "" {
		identity.Path = resolved
	}
	if identity.Path != resolved || identity.Device == "" {
		return Identity{}, refused("disk identity does not match %s", resolved)
	}
	return identity, nil
}

func inspectDisk(ctx context.Context, path string) (Identity, error) {
	output, err := storage.CaptureCommand(ctx, inspectTimeout, "/usr/sbin/diskutil", "info", "-plist", path)
	if err != nil {
		return Identity{}, err
	}
	dict, err := plist.Parse([]byte(output))
	if err != nil {
		return Identity{}, err
	}
	if internal, ok := dict.Bool("Internal"); !ok || internal {
		return Identity{}, errors.New("the volume is internal or its type is unknown")
	}
	protocol, ok := dict.String("BusProtocol")
	if !ok {
		protocol, _ = dict.String("Protocol")
	}
	switch strings.ToUpper(protocol) {
	case "SMB", "NFS", "AFP", "CIFS", "WEBDAV":
		return Identity{}, fmt.Errorf("network volume protocol %s is not supported", protocol)
	}
	mount, _ := dict.String("MountPoint")
	device, _ := dict.String("DeviceIdentifier")
	uuid, _ := dict.String("VolumeUUID")
	return Identity{Path: mount, Device: device, VolumeUUID: uuid}, nil
}

func findItems(ctx context.Context, root string) ([]Item, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	rootStat, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errors.New("external volume has no filesystem identity")
	}
	rootDevice := storage.DeviceID(rootStat)
	items := make([]Item, 0, 32)

	for _, name := range []string{".TemporaryItems", ".Trashes"} {
		path := filepath.Join(root, name)
		item, exists, itemErr := measureItem(ctx, path, name, rootDevice)
		if itemErr != nil {
			return nil, itemErr
		}
		if exists {
			items = append(items, item)
		}
	}

	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrPermission) {
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, relativeErr := filepath.Rel(root, path)
		if relativeErr != nil {
			return relativeErr
		}
		depth := strings.Count(relative, string(filepath.Separator)) + 1
		if entry.IsDir() {
			if depth > maxDepth || entry.Type()&os.ModeSymlink != 0 {
				return filepath.SkipDir
			}
			switch entry.Name() {
			case ".TemporaryItems", ".Trashes", ".Spotlight-V100", ".fseventsd":
				return filepath.SkipDir
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				return filepath.SkipDir
			}
			if stat, statOK := info.Sys().(*syscall.Stat_t); statOK && storage.DeviceID(stat) != rootDevice {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || (entry.Name() != ".DS_Store" && !strings.HasPrefix(entry.Name(), "._")) {
			return nil
		}
		item, exists, itemErr := measureItem(ctx, path, "finder metadata", rootDevice)
		if itemErr != nil {
			return itemErr
		}
		if exists {
			items = append(items, item)
			if len(items) > maxMetadataItems {
				return errors.New("external metadata scan exceeded its item limit")
			}
		}
		return nil
	})
	return items, err
}

func measureItem(ctx context.Context, path, kind string, rootDevice uint64) (Item, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Item{}, false, nil
	}
	if err != nil {
		return Item{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Item{}, false, nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || storage.DeviceID(stat) != rootDevice {
		return Item{}, false, nil
	}
	usage, err := storage.PathUsage(ctx, path)
	if err != nil {
		return Item{}, false, err
	}
	return Item{Path: path, Kind: kind, Bytes: usage.Bytes, Device: storage.DeviceID(stat), Inode: stat.Ino, Modified: info.ModTime()}, true, nil
}

func Recheck(ctx context.Context, plan Plan, options Options) error {
	root := options.VolumesRoot
	if root == "" {
		root = defaultVolumesRoot
	}
	inspector := options.Inspect
	if inspector == nil {
		inspector = inspectDisk
	}
	current, err := validate(ctx, plan.Mount.Path, root, inspector)
	if err != nil {
		return err
	}
	if current.Device != plan.Mount.Device || current.VolumeUUID != plan.Mount.VolumeUUID {
		return refused("external volume identity changed after preview")
	}
	return nil
}

func ItemUnchanged(item Item) bool {
	info, err := os.Lstat(item.Path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.ModTime() != item.Modified {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && storage.DeviceID(stat) == item.Device && stat.Ino == item.Inode
}

func Remove(ctx context.Context, funnel *safety.Funnel, plan Plan, options Options) ([]safety.Result, error) {
	results := make([]safety.Result, 0, len(plan.Items))
	for _, item := range plan.Items {
		if err := Recheck(ctx, plan, options); err != nil {
			return results, err
		}
		if !ItemUnchanged(item) {
			return results, refused("external cleanup item changed after preview: %s", item.Path)
		}
		result, err := funnel.Remove(ctx, safety.Request{Command: "clean-external", Item: item.Kind, Path: item.Path, Bytes: item.Bytes})
		results = append(results, result)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func refused(format string, arguments ...any) error {
	return exitcode.With(exitcode.Refused, fmt.Errorf("refusing external cleanup: "+format, arguments...))
}

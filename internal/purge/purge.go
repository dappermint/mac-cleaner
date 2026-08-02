// Package purge removes project build artifacts. It only ever targets
// directories whose name is on a fixed list of rebuildable artifact names, and
// it never targets a project or a git worktree itself: whether a checkout is
// disposable is not decidable from age, branch state or a build marker.
package purge

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dappermint/ratatouille/internal/config"
	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

const (
	CommandName    = "purge"
	defaultMinAge  = 7 * 24 * time.Hour
	discoveryDepth = 3
	sizeBudget     = 30 * time.Second
	walkBudget     = 2 * time.Minute
)

// Targets are the directory names that are always rebuildable from the project
// they sit in. Nothing outside this list is ever a candidate.
var Targets = []string{
	".build",
	".gradle",
	".next",
	".nuxt",
	".parcel-cache",
	".pytest_cache",
	".ruff_cache",
	".terraform",
	".tox",
	".turbo",
	".venv",
	"__pycache__",
	"build",
	"dist",
	"node_modules",
	"target",
	"venv",
}

// DefaultRoots are searched when the user has configured nothing. Discovery
// does not walk every dot directory; a container like ~/.codex/worktrees is
// registered by name or not searched at all.
var DefaultRoots = []string{
	"Projects",
	"Developer",
	"GitHub",
	"Work",
	"Code",
	"src",
	"dev",
	"repos",
	".codex/worktrees",
}

type Artifact struct {
	Path             string    `json:"path"`
	Project          string    `json:"project"`
	Kind             string    `json:"kind"`
	Bytes            int64     `json:"bytes"`
	Modified         time.Time `json:"modified"`
	Recent           bool      `json:"recent"`
	Device           uint64    `json:"-"`
	Inode            uint64    `json:"-"`
	ArtifactModified time.Time `json:"-"`
}

// Selected reports whether this artifact is chosen by default. A project
// touched inside the age window is shown but left unselected, because it is
// probably the one being worked on.
func (a Artifact) Selected() bool {
	return !a.Recent
}

type Options struct {
	MinAge time.Duration
	Roots  []string
}

func (o Options) minAge() time.Duration {
	if o.MinAge > 0 {
		return o.MinAge
	}
	return defaultMinAge
}

// Roots resolves where to look. Configured paths replace the defaults rather
// than adding to them, so a user who lists two directories gets two.
func Roots(home string, options Options) []string {
	if len(options.Roots) > 0 {
		return options.Roots
	}
	if configured, err := config.PurgePaths(home); err == nil && len(configured) > 0 {
		return configured
	}
	roots := make([]string, 0, len(DefaultRoots))
	for _, name := range DefaultRoots {
		path := filepath.Join(home, name)
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			roots = append(roots, path)
		}
	}
	return roots
}

// Find walks the roots looking for artifact directories. It stops descending at
// an artifact, so a node_modules inside a node_modules is one row rather than
// hundreds.
func Find(ctx context.Context, home string, options Options) ([]Artifact, []string) {
	ctx, cancel := context.WithTimeout(ctx, walkBudget)
	defer cancel()

	targets := make(map[string]bool, len(Targets))
	for _, name := range Targets {
		targets[name] = true
	}

	var found []Artifact
	var issues []string
	for _, root := range Roots(home, options) {
		rootDepth := len(strings.Split(filepath.Clean(root), string(filepath.Separator)))
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if walkErr != nil || !entry.IsDir() {
				return nil //nolint:nilerr // an unreadable directory is skipped, not fatal
			}
			if targets[entry.Name()] {
				found = append(found, Artifact{
					Path:    path,
					Project: filepath.Dir(path),
					Kind:    entry.Name(),
				})
				// Never descend into an artifact: everything inside it belongs
				// to it, and listing those separately would double count.
				return filepath.SkipDir
			}
			depth := len(strings.Split(filepath.Clean(path), string(filepath.Separator))) - rootDepth
			if depth >= discoveryDepth {
				return filepath.SkipDir
			}
			return nil
		})
		if err != nil && ctx.Err() != nil {
			issues = append(issues, "the scan of "+storage.RelativeHome(home, root)+" ran out of time, results are partial")
		}
	}

	size(ctx, found, options.minAge())
	sort.SliceStable(found, func(a, b int) bool {
		if found[a].Bytes != found[b].Bytes {
			return found[a].Bytes > found[b].Bytes
		}
		return found[a].Path < found[b].Path
	})
	return found, issues
}

func size(ctx context.Context, artifacts []Artifact, minAge time.Duration) {
	now := time.Now()
	workers := min(max(runtime.NumCPU()-1, 2), 8)
	queue := make(chan int)
	var waiter sync.WaitGroup
	for range workers {
		waiter.Add(1)
		go func() {
			defer waiter.Done()
			for index := range queue {
				pathCtx, cancel := context.WithTimeout(ctx, sizeBudget)
				usage, _ := storage.PathUsage(pathCtx, artifacts[index].Path)
				cancel()
				artifacts[index].Bytes = usage.Bytes
				// Age is taken from the project, not the artifact: a build
				// directory's mtime says when it was last built, while the
				// project's says when the work happened.
				if info, err := os.Stat(artifacts[index].Project); err == nil {
					artifacts[index].Modified = info.ModTime()
					artifacts[index].Recent = now.Sub(info.ModTime()) < minAge
				}
				if info, err := os.Lstat(artifacts[index].Path); err == nil {
					artifacts[index].ArtifactModified = info.ModTime()
					if stat, ok := info.Sys().(*syscall.Stat_t); ok {
						artifacts[index].Device = storage.DeviceID(stat)
						artifacts[index].Inode = stat.Ino
					}
				}
			}
		}()
	}
	for index := range artifacts {
		select {
		case queue <- index:
		case <-ctx.Done():
		}
	}
	close(queue)
	waiter.Wait()
}

// Remove deletes the chosen artifacts. Permanent is the default because a
// node_modules sitting in Trash helps nobody, but the caller can ask for Trash.
func Remove(ctx context.Context, funnel *safety.Funnel, artifacts []Artifact, toTrash bool) ([]string, int64, []error) {
	var removed []string
	var reclaimed int64
	var failures []error
	for _, artifact := range artifacts {
		if !Unchanged(artifact) {
			failures = append(failures, fmt.Errorf("%s changed after preview", artifact.Path))
			continue
		}
		request := safety.Request{
			Command: CommandName,
			Item:    artifact.Kind,
			Path:    artifact.Path,
			Bytes:   artifact.Bytes,
		}
		var err error
		if toTrash {
			_, err = funnel.Trash(ctx, request)
		} else {
			_, err = funnel.Remove(ctx, request)
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		removed = append(removed, artifact.Path)
		reclaimed += artifact.Bytes
	}
	return removed, reclaimed, failures
}

func Unchanged(artifact Artifact) bool {
	if filepath.Base(artifact.Path) != artifact.Kind || filepath.Clean(filepath.Dir(artifact.Path)) != filepath.Clean(artifact.Project) || !slices.Contains(Targets, artifact.Kind) {
		return false
	}
	info, err := os.Lstat(artifact.Path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.ModTime() != artifact.ArtifactModified {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || storage.DeviceID(stat) != artifact.Device || stat.Ino != artifact.Inode {
		return false
	}
	projectInfo, err := os.Lstat(artifact.Project)
	return err == nil && projectInfo.IsDir() && projectInfo.Mode()&os.ModeSymlink == 0 && projectInfo.ModTime().Equal(artifact.Modified)
}

func Total(artifacts []Artifact) int64 {
	var total int64
	for _, artifact := range artifacts {
		total += artifact.Bytes
	}
	return total
}

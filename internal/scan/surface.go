package scan

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/dappermint/ratatouille/internal/storage"
)

const roleData = "Data"

const (
	surfaceKeepPerNode  = 14
	surfaceMaxDepth     = 8
	surfaceDisplayNodes = 4000
	surfaceFanOutDepth  = 2
	surfaceKeepFloor    = 4 * 1024 * 1024

	defaultLargeFileLimit = 200
)

func keepFraction(depth int) int64 {
	switch {
	case depth <= 2:
		return 200
	case depth <= 4:
		return 50
	default:
		return 20
	}
}

type NodeKind string

const (
	NodeSurface    NodeKind = "surface"
	NodeContainer  NodeKind = "container"
	NodeVolume     NodeKind = "volume"
	NodeDirectory  NodeKind = "directory"
	NodeRemainder  NodeKind = "remainder"
	NodeUnwalked   NodeKind = "unwalked"
	NodeUnreadable NodeKind = "unreadable"
	NodeForeign    NodeKind = "foreign"
	NodeOverhead   NodeKind = "overhead"
	NodeFree       NodeKind = "free"
)

type SurfaceNode struct {
	Name     string           `json:"name"`
	Path     string           `json:"path,omitempty"`
	Kind     NodeKind         `json:"kind"`
	Category storage.Category `json:"category,omitempty"`
	Detail   string           `json:"detail,omitempty"`
	Bytes    int64            `json:"bytes"`
	Files    int64            `json:"files,omitempty"`
	Entries  int64            `json:"entries,omitempty"`
	Children []*SurfaceNode   `json:"children,omitempty"`
}

// LargeFile is one file worth naming on its own. The walk already stats every
// file, so collecting these costs a comparison rather than a second pass.
type LargeFile struct {
	Path     string    `json:"path"`
	Bytes    int64     `json:"bytes"`
	Modified time.Time `json:"modified,omitempty"`
}

type Fault struct {
	Path     string `json:"path"`
	Reason   string `json:"reason"`
	Hardware bool   `json:"hardware"`
}

type Surface struct {
	Root       *SurfaceNode            `json:"root"`
	Containers []storage.Container     `json:"containers"`
	Mounts     []storage.Mount         `json:"mounts"`
	Devices    []storage.StorageDevice `json:"devices,omitempty"`
	WalkedAt   time.Time               `json:"walked_at"`
	Walked     int64                   `json:"walked_bytes"`
	Claimed    int64                   `json:"claimed_bytes"`
	Files      int64                   `json:"files"`
	Denied     int64                   `json:"denied_entries"`
	Loops      int64                   `json:"directory_loops"`
	Hardware   int64                   `json:"hardware_faults"`
	Dedicated  bool                    `json:"dedicated_data_volume"`
	Rootful    bool                    `json:"rootful"`
	Faults     []Fault                 `json:"faults,omitempty"`
	Elapsed    time.Duration           `json:"elapsed_ns"`
	Issues     []string                `json:"issues,omitempty"`
	LargeFiles []LargeFile             `json:"large_files,omitempty"`

	// Scoped is set when the walk covered one path rather than the whole data
	// volume. A scoped walk has no APFS claim to reconcile against, so Claimed
	// stays zero and the coverage signals do not apply.
	Scoped    bool   `json:"scoped,omitempty"`
	ScopeRoot string `json:"scope_root,omitempty"`
}

func (n *SurfaceNode) Total() int64 {
	if n == nil || n.Bytes < 0 {
		return 0
	}
	return n.Bytes
}

type surfaceWalker struct {
	ctx      context.Context
	device   uint64
	home     string
	files    atomic.Int64
	bytes    atomic.Int64
	denied   atomic.Int64
	loops    atomic.Int64
	hardware atomic.Int64
	mutex    sync.Mutex
	hardlink map[storage.InodeKey]struct{}
	visited  map[storage.InodeKey]struct{}
	faults   []Fault

	cross      bool
	minFile    int64
	fileLimit  int
	fileMutex  sync.Mutex
	largeFiles []LargeFile
}

func newSurfaceWalker(ctx context.Context, device uint64, home string) *surfaceWalker {
	return &surfaceWalker{
		ctx:      ctx,
		device:   device,
		home:     home,
		hardlink: make(map[storage.InodeKey]struct{}),
		visited:  make(map[storage.InodeKey]struct{}),
	}
}

// considerFile keeps the largest files seen so far. The slice is allowed to
// grow to a small multiple of the limit before it is trimmed, so the common
// case is an append under a lock rather than a sort.
func (w *surfaceWalker) considerFile(path string, blocks int64, modified time.Time) {
	if w.minFile <= 0 || blocks < w.minFile {
		return
	}
	w.fileMutex.Lock()
	defer w.fileMutex.Unlock()
	w.largeFiles = append(w.largeFiles, LargeFile{Path: path, Bytes: blocks, Modified: modified})
	if len(w.largeFiles) > w.fileCap()*4 {
		w.trimFiles()
	}
}

func (w *surfaceWalker) fileCap() int {
	if w.fileLimit > 0 {
		return w.fileLimit
	}
	return defaultLargeFileLimit
}

func (w *surfaceWalker) trimFiles() {
	sort.SliceStable(w.largeFiles, func(a, b int) bool {
		if w.largeFiles[a].Bytes != w.largeFiles[b].Bytes {
			return w.largeFiles[a].Bytes > w.largeFiles[b].Bytes
		}
		return w.largeFiles[a].Path < w.largeFiles[b].Path
	})
	if len(w.largeFiles) > w.fileCap() {
		w.largeFiles = w.largeFiles[:w.fileCap()]
	}
}

func (w *surfaceWalker) LargeFiles() []LargeFile {
	w.fileMutex.Lock()
	defer w.fileMutex.Unlock()
	w.trimFiles()
	return append([]LargeFile(nil), w.largeFiles...)
}

func (w *surfaceWalker) Progress() (files, bytes int64) {
	return w.files.Load(), w.bytes.Load()
}

func (w *surfaceWalker) Walk(root string) *SurfaceNode {
	info, err := os.Lstat(root)
	if err != nil {
		node := &SurfaceNode{Name: root, Path: root, Kind: NodeDirectory}
		w.record(root, err, node)
		return node
	}
	own := int64(0)
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		own = stat.Blocks * 512
	}
	return w.walkDirectory(root, own, 0)
}

func (w *surfaceWalker) walkDirectory(path string, own int64, depth int) *SurfaceNode {
	node := &SurfaceNode{
		Name:     filepath.Base(path),
		Path:     path,
		Kind:     NodeDirectory,
		Category: surfaceCategory(path, w.home),
		Bytes:    own,
	}
	if w.ctx.Err() != nil {
		return node
	}

	directory, err := os.Open(path)
	if err != nil {
		w.record(path, err, node)
		return node
	}
	entries, readErr := directory.ReadDir(-1)
	_ = directory.Close()
	if readErr != nil && len(entries) == 0 {
		w.record(path, readErr, node)
		return node
	}

	var children []*SurfaceNode
	var pending []*SurfaceNode
	var wait sync.WaitGroup
	var mutex sync.Mutex
	loose := int64(0)
	files := int64(0)

	for _, entry := range entries {
		if w.ctx.Err() != nil {
			break
		}
		childPath := filepath.Join(path, entry.Name())
		info, infoErr := entry.Info()
		if infoErr != nil {
			w.record(childPath, infoErr, node)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			if !info.IsDir() {
				loose += info.Size()
				files++
			}
			continue
		}
		blocks := stat.Blocks * 512
		if !info.IsDir() {
			if stat.Nlink > 1 && !w.claimInode(stat) {
				continue
			}
			loose += blocks
			files++
			w.considerFile(childPath, blocks, info.ModTime())
			continue
		}
		if !w.cross && storage.DeviceID(stat) != w.device {
			mutex.Lock()
			children = append(children, &SurfaceNode{
				Name:   entry.Name(),
				Path:   childPath,
				Kind:   NodeForeign,
				Detail: "separate volume, counted under its own volume row",
			})
			mutex.Unlock()
			continue
		}
		if !w.enterDirectory(stat) {
			w.note(childPath, "directory loop")
			w.loops.Add(1)
			continue
		}
		if depth < surfaceFanOutDepth {
			wait.Add(1)
			go func(childPath string, blocks int64) {
				defer wait.Done()
				child := w.walkDirectory(childPath, blocks, depth+1)
				mutex.Lock()
				pending = append(pending, child)
				mutex.Unlock()
			}(childPath, blocks)
			continue
		}
		if depth >= surfaceMaxDepth {
			node.Bytes += w.measureQuiet(childPath, blocks)
			continue
		}
		pending = append(pending, w.walkDirectory(childPath, blocks, depth+1))
	}
	wait.Wait()

	node.Bytes += loose
	node.Files = files
	w.files.Add(files)
	w.bytes.Add(loose + own)
	children = append(children, pending...)
	for _, child := range children {
		node.Bytes += child.Total()
		node.Files += child.Files
	}
	node.Children = w.prune(node, children, depth)
	return node
}

func (w *surfaceWalker) measureQuiet(path string, own int64) int64 {
	total := own
	local := own
	directory, err := os.Open(path)
	if err != nil {
		w.record(path, err, nil)
		return total
	}
	entries, readErr := directory.ReadDir(-1)
	_ = directory.Close()
	if readErr != nil && len(entries) == 0 {
		w.record(path, readErr, nil)
		return total
	}
	files := int64(0)
	for _, entry := range entries {
		if w.ctx.Err() != nil {
			break
		}
		childPath := filepath.Join(path, entry.Name())
		info, infoErr := entry.Info()
		if infoErr != nil {
			w.record(childPath, infoErr, nil)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			if !info.IsDir() {
				total += info.Size()
				local += info.Size()
				files++
			}
			continue
		}
		blocks := stat.Blocks * 512
		if !info.IsDir() {
			if stat.Nlink > 1 && !w.claimInode(stat) {
				continue
			}
			total += blocks
			local += blocks
			files++
			w.considerFile(childPath, blocks, info.ModTime())
			continue
		}
		if !w.cross && storage.DeviceID(stat) != w.device {
			continue
		}
		if !w.enterDirectory(stat) {
			w.loops.Add(1)
			continue
		}
		total += w.measureQuiet(childPath, blocks)
	}
	w.files.Add(files)
	w.bytes.Add(local)
	return total
}

func (w *surfaceWalker) prune(node *SurfaceNode, children []*SurfaceNode, depth int) []*SurfaceNode {
	sortNodes(children)
	threshold := node.Bytes / keepFraction(depth)
	if threshold < surfaceKeepFloor {
		threshold = surfaceKeepFloor
	}
	kept := make([]*SurfaceNode, 0, surfaceKeepPerNode+2)
	var foldedEntries int64
	for index, child := range children {
		if child.Kind == NodeForeign {
			kept = append(kept, child)
			continue
		}
		if index < surfaceKeepPerNode && child.Total() >= threshold {
			kept = append(kept, child)
			continue
		}
		foldedEntries++
	}
	return appendRemainder(node, kept, foldedEntries)
}

func appendRemainder(node *SurfaceNode, kept []*SurfaceNode, foldedEntries int64) []*SurfaceNode {
	remainder := node.Bytes - sumChildren(kept)
	if remainder <= 0 {
		return kept
	}
	name := "loose files"
	if foldedEntries > 0 {
		name = "smaller directories and loose files"
	}
	return append(kept, &SurfaceNode{
		Name:    name,
		Kind:    NodeRemainder,
		Bytes:   remainder,
		Entries: foldedEntries,
	})
}

func sortNodes(children []*SurfaceNode) {
	sort.SliceStable(children, func(a, b int) bool {
		if children[a].Total() != children[b].Total() {
			return children[a].Total() > children[b].Total()
		}
		return children[a].Name < children[b].Name
	})
}

func sumChildren(children []*SurfaceNode) int64 {
	var total int64
	for _, child := range children {
		total += child.Total()
	}
	return total
}

func trimTree(root *SurfaceNode, budget int) {
	if root == nil {
		return
	}
	queue := []*SurfaceNode{root}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		if len(node.Children) == 0 {
			continue
		}
		if budget < len(node.Children) {
			foldTail(node, budget)
			budget = 0
			continue
		}
		budget -= len(node.Children)
		queue = append(queue, node.Children...)
	}
}

func foldTail(node *SurfaceNode, keep int) {
	if keep < 0 {
		keep = 0
	}
	if keep >= len(node.Children) {
		return
	}
	if keep == 0 {
		node.Children = nil
		return
	}
	sortNodes(node.Children)
	folded := int64(len(node.Children) - keep)
	node.Children = appendRemainder(node, node.Children[:keep], folded)
	for _, child := range node.Children {
		child.Children = nil
	}
}

func (w *surfaceWalker) claimInode(stat *syscall.Stat_t) bool {
	key := storage.InodeKey{Device: storage.DeviceID(stat), Inode: stat.Ino}
	w.mutex.Lock()
	defer w.mutex.Unlock()
	if _, exists := w.hardlink[key]; exists {
		return false
	}
	w.hardlink[key] = struct{}{}
	return true
}

// enterDirectory is keyed by device and inode together. An inode number is only
// unique within its filesystem, so a walk that crosses volumes would otherwise
// report an unrelated directory on a second volume as a loop.
func (w *surfaceWalker) enterDirectory(stat *syscall.Stat_t) bool {
	key := storage.InodeKey{Device: storage.DeviceID(stat), Inode: stat.Ino}
	w.mutex.Lock()
	defer w.mutex.Unlock()
	if _, exists := w.visited[key]; exists {
		return false
	}
	w.visited[key] = struct{}{}
	return true
}

func (w *surfaceWalker) record(path string, err error, node *SurfaceNode) {
	if err == nil || errors.Is(err, os.ErrNotExist) || errors.Is(err, context.Canceled) {
		return
	}
	if errors.Is(err, fs.ErrPermission) {
		w.denied.Add(1)
		if node != nil {
			node.Entries++
		}
		return
	}
	if hardwareFault(err) {
		w.hardware.Add(1)
		w.appendFault(Fault{Path: path, Reason: storage.CompactError(err), Hardware: true})
		return
	}
	w.note(path, storage.CompactError(err))
}

func (w *surfaceWalker) note(path, reason string) {
	w.appendFault(Fault{Path: path, Reason: reason})
}

func (w *surfaceWalker) appendFault(fault Fault) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	if len(w.faults) >= 64 {
		return
	}
	w.faults = append(w.faults, fault)
}

func hardwareFault(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.EIO || errno == syscall.ENXIO
}

func (s Scanner) buildSurface(ctx context.Context, progress func(files, bytes int64)) Surface {
	surface := Surface{WalkedAt: time.Now(), Rootful: s.Rootful}
	started := time.Now()

	mounts, err := storage.MountedFilesystems()
	if err != nil {
		surface.Issues = append(surface.Issues, "mounted filesystems: "+storage.CompactError(err))
	}
	surface.Mounts = mounts

	containers, err := storage.APFSContainers(ctx, s.CommandTimeout, mounts)
	if err != nil {
		surface.Issues = append(surface.Issues, "apfs inventory: "+storage.CompactError(err))
	}
	surface.Containers = containers

	devices, deviceIssues := storage.StorageDevices(ctx, s.CommandTimeout, containers)
	surface.Devices = devices
	surface.Issues = append(surface.Issues, deviceIssues...)

	dataPath := DataVolumePath(mounts)
	surface.Dedicated = dataPath != "/"
	walkRoot := dataPath
	dataDevice := uint64(0)
	for _, mount := range mounts {
		if mount.Path == dataPath {
			dataDevice = mount.Device
		}
	}
	if dataDevice == 0 {
		if info, statErr := os.Lstat(walkRoot); statErr == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				dataDevice = storage.DeviceID(stat)
			}
		}
	}

	if s.SurfaceRoot != "" {
		walkRoot = filepath.Clean(s.SurfaceRoot)
		surface.Scoped = true
		surface.ScopeRoot = walkRoot
		dataDevice = 0
		if info, statErr := os.Lstat(walkRoot); statErr == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				dataDevice = storage.DeviceID(stat)
			}
		} else {
			surface.Issues = append(surface.Issues, "surface root: "+storage.CompactError(statErr))
		}
	}

	walker := newSurfaceWalker(ctx, dataDevice, s.Home)
	walker.cross = surface.Scoped
	walker.minFile = s.MinFileBytes
	walker.fileLimit = s.LargeFileLimit
	done := make(chan struct{})
	if progress != nil {
		go func() {
			ticker := time.NewTicker(300 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					files, bytes := walker.Progress()
					progress(files, bytes)
				case <-done:
					return
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	tree := walker.Walk(walkRoot)
	close(done)

	surface.Walked = tree.Total()
	surface.Files = walker.files.Load()
	surface.Denied = walker.denied.Load()
	surface.Loops = walker.loops.Load()
	surface.Hardware = walker.hardware.Load()
	surface.Faults = walker.faults
	surface.Elapsed = time.Since(started)
	surface.LargeFiles = walker.LargeFiles()
	if surface.Scoped {
		// A scoped walk has no container to reconcile against, so it reports
		// the tree it walked and nothing it cannot support.
		surface.Root = scopedRoot(tree, walker.denied.Load())
		trimTree(surface.Root, surfaceDisplayNodes)
		return surface
	}
	surface.Root = assembleSurface(containers, dataPath, tree, walker.denied.Load())
	surface.Claimed = claimedBytes(containers, dataPath)
	trimTree(surface.Root, surfaceDisplayNodes)
	return surface
}

// scopedRoot wraps a single-path walk. Unreadable entries still get a row of
// their own, because a scoped walk that silently drops them would under-report
// the same way the whole-volume walk refuses to.
func scopedRoot(tree *SurfaceNode, denied int64) *SurfaceNode {
	if tree == nil {
		return nil
	}
	if denied > 0 {
		tree.Children = append(tree.Children, &SurfaceNode{
			Name:    "unreadable entries",
			Kind:    NodeUnreadable,
			Bytes:   -1,
			Entries: denied,
			Detail:  "permission denied, size unknown; grant Full Disk Access or rerun with sudo --root",
		})
	}
	return tree
}

func DataVolumePath(mounts []storage.Mount) string {
	for _, mount := range mounts {
		if mount.Path == storage.DataVolume {
			return mount.Path
		}
	}
	return "/"
}

func claimedBytes(containers []storage.Container, dataPath string) int64 {
	for _, container := range containers {
		for _, volume := range container.Volumes {
			if volume.MountedAt == dataPath {
				return volume.InUse
			}
		}
	}
	return 0
}

func assembleSurface(containers []storage.Container, dataPath string, tree *SurfaceNode, denied int64) *SurfaceNode {
	root := &SurfaceNode{Name: "all storage", Kind: NodeSurface}
	if len(containers) == 0 {
		root.Children = []*SurfaceNode{tree}
		root.Bytes = tree.Total()
		return root
	}
	sort.SliceStable(containers, func(a, b int) bool { return containers[a].Ceiling > containers[b].Ceiling })
	for _, container := range containers {
		node := &SurfaceNode{
			Name:   "container " + container.Reference,
			Kind:   NodeContainer,
			Bytes:  container.Ceiling,
			Detail: containerDetail(container),
		}
		volumes := append([]storage.Volume(nil), container.Volumes...)
		sort.SliceStable(volumes, func(a, b int) bool { return volumes[a].InUse > volumes[b].InUse })
		for _, volume := range volumes {
			child := &SurfaceNode{
				Name:     volume.Name,
				Path:     volume.MountedAt,
				Kind:     NodeVolume,
				Bytes:    volume.InUse,
				Category: volumeCategory(volume),
				Detail:   volumeDetail(volume),
			}
			if volume.MountedAt == dataPath && tree != nil {
				child.Children = adoptWalkedTree(tree, volume.InUse, denied)
				child.Files = tree.Files
			}
			node.Children = append(node.Children, child)
		}
		if unattributed := container.Unattributed(); unattributed > 0 {
			node.Children = append(node.Children, &SurfaceNode{
				Name:   "snapshots and container metadata",
				Kind:   NodeOverhead,
				Bytes:  unattributed,
				Detail: "in use by the container but not claimed by any volume",
			})
		}
		if container.Free > 0 {
			node.Children = append(node.Children, &SurfaceNode{
				Name:   "free",
				Kind:   NodeFree,
				Bytes:  container.Free,
				Detail: "not allocated to any volume",
			})
		}
		root.Children = append(root.Children, node)
		root.Bytes += container.Ceiling
	}
	return root
}

func adoptWalkedTree(tree *SurfaceNode, claimed, denied int64) []*SurfaceNode {
	children := append([]*SurfaceNode(nil), tree.Children...)
	if gap := claimed - tree.Total(); gap > 0 {
		detail := "space the volume reports but the walk could not attribute"
		if denied > 0 {
			detail = "unreadable trees and apfs overhead the walk could not attribute"
		}
		children = append(children, &SurfaceNode{
			Name:    "unaccounted",
			Kind:    NodeUnwalked,
			Bytes:   gap,
			Entries: denied,
			Detail:  detail,
		})
	}
	if denied > 0 {
		children = append(children, &SurfaceNode{
			Name:    "unreadable entries",
			Kind:    NodeUnreadable,
			Bytes:   -1,
			Entries: denied,
			Detail:  "permission denied, size unknown; grant Full Disk Access or rerun with sudo --root",
		})
	}
	return children
}

func containerDetail(container storage.Container) string {
	parts := make([]string, 0, 2)
	for _, store := range container.Physical {
		parts = append(parts, store.Device)
	}
	if len(parts) == 0 {
		return ""
	}
	return "physical store " + strings.Join(parts, ", ")
}

func volumeDetail(volume storage.Volume) string {
	parts := []string{volume.Device, volume.Role()}
	if volume.MountedAt != "" {
		parts = append(parts, "at "+volume.MountedAt)
	} else {
		parts = append(parts, "not mounted")
	}
	if volume.ReadOnly {
		parts = append(parts, "read-only")
	}
	return strings.Join(parts, " / ")
}

func volumeCategory(volume storage.Volume) storage.Category {
	for _, role := range volume.Roles {
		switch role {
		case "System":
			return storage.CategoryMacOS
		case roleData:
			return storage.CategorySystemData
		}
	}
	if strings.EqualFold(volume.Name, "Nix Store") {
		return storage.CategoryDeveloper
	}
	return storage.CategorySystemData
}

func surfaceCategory(path, home string) storage.Category {
	clean := filepath.Clean(path)
	base := filepath.Base(clean)
	switch base {
	case "Applications":
		return storage.CategoryApplications
	case "Documents", "Desktop", "Downloads":
		return storage.CategoryDocuments
	case "Movies":
		return storage.CategoryTV
	case "Music":
		return storage.CategoryMusic
	case "Books":
		return storage.CategoryBooks
	case "Pictures":
		return storage.CategoryPhotos
	case ".Trash":
		return storage.CategoryTrash
	}
	if home != "" {
		if relative, err := filepath.Rel(home, clean); err == nil && !strings.HasPrefix(relative, "..") {
			return storage.CategoryForUserData(clean)
		}
	}
	if strings.HasPrefix(clean, "/Users/") || strings.Contains(clean, "/Data/Users/") {
		return storage.CategoryOtherUsers
	}
	return storage.CategorySystemData
}

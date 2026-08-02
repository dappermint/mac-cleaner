package scan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSized(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func scopedSurface(t *testing.T, root string, options Options) Surface {
	t.Helper()
	options.Surface = true
	options.SkipItems = true
	options.SurfaceRoot = root
	report := Configure(root, options, nil).Scan(context.Background())
	if report.Surface == nil {
		t.Fatal("no surface was produced")
	}
	return *report.Surface
}

func TestScopedWalkStaysInsideItsRoot(t *testing.T) {
	root := t.TempDir()
	writeSized(t, filepath.Join(root, "inside", "payload"), 64*1024)
	outside := t.TempDir()
	writeSized(t, filepath.Join(outside, "payload"), 512*1024)

	surface := scopedSurface(t, root, Options{})
	if !surface.Scoped {
		t.Error("the surface did not report itself as scoped")
	}
	if surface.ScopeRoot != root {
		t.Errorf("scope root = %q, want %q", surface.ScopeRoot, root)
	}
	if surface.Claimed != 0 {
		t.Errorf("a scoped walk reported %d claimed bytes, but it has no container to reconcile against", surface.Claimed)
	}
	if surface.Root == nil || surface.Root.Path != root {
		t.Fatalf("the root node is not the scope root: %+v", surface.Root)
	}
	if surface.Walked < 64*1024 {
		t.Errorf("walked %d bytes, want at least the payload", surface.Walked)
	}
	if surface.Walked >= 512*1024 {
		t.Errorf("walked %d bytes, which means it left its root", surface.Walked)
	}
}

// Every level still has to sum to its parent when the walk is scoped, which is
// the invariant the whole tool rests on.
func TestScopedWalkStillSumsToItsParent(t *testing.T) {
	root := t.TempDir()
	writeSized(t, filepath.Join(root, "one", "a"), 32*1024)
	writeSized(t, filepath.Join(root, "two", "b"), 48*1024)
	writeSized(t, filepath.Join(root, "loose"), 16*1024)

	surface := scopedSurface(t, root, Options{})
	assertSums(t, surface.Root)
}

func assertSums(t *testing.T, node *SurfaceNode) {
	t.Helper()
	if node == nil || len(node.Children) == 0 {
		return
	}
	var total int64
	for _, child := range node.Children {
		total += child.Total()
		assertSums(t, child)
	}
	if total > node.Total() {
		t.Errorf("%s: children sum to %d, which is more than the parent's %d", node.Name, total, node.Total())
	}
}

func TestLargeFilesAreCollectedAboveTheFloor(t *testing.T) {
	root := t.TempDir()
	writeSized(t, filepath.Join(root, "big.bin"), 512*1024)
	writeSized(t, filepath.Join(root, "nested", "bigger.bin"), 1024*1024)
	writeSized(t, filepath.Join(root, "small.bin"), 4*1024)

	surface := scopedSurface(t, root, Options{MinFileBytes: 256 * 1024, LargeFileLimit: 10})
	if len(surface.LargeFiles) != 2 {
		t.Fatalf("collected %d files, want the two above the floor: %+v", len(surface.LargeFiles), surface.LargeFiles)
	}
	if !strings.HasSuffix(surface.LargeFiles[0].Path, "bigger.bin") {
		t.Errorf("the list is not sorted by size: %+v", surface.LargeFiles)
	}
	for _, file := range surface.LargeFiles {
		if strings.HasSuffix(file.Path, "small.bin") {
			t.Error("a file below the floor was collected")
		}
		if file.Bytes <= 0 {
			t.Errorf("%s has no size", file.Path)
		}
		if file.Modified.IsZero() {
			t.Errorf("%s has no modified time", file.Path)
		}
	}
}

func TestLargeFileLimitIsHonoured(t *testing.T) {
	root := t.TempDir()
	for index := range 12 {
		writeSized(t, filepath.Join(root, string(rune('a'+index))+".bin"), (index+1)*128*1024)
	}

	surface := scopedSurface(t, root, Options{MinFileBytes: 64 * 1024, LargeFileLimit: 3})
	if len(surface.LargeFiles) != 3 {
		t.Fatalf("collected %d files, want 3", len(surface.LargeFiles))
	}
	// The three kept must be the three biggest, not the first three seen.
	for index := 1; index < len(surface.LargeFiles); index++ {
		if surface.LargeFiles[index-1].Bytes < surface.LargeFiles[index].Bytes {
			t.Errorf("the kept files are not the largest: %+v", surface.LargeFiles)
		}
	}
	if surface.LargeFiles[0].Bytes < int64(12*128*1024) {
		t.Errorf("the biggest file was dropped: %+v", surface.LargeFiles[0])
	}
}

func TestNoLargeFilesWithoutAFloor(t *testing.T) {
	root := t.TempDir()
	writeSized(t, filepath.Join(root, "big.bin"), 512*1024)

	surface := scopedSurface(t, root, Options{})
	if len(surface.LargeFiles) != 0 {
		t.Errorf("collected files without being asked: %+v", surface.LargeFiles)
	}
}

func TestScopedWalkNamesUnreadableEntries(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read the unreadable directory")
	}
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	writeSized(t, filepath.Join(blocked, "payload"), 8*1024)
	if err := os.Chmod(blocked, 0000); err != nil {
		t.Fatalf("blocking the directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0700) })

	surface := scopedSurface(t, root, Options{})
	if surface.Denied == 0 {
		t.Fatal("the unreadable directory was not counted")
	}
	named := false
	for _, child := range surface.Root.Children {
		if child.Kind == NodeUnreadable {
			named = true
		}
	}
	if !named {
		t.Error("the unreadable entries got no row of their own")
	}
}

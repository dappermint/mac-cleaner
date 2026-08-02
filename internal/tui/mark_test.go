package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dappermint/ratatouille/internal/scan"
)

func markState(root *scan.SurfaceNode) *tuiState {
	state := &tuiState{
		report:      scan.Report{Home: "/Users/someone", Surface: &scan.Surface{Root: root}},
		selected:    make(map[string]bool),
		expanded:    make(map[string]bool),
		marked:      make(map[string]bool),
		markedBytes: make(map[string]int64),
		view:        viewSurface,
	}
	// Open every branch so each node has a row the cursor can reach. The key
	// scheme has to match surfaceRows: the root is its own name, and each child
	// appends its name to the parent key.
	var open func(node *scan.SurfaceNode, key string)
	open = func(node *scan.SurfaceNode, key string) {
		if node == nil {
			return
		}
		state.expanded[key] = true
		for _, child := range node.Children {
			open(child, key+"/"+child.Name)
		}
	}
	open(root, root.Name)
	return state
}

func directory(name, path string, bytes int64, children ...*scan.SurfaceNode) *scan.SurfaceNode {
	return &scan.SurfaceNode{Name: name, Path: path, Kind: scan.NodeDirectory, Bytes: bytes, Children: children}
}

func markAt(t *testing.T, state *tuiState, name string) {
	t.Helper()
	rows := state.surfaceRows()
	for index, row := range rows {
		if row.node.Name == name {
			state.setCursor(index)
			state.toggleMark()
			return
		}
	}
	t.Fatalf("no row named %q in %d rows", name, len(rows))
}

// Only a real directory can be marked. Every other row is an accounting row
// that describes bytes rather than owning them.
func TestOnlyDirectoriesAreMarkable(t *testing.T) {
	cases := []struct {
		kind    scan.NodeKind
		path    string
		allowed bool
	}{
		{scan.NodeDirectory, "/Users/someone/Library/Caches/com.example", true},
		{scan.NodeRemainder, "", false},
		{scan.NodeUnwalked, "", false},
		{scan.NodeUnreadable, "", false},
		{scan.NodeForeign, "/Volumes/External", false},
		{scan.NodeContainer, "", false},
		{scan.NodeVolume, "", false},
		{scan.NodeFree, "", false},
	}
	for _, testCase := range cases {
		node := &scan.SurfaceNode{Name: "row", Path: testCase.path, Kind: testCase.kind}
		got, reason := markable(node)
		if got != testCase.allowed {
			t.Errorf("%s: markable = %v, want %v (%s)", testCase.kind, got, testCase.allowed, reason)
		}
		if !got && reason == "" {
			t.Errorf("%s was refused without a reason", testCase.kind)
		}
	}
}

// The validator has the last word. A bare home Library is a directory and still
// must not be markable.
func TestMarkingHonoursTheSafetyValidator(t *testing.T) {
	for _, path := range []string{
		"/Users/someone/Library",
		"/Users/someone",
		"/System/Library/Caches",
		"/Library/Updates",
	} {
		node := &scan.SurfaceNode{Name: filepath.Base(path), Path: path, Kind: scan.NodeDirectory}
		if ok, _ := markable(node); ok {
			t.Errorf("%s was markable", path)
		}
	}
}

// A directory inside an already-marked one must not be marked as well, or the
// running total counts the same bytes at two depths.
func TestMarkingADescendantIsRefused(t *testing.T) {
	root := directory("Caches", "/Users/someone/Library/Caches", 300,
		directory("com.example", "/Users/someone/Library/Caches/com.example", 200,
			directory("inner", "/Users/someone/Library/Caches/com.example/inner", 100),
		),
	)
	state := markState(root)

	markAt(t, state, "com.example")
	if state.markedTotal() != 200 {
		t.Fatalf("total = %d, want 200", state.markedTotal())
	}

	markAt(t, state, "inner")
	if len(state.marked) != 1 {
		t.Errorf("a descendant was marked as well: %v", state.markedPaths())
	}
	if state.markedTotal() != 200 {
		t.Errorf("total = %d, want the ancestor's 200", state.markedTotal())
	}
	if !strings.Contains(state.notice, "already covered") {
		t.Errorf("the refusal was not explained: %q", state.notice)
	}
}

// Marking an ancestor of something already marked replaces it rather than
// adding to it, for the same reason.
func TestMarkingAnAncestorSwallowsTheDescendant(t *testing.T) {
	root := directory("Caches", "/Users/someone/Library/Caches", 300,
		directory("com.example", "/Users/someone/Library/Caches/com.example", 200,
			directory("inner", "/Users/someone/Library/Caches/com.example/inner", 100),
		),
	)
	state := markState(root)

	markAt(t, state, "inner")
	markAt(t, state, "com.example")

	paths := state.markedPaths()
	if len(paths) != 1 || paths[0] != "/Users/someone/Library/Caches/com.example" {
		t.Fatalf("marked = %v, want only the ancestor", paths)
	}
	if state.markedTotal() != 200 {
		t.Errorf("total = %d, want 200 rather than 300", state.markedTotal())
	}
}

func TestMarkingTwiceUnmarks(t *testing.T) {
	root := directory("Caches", "/Users/someone/Library/Caches", 200,
		directory("com.example", "/Users/someone/Library/Caches/com.example", 200),
	)
	state := markState(root)

	markAt(t, state, "com.example")
	markAt(t, state, "com.example")
	if len(state.marked) != 0 {
		t.Errorf("still marked: %v", state.markedPaths())
	}
	if !strings.Contains(state.notice, "unmarked") {
		t.Errorf("notice = %q", state.notice)
	}
}

// The explorer's removals go through the same item shape as everything else, so
// they get the same confirmation, funnel and log.
func TestMarkedItemCarriesPerPathSizes(t *testing.T) {
	root := directory("Caches", "/Users/someone/Library/Caches", 300,
		directory("one", "/Users/someone/Library/Caches/one", 100),
		directory("two", "/Users/someone/Library/Caches/two", 200),
	)
	state := markState(root)
	markAt(t, state, "one")
	markAt(t, state, "two")

	item, ok := state.markedItem(nil)
	if !ok {
		t.Fatal("no item was produced")
	}
	if item.Action == nil || item.Action.Kind != scan.ActionTrash {
		t.Fatalf("the explorer produced %+v, want a Trash action", item.Action)
	}
	if len(item.Action.Paths) != 2 || len(item.Action.PathBytes) != 2 {
		t.Fatalf("paths=%v sizes=%v", item.Action.Paths, item.Action.PathBytes)
	}
	if item.Bytes != 300 {
		t.Errorf("bytes = %d, want 300", item.Bytes)
	}
	var summed int64
	for _, size := range item.Action.PathBytes {
		summed += size
	}
	if summed != item.Bytes {
		t.Errorf("per-path sizes sum to %d but the item claims %d", summed, item.Bytes)
	}
	if !item.Selectable() {
		t.Error("the explorer item is not selectable")
	}
}

func TestNoMarksProducesNoItem(t *testing.T) {
	state := markState(directory("Caches", "/Users/someone/Library/Caches", 0))
	if _, ok := state.markedItem(nil); ok {
		t.Error("an item was produced with nothing marked")
	}
}

func TestResetClearsMarks(t *testing.T) {
	root := directory("Caches", "/Users/someone/Library/Caches", 200,
		directory("com.example", "/Users/someone/Library/Caches/com.example", 200),
	)
	state := markState(root)
	markAt(t, state, "com.example")

	state.reset(scan.Report{Home: "/Users/someone"})
	if len(state.marked) != 0 || state.markedTotal() != 0 {
		t.Errorf("a rescan kept stale marks: %v", state.markedPaths())
	}
}

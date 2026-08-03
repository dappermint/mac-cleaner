package tui

import (
	"testing"

	"github.com/dappermint/ratatouille/internal/scan"
)

func TestSurfaceRowsFollowExpansion(t *testing.T) {
	root := &scan.SurfaceNode{
		Name:  "all storage",
		Kind:  scan.NodeSurface,
		Bytes: 100,
		Children: []*scan.SurfaceNode{{
			Name:     "container disk3",
			Kind:     scan.NodeContainer,
			Bytes:    100,
			Children: []*scan.SurfaceNode{{Name: "Data", Kind: scan.NodeVolume, Bytes: 100, Path: "/System/Volumes/Data"}},
		}},
	}
	rows := surfaceRows(root, map[string]bool{})
	if len(rows) != 1 {
		t.Fatalf("a collapsed root should render one row, got %d", len(rows))
	}
	expanded := defaultExpansion(root, "/System/Volumes/Data", 2)
	rows = surfaceRows(root, expanded)
	if len(rows) != 3 {
		t.Fatalf("the data volume chain should be open, got %d rows", len(rows))
	}
	if rows[2].node.Name != "Data" {
		t.Fatalf("expected the data volume last, got %q", rows[2].node.Name)
	}
}

func TestDefaultExpansionHonoursDepth(t *testing.T) {
	leaf := &scan.SurfaceNode{Name: "leaf", Kind: scan.NodeDirectory, Path: "/System/Volumes/Data/one/two/leaf", Bytes: 100}
	two := &scan.SurfaceNode{Name: "two", Kind: scan.NodeDirectory, Path: "/System/Volumes/Data/one/two", Bytes: 100, Children: []*scan.SurfaceNode{leaf}}
	one := &scan.SurfaceNode{Name: "one", Kind: scan.NodeDirectory, Path: "/System/Volumes/Data/one", Bytes: 100, Children: []*scan.SurfaceNode{two}}
	volume := &scan.SurfaceNode{Name: "Data", Kind: scan.NodeVolume, Path: "/System/Volumes/Data", Bytes: 100, Children: []*scan.SurfaceNode{one}}
	root := &scan.SurfaceNode{Name: "all storage", Kind: scan.NodeSurface, Bytes: 100, Children: []*scan.SurfaceNode{{
		Name: "container disk3", Kind: scan.NodeContainer, Bytes: 100, Children: []*scan.SurfaceNode{volume},
	}}}

	shallow := surfaceRows(root, defaultExpansion(root, volume.Path, 0))
	deep := surfaceRows(root, defaultExpansion(root, volume.Path, 2))
	if len(shallow) != 4 {
		t.Fatalf("depth 0 rendered %d rows, want 4", len(shallow))
	}
	if len(deep) != 6 {
		t.Fatalf("depth 2 rendered %d rows, want 6", len(deep))
	}
}

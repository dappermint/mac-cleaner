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
	expanded := defaultExpansion(root, "/System/Volumes/Data")
	rows = surfaceRows(root, expanded)
	if len(rows) != 3 {
		t.Fatalf("the data volume chain should be open, got %d rows", len(rows))
	}
	if rows[2].node.Name != "Data" {
		t.Fatalf("expected the data volume last, got %q", rows[2].node.Name)
	}
}

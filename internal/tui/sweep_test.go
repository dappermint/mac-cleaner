package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dappermint/mac-cleaner/internal/scan"
	"github.com/dappermint/mac-cleaner/internal/storage"
)

func deepReport() scan.Report {
	report := sampleReport()
	node := report.Surface.Root.Children[0].Children[0]
	for depth := 0; depth < 7; depth++ {
		child := &scan.SurfaceNode{
			Name:     fmt.Sprintf("a very long directory name that will not fit at level %d", depth),
			Kind:     scan.NodeDirectory,
			Path:     "/System/Volumes/Data/" + strings.Repeat("nested/", depth+1),
			Bytes:    node.Bytes,
			Category: storage.CategorySystemData,
		}
		node.Children = append([]*scan.SurfaceNode{child}, node.Children...)
		node = child
	}
	for index := 0; index < 12; index++ {
		report.Items = append(report.Items, scan.Item{
			ID:     fmt.Sprintf("filler-%02d", index),
			Name:   strings.Repeat("long item name ", 6),
			Group:  "app caches",
			Risk:   scan.RiskReview,
			Bytes:  int64(index) * 1024,
			Detail: strings.Repeat("detail ", 20),
			Action: &scan.Action{Kind: scan.ActionTrash, Paths: []string{"/tmp/example"}},
		})
	}
	return report
}

func TestEveryViewSurvivesEveryTerminalSize(t *testing.T) {
	report := deepReport()
	for _, view := range tuiViewOrder {
		for height := 1; height <= 48; height++ {
			for width := 1; width <= 160; width++ {
				state := tuiState{
					report:   report,
					selected: map[string]bool{},
					expanded: defaultExpansion(surfaceRoot(report), report.Disk.Path),
					view:     view,
					color:    true,
				}
				for step := 0; step < 5; step++ {
					var sink strings.Builder
					renderer := &screenRenderer{out: &sink, height: height, width: width, active: true}
					state.render(renderer)
					if len(renderer.previous) != height {
						t.Fatalf("view %v at %dx%d produced %d rows", view, height, width, len(renderer.previous))
					}
					for row, line := range renderer.previous {
						if visibleWidth(line) > width {
							t.Fatalf("view %v at %dx%d row %d is %d columns: %q",
								view, height, width, row, visibleWidth(line), ansiPattern.ReplaceAllString(line, ""))
						}
					}
					state.setCursor(state.cursor() + 3)
					state.toggleSurfaceRow()
				}
			}
		}
	}
}

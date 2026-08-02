package tui

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dappermint/ratatouille/internal/scan"
	"github.com/dappermint/ratatouille/internal/storage"
	"github.com/dappermint/ratatouille/internal/text"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

func visibleWidth(line string) int {
	return utf8.RuneCountInString(ansiPattern.ReplaceAllString(line, ""))
}

func sampleReport() scan.Report {
	data := &scan.SurfaceNode{
		Name: "Macintosh HD - Data", Kind: scan.NodeVolume, Path: "/System/Volumes/Data", Bytes: 440,
		Children: []*scan.SurfaceNode{
			{Name: "Users", Kind: scan.NodeDirectory, Bytes: 300, Category: storage.CategoryDocuments, Children: []*scan.SurfaceNode{
				{Name: "dappy", Kind: scan.NodeDirectory, Bytes: 280},
				{Name: "smaller directories and loose files", Kind: scan.NodeRemainder, Bytes: 20, Entries: 4},
			}},
			{Name: "Library", Kind: scan.NodeDirectory, Bytes: 90},
			{Name: "unaccounted", Kind: scan.NodeUnwalked, Bytes: 50, Detail: "claimed but not attributed"},
			{Name: "unreadable entries", Kind: scan.NodeUnreadable, Bytes: -1, Entries: 226},
		},
	}
	root := &scan.SurfaceNode{
		Name: "all storage", Kind: scan.NodeSurface, Bytes: 500,
		Children: []*scan.SurfaceNode{{
			Name: "container disk3", Kind: scan.NodeContainer, Bytes: 500,
			Children: []*scan.SurfaceNode{
				data,
				{Name: "free", Kind: scan.NodeFree, Bytes: 60},
			},
		}},
	}
	return scan.Report{
		Home:    "/Users/dappy",
		Disk:    storage.Disk{Path: "/System/Volumes/Data", Total: 500, Free: 60, InUse: 440, Container: "disk3"},
		Surface: &scan.Surface{Root: root, Walked: 390, Claimed: 440, Files: 1786316, Denied: 226, Containers: []storage.Container{{Reference: "disk3", Ceiling: 500, Free: 60, Volumes: []storage.Volume{{Name: "Data", Roles: []string{"Data"}, InUse: 440, MountedAt: "/System/Volumes/Data"}}}}},
		Health: &scan.Health{Level: scan.HealthWatch, Signals: []scan.HealthSignal{
			{ID: "headroom", Name: "write headroom", Level: scan.HealthWatch, Value: "60 B free", Detail: "low headroom", Source: "statfs"},
			{ID: "walk-io", Name: "io errors during walk", Level: scan.HealthOK, Value: "0", Detail: "clean", Source: "surface walk"},
		}},
		Items: []scan.Item{
			{ID: "a", Name: "homebrew leftovers", Group: "supported cleanup", Category: storage.CategoryDeveloper, Risk: scan.RiskSafe, Bytes: 12, Detail: "old bottles", Source: "brew", Action: &scan.Action{Kind: scan.ActionCommand, Command: "brew", Args: []string{"cleanup"}}},
			{ID: "b", Name: "Steam", Group: "protected app data", Category: storage.CategorySystemData, Risk: scan.RiskProtected, Bytes: 195, Detail: "game library"},
		},
		Issues: []string{"/private/var/db: 3 permission-denied entries skipped"},
	}
}

func TestEveryViewRendersAFullFrame(t *testing.T) {
	sizes := [][2]int{{24, 80}, {40, 120}, {12, 60}, {8, 40}, {6, 24}}
	for _, view := range tuiViewOrder {
		for _, size := range sizes {
			height, width := size[0], size[1]
			report := sampleReport()
			state := tuiState{
				report:   report,
				selected: map[string]bool{},
				expanded: defaultExpansion(report.Surface.Root, report.Disk.Path),
				view:     view,
				color:    false,
			}
			var sink strings.Builder
			renderer := &screenRenderer{out: &sink, height: height, width: width, active: true}
			state.render(renderer)
			if len(renderer.previous) != height {
				t.Fatalf("view %s at %dx%d produced %d rows", view, height, width, len(renderer.previous))
			}
			for row, line := range renderer.previous {
				if visibleWidth(line) > width {
					t.Fatalf("view %s at %dx%d row %d is %d columns wide: %q", view, height, width, row, visibleWidth(line), line)
				}
			}
		}
	}
}

func TestSurfaceViewShowsTheUnaccountedRow(t *testing.T) {
	report := sampleReport()
	state := tuiState{
		report:   report,
		selected: map[string]bool{},
		expanded: defaultExpansion(report.Surface.Root, report.Disk.Path),
		view:     viewSurface,
		color:    false,
	}
	var sink strings.Builder
	renderer := &screenRenderer{out: &sink, height: 30, width: 100, active: true}
	state.render(renderer)
	frame := strings.Join(renderer.previous, "\n")
	for _, want := range []string{"unaccounted", "unreadable entries", "container disk3", "explained"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("the surface frame never mentions %q:\n%s", want, frame)
		}
	}
}

func TestKeyGuideNeverOverflows(t *testing.T) {
	for _, view := range tuiViewOrder {
		for width := 20; width <= 140; width += 7 {
			if guide := keyGuide(width, view); visibleWidth(text.Truncate(guide, width)) > width {
				t.Fatalf("view %s guide overflows at width %d", view, width)
			}
		}
	}
}

package main

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

func visibleWidth(line string) int {
	return utf8.RuneCountInString(ansiPattern.ReplaceAllString(line, ""))
}

func sampleReport() Report {
	data := &SurfaceNode{
		Name: "Macintosh HD - Data", Kind: NodeVolume, Path: "/System/Volumes/Data", Bytes: 440,
		Children: []*SurfaceNode{
			{Name: "Users", Kind: NodeDirectory, Bytes: 300, Category: CategoryDocuments, Children: []*SurfaceNode{
				{Name: "dappy", Kind: NodeDirectory, Bytes: 280},
				{Name: "smaller directories and loose files", Kind: NodeRemainder, Bytes: 20, Entries: 4},
			}},
			{Name: "Library", Kind: NodeDirectory, Bytes: 90},
			{Name: "unaccounted", Kind: NodeUnwalked, Bytes: 50, Detail: "claimed but not attributed"},
			{Name: "unreadable entries", Kind: NodeUnreadable, Bytes: -1, Entries: 226},
		},
	}
	root := &SurfaceNode{
		Name: "all storage", Kind: NodeSurface, Bytes: 500,
		Children: []*SurfaceNode{{
			Name: "container disk3", Kind: NodeContainer, Bytes: 500,
			Children: []*SurfaceNode{
				data,
				{Name: "free", Kind: NodeFree, Bytes: 60},
			},
		}},
	}
	return Report{
		Home:    "/Users/dappy",
		Disk:    Disk{Path: "/System/Volumes/Data", Total: 500, Free: 60, InUse: 440, Container: "disk3"},
		Surface: &Surface{Root: root, Walked: 390, Claimed: 440, Files: 1786316, Denied: 226, Containers: []Container{{Reference: "disk3", Ceiling: 500, Free: 60, Volumes: []Volume{{Name: "Data", Roles: []string{"Data"}, InUse: 440, MountedAt: "/System/Volumes/Data"}}}}},
		Health: &Health{Level: HealthWatch, Signals: []HealthSignal{
			{ID: "headroom", Name: "write headroom", Level: HealthWatch, Value: "60 B free", Detail: "low headroom", Source: "statfs"},
			{ID: "walk-io", Name: "io errors during walk", Level: HealthOK, Value: "0", Detail: "clean", Source: "surface walk"},
		}},
		Items: []Item{
			{ID: "a", Name: "homebrew leftovers", Group: "supported cleanup", Category: CategoryDeveloper, Risk: RiskSafe, Bytes: 12, Detail: "old bottles", Source: "brew", Action: &Action{Kind: ActionCommand, Command: "brew", Args: []string{"cleanup"}}},
			{ID: "b", Name: "Steam", Group: "protected app data", Category: CategorySystemData, Risk: RiskProtected, Bytes: 195, Detail: "game library"},
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

func TestSurfaceRowsAlwaysSumToTheirParent(t *testing.T) {
	report := sampleReport()
	assertChildrenSumToParent(t, report.Surface.Root.Children[0].Children[0])
}

func TestKeyGuideNeverOverflows(t *testing.T) {
	for _, view := range tuiViewOrder {
		for width := 20; width <= 140; width += 7 {
			if guide := keyGuide(width, view); visibleWidth(truncate(guide, width)) > width {
				t.Fatalf("view %s guide overflows at width %d", view, width)
			}
		}
	}
}

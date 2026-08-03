package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/dappermint/ratatouille/internal/scan"
	"github.com/dappermint/ratatouille/internal/storage"
	"github.com/dappermint/ratatouille/internal/text"
)

func TestDisplaySanitizesTerminalControls(t *testing.T) {
	if got := text.Clean("safe\x1b[2J\nname"); strings.ContainsRune(got, 27) || strings.ContainsRune(got, '\n') {
		t.Fatalf("terminal controls remained in %q", got)
	}
}

func TestRendererAddressesRowsWithoutTerminalNewlines(t *testing.T) {
	var output bytes.Buffer
	renderer := screenRenderer{out: &output, height: 3, width: 20, active: true}
	renderer.Render([]string{"one", "two"})
	if strings.ContainsRune(output.String(), '\n') {
		t.Fatalf("renderer emitted a terminal newline: %q", output.String())
	}
	if !strings.Contains(output.String(), "\x1b[1;1Hone") || !strings.Contains(output.String(), "\x1b[2;1Htwo") {
		t.Fatalf("renderer did not address rows directly: %q", output.String())
	}

	output.Reset()
	renderer.Render([]string{"one", "changed"})
	if strings.Contains(output.String(), "\x1b[1;1H") || !strings.Contains(output.String(), "\x1b[2;1Hchanged") {
		t.Fatalf("renderer did not diff the frame: %q", output.String())
	}
}

func TestStatusScoreAndPressureColoursRunInOppositeDirections(t *testing.T) {
	if scoreColour(100) != colorMint || scoreColour(0) != colorCoral {
		t.Fatal("load score colour treats a healthy score as pressure")
	}
	if pressureColour(100) != colorCoral || pressureColour(0) != colorMint {
		t.Fatal("pressure colour treats high utilisation as healthy")
	}
}

func TestOptionsValidateUserPreferences(t *testing.T) {
	valid := Options{View: "status", Depth: 4, StatusInterval: 3 * time.Second}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid options: %v", err)
	}
	for _, options := range []Options{
		{View: "unknown", Depth: 3, StatusInterval: time.Second},
		{View: "surface", Depth: -1, StatusInterval: time.Second},
		{View: "surface", Depth: 3},
	} {
		if err := options.Validate(); err == nil {
			t.Errorf("invalid options were accepted: %+v", options)
		}
	}
}

func TestOpeningViewHonoursThePreference(t *testing.T) {
	report := sampleReport()
	for name, want := range map[string]tuiView{
		"surface": viewSurface,
		"actions": viewActions,
		"apps":    viewApps,
		"health":  viewHealth,
		"status":  viewStatus,
	} {
		state := tuiState{report: report, opening: name}
		if got := state.openingView(); got != want {
			t.Errorf("opening view %q = %s, want %s", name, got, want)
		}
	}
}

func TestTUIFiltersKeepCursorOnVisibleItems(t *testing.T) {
	state := tuiState{
		report: scan.Report{Items: []scan.Item{
			{ID: "safe", Risk: scan.RiskSafe},
			{ID: "review", Risk: scan.RiskReview},
			{ID: "protected", Risk: scan.RiskProtected},
		}},
		selected: make(map[string]bool),
	}
	state.cycleFilter()
	indices := state.filteredIndices()
	if len(indices) != 1 || indices[0] != 0 {
		t.Fatalf("safe filter indices = %#v", indices)
	}
	item, ok := state.focusedItem()
	if !ok || item.ID != "safe" {
		t.Fatalf("focused item = %#v, %v", item, ok)
	}
	state.cycleFilter()
	item, ok = state.focusedItem()
	if !ok || item.ID != "review" {
		t.Fatalf("focused review item = %#v, %v", item, ok)
	}
}

func TestStorageRailShowsUsedSelectedAndFreeSegments(t *testing.T) {
	state := tuiState{}
	rail := state.storageRail(10, 20, 100, 10)
	if got := len([]rune(rail)); got != 10 {
		t.Fatalf("rail width = %d, want 10", got)
	}
	if got, want := rail, "███████▓░░"; got != want {
		t.Fatalf("rail = %q, want %q", got, want)
	}
}

func TestStageAndItemRowsStayInsideTerminalWidth(t *testing.T) {
	launch := launchState{}
	stage := launch.stageLine(scan.ScanProgress{
		Name:    "very long scan stage name",
		Detail:  "a deliberately long description that must not wrap into another terminal row",
		State:   scan.ScanRunning,
		Started: time.Now(),
	}, time.Now(), 80)
	if got := len([]rune(stage)); got != 80 {
		t.Fatalf("stage width = %d, want 80", got)
	}

	state := tuiState{}
	item := scan.Item{Name: strings.Repeat("wide item ", 20), Category: storage.CategoryOtherUsers, Risk: scan.RiskProtected, Bytes: 1024}
	for _, width := range []int{40, 80} {
		if got := len([]rune(state.itemLine(item, true, width))); got > width {
			t.Fatalf("item width = %d, terminal width = %d", got, width)
		}
	}
}

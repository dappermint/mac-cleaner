package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPathUsageCountsAllocatedBlocks(t *testing.T) {
	root := t.TempDir()
	sparse := filepath.Join(root, "sparse.img")
	file, err := os.Create(sparse)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(512 * 1024 * 1024); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	measured, err := pathUsage(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if measured.Bytes >= 1024*1024 {
		t.Fatalf("sparse file counted by apparent size: %d", measured.Bytes)
	}
}

func TestMoveToTrashIsRecoverable(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "Library", "Caches", "example")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "cache.bin"), []byte("cache"), 0600); err != nil {
		t.Fatal(err)
	}

	destination, err := moveToTrash(home, source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(destination, filepath.Join(home, ".Trash")+string(filepath.Separator)) {
		t.Fatalf("destination escaped Trash: %s", destination)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "cache.bin")); err != nil {
		t.Fatalf("trashed data is not recoverable: %v", err)
	}
}

func TestMoveToTrashRejectsEscapingParentSymlink(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "data"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "linked")); err != nil {
		t.Fatal(err)
	}

	_, err := moveToTrash(home, filepath.Join(home, "linked", "data"))
	if err == nil || !strings.Contains(err.Error(), "symlink outside home") {
		t.Fatalf("expected symlink escape refusal, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "data")); err != nil {
		t.Fatalf("outside data was changed: %v", err)
	}
}

func TestEmptyTrashRejectsUnsafeHome(t *testing.T) {
	if err := emptyTrash("/"); err == nil {
		t.Fatal("expected root home path to be rejected")
	}
}

func TestDryRunDoesNotMoveFiles(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "Downloads", "large.dmg")
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	item := Item{
		ID:   "download-test",
		Name: "large.dmg",
		Risk: RiskReview,
		Action: &Action{
			Kind:  ActionTrash,
			Paths: []string{source},
		},
	}
	var output bytes.Buffer
	results := executeItems(context.Background(), home, []Item{item}, true, &output)
	if err := actionErrors(results); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("dry run changed source: %v", err)
	}
	if !strings.Contains(output.String(), "dry-run") {
		t.Fatalf("dry-run output did not state its boundary: %s", output.String())
	}
}

func TestScannerClassifiesDataAndArtifacts(t *testing.T) {
	home := t.TempDir()
	paths := []string{
		filepath.Join(home, "Library", "Caches", "com.example.app", "cache.bin"),
		filepath.Join(home, "Library", "Application Support", "Steam", "steamapps", "game.bin"),
		filepath.Join(home, "Downloads", "archive.dmg"),
		filepath.Join(home, "Documents", "site", "node_modules", "package", "index.js"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("allocated data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "Documents", "site", "package.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	scanner := NewScanner(home, true)
	scanner.SkipCommands = true
	scanner.SkipSystem = true
	scanner.CacheMinimum = 1
	scanner.DataMinimum = 1
	scanner.ArtifactMinimum = 1
	report := scanner.Scan(context.Background())

	assertRisk := func(fragment string, risk Risk, selectable bool) {
		t.Helper()
		for _, item := range report.Items {
			if strings.Contains(item.Name, fragment) || strings.Contains(item.Source, fragment) {
				if item.Risk != risk {
					t.Fatalf("%s risk = %s, want %s", fragment, item.Risk, risk)
				}
				if item.Selectable() != selectable {
					t.Fatalf("%s selectable = %v, want %v", fragment, item.Selectable(), selectable)
				}
				return
			}
		}
		t.Fatalf("did not find %s in %#v", fragment, report.Items)
	}
	assertRisk("steam library", RiskProtected, false)
	assertRisk("com.example.app", RiskReview, true)
	assertRisk("archive.dmg", RiskReview, true)
	assertRisk("node_modules", RiskReview, true)
}

func TestSizeParsing(t *testing.T) {
	output := "Would remove one (807.5MB)\nThis operation would free approximately 2.3GB\n"
	if got, want := parseLargestSize(output), int64(2300000000); got != want {
		t.Fatalf("largest size = %d, want %d", got, want)
	}
	if got, want := sumSizes("2GB\n500MB\n"), int64(2500000000); got != want {
		t.Fatalf("summed size = %d, want %d", got, want)
	}
	if got := parseLargestSize("/nix/store/abc93171b-package.drv\n"); got != 0 {
		t.Fatalf("nix store hash parsed as a byte size: %d", got)
	}
	if got, want := parseNixGCCount("/nix/store/path\n5154 store paths would be deleted\n"), 5154; got != want {
		t.Fatalf("nix gc count = %d, want %d", got, want)
	}
}

func TestSelectionTotalsOnlyCountTrashAfterEmpty(t *testing.T) {
	move := Item{Bytes: 100, Action: &Action{Kind: ActionTrash}}
	empty := Item{Bytes: 25, Action: &Action{Kind: ActionEmptyTrash}}
	direct, toTrash, empties := selectionTotals([]Item{move})
	if direct != 0 || toTrash != 100 || empties {
		t.Fatalf("move-only totals = %d, %d, %v", direct, toTrash, empties)
	}
	direct, toTrash, empties = selectionTotals([]Item{move, empty})
	if direct != 125 || toTrash != 0 || !empties {
		t.Fatalf("empty-trash totals = %d, %d, %v", direct, toTrash, empties)
	}
}

func TestDisplaySanitizesTerminalControls(t *testing.T) {
	if got := cleanDisplay("safe\x1b[2J\nname"); strings.ContainsRune(got, 27) || strings.ContainsRune(got, '\n') {
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

func TestScannerReportsEveryStageLifecycle(t *testing.T) {
	scanner := NewScanner(t.TempDir(), true)
	scanner.SkipCommands = true
	scanner.SkipSystem = true
	scanner.CacheMinimum = 1
	scanner.DataMinimum = 1
	scanner.ArtifactMinimum = 1

	var mutex sync.Mutex
	events := make(map[string]map[ScanStageState]bool)
	totals := make(map[int]bool)
	scanner.Progress = func(progress ScanProgress) {
		mutex.Lock()
		defer mutex.Unlock()
		if events[progress.ID] == nil {
			events[progress.ID] = make(map[ScanStageState]bool)
		}
		events[progress.ID][progress.State] = true
		totals[progress.Total] = true
	}
	scanner.Scan(context.Background())

	expectedIDs := []string{"volume"}
	for _, collector := range scanner.collectors() {
		expectedIDs = append(expectedIDs, collector.id)
	}
	if len(totals) != 1 || !totals[len(expectedIDs)] {
		t.Fatalf("progress totals = %#v, want %d", totals, len(expectedIDs))
	}
	for _, id := range expectedIDs {
		for _, lifecycle := range []ScanStageState{ScanQueued, ScanRunning, ScanDone} {
			if !events[id][lifecycle] {
				t.Errorf("%s never reported %s", id, lifecycle)
			}
		}
	}
}

func TestTUIFiltersKeepCursorOnVisibleItems(t *testing.T) {
	state := tuiState{
		report: Report{Items: []Item{
			{ID: "safe", Risk: RiskSafe},
			{ID: "review", Risk: RiskReview},
			{ID: "protected", Risk: RiskProtected},
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
	stage := launch.stageLine(ScanProgress{
		Name:    "very long scan stage name",
		Detail:  "a deliberately long description that must not wrap into another terminal row",
		State:   ScanRunning,
		Started: time.Now(),
	}, time.Now(), 80)
	if got := len([]rune(stage)); got != 80 {
		t.Fatalf("stage width = %d, want 80", got)
	}

	state := tuiState{}
	item := Item{Name: strings.Repeat("wide item ", 20), Category: CategoryOtherUsers, Risk: RiskProtected, Bytes: 1024}
	for _, width := range []int{40, 80} {
		if got := len([]rune(state.itemLine(item, true, width))); got > width {
			t.Fatalf("item width = %d, terminal width = %d", got, width)
		}
	}
}

func TestReportSortsByCategoryThenSize(t *testing.T) {
	report := Report{Items: []Item{
		{ID: "system-small", Name: "small", Category: CategorySystemData, Bytes: 10},
		{ID: "developer", Name: "dev", Category: CategoryDeveloper, Bytes: 50},
		{ID: "application", Name: "app", Category: CategoryApplications, Bytes: 1},
		{ID: "system-large", Name: "large", Category: CategorySystemData, Bytes: 100},
	}}
	report.Sort()
	got := []string{report.Items[0].ID, report.Items[1].ID, report.Items[2].ID, report.Items[3].ID}
	want := []string{"application", "developer", "system-large", "system-small"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("category order = %#v, want %#v", got, want)
	}
}

func TestUserDataCategoriesMatchMacOSBuckets(t *testing.T) {
	tests := map[string]StorageCategory{
		"~/Library/Application Support/CloudDocs":  CategoryICloudDrive,
		"~/Library/Application Support/MobileSync": CategoryIOSFiles,
		"~/Library/Messages":                       CategoryMessages,
		"~/Library/Mail":                           CategoryMail,
		"~/Pictures/Photos Library.photoslibrary":  CategoryPhotos,
		"~/Library/Application Support/Steam":      CategorySystemData,
	}
	for path, want := range tests {
		if got := categoryForUserData(path); got != want {
			t.Errorf("categoryForUserData(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestRootInventoryClassifiesAndProtectsSystemTrees(t *testing.T) {
	root := t.TempDir()
	paths := rootInventoryPaths{
		library:  filepath.Join(root, "Library"),
		variable: filepath.Join(root, "var"),
		system:   filepath.Join(root, "System"),
		users:    filepath.Join(root, "Users"),
		fixed: []inventoryTarget{
			{path: filepath.Join(root, "nix", "store"), category: CategorySystemData},
			{path: filepath.Join(root, "opt", "homebrew"), category: CategoryDeveloper},
		},
	}
	home := filepath.Join(paths.users, "current")
	for _, path := range []string{
		filepath.Join(paths.library, "Caches", "cache.bin"),
		filepath.Join(paths.library, "Developer", "sdk.bin"),
		filepath.Join(paths.variable, "vm", "swapfile"),
		filepath.Join(paths.system, "Library", "system.bin"),
		filepath.Join(paths.users, "current", "keep.bin"),
		filepath.Join(paths.users, "other", "data.bin"),
		filepath.Join(paths.users, "Shared", "shared.bin"),
		filepath.Join(root, "nix", "store", "path.bin"),
		filepath.Join(root, "opt", "homebrew", "cellar.bin"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("allocated"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	dataVolume := filepath.Join(paths.system, "Volumes", "Data", "user.bin")
	if err := os.MkdirAll(filepath.Dir(dataVolume), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataVolume, bytes.Repeat([]byte("x"), 4*1024*1024), 0600); err != nil {
		t.Fatal(err)
	}

	scanner := NewScanner(home, false)
	scanner.Rootful = true
	scanner.SystemMinimum = 1
	scanner.rootPaths = &paths
	system := scanner.collectSystemData(context.Background())
	wantCategories := map[string]StorageCategory{
		filepath.Join(paths.library, "Caches"):    CategorySystemData,
		filepath.Join(paths.library, "Developer"): CategoryDeveloper,
		filepath.Join(paths.variable, "vm"):       CategorySystemData,
		filepath.Join(root, "nix", "store"):       CategorySystemData,
		filepath.Join(root, "opt", "homebrew"):    CategoryDeveloper,
	}
	for _, item := range system.items {
		want, ok := wantCategories[item.Source]
		if !ok {
			continue
		}
		if item.Category != want {
			t.Errorf("%s category = %s, want %s", item.Source, item.Category, want)
		}
		if item.Risk != RiskProtected || item.Selectable() {
			t.Errorf("root item is not protected: %#v", item)
		}
		delete(wantCategories, item.Source)
	}
	if len(wantCategories) != 0 {
		t.Fatalf("missing root inventory paths: %#v", wantCategories)
	}

	macOS := scanner.collectMacOS(context.Background())
	if len(macOS.items) != 1 || macOS.items[0].Category != CategoryMacOS || macOS.items[0].Selectable() {
		t.Fatalf("macOS inventory = %#v", macOS.items)
	}
	if macOS.items[0].Bytes >= 4*1024*1024 {
		t.Fatalf("macOS inventory crossed into Volumes: %d", macOS.items[0].Bytes)
	}
	otherUsers := scanner.collectOtherUsers(context.Background())
	if len(otherUsers.items) != 2 {
		t.Fatalf("other users = %#v", otherUsers.items)
	}
	for _, item := range otherUsers.items {
		if item.Source == home || item.Category != CategoryOtherUsers || item.Selectable() {
			t.Fatalf("invalid other-user item: %#v", item)
		}
	}
}

func TestRootModeIsExplicit(t *testing.T) {
	rootful, args := extractRootFlag([]string{"scan", "--json", "--root"})
	if !rootful || strings.Join(args, " ") != "scan --json" {
		t.Fatalf("root args = %v, %#v", rootful, args)
	}
	if err := validateRootMode(true, 501); err == nil {
		t.Fatal("root mode accepted a non-root uid")
	}
	if err := validateRootMode(true, 0); err != nil {
		t.Fatalf("root mode rejected uid 0: %v", err)
	}
}

func TestCommandEnvironmentDropsRootHome(t *testing.T) {
	identity := &commandIdentity{Username: "operator", Home: "/Users/operator"}
	environment := commandEnvironment([]string{
		"PATH=/bin",
		"HOME=/var/root",
		"USER=root",
		"LOGNAME=root",
		"KEEP=value",
	}, identity)
	joined := strings.Join(environment, "\n")
	for _, want := range []string{"HOME=/Users/operator", "USER=operator", "LOGNAME=operator", "KEEP=value"} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "HOME=/var/root") || strings.Contains(joined, "USER=root") {
		t.Fatalf("root identity leaked into command environment: %s", joined)
	}
}

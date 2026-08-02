package scan

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dappermint/ratatouille/internal/storage"
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

	measured, err := storage.PathUsage(context.Background(), root)
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

	destination, err := MoveToTrash(home, source)
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

	_, err := MoveToTrash(home, filepath.Join(home, "linked", "data"))
	if err == nil || !strings.Contains(err.Error(), "symlink outside home") {
		t.Fatalf("expected symlink escape refusal, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "data")); err != nil {
		t.Fatalf("outside data was changed: %v", err)
	}
}

func TestEmptyTrashRejectsUnsafeHome(t *testing.T) {
	if err := EmptyTrash("/"); err == nil {
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
	results := ExecuteItems(context.Background(), home, []Item{item}, true, &output)
	if err := ActionErrors(results); err != nil {
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
	if got, want := storage.ParseLargestSize(output), int64(2300000000); got != want {
		t.Fatalf("largest size = %d, want %d", got, want)
	}
	if got, want := storage.SumSizes("2GB\n500MB\n"), int64(2500000000); got != want {
		t.Fatalf("summed size = %d, want %d", got, want)
	}
	if got := storage.ParseLargestSize("/nix/store/abc93171b-package.drv\n"); got != 0 {
		t.Fatalf("nix store hash parsed as a byte size: %d", got)
	}
	if got, want := parseNixGCCount("/nix/store/path\n5154 store paths would be deleted\n"), 5154; got != want {
		t.Fatalf("nix gc count = %d, want %d", got, want)
	}
}

func TestSelectionTotalsOnlyCountTrashAfterEmpty(t *testing.T) {
	move := Item{Bytes: 100, Action: &Action{Kind: ActionTrash}}
	empty := Item{Bytes: 25, Action: &Action{Kind: ActionEmptyTrash}}
	direct, toTrash, empties := SelectionTotals([]Item{move})
	if direct != 0 || toTrash != 100 || empties {
		t.Fatalf("move-only totals = %d, %d, %v", direct, toTrash, empties)
	}
	direct, toTrash, empties = SelectionTotals([]Item{move, empty})
	if direct != 125 || toTrash != 0 || !empties {
		t.Fatalf("empty-trash totals = %d, %d, %v", direct, toTrash, empties)
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

	expectedIDs := make([]string, 0, 1+len(scanner.collectors()))
	expectedIDs = append(expectedIDs, "volume")
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

func TestReportSortsByCategoryThenSize(t *testing.T) {
	report := Report{Items: []Item{
		{ID: "system-small", Name: "small", Category: storage.CategorySystemData, Bytes: 10},
		{ID: "developer", Name: "dev", Category: storage.CategoryDeveloper, Bytes: 50},
		{ID: "application", Name: "app", Category: storage.CategoryApplications, Bytes: 1},
		{ID: "system-large", Name: "large", Category: storage.CategorySystemData, Bytes: 100},
	}}
	report.Sort()
	got := []string{report.Items[0].ID, report.Items[1].ID, report.Items[2].ID, report.Items[3].ID}
	want := []string{"application", "developer", "system-large", "system-small"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("category order = %#v, want %#v", got, want)
	}
}

func TestUserDataCategoriesMatchMacOSBuckets(t *testing.T) {
	tests := map[string]storage.Category{
		"~/Library/Application Support/CloudDocs":  storage.CategoryICloudDrive,
		"~/Library/Application Support/MobileSync": storage.CategoryIOSFiles,
		"~/Library/Messages":                       storage.CategoryMessages,
		"~/Library/Mail":                           storage.CategoryMail,
		"~/Pictures/Photos Library.photoslibrary":  storage.CategoryPhotos,
		"~/Library/Application Support/Steam":      storage.CategorySystemData,
	}
	for path, want := range tests {
		if got := storage.CategoryForUserData(path); got != want {
			t.Errorf("storage.CategoryForUserData(%q) = %q, want %q", path, got, want)
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
			{path: filepath.Join(root, "nix", "store"), category: storage.CategorySystemData},
			{path: filepath.Join(root, "opt", "homebrew"), category: storage.CategoryDeveloper},
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
	wantCategories := map[string]storage.Category{
		filepath.Join(paths.library, "Caches"):    storage.CategorySystemData,
		filepath.Join(paths.library, "Developer"): storage.CategoryDeveloper,
		filepath.Join(paths.variable, "vm"):       storage.CategorySystemData,
		filepath.Join(root, "nix", "store"):       storage.CategorySystemData,
		filepath.Join(root, "opt", "homebrew"):    storage.CategoryDeveloper,
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
	if len(macOS.items) != 1 || macOS.items[0].Category != storage.CategoryMacOS || macOS.items[0].Selectable() {
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
		if item.Source == home || item.Category != storage.CategoryOtherUsers || item.Selectable() {
			t.Fatalf("invalid other-user item: %#v", item)
		}
	}
}

func TestCommandEnvironmentDropsRootHome(t *testing.T) {
	identity := &storage.CommandIdentity{Username: "operator", Home: "/Users/operator"}
	environment := storage.CommandEnvironment([]string{
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

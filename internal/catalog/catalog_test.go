package catalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dappermint/ratatouille/internal/config"
	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

func testEnv(t *testing.T, home string) Env {
	t.Helper()
	t.Setenv(config.EnvDir, filepath.Join(home, "config"))
	whitelist, err := config.LoadWhitelist(home, config.WhitelistFile)
	if err != nil {
		t.Fatalf("loading the whitelist: %v", err)
	}
	return Env{
		Home:           home,
		Whitelist:      whitelist,
		Processes:      map[string]bool{},
		ProcessesKnown: true,
		InstalledKnown: true,
		Now:            time.Now(),
	}
}

func write(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// Every entry has to say what proves it is what it claims, and anything above
// safe has to name what it deliberately leaves alone. These are the two rules
// that keep "it looks like a cache" out of the catalog.
func TestEveryTargetCarriesItsEvidence(t *testing.T) {
	seen := make(map[string]bool)
	for _, target := range All() {
		if target.ID == "" || target.Name == "" {
			t.Errorf("a target has no id or name: %+v", target)
			continue
		}
		if seen[target.ID] {
			t.Errorf("duplicate target id %q", target.ID)
		}
		seen[target.ID] = true

		if strings.TrimSpace(target.Evidence) == "" {
			t.Errorf("%s has no evidence", target.ID)
		}
		if target.Qualification != EvidenceObserved && target.Qualification != EvidencePending {
			t.Errorf("%s has no evidence qualification", target.ID)
		}
		if target.Qualification == EvidenceObserved {
			if len(target.Observations) == 0 {
				t.Errorf("%s is observed without an observation", target.ID)
			}
			for _, observation := range target.Observations {
				if observation.Product == "" || observation.Version == "" || observation.MacOS == "" || observation.Bytes <= 0 || observation.ObservedAt.IsZero() {
					t.Errorf("%s has an incomplete observation: %+v", target.ID, observation)
				}
			}
		}
		if target.Group == "" {
			t.Errorf("%s has no group", target.ID)
		}
		if target.Category == "" {
			t.Errorf("%s has no storage category", target.ID)
		}
		if target.Recovery == "" {
			t.Errorf("%s does not say how recoverable it is", target.ID)
		}
		if len(target.Paths) == 0 {
			t.Errorf("%s has no paths", target.ID)
		}
		if RiskOrder(target.Risk) > RiskOrder(RiskSafe) && len(target.NotTargets) == 0 {
			t.Errorf("%s is %s but does not name what it leaves alone", target.ID, target.Risk)
		}
	}
}

func TestParityCatalogueCount(t *testing.T) {
	if count := len(All()); count != 99 {
		t.Fatalf("catalogue has %d targets, update the pinned parity ledger", count)
	}
}

func TestEveryGroupIsOrdered(t *testing.T) {
	known := make(map[Group]bool, len(GroupOrder))
	for _, group := range GroupOrder {
		known[group] = true
	}
	for _, target := range All() {
		if !known[target.Group] {
			t.Errorf("%s is in group %q, which is not in GroupOrder", target.ID, target.Group)
		}
	}
}

func TestProcessGuardFailsClosedWithoutAProcessList(t *testing.T) {
	guard := ProcessNotRunning("Example")
	ok, reason := guard.Allow(context.Background(), Env{}, "/Users/someone/Library/Caches/example")
	if ok || !strings.Contains(reason, "unavailable") {
		t.Fatalf("unknown process state was allowed: ok=%v reason=%q", ok, reason)
	}
	ok, reason = guard.Allow(context.Background(), Env{ProcessesKnown: true, Processes: map[string]bool{}}, "/Users/someone/Library/Caches/example")
	if !ok || reason != "" {
		t.Fatalf("known empty process state was refused: ok=%v reason=%q", ok, reason)
	}
}

// A literal home-relative path must survive the safety validator against a real
// home, otherwise the target can never fire and nobody would notice.
func TestLiteralPathsSurviveTheValidator(t *testing.T) {
	const home = "/Users/someone"
	for _, target := range All() {
		for _, spec := range target.Paths {
			if spec.Kind != PathHome {
				continue
			}
			path := filepath.Join(home, spec.Pattern)
			if err := safety.ValidateForDeletion(path); err != nil {
				t.Errorf("%s targets %s, which the validator refuses: %v", target.ID, path, err)
			}
		}
	}
}

func TestAbsoluteGlobDoesNotMoveUnderTheHome(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rotated.gz"), 1)
	target := Target{Paths: []PathSpec{Glob(filepath.Join(root, "*.gz"))}}
	paths := target.Expand(context.Background(), Env{Home: "/Users/someone"})
	if len(paths) != 1 || paths[0] != filepath.Join(root, "rotated.gz") {
		t.Fatalf("absolute glob paths = %v", paths)
	}
}

func TestFinderMetadataSkipsTrashAndSymlinkedTrees(t *testing.T) {
	home := t.TempDir()
	visible := filepath.Join(home, "Documents", ".DS_Store")
	write(t, visible, 1)
	write(t, filepath.Join(home, ".Trash", ".DS_Store"), 1)
	outside := t.TempDir()
	write(t, filepath.Join(outside, ".DS_Store"), 1)
	if err := os.Symlink(outside, filepath.Join(home, "linked")); err != nil {
		t.Fatal(err)
	}
	paths := finderMetadata(home)
	if len(paths) != 1 || paths[0] != visible {
		t.Fatalf("finder metadata = %v", paths)
	}
}

func TestResolveMeasuresAndGuards(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "Library", "Caches", "com.example.app", "blob"), 4096)
	write(t, filepath.Join(home, "Library", "Caches", "com.1password.app", "blob"), 4096)

	target := Target{
		ID:       "test-caches",
		Name:     "test caches",
		Group:    GroupAppCaches,
		Category: storage.CategorySystemData,
		Risk:     RiskSafe,
		Recovery: safety.RecoveryTrash,
		Paths:    []PathSpec{Glob("Library/Caches/*")},
		Guards:   []Guard{NotDataProtected()},
		Evidence: "fixture",
	}

	candidates := Resolve(context.Background(), testEnv(t, home), []Target{target}, Options{})
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
	candidate := candidates[0]
	if len(candidate.Paths()) != 1 {
		t.Fatalf("got paths %v, want only the unprotected one", candidate.Paths())
	}
	if filepath.Base(candidate.Paths()[0]) != "com.example.app" {
		t.Errorf("kept the wrong path: %s", candidate.Paths()[0])
	}
	if candidate.Bytes() <= 0 {
		t.Error("the candidate was not measured")
	}
	if len(candidate.Skipped) != 1 || !strings.Contains(candidate.Skipped[0].Reason, "credentials") {
		t.Errorf("the protected path was not skipped with a reason: %+v", candidate.Skipped)
	}
}

// The preview and the removal call the same guard chain, so a target that stops
// qualifying between the two has to disappear rather than be removed anyway.
func TestRecheckUsesTheSameGuards(t *testing.T) {
	home := t.TempDir()
	stale := filepath.Join(home, "Library", "Saved Application State", "com.example.app.savedState")
	write(t, filepath.Join(stale, "data"), 1024)
	old := time.Now().Add(-90 * 24 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("ageing the fixture: %v", err)
	}

	target := Target{
		ID:       "test-saved-state",
		Name:     "saved state",
		Group:    GroupUserEssentials,
		Category: storage.CategorySystemData,
		Risk:     RiskSafe,
		Recovery: safety.RecoveryTrash,
		Paths:    []PathSpec{Glob("Library/Saved Application State/*.savedState")},
		Guards:   []Guard{OlderThan(30 * 24 * time.Hour)},
		Evidence: "fixture",
	}
	env := testEnv(t, home)

	candidates := Resolve(context.Background(), env, []Target{target}, Options{})
	if len(candidates[0].Paths()) != 1 {
		t.Fatalf("the aged fixture was not offered: %+v", candidates[0])
	}

	allowed, skipped := RecheckPaths(context.Background(), env, target, candidates[0].Paths())
	if len(allowed) != 1 || len(skipped) != 0 {
		t.Fatalf("the recheck disagreed with the preview: allowed=%v skipped=%+v", allowed, skipped)
	}

	now := time.Now()
	if err := os.Chtimes(stale, now, now); err != nil {
		t.Fatalf("touching the fixture: %v", err)
	}
	allowed, skipped = RecheckPaths(context.Background(), env, target, candidates[0].Paths())
	if len(allowed) != 0 || len(skipped) != 1 {
		t.Fatalf("a target that stopped qualifying was still allowed: allowed=%v skipped=%+v", allowed, skipped)
	}
}

func TestRecheckDropsAPathThatWentAway(t *testing.T) {
	home := t.TempDir()
	target := Target{
		ID:       "test-gone",
		Name:     "gone",
		Group:    GroupUserEssentials,
		Category: storage.CategorySystemData,
		Risk:     RiskSafe,
		Recovery: safety.RecoveryTrash,
		Paths:    []PathSpec{Home("Library/Caches/gone")},
		Evidence: "fixture",
	}
	allowed, skipped := RecheckPaths(context.Background(), testEnv(t, home), target,
		[]string{filepath.Join(home, "Library", "Caches", "gone")})
	if len(allowed) != 0 {
		t.Errorf("a missing path was allowed: %v", allowed)
	}
	if len(skipped) != 1 || skipped[0].Reason != "already gone" {
		t.Errorf("the missing path was not reported: %+v", skipped)
	}
}

// A path claimed by two targets is counted once, so the total the interface
// prints is the total the user gets back.
func TestOnePathIsCountedOnce(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "Library", "Caches", "shared", "blob"), 8192)

	first := Target{
		ID: "first", Name: "first", Group: GroupAppCaches,
		Category: storage.CategorySystemData, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
		Paths: []PathSpec{Home("Library/Caches/shared")}, Evidence: "fixture",
	}
	second := first
	second.ID = "second"
	second.Name = "second"

	candidates := Resolve(context.Background(), testEnv(t, home), []Target{first, second}, Options{})
	if len(candidates[0].Paths()) != 1 {
		t.Fatalf("the first target did not claim the path: %+v", candidates[0])
	}
	if len(candidates[1].Paths()) != 0 {
		t.Errorf("the second target counted the same path again: %+v", candidates[1])
	}
	if len(candidates[1].Skipped) != 1 || !strings.Contains(candidates[1].Skipped[0].Reason, "first") {
		t.Errorf("the duplicate was not explained: %+v", candidates[1].Skipped)
	}
}

func TestRefusedSpecificTargetStillOwnsItsPath(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "Library", "Caches", "org.example.tool")
	write(t, filepath.Join(path, "blob"), 4096)
	env := testEnv(t, home)
	env.Processes["tool"] = true

	specific := Target{
		ID: "specific", Name: "specific", Group: GroupDeveloper,
		Category: storage.CategoryDeveloper, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
		Paths: []PathSpec{Home("Library/Caches/org.example.tool")}, Guards: []Guard{ProcessNotRunning("tool")}, Evidence: "fixture",
	}
	broad := Target{
		ID: "broad", Name: "broad", Group: GroupLeftovers,
		Category: storage.CategorySystemData, Risk: RiskReview, Recovery: safety.RecoveryTrash,
		Paths: []PathSpec{Glob("Library/Caches/*")}, Evidence: "fixture", NotTargets: []string{"specific targets"},
	}
	candidates := Resolve(context.Background(), env, []Target{specific, broad}, Options{})
	if len(candidates[0].Measurements) != 0 || len(candidates[1].Measurements) != 0 {
		t.Fatalf("guard-refused specific path leaked into a broad target: %+v", candidates)
	}
}

// A sweep must not take a directory a specific target knows how to handle,
// because the specific target is the one carrying the process guard.
func TestSpecificTargetsClaimBeforeSweeps(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "Library", "Caches", "com.example.app", "blob"), 4096)

	sweep := Target{
		ID: "sweep", Name: "sweep", Group: GroupUserEssentials,
		Category: storage.CategorySystemData, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
		Paths: []PathSpec{Glob("Library/Caches/*")}, Sweep: true, Evidence: "fixture",
	}
	specific := Target{
		ID: "specific", Name: "specific", Group: GroupBrowsers,
		Category: storage.CategorySystemData, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
		Paths: []PathSpec{Home("Library/Caches/com.example.app")}, Evidence: "fixture",
	}

	// The sweep is listed first on purpose: display order must not decide who
	// claims a path.
	candidates := Resolve(context.Background(), testEnv(t, home), []Target{sweep, specific}, Options{})
	if len(candidates[0].Paths()) != 0 {
		t.Errorf("the sweep took a path the specific target owns: %v", candidates[0].Paths())
	}
	if len(candidates[1].Paths()) != 1 {
		t.Errorf("the specific target did not claim its path: %+v", candidates[1])
	}
}

// A sweep that contains an already-claimed subtree still gets a row, but only
// for the bytes nobody else counted.
func TestSweepExcludesBytesAlreadyClaimed(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "Library", "Caches", "Vendor", "Product", "big"), 64*1024)
	write(t, filepath.Join(home, "Library", "Caches", "Vendor", "own"), 8*1024)

	specific := Target{
		ID: "specific", Name: "specific", Group: GroupBrowsers,
		Category: storage.CategorySystemData, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
		Paths: []PathSpec{Home("Library/Caches/Vendor/Product")}, Evidence: "fixture",
	}
	sweep := Target{
		ID: "sweep", Name: "sweep", Group: GroupUserEssentials,
		Category: storage.CategorySystemData, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
		Paths: []PathSpec{Glob("Library/Caches/*")}, Sweep: true, Evidence: "fixture",
	}

	candidates := Resolve(context.Background(), testEnv(t, home), []Target{specific, sweep}, Options{})
	specificBytes := candidates[0].Bytes()
	sweepBytes := candidates[1].Bytes()
	if specificBytes < 64*1024 {
		t.Fatalf("the specific target measured %d bytes, want at least the 64k payload", specificBytes)
	}
	if sweepBytes >= specificBytes {
		t.Errorf("the sweep counted the claimed subtree again: sweep=%d specific=%d", sweepBytes, specificBytes)
	}
}

func TestWhitelistBlocksByIDAndByPath(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "Library", "Caches", "com.example.app", "blob"), 4096)
	write(t, filepath.Join(home, "Library", "Caches", "com.other.app", "blob"), 4096)

	configDir := filepath.Join(home, "config")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatalf("creating the config dir: %v", err)
	}
	entries := "~/Library/Caches/com.other.app\nblocked-target\n"
	if err := os.WriteFile(filepath.Join(configDir, config.WhitelistFile), []byte(entries), 0600); err != nil {
		t.Fatalf("writing the whitelist: %v", err)
	}
	env := testEnv(t, home)

	target := Target{
		ID: "kept", Name: "kept", Group: GroupAppCaches,
		Category: storage.CategorySystemData, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
		Paths: []PathSpec{Glob("Library/Caches/*")}, Evidence: "fixture",
	}
	blocked := target
	blocked.ID = "blocked-target"

	candidates := Resolve(context.Background(), env, []Target{target, blocked}, Options{})
	for _, path := range candidates[0].Paths() {
		if strings.Contains(path, "com.other.app") {
			t.Error("a whitelisted path was offered")
		}
	}
	if len(candidates[1].Paths()) != 0 {
		t.Errorf("a whitelisted target id was offered: %+v", candidates[1].Paths())
	}
}

func TestBundleFromPath(t *testing.T) {
	cases := map[string]string{
		"/Users/x/Library/Caches/com.example.app":                             "com.example.app",
		"/Users/x/Library/Saved Application State/com.example.app.savedState": "com.example.app",
		"/Users/x/Library/Caches/Yarn":                                        "",
		"/Users/x/Library/Caches/some.thing":                                  "",
		"/Users/x/Library/Caches/a..b":                                        "",
	}
	for input, want := range cases {
		if got := BundleFromPath(input); got != want {
			t.Errorf("BundleFromPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBundleFromContainer(t *testing.T) {
	cases := map[string]string{
		"/Users/x/Library/Containers/com.example.app/Data/Library/Caches":    "com.example.app",
		"/Users/x/Library/Group Containers/group.com.example/Library/Caches": "group.com.example",
		"/Users/x/Library/Caches/com.example.app":                            "",
	}
	for input, want := range cases {
		if got := BundleFromContainer(input); got != want {
			t.Errorf("BundleFromContainer(%q) = %q, want %q", input, got, want)
		}
	}
}

// AppAbsent decides that an application no longer exists, and then something
// removes its data on the strength of that. An incomplete index means the scan
// could not tell, which must never read as "it is gone".
func TestAppAbsentRefusesWhenItCannotTell(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "Library", "Caches", "com.example.app", "blob"), 4096)

	target := Target{
		ID: "leftovers", Name: "leftovers", Group: GroupLeftovers,
		Category: storage.CategorySystemData, Risk: RiskReview, Recovery: safety.RecoveryTrash,
		Paths: []PathSpec{Glob("Library/Caches/*")}, Guards: []Guard{AppAbsent()},
		Evidence: "fixture", NotTargets: []string{"fixture"},
	}

	env := testEnv(t, home)
	env.Installed = nil
	candidates := Resolve(context.Background(), env, []Target{target}, Options{})
	if len(candidates[0].Paths()) != 0 {
		t.Fatal("an empty installed index allowed a removal")
	}
	if len(candidates[0].Skipped) == 0 || !strings.Contains(candidates[0].Skipped[0].Reason, "incomplete") {
		t.Errorf("the refusal was not explained: %+v", candidates[0].Skipped)
	}
}

func TestAppAbsentKeepsInstalledAppsData(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "Library", "Caches", "com.example.app", "blob"), 4096)
	write(t, filepath.Join(home, "Library", "Caches", "com.gone.app", "blob"), 4096)

	target := Target{
		ID: "leftovers", Name: "leftovers", Group: GroupLeftovers,
		Category: storage.CategorySystemData, Risk: RiskReview, Recovery: safety.RecoveryTrash,
		Paths: []PathSpec{Glob("Library/Caches/*")}, Guards: []Guard{AppAbsent()},
		Evidence: "fixture", NotTargets: []string{"fixture"},
	}

	env := testEnv(t, home)
	env.Installed = map[string]bool{"com.example.app": true}
	candidates := Resolve(context.Background(), env, []Target{target}, Options{})

	paths := candidates[0].Paths()
	if len(paths) != 1 {
		t.Fatalf("got %v, want only the abandoned one", paths)
	}
	if filepath.Base(paths[0]) != "com.gone.app" {
		t.Errorf("kept the wrong path: %s", paths[0])
	}
	for _, skip := range candidates[0].Skipped {
		if strings.Contains(skip.Path, "com.example.app") && !strings.Contains(skip.Reason, "still installed") {
			t.Errorf("an installed app's data was skipped for the wrong reason: %+v", skip)
		}
	}
}

// A helper bundle keeps its parent alive, so com.example.app.helper must not
// read as abandoned merely because no .app is named that.
func TestAppPresentCountsHelpers(t *testing.T) {
	env := Env{Installed: map[string]bool{"com.example.app": true, "com.example.app.helper": true}, InstalledKnown: true}
	if !env.AppPresent("com.example.app.helper") {
		t.Error("a helper of an installed app read as absent")
	}
	if env.AppPresent("com.other.app") {
		t.Error("an unrelated bundle read as present")
	}
}

// Root-only targets have to be visible without root so the interface can say
// what sudo would add, but never selectable.
func TestSystemTargetsNeedRoot(t *testing.T) {
	var systemTargets int
	for _, target := range All() {
		if target.Group != GroupSystem {
			continue
		}
		systemTargets++
		var guarded bool
		for _, guard := range target.Guards {
			if strings.Contains(guard.Name, "--root") {
				guarded = true
			}
		}
		if !guarded {
			t.Errorf("%s is in the system group but does not require root", target.ID)
		}
		ok, reason := target.Allows(context.Background(), Env{Home: "/Users/someone"}, "/Library/Caches/example")
		if ok {
			t.Errorf("%s was allowed without root", target.ID)
		}
		if !strings.Contains(reason, "root") {
			t.Errorf("%s refused for the wrong reason: %s", target.ID, reason)
		}
	}
	if systemTargets == 0 {
		t.Fatal("no system targets exist")
	}
}

func TestLeftoverTargetsAllGuardOnAbsence(t *testing.T) {
	found := 0
	for _, target := range All() {
		if target.Group != GroupLeftovers {
			continue
		}
		found++
		var guarded bool
		for _, guard := range target.Guards {
			if strings.Contains(guard.Name, "no longer installed") {
				guarded = true
			}
		}
		if !guarded {
			t.Errorf("%s removes leftovers without checking the app is gone", target.ID)
		}
	}
	if found == 0 {
		t.Fatal("no leftover targets exist")
	}
}

// These are the real directory names that a first version of AppAbsent offered
// for deletion on a live machine. CloudDocs is iCloud Drive's local data and
// FileProvider is system state; the rest belong to installed applications whose
// display name differs from their directory. None of them is named for a bundle
// id, and that is exactly why none of them may ever qualify.
func TestLeftoversNeverMatchBareVendorDirectories(t *testing.T) {
	home := t.TempDir()
	dangerous := []string{
		"CloudDocs", "FileProvider", "Claude", "Perplexity", "Razer",
		"Paradox Interactive", "Google", "Steam", "discordptb", "Firefox",
		"MobileSync", "CrashReporter", "Microsoft", "Adobe",
	}
	for _, name := range dangerous {
		write(t, filepath.Join(home, "Library", "Application Support", name, "blob"), 4096)
	}
	write(t, filepath.Join(home, "Library", "Application Support", "com.gone.app", "blob"), 4096)

	target := Target{
		ID: "leftovers", Name: "leftovers", Group: GroupLeftovers,
		Category: storage.CategorySystemData, Risk: RiskReview, Recovery: safety.RecoveryTrash,
		Paths: []PathSpec{Glob("Library/Application Support/*")}, Guards: []Guard{AppAbsent()},
		Evidence: "fixture", NotTargets: []string{"fixture"},
	}

	env := testEnv(t, home)
	env.Installed = map[string]bool{"com.example.app": true}
	candidates := Resolve(context.Background(), env, []Target{target}, Options{})

	for _, path := range candidates[0].Paths() {
		base := filepath.Base(path)
		for _, name := range dangerous {
			if base == name {
				t.Errorf("offered %q for deletion, which is not named for any bundle id", name)
			}
		}
	}
	// The one directory that is named for an absent bundle id still qualifies,
	// or the guard would be refusing everything rather than refusing correctly.
	found := false
	for _, path := range candidates[0].Paths() {
		if filepath.Base(path) == "com.gone.app" {
			found = true
		}
	}
	if !found {
		t.Error("a genuinely abandoned bundle-id directory was not offered")
	}
}

// A system bundle is never a leftover, whatever the installed index says.
func TestLeftoversSkipSystemBundles(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "Library", "Caches", "com.apple.Something", "blob"), 4096)

	target := Target{
		ID: "leftovers", Name: "leftovers", Group: GroupLeftovers,
		Category: storage.CategorySystemData, Risk: RiskReview, Recovery: safety.RecoveryTrash,
		Paths: []PathSpec{Glob("Library/Caches/*")}, Guards: []Guard{AppAbsent()},
		Evidence: "fixture", NotTargets: []string{"fixture"},
	}
	env := testEnv(t, home)
	env.Installed = map[string]bool{"com.example.app": true}

	candidates := Resolve(context.Background(), env, []Target{target}, Options{})
	if len(candidates[0].Paths()) != 0 {
		t.Errorf("an Apple bundle was offered as a leftover: %v", candidates[0].Paths())
	}
}

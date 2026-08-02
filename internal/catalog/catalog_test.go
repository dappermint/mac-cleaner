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
		Home:      home,
		Whitelist: whitelist,
		Processes: map[string]bool{},
		Now:       time.Now(),
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

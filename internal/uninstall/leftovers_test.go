package uninstall

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dappermint/ratatouille/internal/storage"
)

func makeEntry(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	if strings.HasSuffix(path, ".plist") || strings.HasSuffix(path, ".binarycookies") {
		if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		return
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	if err := os.WriteFile(filepath.Join(path, "payload"), []byte("x"), 0600); err != nil {
		t.Fatalf("writing payload: %v", err)
	}
}

func found(leftovers []Leftover, path string) bool {
	for _, leftover := range leftovers {
		if leftover.Path == path {
			return true
		}
	}
	return false
}

func exampleApp() App {
	return App{
		Path:   filepath.Join("/Applications", "Example.app"),
		Name:   "Example",
		Bundle: "com.example.app",
		Scope:  ScopeLocal,
	}
}

func TestLeftoversAcceptOnlyExactEvidence(t *testing.T) {
	home := t.TempDir()
	app := exampleApp()

	accepted := map[string]Evidence{
		filepath.Join(home, "Library/Caches/com.example.app"):                             EvidenceBundle,
		filepath.Join(home, "Library/Containers/com.example.app"):                         EvidenceBundle,
		filepath.Join(home, "Library/Preferences/com.example.app.plist"):                  EvidenceBundle,
		filepath.Join(home, "Library/Saved Application State/com.example.app.savedState"): EvidenceBundle,
		filepath.Join(home, "Library/Cookies/com.example.app.binarycookies"):              EvidenceBundle,
		filepath.Join(home, "Library/Caches/com.example.app.helper"):                      EvidenceHelper,
		filepath.Join(home, "Library/LaunchAgents/com.example.app.updater.plist"):         EvidenceHelper,
		filepath.Join(home, "Library/Group Containers/AB12CD34EF.com.example.app"):        EvidenceGroup,
		filepath.Join(home, "Library/Application Support/Example"):                        EvidenceName,
		filepath.Join(home, "Library/Logs/Example"):                                       EvidenceName,
	}
	rejected := []string{
		// a vendor prefix is not evidence about one app
		filepath.Join(home, "Library/Caches/com.example.other"),
		filepath.Join(home, "Library/Caches/com.example"),
		// a longer id that merely starts with ours
		filepath.Join(home, "Library/Caches/com.example.application"),
		// a helper suffix on somebody else's id
		filepath.Join(home, "Library/Caches/com.other.app.helper"),
		// a group container prefix that is not a team id
		filepath.Join(home, "Library/Group Containers/group.com.example.app"),
		filepath.Join(home, "Library/Group Containers/ab12cd34ef.com.example.app"),
		// display names outside the two locations that use them
		filepath.Join(home, "Library/Caches/Example"),
		filepath.Join(home, "Library/Containers/Example"),
		// unrelated
		filepath.Join(home, "Library/Application Support/ExampleOther"),
		filepath.Join(home, "Library/Application Support/Other"),
	}

	for path := range accepted {
		makeEntry(t, path)
	}
	// The group container directory has to exist for the rejected cases too.
	for _, path := range rejected {
		makeEntry(t, path)
	}

	leftovers, _ := Leftovers(context.Background(), Env{Home: home}, app, nil)

	for path, evidence := range accepted {
		if !found(leftovers, path) {
			t.Errorf("did not find %s, which is %s", path, evidence)
			continue
		}
		for _, leftover := range leftovers {
			if leftover.Path == path && leftover.Evidence != evidence {
				t.Errorf("%s: evidence = %q, want %q", path, leftover.Evidence, evidence)
			}
		}
	}
	for _, path := range rejected {
		if found(leftovers, path) {
			t.Errorf("matched %s, which is not evidence about this app", path)
		}
	}
}

// A generic word is never evidence, no matter which location it is in.
func TestGenericNamesNeverMatch(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"Support", "Updater", "Data", "Logs", "Helper"} {
		app := App{Path: "/Applications/" + name + ".app", Name: name, Bundle: "com.vendor." + strings.ToLower(name)}
		path := filepath.Join(home, "Library/Application Support", name)
		makeEntry(t, path)

		leftovers, _ := Leftovers(context.Background(), Env{Home: home}, app, nil)
		if found(leftovers, path) {
			t.Errorf("matched the generic name %q", name)
		}
	}
}

func TestShortNamesNeverMatch(t *testing.T) {
	home := t.TempDir()
	app := App{Path: "/Applications/Go.app", Name: "Go", Bundle: "com.vendor.go"}
	path := filepath.Join(home, "Library/Application Support", "Go")
	makeEntry(t, path)

	leftovers, _ := Leftovers(context.Background(), Env{Home: home}, app, nil)
	if found(leftovers, path) {
		t.Error("matched a name below the length floor")
	}
}

// If a second installed app could also claim a file, neither app may remove it.
func TestSiblingGuard(t *testing.T) {
	cases := []struct {
		name    string
		sibling App
		entry   string
	}{
		{
			name:    "a second copy of the same app on another volume",
			sibling: App{Path: "/Volumes/Backup/Example.app", Name: "Example", Bundle: "com.example.app"},
			entry:   "Library/Caches/com.example.app",
		},
		{
			name:    "another app that shares the bundle id",
			sibling: App{Path: "/Applications/Example Pro.app", Name: "Example Pro", Bundle: "com.example.app"},
			entry:   "Library/Preferences/com.example.app.plist",
		},
		{
			name:    "another app with the same display name",
			sibling: App{Path: "/Users/someone/Applications/Example.app", Name: "Example", Bundle: "com.other.example"},
			entry:   "Library/Application Support/Example",
		},
		{
			name:    "another app that owns the same helper",
			sibling: App{Path: "/Applications/Other.app", Name: "Other", Bundle: "com.example.app.helper"},
			entry:   "Library/Caches/com.example.app.helper",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			home := t.TempDir()
			app := exampleApp()
			path := filepath.Join(home, testCase.entry)
			makeEntry(t, path)

			leftovers, skipped := Leftovers(context.Background(), Env{Home: home}, app, []App{app, testCase.sibling})
			if found(leftovers, path) {
				t.Fatalf("removed %s, which %s also claims", path, testCase.sibling.Name)
			}
			if len(skipped) == 0 {
				t.Fatal("the conflict was not reported")
			}
			if !strings.Contains(skipped[0].Reason, testCase.sibling.Name) {
				t.Errorf("the skip did not name the sibling: %+v", skipped[0])
			}
		})
	}
}

func TestDataProtectedLeftoversAreSkipped(t *testing.T) {
	home := t.TempDir()
	app := App{Path: "/Applications/1Password.app", Name: "1Password", Bundle: "com.1password.app"}
	path := filepath.Join(home, "Library/Caches/com.1password.app")
	makeEntry(t, path)

	leftovers, skipped := Leftovers(context.Background(), Env{Home: home}, app, nil)
	if found(leftovers, path) {
		t.Error("a data-protected bundle was offered for removal")
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "credentials") {
		t.Errorf("the refusal was not explained: %+v", skipped)
	}
}

func TestRootLocationsNeedRoot(t *testing.T) {
	home := t.TempDir()
	app := exampleApp()
	if _, skipped := Leftovers(context.Background(), Env{Home: home, Rootful: false}, app, nil); len(skipped) != 0 {
		t.Errorf("an unprivileged run looked at root locations: %+v", skipped)
	}
}

func TestAnAppWithoutABundleIDIsRefused(t *testing.T) {
	home := t.TempDir()
	app := App{Path: "/Applications/Mystery.app", Name: "Mystery"}
	leftovers, skipped := Leftovers(context.Background(), Env{Home: home}, app, nil)
	if len(leftovers) != 0 {
		t.Error("matched files for an app with no identifier")
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "no bundle identifier") {
		t.Errorf("the refusal was not explained: %+v", skipped)
	}
}

func TestTeamPrefixShape(t *testing.T) {
	cases := map[string]bool{
		"AB12CD34EF.com.example.app":   true,
		"6N38VWS5BX.com.example.app":   true,
		"ab12cd34ef.com.example.app":   false,
		"AB12CD34E.com.example.app":    false,
		"AB12CD34EFG.com.example.app":  false,
		"group.com.example.app":        false,
		"com.example.app":              false,
		"AB12CD34EF.com.example.other": false,
	}
	for name, want := range cases {
		if got := teamPrefixed(name, "com.example.app"); got != want {
			t.Errorf("teamPrefixed(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestFindRefusesToGuess(t *testing.T) {
	apps := []App{
		{Name: "Example", Bundle: "com.example.app"},
		{Name: "Example Pro", Bundle: "com.example.pro"},
		{Name: "Unrelated", Bundle: "com.other.app"},
	}

	if app, _ := Find(apps, "Example"); app.Bundle != "com.example.app" {
		t.Errorf("an exact name did not win: %+v", app)
	}
	if app, _ := Find(apps, "com.example.pro"); app.Name != "Example Pro" {
		t.Errorf("a bundle id did not resolve: %+v", app)
	}
	if app, _ := Find(apps, "unrelate"); app.Name != "Unrelated" {
		t.Errorf("a unique partial did not resolve: %+v", app)
	}

	app, candidates := Find(apps, "exampl")
	if app.Name != "" {
		t.Errorf("an ambiguous query resolved to %q instead of asking", app.Name)
	}
	if len(candidates) != 2 {
		t.Errorf("got %d candidates, want both matches", len(candidates))
	}
}

func TestFindRefusesDuplicateExactNames(t *testing.T) {
	apps := []App{
		{Name: "Example", Bundle: "com.example.local", Path: "/Applications/Example.app"},
		{Name: "Example", Bundle: "com.example.user", Path: "/Users/someone/Applications/Example.app"},
	}

	if app, candidates := Find(apps, "Example"); app.Path != "" || len(candidates) != 2 {
		t.Fatalf("duplicate exact name resolved to %+v with candidates %+v", app, candidates)
	}
	if app, candidates := Find(apps, "com.example.user"); app.Path != apps[1].Path || len(candidates) != 0 {
		t.Fatalf("bundle selector resolved to %+v with candidates %+v", app, candidates)
	}
	if app, candidates := Find(apps, apps[0].Path); app.Path != apps[0].Path || len(candidates) != 0 {
		t.Fatalf("path selector resolved to %+v with candidates %+v", app, candidates)
	}
}

func TestFindRefusesDuplicateBundleCopies(t *testing.T) {
	apps := []App{
		{Name: "Example", Bundle: "com.example.app", Path: "/Applications/Example.app"},
		{Name: "Example Copy", Bundle: "com.example.app", Path: "/Users/someone/Applications/Example.app"},
	}

	if app, candidates := Find(apps, "com.example.app"); app.Path != "" || len(candidates) != 2 {
		t.Fatalf("duplicate bundle resolved to %+v with candidates %+v", app, candidates)
	}
}

func TestServiceDomainUsesTheInvokingUser(t *testing.T) {
	identity := &storage.CommandIdentity{UID: 501}
	if domain, commandIdentity := serviceDomain("launch agent", identity); domain != "gui/501" || commandIdentity != identity {
		t.Fatalf("launch agent domain = %q identity=%p", domain, commandIdentity)
	}
	if domain, commandIdentity := serviceDomain("launch daemon", identity); domain != "system" || commandIdentity != nil {
		t.Fatalf("launch daemon domain = %q identity=%p", domain, commandIdentity)
	}
}

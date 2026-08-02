package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	home := t.TempDir()
	directory := filepath.Join(home, "config")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatalf("creating the config dir: %v", err)
	}
	t.Setenv(EnvDir, directory)
	if err := os.WriteFile(filepath.Join(directory, WhitelistFile), []byte(contents), 0600); err != nil {
		t.Fatalf("writing the whitelist: %v", err)
	}
	return home
}

func TestWhitelistMatchesIDsPathsAndPatterns(t *testing.T) {
	home := writeConfig(t, `
# a comment, and a blank line follow

some-target-id
~/Library/Caches/com.example.app
/Volumes/mail
/Users/*/Library/Caches/globbed
`)
	list, err := LoadWhitelist(home, WhitelistFile)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	cases := []struct {
		id, path string
		blocked  bool
	}{
		{id: "some-target-id", blocked: true},
		{id: "other-target-id", blocked: false},
		{path: filepath.Join(home, "Library/Caches/com.example.app"), blocked: true},
		{path: filepath.Join(home, "Library/Caches/com.example.app/inner"), blocked: true},
		{path: filepath.Join(home, "Library/Caches/com.other.app"), blocked: false},
		{path: "/Volumes/mail", blocked: true},
		{path: "/Volumes/mail/inbox", blocked: true},
		{path: "/Volumes/other", blocked: false},
		{path: "/Users/someone/Library/Caches/globbed", blocked: true},
	}
	for _, testCase := range cases {
		if got := list.Blocks(testCase.id, testCase.path); got != testCase.blocked {
			t.Errorf("Blocks(%q, %q) = %v, want %v", testCase.id, testCase.path, got, testCase.blocked)
		}
	}
}

func TestMissingWhitelistBlocksNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvDir, filepath.Join(home, "no-such-dir"))
	list, err := LoadWhitelist(home, WhitelistFile)
	if err != nil {
		t.Fatalf("a missing whitelist returned an error: %v", err)
	}
	if !list.Empty() {
		t.Error("a missing whitelist was not empty")
	}
	if list.Blocks("anything", "/Users/someone/Library/Caches/x") {
		t.Error("an empty whitelist blocked something")
	}
}

func TestNilWhitelistIsSafe(t *testing.T) {
	var list *Whitelist
	if list.Blocks("id", "/path") {
		t.Error("a nil whitelist blocked something")
	}
	if !list.Empty() {
		t.Error("a nil whitelist is not empty")
	}
}

func TestDirPrefersTheExplicitOverride(t *testing.T) {
	t.Setenv(EnvDir, "/tmp/explicit")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	if got := Dir("/Users/someone"); got != "/tmp/explicit" {
		t.Errorf("Dir = %q, want the explicit override", got)
	}

	t.Setenv(EnvDir, "")
	if got := Dir("/Users/someone"); got != "/tmp/xdg/ratatouille" {
		t.Errorf("Dir = %q, want the XDG path", got)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	if got := Dir("/Users/someone"); got != "/Users/someone/.config/ratatouille" {
		t.Errorf("Dir = %q, want the default", got)
	}
}

func TestSettingsParse(t *testing.T) {
	settings, err := ParseSettings(`
# a comment
keymap = vim
depth = 5
purge.trash = yes

[keys]
mark = m, M
execute-marks = X

[keys.visual]
mark = d
`)
	if err != nil {
		t.Fatalf("ParseSettings: %v", err)
	}
	if got := settings.String("keymap", "default"); got != "vim" {
		t.Errorf("keymap = %q", got)
	}
	if got := settings.Int("depth", 3); got != 5 {
		t.Errorf("depth = %d", got)
	}
	if !settings.Bool("purge.trash", false) {
		t.Error("purge.trash did not read as true")
	}
	if got := settings.List("keys.mark"); len(got) != 2 || got[0] != "m" || got[1] != "M" {
		t.Errorf("keys.mark = %v", got)
	}
	if got := settings.String("keys.visual.mark", ""); got != "d" {
		t.Errorf("keys.visual.mark = %q", got)
	}
	if settings.Has("keys.nothing") {
		t.Error("an absent key reported present")
	}
}

// A line the parser cannot read has to be an error. Skipping it silently means
// a typo turns into a setting that never took effect.
func TestSettingsRejectMalformedLines(t *testing.T) {
	for _, contents := range []string{"keymap vim\n", "= value\n", "[keys]\nbroken line\n"} {
		if _, err := ParseSettings(contents); err == nil {
			t.Errorf("accepted malformed settings: %q", contents)
		}
	}
}

func TestSettingsDurationAcceptsDaysAndWeeks(t *testing.T) {
	settings, err := ParseSettings("a = 7d\nb = 2w\nc = 90m\nd = nonsense\n")
	if err != nil {
		t.Fatalf("ParseSettings: %v", err)
	}
	cases := map[string]time.Duration{
		"a": 7 * 24 * time.Hour,
		"b": 14 * 24 * time.Hour,
		"c": 90 * time.Minute,
		"d": time.Hour,
	}
	for key, want := range cases {
		if got := settings.Duration(key, time.Hour); got != want {
			t.Errorf("Duration(%q) = %s, want %s", key, got, want)
		}
	}
}

func TestMissingSettingsFileIsNotAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvDir, filepath.Join(home, "nothing-here"))
	settings, err := LoadSettings(home)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	preferences := PreferencesFrom(settings)
	if preferences.Keymap != "default" || preferences.Depth != 3 {
		t.Errorf("defaults were not applied: %+v", preferences)
	}
}

func TestColourResolution(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	if !(Preferences{Colour: "auto"}).UseColour(true) {
		t.Error("auto on a terminal should use colour")
	}
	if (Preferences{Colour: "auto"}).UseColour(false) {
		t.Error("auto off a terminal should not use colour")
	}
	if !(Preferences{Colour: "always"}).UseColour(false) {
		t.Error("always should use colour even off a terminal")
	}
	if (Preferences{Colour: "never"}).UseColour(true) {
		t.Error("never should not use colour")
	}
	t.Setenv("NO_COLOR", "1")
	if (Preferences{Colour: "auto"}).UseColour(true) {
		t.Error("NO_COLOR should win under auto")
	}
}

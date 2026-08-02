package config

import (
	"os"
	"path/filepath"
	"testing"
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

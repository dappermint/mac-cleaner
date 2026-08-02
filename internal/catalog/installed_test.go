package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadInstalledFindsNestedApplications(t *testing.T) {
	home := t.TempDir()
	contents := filepath.Join(home, "Applications", "Utilities", "Fixture.app", "Contents")
	if err := os.MkdirAll(contents, 0700); err != nil {
		t.Fatalf("creating app: %v", err)
	}
	info := `<?xml version="1.0"?><plist version="1.0"><dict>` +
		`<key>CFBundleIdentifier</key><string>org.example.fixture</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(info), 0600); err != nil {
		t.Fatalf("writing app info: %v", err)
	}
	installed := ReadInstalled(home)
	if !installed["org.example.fixture"] || !installed["fixture"] {
		t.Fatalf("installed index = %v", installed)
	}
}

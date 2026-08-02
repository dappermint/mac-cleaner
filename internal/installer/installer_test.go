package installer

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func writeZip(t *testing.T, path string, entries ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	writer := zip.NewWriter(file)
	for _, entry := range entries {
		part, err := writer.Create(entry)
		if err != nil {
			t.Fatalf("adding %s: %v", entry, err)
		}
		if _, err := part.Write(make([]byte, 32*1024)); err != nil {
			t.Fatalf("writing %s: %v", entry, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("closing the archive: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("closing the file: %v", err)
	}
}

func names(files []File) map[string]bool {
	found := make(map[string]bool, len(files))
	for _, file := range files {
		found[filepath.Base(file.Path)] = true
	}
	return found
}

func TestFindMatchesInstallerFormats(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"app.dmg", "tool.pkg", "suite.mpkg", "image.iso", "xcode.xip"} {
		writeFile(t, filepath.Join(home, "Downloads", name), 1024)
	}
	for _, name := range []string{"notes.pdf", "photo.png", "archive.tar.gz", "data.json"} {
		writeFile(t, filepath.Join(home, "Downloads", name), 1024)
	}

	found := names(Find(context.Background(), home, Options{MinSize: 1}))
	for _, want := range []string{"app.dmg", "tool.pkg", "suite.mpkg", "image.iso", "xcode.xip"} {
		if !found[want] {
			t.Errorf("%s was not found", want)
		}
	}
	for _, unwanted := range []string{"notes.pdf", "photo.png", "archive.tar.gz", "data.json"} {
		if found[unwanted] {
			t.Errorf("%s is not an installer but was listed", unwanted)
		}
	}
}

// Most zips are not installers, so a zip has to prove it carries one.
func TestZipsMustContainAnAppOrPackage(t *testing.T) {
	home := t.TempDir()
	writeZip(t, filepath.Join(home, "Downloads", "carries-app.zip"), "Thing.app/Contents/Info.plist")
	writeZip(t, filepath.Join(home, "Downloads", "carries-pkg.zip"), "Installer.pkg")
	writeZip(t, filepath.Join(home, "Downloads", "just-documents.zip"), "notes/report.txt", "notes/data.csv")

	found := names(Find(context.Background(), home, Options{MinSize: 1}))
	if !found["carries-app.zip"] {
		t.Error("a zip holding an app was not listed")
	}
	if !found["carries-pkg.zip"] {
		t.Error("a zip holding a package was not listed")
	}
	if found["just-documents.zip"] {
		t.Error("a zip of documents was listed as an installer")
	}
}

func TestSizeFloorIsHonoured(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "Downloads", "big.dmg"), 64*1024)
	writeFile(t, filepath.Join(home, "Downloads", "tiny.dmg"), 128)

	found := names(Find(context.Background(), home, Options{MinSize: 32 * 1024}))
	if !found["big.dmg"] {
		t.Error("a file above the floor was not listed")
	}
	if found["tiny.dmg"] {
		t.Error("a file below the floor was listed")
	}
}

func TestHomebrewNamesAreReadable(t *testing.T) {
	cases := map[string]string{
		"abc123--firefox--120.0.dmg": "firefox--120.0.dmg",
		"plain.dmg":                  "plain.dmg",
	}
	for input, want := range cases {
		if got := displayName("Homebrew", input); got != want {
			t.Errorf("displayName(%q) = %q, want %q", input, got, want)
		}
	}
	if got := displayName("Downloads", "abc--def.dmg"); got != "abc--def.dmg" {
		t.Errorf("a non-Homebrew name was rewritten: %q", got)
	}
}

func TestFilesAreSortedBiggestFirst(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "Downloads", "small.dmg"), 16*1024)
	writeFile(t, filepath.Join(home, "Downloads", "large.dmg"), 128*1024)

	files := Find(context.Background(), home, Options{MinSize: 1})
	if len(files) != 2 {
		t.Fatalf("got %d files", len(files))
	}
	if files[0].Bytes < files[1].Bytes {
		t.Error("not sorted biggest first")
	}
}

func TestTotal(t *testing.T) {
	if got := Total([]File{{Bytes: 100}, {Bytes: 23}}); got != 123 {
		t.Errorf("Total = %d, want 123", got)
	}
}

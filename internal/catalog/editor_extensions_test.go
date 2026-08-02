package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestObsoleteExtensionsTrustOnlyPhysicalDirectChildren(t *testing.T) {
	root := filepath.Join(t.TempDir(), "extensions")
	old := filepath.Join(root, "example.tool-1.0.0")
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(old, filepath.Join(root, "linked-1.0.0")); err != nil {
		t.Fatal(err)
	}
	inventory := `{"example.tool-1.0.0":true,"linked-1.0.0":true,"../escape":true}`
	if err := os.WriteFile(filepath.Join(root, ".obsolete"), []byte(inventory), 0600); err != nil {
		t.Fatal(err)
	}
	paths := listedObsoleteExtensions(root)
	if len(paths) != 1 || paths[0] != old {
		t.Fatalf("obsolete paths = %v", paths)
	}
}

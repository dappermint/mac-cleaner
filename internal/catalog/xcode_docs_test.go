package catalog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestXcodeDocumentationKeepsNewestIndex(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "DeveloperDocumentation-1.index")
	newest := filepath.Join(root, "DeveloperDocumentation-2.index")
	write(t, old, 1)
	write(t, newest, 1)
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	stale := staleXcodeDocumentation(root)
	if len(stale) != 1 || stale[0] != old {
		t.Fatalf("stale indexes = %v", stale)
	}
}

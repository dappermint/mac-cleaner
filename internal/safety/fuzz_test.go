package safety

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzValidateForDeletion asserts the invariant rather than a decision table:
// whatever the validator accepts has to be absolute, free of control bytes,
// free of .. components, and outside every root we said we would never touch.
func FuzzValidateForDeletion(f *testing.F) {
	seeds := []string{
		"",
		"/",
		"/Users/someone/Library/Caches/com.example",
		"/System",
		"/../etc",
		"/Users/someone/../../etc/passwd",
		"//Users//someone//Library//Caches//x",
		"/private/tmp/x",
		"/usr/local/Cellar/x",
		"/Volumes/External/x/y",
		"/System/Volumes/Data/Users/someone",
		"/Library/Updates/x",
		"/Users/someone/Library/Caches/\x00",
		"relative/path",
		"/etc/../Users/someone/Library/Caches/x",
		strings.Repeat("/a", 200),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, candidate string) {
		if err := ValidateForDeletion(candidate); err != nil {
			return
		}
		if !filepath.IsAbs(candidate) {
			t.Fatalf("accepted a relative path: %q", candidate)
		}
		if strings.ContainsRune(candidate, 0) {
			t.Fatalf("accepted a path with a null byte: %q", candidate)
		}
		for _, char := range candidate {
			if char < 32 || char == 127 {
				t.Fatalf("accepted a path with a control character: %q", candidate)
			}
		}
		normalized := Normalize(candidate)
		for _, component := range components(candidate) {
			if component == ".." {
				t.Fatalf("accepted a path with a .. component: %q", candidate)
			}
		}
		if isBareRoot(normalized) {
			t.Fatalf("accepted a bare root: %q", candidate)
		}
		if !isAllowed(normalized) {
			for _, root := range deniedRoots {
				if under(normalized, root) && !under(normalized, "/usr/local") {
					t.Fatalf("accepted %q, which is inside %s", candidate, root)
				}
			}
			for _, protected := range ProtectedPaths() {
				if under(normalized, protected) {
					t.Fatalf("accepted %q, which is inside protected %s", candidate, protected)
				}
			}
		}
	})
}

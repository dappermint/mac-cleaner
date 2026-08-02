package safety

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRejectsEveryDangerousPath(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "dangerous_paths.txt"))
	if err != nil {
		t.Fatalf("reading the corpus: %v", err)
	}
	checked := 0
	for line := range strings.SplitSeq(string(raw), "\n") {
		candidate := strings.TrimRight(line, " \t\r")
		if candidate == "" || strings.HasPrefix(candidate, "#") {
			continue
		}
		checked++
		if err := ValidateForDeletion(candidate); err == nil {
			t.Errorf("accepted a dangerous path: %q", candidate)
		} else if !Refused(err) {
			t.Errorf("path %q was rejected with %v, which is not a refusal", candidate, err)
		}
	}
	if checked < 60 {
		t.Fatalf("the corpus only had %d entries, it should not shrink", checked)
	}
}

func TestValidateRejectsControlCharacters(t *testing.T) {
	for _, candidate := range []string{
		"/Users/someone/Library/Caches/bad\nname",
		"/Users/someone/Library/Caches/bad\tname",
		"/Users/someone/Library/Caches/bad\x00name",
		"/Users/someone/Library/Caches/bad\x1bname",
	} {
		if err := ValidateForDeletion(candidate); err == nil {
			t.Errorf("accepted a path with a control character: %q", candidate)
		}
	}
}

func TestValidateAcceptsRealCleanupTargets(t *testing.T) {
	for _, candidate := range []string{
		"/Users/someone/Library/Caches/com.example.app",
		"/Users/someone/Library/Application Support/SomeApp",
		"/Users/someone/Library/Developer/Xcode/DerivedData",
		"/Users/someone/Downloads/installer.dmg",
		"/Users/someone/.Trash/old-thing",
		"/Library/Caches/com.example",
		"/Library/Logs/Adobe",
		"/private/tmp/build-123",
		"/private/var/log/install.log",
		"/private/var/folders/ab/cd/T/scratch",
		"/private/var/db/diagnostics/Persist/one.tracev3",
		"/System/Library/Caches/com.apple.coresymbolicationd/data/one",
		"/usr/local/Cellar/foo",
		"/opt/homebrew/Cellar/foo",
		"/Applications/Some.app",
		"/Volumes/External/projects/build",
	} {
		if err := ValidateForDeletion(candidate); err != nil {
			t.Errorf("refused a legitimate target %q: %v", candidate, err)
		}
	}
}

func TestValidateFollowsLeafSymlink(t *testing.T) {
	directory := t.TempDir()
	link := filepath.Join(directory, "leaf")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatalf("creating the link: %v", err)
	}
	if err := ValidateForDeletion(link); err == nil {
		t.Fatal("a symlink pointing at /etc was accepted")
	}
}

func TestValidateFollowsAncestorSymlink(t *testing.T) {
	directory := t.TempDir()
	link := filepath.Join(directory, "ancestor")
	if err := os.Symlink("/System/Library", link); err != nil {
		t.Fatalf("creating the link: %v", err)
	}
	if err := ValidateForDeletion(filepath.Join(link, "Caches")); err == nil {
		t.Fatal("a path through an ancestor link into /System was accepted")
	}
}

// Resolution is deny-only: a link that lands somewhere allowed must not turn a
// refused literal into an accepted one.
func TestResolutionNeverGrantsPermission(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "real")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatalf("creating the target: %v", err)
	}
	if err := ValidateForDeletion("/Users/someone/Library"); err == nil {
		t.Fatal("a bare home Library was accepted")
	}
}

func TestNormalizeFoldsTheDataVolume(t *testing.T) {
	cases := map[string]string{
		"/System/Volumes/Data":                    "/",
		"/System/Volumes/Data/Users/someone":      "/Users/someone",
		"/System/Volumes/Data/Users/someone/work": "/Users/someone/work",
		"/Users/someone":                          "/Users/someone",
		"/Users/someone/":                         "/Users/someone",
	}
	for input, want := range cases {
		if got := Normalize(input); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestProtectedPathsAreRefusedWithTheirReason(t *testing.T) {
	err := ValidateForDeletion("/Library/Updates/031-12345")
	if err == nil {
		t.Fatal("a Software Update staging path was accepted")
	}
	if !strings.Contains(err.Error(), "/Library/Updates") {
		t.Errorf("the refusal did not name the protected root: %v", err)
	}
}

// The drift audit is the only thing that notices a new macOS release adding a
// system app, because the runtime com.apple.* blanket would cover it silently.
func TestProtectionTableCoversThisMachine(t *testing.T) {
	missing, checked := AuditSystemBundles()
	if checked == 0 {
		t.Skip("no system applications on this machine")
	}
	if len(missing) > 0 {
		t.Errorf("the protection table has drifted:\n%s", AuditReport(missing, checked))
	}
}

func TestAuditReportSaysWhatToDo(t *testing.T) {
	report := AuditReport([]string{"com.apple.NewThing"}, 200)
	for _, want := range []string{"com.apple.NewThing", "protection.txt", "case sensitive"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not mention %q: %s", want, report)
		}
	}
	if clean := AuditReport(nil, 200); !strings.Contains(clean, "all named") {
		t.Errorf("a clean audit reads oddly: %s", clean)
	}
}

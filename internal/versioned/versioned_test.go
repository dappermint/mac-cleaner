package versioned

import (
	"os"
	"path/filepath"
	"testing"
)

func versions(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range names {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestResolveKeepsActiveAndPrevious(t *testing.T) {
	root := versions(t, "1.9.0", "1.10.0", "2.0.0")
	if err := os.Symlink("1.10.0", filepath.Join(root, "Current")); err != nil {
		t.Fatal(err)
	}
	plan, err := Resolve(Spec{Root: root, ActiveLink: "Current", KeepPrevious: 1})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Active != "1.10.0" || len(plan.Keep) != 2 || len(plan.Stale) != 1 || filepath.Base(plan.Stale[0]) != "1.9.0" {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestBrokenActiveLinkFailsClosed(t *testing.T) {
	root := versions(t, "1.0.0", "2.0.0")
	if err := os.Symlink("3.0.0", filepath.Join(root, "Current")); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(Spec{Root: root, ActiveLink: "Current"}); err == nil {
		t.Fatal("broken active link was accepted")
	}
}

func TestStillStaleReplansAfterActiveChanges(t *testing.T) {
	root := versions(t, "1.0.0", "2.0.0")
	link := filepath.Join(root, "Current")
	if err := os.Symlink("2.0.0", link); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Root: root, ActiveLink: "Current"}
	if ok, _ := StillStale(spec, filepath.Join(root, "1.0.0")); !ok {
		t.Fatal("old version was not stale")
	}
	if err := os.Remove(link); err != nil { //nolint:forbidigo // the fixture changes the active link between checks
		t.Fatal(err)
	}
	if err := os.Symlink("1.0.0", link); err != nil {
		t.Fatal(err)
	}
	if ok, _ := StillStale(spec, filepath.Join(root, "1.0.0")); ok {
		t.Fatal("newly active version remained stale")
	}
}

func TestInstalledVersionKeepsEqualAndPendingUpdates(t *testing.T) {
	root := versions(t, "119.0.0", "120.0.0", "121.0.0")
	plan, err := Resolve(Spec{Root: root, Installed: "120.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Stale) != 1 || filepath.Base(plan.Stale[0]) != "119.0.0" {
		t.Fatalf("stale = %v", plan.Stale)
	}
	if len(plan.Keep) != 2 {
		t.Fatalf("keep = %v", plan.Keep)
	}
}

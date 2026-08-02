package purge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func project(t *testing.T, root, name string, artifacts ...string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(path, "src"), 0700); err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	if err := os.WriteFile(filepath.Join(path, "src", "main.go"), []byte("package main"), 0600); err != nil {
		t.Fatalf("writing source: %v", err)
	}
	for _, artifact := range artifacts {
		directory := filepath.Join(path, artifact)
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatalf("creating %s: %v", directory, err)
		}
		if err := os.WriteFile(filepath.Join(directory, "blob"), make([]byte, 4096), 0600); err != nil {
			t.Fatalf("writing artifact: %v", err)
		}
	}
	return path
}

func age(t *testing.T, path string, when time.Duration) {
	t.Helper()
	stamp := time.Now().Add(-when)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("ageing %s: %v", path, err)
	}
}

func paths(artifacts []Artifact) []string {
	found := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		found = append(found, artifact.Path)
	}
	return found
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestFindOnlyMatchesKnownArtifactNames(t *testing.T) {
	root := t.TempDir()
	old := project(t, root, "old", "node_modules", "dist", "src-that-is-not-an-artifact")
	age(t, old, 30*24*time.Hour)

	artifacts, _ := Find(context.Background(), root, Options{Roots: []string{root}})
	found := paths(artifacts)

	if !contains(found, filepath.Join(old, "node_modules")) {
		t.Errorf("node_modules was not found: %v", found)
	}
	if !contains(found, filepath.Join(old, "dist")) {
		t.Errorf("dist was not found: %v", found)
	}
	for _, path := range found {
		if filepath.Base(path) == "src" || filepath.Base(path) == "src-that-is-not-an-artifact" {
			t.Errorf("matched something that is not on the artifact list: %s", path)
		}
	}
	// The project itself is never a candidate, whatever its age.
	if contains(found, old) {
		t.Error("the project directory itself was listed")
	}
}

// A node_modules inside a node_modules is part of the outer one. Listing both
// would count the same bytes twice and remove a directory already gone.
func TestNestedArtifactsCollapse(t *testing.T) {
	root := t.TempDir()
	outer := project(t, root, "app", "node_modules")
	inner := filepath.Join(outer, "node_modules", "package", "node_modules")
	if err := os.MkdirAll(inner, 0700); err != nil {
		t.Fatalf("creating the nested artifact: %v", err)
	}
	age(t, outer, 30*24*time.Hour)

	artifacts, _ := Find(context.Background(), root, Options{Roots: []string{root}})
	for _, artifact := range artifacts {
		if artifact.Path == inner {
			t.Error("a nested artifact was listed separately from its parent")
		}
	}
}

// Age comes from the project, not the artifact: a build directory's timestamp
// says when it was last built, the project's says when the work happened.
func TestRecentProjectsAreListedButUnselected(t *testing.T) {
	root := t.TempDir()
	stale := project(t, root, "stale", "node_modules")
	fresh := project(t, root, "fresh", "node_modules")
	age(t, stale, 90*24*time.Hour)
	age(t, fresh, time.Hour)

	artifacts, _ := Find(context.Background(), root, Options{Roots: []string{root}, MinAge: 7 * 24 * time.Hour})
	if len(artifacts) != 2 {
		t.Fatalf("got %d artifacts, want both: %v", len(artifacts), paths(artifacts))
	}
	for _, artifact := range artifacts {
		switch filepath.Base(artifact.Project) {
		case "stale":
			if !artifact.Selected() {
				t.Error("an old project was not selected")
			}
		case "fresh":
			if artifact.Selected() {
				t.Error("a project touched an hour ago was selected by default")
			}
			if !artifact.Recent {
				t.Error("a recent project was not marked recent")
			}
		}
	}
}

func TestArtifactsAreSizedAndSortedBiggestFirst(t *testing.T) {
	root := t.TempDir()
	small := project(t, root, "small", "dist")
	large := project(t, root, "large", "node_modules")
	if err := os.WriteFile(filepath.Join(large, "node_modules", "big"), make([]byte, 256*1024), 0600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	age(t, small, 30*24*time.Hour)
	age(t, large, 30*24*time.Hour)

	artifacts, _ := Find(context.Background(), root, Options{Roots: []string{root}})
	if len(artifacts) < 2 {
		t.Fatalf("got %d artifacts", len(artifacts))
	}
	if artifacts[0].Bytes < artifacts[1].Bytes {
		t.Errorf("not sorted biggest first: %d then %d", artifacts[0].Bytes, artifacts[1].Bytes)
	}
	if artifacts[0].Bytes <= 0 {
		t.Error("artifacts were not sized")
	}
}

func TestConfiguredRootsReplaceTheDefaults(t *testing.T) {
	root := t.TempDir()
	if got := Roots("/Users/someone", Options{Roots: []string{root}}); len(got) != 1 || got[0] != root {
		t.Errorf("Roots = %v, want only the configured one", got)
	}
}

func TestTotal(t *testing.T) {
	if got := Total([]Artifact{{Bytes: 10}, {Bytes: 32}}); got != 42 {
		t.Errorf("Total = %d, want 42", got)
	}
}

func TestArtifactChangeInvalidatesPreview(t *testing.T) {
	root := t.TempDir()
	projectPath := project(t, root, "changing", "dist")
	age(t, projectPath, 30*24*time.Hour)
	artifacts, _ := Find(context.Background(), root, Options{Roots: []string{root}})
	if len(artifacts) != 1 || !Unchanged(artifacts[0]) {
		t.Fatalf("fresh preview was not valid: %+v", artifacts)
	}
	stamp := time.Now().Add(time.Minute)
	if err := os.Chtimes(artifacts[0].Path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if Unchanged(artifacts[0]) {
		t.Fatal("artifact modified after preview was still accepted")
	}
}

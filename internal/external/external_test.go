package external

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) (string, Options) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Volumes")
	volume := filepath.Join(root, "backup")
	if err := os.MkdirAll(volume, 0700); err != nil {
		t.Fatal(err)
	}
	options := Options{
		VolumesRoot: root,
		Inspect: func(_ context.Context, path string) (Identity, error) {
			return Identity{Path: path, Device: "disk9s1", VolumeUUID: "fixture"}, nil
		},
	}
	return volume, options
}

func TestFindOnlyIncludesApprovedMetadata(t *testing.T) {
	volume, options := fixture(t)
	paths := []string{
		filepath.Join(volume, ".TemporaryItems", "item"),
		filepath.Join(volume, ".Trashes", "501", "item"),
		filepath.Join(volume, "folder", ".DS_Store"),
		filepath.Join(volume, "folder", "._photo.jpg"),
		filepath.Join(volume, ".Spotlight-V100", "index"),
		filepath.Join(volume, ".fseventsd", "events"),
		filepath.Join(volume, "photo.jpg"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := Find(context.Background(), volume, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 4 {
		t.Fatalf("items = %+v", plan.Items)
	}
	for _, item := range plan.Items {
		if filepath.Base(item.Path) == ".Spotlight-V100" || filepath.Base(item.Path) == ".fseventsd" || filepath.Base(item.Path) == "photo.jpg" {
			t.Errorf("protected or ordinary data was included: %s", item.Path)
		}
	}
}

func TestValidationRejectsNestedAndSymlinkedPaths(t *testing.T) {
	volume, options := fixture(t)
	nested := filepath.Join(volume, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Find(context.Background(), nested, options); err == nil {
		t.Fatal("nested path was accepted")
	}
	link := filepath.Join(options.VolumesRoot, "linked")
	if err := os.Symlink(volume, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Find(context.Background(), link, options); err == nil {
		t.Fatal("symlinked path was accepted")
	}
}

func TestRecheckDetectsMountIdentityChange(t *testing.T) {
	volume, options := fixture(t)
	plan, err := Find(context.Background(), volume, options)
	if err != nil {
		t.Fatal(err)
	}
	options.Inspect = func(_ context.Context, path string) (Identity, error) {
		return Identity{Path: path, Device: "disk10s1", VolumeUUID: "replacement"}, nil
	}
	if err := Recheck(context.Background(), plan, options); err == nil {
		t.Fatal("replacement mount was accepted")
	}
}

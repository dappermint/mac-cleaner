package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dappermint/ratatouille/internal/config"
	"github.com/dappermint/ratatouille/internal/storage"
)

func TestSurfaceCacheRoundTripIsScoped(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvDir, filepath.Join(home, "config"))
	report := Report{
		GeneratedAt: time.Now(), Home: home,
		Surface: &Surface{Root: &SurfaceNode{Name: "home", Path: home, Bytes: 1024}, WalkedAt: time.Now()},
	}
	if err := saveCachedReport(report, nil); err != nil {
		t.Fatal(err)
	}
	cached, err := LoadCachedReport(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cached.Cached || cached.Surface.Root.Bytes != 1024 {
		t.Fatalf("cached report = %+v", cached)
	}
	if _, err := LoadCachedReport(home, true); err == nil {
		t.Fatal("user cache was reused for a root scan")
	}
}

func TestSurfaceCacheUsesTheInvokingUserIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvDir, filepath.Join(home, ".config", "ratatouille"))
	identity := &storage.CommandIdentity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), Home: home}
	report := Report{
		GeneratedAt: time.Now(), Home: home, Rootful: true,
		Surface: &Surface{Root: &SurfaceNode{Name: "home", Path: home, Bytes: 1024}, WalkedAt: time.Now()},
	}
	if err := saveCachedReport(report, identity); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(home, ".config"), filepath.Join(home, ".config", "ratatouille"), SurfaceCachePath(home)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if owner, ok := storage.FileOwner(info); !ok || owner != identity.UID {
			t.Errorf("%s owner = %d, known=%v", path, owner, ok)
		}
	}
}

func TestGrowthInsightUsesPreviousWalk(t *testing.T) {
	path := "/Users/someone/Library/Caches/example"
	previous := Report{Surface: &Surface{Root: &SurfaceNode{Name: "example", Path: path, Bytes: 512 << 20}}}
	current := Report{Surface: &Surface{Root: &SurfaceNode{Name: "example", Path: path, Bytes: 1024 << 20}}}
	insights := buildInsights(context.Background(), current, &previous)
	if len(insights) != 1 || insights[0].Kind != "growth" || insights[0].Bytes != 512<<20 {
		t.Fatalf("insights = %+v", insights)
	}
}

func TestDuplicateInsightHashesContents(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.bin")
	second := filepath.Join(root, "second.bin")
	third := filepath.Join(root, "third.bin")
	for path, contents := range map[string]string{first: "same", second: "same", third: "nope"} {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	files := []LargeFile{{Path: first, Bytes: 4096}, {Path: second, Bytes: 4096}, {Path: third, Bytes: 4096}}
	insights := duplicateInsights(context.Background(), files)
	if len(insights) != 1 || len(insights[0].Paths) != 2 {
		t.Fatalf("duplicates = %+v", insights)
	}
}

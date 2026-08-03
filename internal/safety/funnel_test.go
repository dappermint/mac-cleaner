package safety

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dappermint/ratatouille/internal/config"
)

func newTestFunnel(t *testing.T, dryRun bool) (*Funnel, string, *Log) {
	t.Helper()
	t.Setenv(EnvNoAuth, "1")
	t.Setenv(EnvDryRun, "")
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatalf("creating the home: %v", err)
	}
	t.Setenv(EnvLog, filepath.Join(home, "operations.jsonl"))
	log := OpenLog(home, nil)
	return NewFunnel(home, nil, dryRun, log), home, log
}

func writeTree(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, name := range names {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatalf("creating %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("payload"), 0600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
}

func TestTrashKeepsTheFileRecoverable(t *testing.T) {
	funnel, home, _ := newTestFunnel(t, false)
	writeTree(t, home, "Library/Caches/com.example/blob")

	source := filepath.Join(home, "Library", "Caches", "com.example")
	result, err := funnel.Trash(context.Background(), Request{Command: "clean", Item: "app-caches", Path: source})
	if err != nil {
		t.Fatalf("trashing: %v", err)
	}
	if result.PutBack {
		t.Error("the rename fallback should not claim a Put Back entry")
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Error("the source survived the move")
	}
	if _, err := os.Stat(filepath.Join(result.Destination, "blob")); err != nil {
		t.Errorf("the payload is not recoverable from Trash: %v", err)
	}
}

func TestTrashRefusesABareRoot(t *testing.T) {
	funnel, _, _ := newTestFunnel(t, false)
	_, err := funnel.Trash(context.Background(), Request{Command: "clean", Path: "/Users/someone/Library"})
	if !Refused(err) {
		t.Fatalf("expected a refusal, got %v", err)
	}
}

func TestFunnelRefusesAWhitelistedPath(t *testing.T) {
	t.Setenv(EnvNoAuth, "1")
	t.Setenv(EnvDryRun, "")
	home := filepath.Join(t.TempDir(), "home")
	source := filepath.Join(home, "Library", "Caches", "com.example")
	writeTree(t, source, "blob")
	configDir := filepath.Join(home, ".config", "ratatouille")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, config.WhitelistFile), []byte(source+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	funnel := NewFunnel(home, nil, false, OpenLog(home, nil))
	if _, err := funnel.Trash(context.Background(), Request{Command: "clean", Item: "explorer-selection", Path: source}); !Refused(err) {
		t.Fatalf("expected a whitelist refusal, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "blob")); err != nil {
		t.Errorf("the whitelisted path was changed: %v", err)
	}
}

func TestFunnelFailsClosedWhenWhitelistCannotBeRead(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	source := filepath.Join(home, "Library", "Caches", "com.example")
	writeTree(t, source, "blob")
	whitelist := filepath.Join(home, ".config", "ratatouille", config.WhitelistFile)
	if err := os.MkdirAll(whitelist, 0700); err != nil {
		t.Fatal(err)
	}

	funnel := NewFunnel(home, nil, false, OpenLog(home, nil))
	if _, err := funnel.Remove(context.Background(), Request{Command: "clean", Path: source}); !Refused(err) {
		t.Fatalf("expected an unreadable-whitelist refusal, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "blob")); err != nil {
		t.Errorf("the path changed after a whitelist error: %v", err)
	}
}

func TestTrashRefusesToLeaveTheHomeWithoutFinder(t *testing.T) {
	funnel, _, _ := newTestFunnel(t, false)
	outside := filepath.Join(t.TempDir(), "elsewhere")
	writeTree(t, outside, "file")

	_, err := funnel.Trash(context.Background(), Request{Command: "clean", Path: filepath.Join(outside, "file")})
	if !errors.Is(err, ErrNotTrashable) {
		t.Fatalf("expected ErrNotTrashable, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "file")); err != nil {
		t.Error("a refused Trash left the file removed anyway")
	}
}

// The rename runs relative to a handle on the home directory, so a link that
// leaves home cannot be used to drag an outside file into Trash.
func TestTrashRefusesAnEscapingAncestorSymlink(t *testing.T) {
	funnel, home, _ := newTestFunnel(t, false)
	outside := filepath.Join(filepath.Dir(home), "outside")
	writeTree(t, outside, "data")
	if err := os.Symlink(outside, filepath.Join(home, "linked")); err != nil {
		t.Fatalf("creating the link: %v", err)
	}

	if _, err := funnel.Trash(context.Background(), Request{Command: "clean", Path: filepath.Join(home, "linked", "data")}); err == nil {
		t.Fatal("a path through a link out of home was trashed")
	}
	if _, err := os.Stat(filepath.Join(outside, "data")); err != nil {
		t.Errorf("the outside file was moved anyway: %v", err)
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	funnel, home, _ := newTestFunnel(t, true)
	writeTree(t, home, "Library/Caches/com.example/blob")
	source := filepath.Join(home, "Library", "Caches", "com.example")

	for _, run := range []func() (Result, error){
		func() (Result, error) {
			return funnel.Trash(context.Background(), Request{Command: "clean", Path: source})
		},
		func() (Result, error) {
			return funnel.Remove(context.Background(), Request{Command: "clean", Path: source})
		},
	} {
		result, err := run()
		if err != nil {
			t.Fatalf("dry run returned an error: %v", err)
		}
		if !result.DryRun {
			t.Error("the result did not report itself as a dry run")
		}
		if _, err := os.Stat(filepath.Join(source, "blob")); err != nil {
			t.Fatalf("a dry run removed something: %v", err)
		}
	}
}

func TestRemoveDeletesATree(t *testing.T) {
	funnel, home, _ := newTestFunnel(t, false)
	writeTree(t, home, "Library/Caches/com.example/nested/blob")
	source := filepath.Join(home, "Library", "Caches", "com.example")

	if _, err := funnel.Remove(context.Background(), Request{Command: "clean", Path: source}); err != nil {
		t.Fatalf("removing: %v", err)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Error("the tree survived")
	}
}

// The funnel resolves the leaf through a directory handle, so a symlink is
// removed as a symlink and never as the thing it points at.
func TestRemoveDoesNotFollowTheLeafSymlink(t *testing.T) {
	funnel, home, _ := newTestFunnel(t, false)
	writeTree(t, home, "Library/Caches/victim/blob")
	victim := filepath.Join(home, "Library", "Caches", "victim")
	link := filepath.Join(home, "Library", "Caches", "link")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatalf("creating the link: %v", err)
	}

	if _, err := funnel.Remove(context.Background(), Request{Command: "clean", Path: link}); err != nil {
		t.Fatalf("removing the link: %v", err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Error("the link survived")
	}
	if _, err := os.Stat(filepath.Join(victim, "blob")); err != nil {
		t.Errorf("the link target was removed through the link: %v", err)
	}
}

func TestEmptyTrashKeepsTheTrashDirectory(t *testing.T) {
	funnel, home, _ := newTestFunnel(t, false)
	writeTree(t, home, ".Trash/one", ".Trash/nested/two")

	if _, err := funnel.EmptyTrash(context.Background(), Request{Command: "clean"}); err != nil {
		t.Fatalf("emptying: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".Trash"))
	if err != nil {
		t.Fatalf("the Trash directory itself was removed: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the Trash still holds %d entries", len(entries))
	}
}

func TestMissingTargetIsSkippedNotFailed(t *testing.T) {
	funnel, home, _ := newTestFunnel(t, false)
	result, err := funnel.Remove(context.Background(), Request{Command: "clean", Path: filepath.Join(home, "gone")})
	if err != nil {
		t.Fatalf("a missing target returned an error: %v", err)
	}
	if result.Outcome != OutcomeSkipped {
		t.Errorf("outcome was %q, want %q", result.Outcome, OutcomeSkipped)
	}
}

func TestEveryOperationIsLogged(t *testing.T) {
	funnel, home, log := newTestFunnel(t, false)
	writeTree(t, home, "Library/Caches/com.example/blob")
	source := filepath.Join(home, "Library", "Caches", "com.example")

	if _, err := funnel.Trash(context.Background(), Request{Command: "clean", Item: "app-caches", Path: source, Bytes: 7}); err != nil {
		t.Fatalf("trashing: %v", err)
	}
	if _, err := funnel.Trash(context.Background(), Request{Command: "clean", Item: "bad", Path: "/System"}); err == nil {
		t.Fatal("a refusal was expected")
	}

	raw, err := os.ReadFile(log.Path())
	if err != nil {
		t.Fatalf("reading the log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("the log has %d lines, want 2", len(lines))
	}

	var first Entry
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("the log is not valid json: %v", err)
	}
	if first.Outcome != OutcomeOK || first.Item != "app-caches" || first.Bytes != 7 || first.Run != log.Run() {
		t.Errorf("the first entry is wrong: %+v", first)
	}

	var second Entry
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("the log is not valid json: %v", err)
	}
	if second.Outcome != OutcomeRefused {
		t.Errorf("a refusal was logged as %q", second.Outcome)
	}
}

func TestLoggingCanBeDisabled(t *testing.T) {
	funnel, home, log := newTestFunnel(t, false)
	t.Setenv(EnvNoLog, "1")
	disabled := OpenLog(home, nil)
	funnel.log = disabled

	writeTree(t, home, "Library/Caches/com.example/blob")
	if _, err := funnel.Trash(context.Background(), Request{Command: "clean", Path: filepath.Join(home, "Library", "Caches", "com.example")}); err != nil {
		t.Fatalf("trashing: %v", err)
	}
	if _, err := os.Stat(log.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Error("the log was written while logging was disabled")
	}
}

package optimize

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dappermint/ratatouille/internal/config"
)

func testEnv(t *testing.T, dryRun bool) Env {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.EnvDir, filepath.Join(home, "config"))
	return NewEnv(home, false, nil, dryRun)
}

// Every task has to say what it changes and whether that can be undone, because
// those two lines are the whole basis for letting it run at all.
func TestEveryTaskDeclaresWhatItChanges(t *testing.T) {
	seen := make(map[string]bool)
	for _, task := range All() {
		if task.ID == "" || task.Name == "" {
			t.Errorf("a task has no id or name: %+v", task)
			continue
		}
		if seen[task.ID] {
			t.Errorf("duplicate task id %q", task.ID)
		}
		seen[task.ID] = true

		if strings.TrimSpace(task.Changes) == "" {
			t.Errorf("%s does not say what it changes", task.ID)
		}
		if task.Reverses == "" {
			t.Errorf("%s does not say whether it can be undone", task.ID)
		}
		if task.Probe == nil {
			t.Errorf("%s has no probe, so it would run whether it was needed or not", task.ID)
		}
		if task.Run == nil {
			t.Errorf("%s has nothing to run", task.ID)
		}
	}
}

// The declined list is part of the interface. A task dropped without a reason
// is indistinguishable from one nobody thought of.
func TestDeclinedTasksCarryReasons(t *testing.T) {
	declined := DeclinedTasks()
	if len(declined) == 0 {
		t.Fatal("nothing is declined, which cannot be right for system mutation")
	}
	offered := make(map[string]bool)
	for _, task := range All() {
		offered[task.ID] = true
	}
	for _, entry := range declined {
		if entry.Reason == "" {
			t.Errorf("%s is declined without a reason", entry.ID)
		}
		if offered[entry.ID] {
			t.Errorf("%s is both offered and declined", entry.ID)
		}
	}
}

func TestSelectRejectsUnknownIDs(t *testing.T) {
	if _, err := Select([]string{"no-such-task"}, nil); err == nil {
		t.Error("an unknown --only id was accepted")
	}
	if _, err := Select(nil, []string{"no-such-task"}); err == nil {
		t.Error("an unknown --skip id was accepted")
	}
}

func TestSelectOnlyAndSkip(t *testing.T) {
	all := All()
	first, second := all[0].ID, all[1].ID

	only, err := Select([]string{first}, nil)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(only) != 1 || only[0].ID != first {
		t.Errorf("--only selected %d tasks", len(only))
	}

	skipped, err := Select(nil, []string{second})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(skipped) != len(all)-1 {
		t.Errorf("--skip left %d tasks, want %d", len(skipped), len(all)-1)
	}
	for _, task := range skipped {
		if task.ID == second {
			t.Errorf("%s was skipped but still ran", second)
		}
	}
}

// A dry run must not reach any task's Run function at all.
func TestDryRunNeverRunsATask(t *testing.T) {
	ran := false
	task := Task{
		ID: "test", Name: "test", Changes: "nothing", Reverses: ReadOnly,
		Probe: alwaysNeeded,
		Run: func(context.Context, Env) (Outcome, string, error) {
			ran = true
			return OutcomeApplied, "", nil
		},
	}
	results := Run(context.Background(), testEnv(t, true), []Task{task}, nil)
	if ran {
		t.Fatal("a dry run executed the task")
	}
	if results[0].Outcome != OutcomeSkipped {
		t.Errorf("outcome = %q, want skipped", results[0].Outcome)
	}
	if !strings.Contains(results[0].Detail, "would have run") {
		t.Errorf("the dry run did not say what it would have done: %q", results[0].Detail)
	}
}

// A task whose probe says there is nothing to do must not run either.
func TestAProbeThatSaysNoStopsTheTask(t *testing.T) {
	ran := false
	task := Task{
		ID: "test", Name: "test", Changes: "nothing", Reverses: ReadOnly,
		Probe: func(context.Context, Env) (bool, string) { return false, "already in the desired state" },
		Run: func(context.Context, Env) (Outcome, string, error) {
			ran = true
			return OutcomeApplied, "", nil
		},
	}
	results := Run(context.Background(), testEnv(t, false), []Task{task}, nil)
	if ran {
		t.Fatal("a task ran despite its probe saying it was not needed")
	}
	if results[0].Detail != "already in the desired state" {
		t.Errorf("the probe's reason was lost: %q", results[0].Detail)
	}
}

func TestRootTasksAreUnavailableWithoutRoot(t *testing.T) {
	task := Task{
		ID: "test", Name: "test", Changes: "nothing", Reverses: ReadOnly, NeedsRoot: true,
		Probe: alwaysNeeded,
		Run:   func(context.Context, Env) (Outcome, string, error) { return OutcomeApplied, "", nil },
	}
	results := Run(context.Background(), testEnv(t, false), []Task{task}, nil)
	if results[0].Outcome != OutcomeUnavailable {
		t.Errorf("outcome = %q, want unavailable", results[0].Outcome)
	}
}

func TestWhitelistedTasksAreSkipped(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, "config")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatalf("creating the config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, config.OptimizeWhitelistFile), []byte("blocked\n"), 0600); err != nil {
		t.Fatalf("writing the whitelist: %v", err)
	}
	t.Setenv(config.EnvDir, directory)

	task := Task{
		ID: "blocked", Name: "blocked", Changes: "nothing", Reverses: ReadOnly,
		Probe: alwaysNeeded,
		Run:   func(context.Context, Env) (Outcome, string, error) { return OutcomeApplied, "", nil },
	}
	results := Run(context.Background(), NewEnv(home, false, nil, false), []Task{task}, nil)
	if results[0].Outcome != OutcomeSkipped || results[0].Detail != "whitelisted" {
		t.Errorf("result = %+v, want a whitelisted skip", results[0])
	}
}

func TestAgentProgramOnlyAcceptsAbsolutePaths(t *testing.T) {
	directory := t.TempDir()
	relative := filepath.Join(directory, "relative.plist")
	contents := `<?xml version="1.0"?><plist version="1.0"><dict>` +
		`<key>Program</key><string>relative/path</string></dict></plist>`
	if err := os.WriteFile(relative, []byte(contents), 0600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if _, ok := agentProgram(relative); ok {
		t.Error("a relative program path was accepted as a path")
	}

	broken := filepath.Join(directory, "broken.plist")
	if err := os.WriteFile(broken, []byte("this is not a plist"), 0600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if _, ok := agentProgram(broken); ok {
		t.Error("an unparseable plist yielded a program path")
	}
}

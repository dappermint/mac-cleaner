// Package optimize runs bounded system maintenance. Every task here changes
// system state rather than removing files, so each one has to say what it
// changes, whether that is reversible, and whether it needs to run at all
// before it runs.
package optimize

import (
	"context"
	"time"

	"github.com/dappermint/ratatouille/internal/config"
	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

const (
	CommandName  = "optimize"
	taskTimeout  = 2 * time.Minute
	probeTimeout = 10 * time.Second
)

type Outcome string

const (
	// OutcomeApplied means the task ran and changed something.
	OutcomeApplied Outcome = "applied"
	// OutcomeUnchanged means the task ran and found nothing to do.
	OutcomeUnchanged Outcome = "unchanged"
	// OutcomeSkipped means the probe said it was not needed, or the user
	// excluded it.
	OutcomeSkipped Outcome = "skipped"
	// OutcomeUnavailable means this machine cannot run it: no root, a missing
	// command, or an authorization prompt this run refuses to raise.
	OutcomeUnavailable Outcome = "unavailable"
	OutcomeFailed      Outcome = "failed"
)

// Reversibility is stated per task rather than assumed. "rebuildable" means the
// system recreates it, "reversible" means one setting flips back, and "no"
// means the change stands.
type Reversibility string

const (
	Rebuildable Reversibility = "rebuildable"
	Reversible  Reversibility = "reversible"
	Permanent   Reversibility = "no"
	ReadOnly    Reversibility = "read-only"
)

type Env struct {
	Home      string
	Rootful   bool
	Identity  *storage.CommandIdentity
	Whitelist *config.Whitelist
	DryRun    bool
}

type Task struct {
	ID          string
	Name        string
	Description string
	// Changes says in one line what this leaves different afterwards.
	Changes   string
	NeedsRoot bool
	Reverses  Reversibility

	// Probe decides whether the task has anything to do. A task that runs
	// without its probe passing is a bug, not an optimisation.
	Probe func(ctx context.Context, env Env) (bool, string)
	// Run performs the change and says what happened.
	Run func(ctx context.Context, env Env) (Outcome, string, error)
}

type Result struct {
	Task    Task    `json:"-"`
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Outcome Outcome `json:"outcome"`
	Detail  string  `json:"detail,omitempty"`
	Error   string  `json:"error,omitempty"`
}

func NewEnv(home string, rootful bool, identity *storage.CommandIdentity, dryRun bool) Env {
	whitelist, _ := config.LoadWhitelist(home, config.OptimizeWhitelistFile)
	return Env{
		Home:      home,
		Rootful:   rootful,
		Identity:  identity,
		Whitelist: whitelist,
		DryRun:    dryRun || safety.DryRunFromEnv(),
	}
}

// Run executes the selected tasks in order, one at a time. These are system
// mutations, and running them concurrently would make a failure impossible to
// attribute.
func Run(ctx context.Context, env Env, tasks []Task, log *safety.Log) []Result {
	results := make([]Result, 0, len(tasks))
	for _, task := range tasks {
		results = append(results, runOne(ctx, env, task, log))
	}
	return results
}

func runOne(ctx context.Context, env Env, task Task, log *safety.Log) Result {
	result := Result{Task: task, ID: task.ID, Name: task.Name}

	if env.Whitelist.Blocks(task.ID, "") {
		result.Outcome, result.Detail = OutcomeSkipped, "whitelisted"
		return result
	}
	if task.NeedsRoot && !env.Rootful {
		result.Outcome, result.Detail = OutcomeUnavailable, "needs sudo --root"
		return result
	}
	if task.NeedsRoot && safety.NoAuth() {
		result.Outcome, result.Detail = OutcomeUnavailable, "privileged commands are disabled"
		return result
	}

	probeCtx, cancelProbe := context.WithTimeout(ctx, probeTimeout)
	needed, reason := task.Probe(probeCtx, env)
	cancelProbe()
	if !needed {
		result.Outcome, result.Detail = OutcomeSkipped, reason
		return result
	}

	if env.DryRun {
		result.Outcome, result.Detail = OutcomeSkipped, "dry run, would have run: "+task.Changes
		return result
	}

	taskCtx, cancel := context.WithTimeout(ctx, taskTimeout)
	outcome, detail, err := task.Run(taskCtx, env)
	cancel()

	result.Outcome, result.Detail = outcome, detail
	if err != nil {
		result.Outcome = OutcomeFailed
		result.Error = storage.CompactError(err)
	}
	log.Record(safety.Entry{
		Command:  CommandName,
		Item:     task.ID,
		Kind:     safety.KindCommand,
		Path:     task.Changes,
		Outcome:  logOutcome(result.Outcome),
		Recovery: safety.Recovery(task.Reverses),
		DryRun:   env.DryRun,
		Error:    result.Error,
	})
	return result
}

func logOutcome(outcome Outcome) safety.Outcome {
	switch outcome {
	case OutcomeApplied, OutcomeUnchanged:
		return safety.OutcomeOK
	case OutcomeFailed:
		return safety.OutcomeFailed
	default:
		return safety.OutcomeSkipped
	}
}

// Select resolves the task list a user asked for. An unknown id is an error
// rather than a silent omission.
func Select(only, skip []string) ([]Task, error) {
	all := All()
	if len(only) == 0 && len(skip) == 0 {
		return all, nil
	}
	byID := make(map[string]Task, len(all))
	for _, task := range all {
		byID[task.ID] = task
	}
	for _, id := range append(append([]string{}, only...), skip...) {
		if _, ok := byID[id]; !ok {
			return nil, unknownTask(id, all)
		}
	}

	excluded := make(map[string]bool, len(skip))
	for _, id := range skip {
		excluded[id] = true
	}
	if len(only) > 0 {
		selected := make([]Task, 0, len(only))
		for _, task := range all {
			for _, id := range only {
				if task.ID == id && !excluded[id] {
					selected = append(selected, task)
				}
			}
		}
		return selected, nil
	}
	selected := make([]Task, 0, len(all))
	for _, task := range all {
		if !excluded[task.ID] {
			selected = append(selected, task)
		}
	}
	return selected, nil
}

func Summarize(results []Result) map[Outcome]int {
	counts := make(map[Outcome]int)
	for _, result := range results {
		counts[result.Outcome]++
	}
	return counts
}

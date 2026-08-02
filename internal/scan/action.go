package scan

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dappermint/ratatouille/internal/catalog"
	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

type ActionResult struct {
	ID    string
	Name  string
	Error error
}

func PlanText(items []Item) string {
	if len(items) == 0 {
		return "mark=0\n"
	}
	var result strings.Builder
	result.WriteString("execute\n\n")
	for _, item := range orderedActions(items) {
		fmt.Fprintf(&result, "%-20s %-11s %-9s %s\n", storage.DisplayCategory(item.Category), item.Risk, storage.HumanBytes(item.Bytes), item.Name)
		fmt.Fprintf(&result, "  %s\n", item.Action.Display())
	}
	fmt.Fprintf(&result, "\n%s\n", DescribeSelection(items))
	return result.String()
}

func orderedActions(items []Item) []Item {
	ordered := append([]Item(nil), items...)
	sort.SliceStable(ordered, func(a, b int) bool {
		leftEmpty := ordered[a].Action != nil && ordered[a].Action.Kind == ActionEmptyTrash
		rightEmpty := ordered[b].Action != nil && ordered[b].Action.Kind == ActionEmptyTrash
		return !leftEmpty && rightEmpty
	})
	return ordered
}

// ExecuteItems runs a selection. Every path it touches goes through the
// safety funnel, which is the only thing in this binary allowed to remove
// anything, and which records what it did.
func ExecuteItems(ctx context.Context, funnel *safety.Funnel, items []Item, out io.Writer) []ActionResult {
	results := make([]ActionResult, 0, len(items))
	var env catalog.Env
	if hasCatalogItem(items) {
		env = catalog.NewEnv(ctx, funnel.Home(), os.Geteuid() == 0, funnel.Identity())
	}
	for _, item := range orderedActions(items) {
		result := ActionResult{ID: item.ID, Name: item.Name}
		fmt.Fprintf(out, "\n%s\n  %s\n", item.Name, item.Action.Display())

		request := safety.Request{Command: CommandName, Item: item.ID, Bytes: item.Bytes}
		switch item.Action.Kind {
		case ActionCommand:
			result.Error = executeCommand(ctx, funnel, *item.Action, request, out)
		case ActionTrash:
			result.Error = executeTrash(ctx, funnel, env, item, request, out)
		case ActionEmptyTrash:
			outcome, err := funnel.EmptyTrash(ctx, request)
			result.Error = err
			reportOutcome(out, funnel.Home(), outcome, err)
		default:
			result.Error = fmt.Errorf("unsupported action %q", item.Action.Kind)
		}

		if result.Error != nil {
			fmt.Fprintf(out, "  failed: %v\n", result.Error)
		}
		results = append(results, result)
	}
	return results
}

func executeCommand(ctx context.Context, funnel *safety.Funnel, action Action, request safety.Request, out io.Writer) error {
	if funnel.DryRun() {
		fmt.Fprintln(out, "  dry-run")
		funnel.RecordCommand(request, action.Display(), nil)
		return nil
	}
	err := runInteractiveCommand(ctx, action, out)
	funnel.RecordCommand(request, action.Display(), err)
	if err == nil {
		fmt.Fprintln(out, "  done")
	}
	return err
}

func executeTrash(ctx context.Context, funnel *safety.Funnel, env catalog.Env, item Item, request safety.Request, out io.Writer) error {
	home := funnel.Home()
	paths, skipped := recheckCatalogPaths(ctx, env, item)
	for _, reason := range skipped {
		fmt.Fprintf(out, "  skipped %s\n", reason)
	}
	sizes := pathSizes(item, paths)
	for index, path := range paths {
		request.Path = path
		request.Bytes = sizes[index]
		outcome, err := funnel.Trash(ctx, request)
		reportOutcome(out, home, outcome, err)
		if err != nil {
			return err
		}
	}
	return nil
}

// pathSizes attributes each path its own measured size. Reusing the item total
// for every path would make a fifty-path item log fifty times its real size.
func pathSizes(item Item, paths []string) []int64 {
	sizes := make([]int64, len(paths))
	if item.Action == nil {
		return sizes
	}
	if len(item.Action.PathBytes) == len(item.Action.Paths) {
		measured := make(map[string]int64, len(item.Action.Paths))
		for index, path := range item.Action.Paths {
			measured[path] = item.Action.PathBytes[index]
		}
		for index, path := range paths {
			sizes[index] = measured[path]
		}
		return sizes
	}
	if len(paths) == 1 {
		sizes[0] = item.Bytes
	}
	return sizes
}

func reportOutcome(out io.Writer, home string, result safety.Result, err error) {
	where := storage.RelativeHome(home, result.Path)
	switch {
	case err != nil:
		return
	case result.DryRun:
		fmt.Fprintf(out, "  dry-run  %s\n", where)
	case result.Outcome == safety.OutcomeSkipped:
		fmt.Fprintf(out, "  already gone  %s\n", where)
	case result.Destination != "":
		fmt.Fprintf(out, "  done  %s, in Trash as %s\n", where, filepath.Base(result.Destination))
	default:
		fmt.Fprintf(out, "  done  %s\n", where)
	}
}

func runInteractiveCommand(ctx context.Context, action Action, out io.Writer) error {
	cmd := exec.CommandContext(ctx, action.Command, action.Args...)
	storage.ApplyCommandIdentity(cmd, action.Identity)
	cmd.Stdin = os.Stdin
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

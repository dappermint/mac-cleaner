package scan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

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

func ExecuteItems(ctx context.Context, home string, items []Item, dryRun bool, out io.Writer) []ActionResult {
	results := make([]ActionResult, 0, len(items))
	for _, item := range orderedActions(items) {
		result := ActionResult{ID: item.ID, Name: item.Name}
		fmt.Fprintf(out, "\n%s\n  %s\n", item.Name, item.Action.Display())
		if dryRun {
			fmt.Fprintln(out, "  dry-run")
			results = append(results, result)
			continue
		}

		switch item.Action.Kind {
		case ActionCommand:
			result.Error = runInteractiveCommand(ctx, *item.Action, out)
		case ActionTrash:
			for _, path := range item.Action.Paths {
				if _, err := MoveToTrashAs(home, path, item.Action.Identity); err != nil {
					result.Error = err
					break
				}
			}
		case ActionEmptyTrash:
			result.Error = EmptyTrash(home)
		default:
			result.Error = fmt.Errorf("unsupported action %q", item.Action.Kind)
		}

		if result.Error != nil {
			fmt.Fprintf(out, "  failed: %v\n", result.Error)
		} else {
			fmt.Fprintln(out, "  done")
		}
		results = append(results, result)
	}
	return results
}

func runInteractiveCommand(ctx context.Context, action Action, out io.Writer) error {
	cmd := exec.CommandContext(ctx, action.Command, action.Args...)
	storage.ApplyCommandIdentity(cmd, action.Identity)
	cmd.Stdin = os.Stdin
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

func MoveToTrash(home, source string) (string, error) {
	return MoveToTrashAs(home, source, nil)
}

func MoveToTrashAs(home, source string, identity *storage.CommandIdentity) (string, error) {
	home, source, err := validateHomePath(home, source)
	if err != nil {
		return "", err
	}
	trash := filepath.Join(home, ".Trash")
	if source == trash || strings.HasPrefix(source, trash+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing to re-trash %s", source)
	}
	if _, err := os.Lstat(source); err != nil {
		return "", err
	}
	if err := ensureTrashDirectory(trash, identity); err != nil {
		return "", err
	}

	base := filepath.Base(source)
	stamp := time.Now().Format("20060102-150405")
	destination := filepath.Join(trash, base+"-ratatouille-"+stamp)
	for suffix := 2; ; suffix++ {
		if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
			break
		} else if err != nil {
			return "", err
		}
		destination = filepath.Join(trash, fmt.Sprintf("%s-ratatouille-%s-%d", base, stamp, suffix))
	}

	if err := os.Rename(source, destination); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return "", fmt.Errorf("%s is on another volume, move it in Finder instead", source)
		}
		return "", err
	}
	return destination, nil
}

func EmptyTrash(home string) error {
	home, trash, err := validateHomePath(home, filepath.Join(home, ".Trash"))
	if err != nil {
		return err
	}
	if trash != filepath.Join(home, ".Trash") {
		return fmt.Errorf("refusing unexpected Trash path %s", trash)
	}
	info, err := os.Lstat(trash)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing non-directory Trash path %s", trash)
	}
	entries, err := os.ReadDir(trash)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(trash, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func ensureTrashDirectory(path string, identity *storage.CommandIdentity) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0700); err != nil {
			return err
		}
		if identity != nil {
			return os.Chown(path, int(identity.UID), int(identity.GID))
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing non-directory Trash path %s", path)
	}
	return nil
}

func validateHomePath(home, path string) (string, string, error) {
	homeAbsolute, err := filepath.Abs(filepath.Clean(home))
	if err != nil {
		return "", "", err
	}
	pathAbsolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", "", err
	}
	if homeAbsolute == string(filepath.Separator) || len(strings.Split(strings.Trim(homeAbsolute, string(filepath.Separator)), string(filepath.Separator))) < 2 {
		return "", "", fmt.Errorf("refusing unsafe home path %s", homeAbsolute)
	}
	relative, err := filepath.Rel(homeAbsolute, pathAbsolute)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("refusing path outside home: %s", pathAbsolute)
	}
	resolvedHome, err := filepath.EvalSymlinks(homeAbsolute)
	if err != nil {
		return "", "", err
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(pathAbsolute))
	if err != nil {
		return "", "", err
	}
	resolvedRelative, err := filepath.Rel(resolvedHome, resolvedParent)
	if err != nil || resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("refusing path through a symlink outside home: %s", pathAbsolute)
	}
	return homeAbsolute, pathAbsolute, nil
}

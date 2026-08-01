package main

import (
	"bytes"
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
)

type cappedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

type commandIdentity struct {
	UID      uint32
	GID      uint32
	Groups   []uint32
	Username string
	Home     string
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.buffer.Write(data)
	}
	return original, nil
}

func (b *cappedBuffer) String() string {
	return b.buffer.String()
}

func captureCommand(ctx context.Context, timeout time.Duration, command string, args ...string) (string, error) {
	return captureCommandAs(ctx, timeout, nil, command, args...)
}

func captureCommandAs(ctx context.Context, timeout time.Duration, identity *commandIdentity, command string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	output := &cappedBuffer{limit: 2 * 1024 * 1024}
	cmd := exec.CommandContext(commandCtx, command, args...)
	applyCommandIdentity(cmd, identity)
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
		return output.String(), fmt.Errorf("timed out after %s", timeout)
	}
	return output.String(), err
}

type ActionResult struct {
	ID    string
	Name  string
	Error error
}

func planText(items []Item) string {
	if len(items) == 0 {
		return "mark=0\n"
	}
	var result strings.Builder
	result.WriteString("execute\n\n")
	for _, item := range orderedActions(items) {
		fmt.Fprintf(&result, "%-20s %-11s %-9s %s\n", displayCategory(item.Category), item.Risk, humanBytes(item.Bytes), item.Name)
		fmt.Fprintf(&result, "  %s\n", item.Action.Display())
	}
	fmt.Fprintf(&result, "\n%s\n", describeSelection(items))
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

func executeItems(ctx context.Context, home string, items []Item, dryRun bool, out io.Writer) []ActionResult {
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
				if _, err := moveToTrashAs(home, path, item.Action.Identity); err != nil {
					result.Error = err
					break
				}
			}
		case ActionEmptyTrash:
			result.Error = emptyTrash(home)
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
	applyCommandIdentity(cmd, action.Identity)
	cmd.Stdin = os.Stdin
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

func applyCommandIdentity(cmd *exec.Cmd, identity *commandIdentity) {
	if identity == nil {
		return
	}
	cmd.Dir = identity.Home
	cmd.Env = commandEnvironment(os.Environ(), identity)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
		Uid:    identity.UID,
		Gid:    identity.GID,
		Groups: identity.Groups,
	}}
}

func commandEnvironment(environment []string, identity *commandIdentity) []string {
	filtered := make([]string, 0, len(environment)+3)
	for _, entry := range environment {
		if strings.HasPrefix(entry, "HOME=") || strings.HasPrefix(entry, "USER=") || strings.HasPrefix(entry, "LOGNAME=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered,
		"HOME="+identity.Home,
		"USER="+identity.Username,
		"LOGNAME="+identity.Username,
	)
}

func moveToTrash(home, source string) (string, error) {
	return moveToTrashAs(home, source, nil)
}

func moveToTrashAs(home, source string, identity *commandIdentity) (string, error) {
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
	destination := filepath.Join(trash, base+"-mac-cleaner-"+stamp)
	for suffix := 2; ; suffix++ {
		if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
			break
		} else if err != nil {
			return "", err
		}
		destination = filepath.Join(trash, fmt.Sprintf("%s-mac-cleaner-%s-%d", base, stamp, suffix))
	}

	if err := os.Rename(source, destination); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return "", fmt.Errorf("%s is on another volume, move it in Finder instead", source)
		}
		return "", err
	}
	return destination, nil
}

func emptyTrash(home string) error {
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

func ensureTrashDirectory(path string, identity *commandIdentity) error {
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

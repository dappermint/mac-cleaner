package safety

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dappermint/ratatouille/internal/storage"
)

const finderTimeout = 30 * time.Second

// ErrNotTrashable means neither Trash route can take this path. The caller has
// to decide whether a permanent removal is warranted; the funnel will not
// silently upgrade one to the other.
var ErrNotTrashable = errors.New("this path cannot be routed to Trash")

// trashViaFinder is the route that produces a real Put Back entry. It is
// skipped as root, because root's Finder is not the user's Finder, and under
// RATATOUILLE_NO_AUTH, because it can prompt.
func trashViaFinder(ctx context.Context, path string) error {
	if NoAuth() {
		return errors.New("osascript is disabled by " + EnvNoAuth)
	}
	if os.Geteuid() == 0 {
		return errors.New("finder cannot be driven as root")
	}
	script := fmt.Sprintf("tell application %q to delete POSIX file %q", "Finder", appleScriptLiteral(path))
	output, err := storage.CaptureCommand(ctx, finderTimeout, "/usr/bin/osascript", "-e", script)
	if err != nil {
		return fmt.Errorf("finder refused: %s", storage.CompactError(errors.New(strings.TrimSpace(output))))
	}
	return nil
}

func appleScriptLiteral(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

// trashViaRename is the fallback for headless sessions and for a Finder that
// does not answer. It loses Put Back, which the result says out loud.
func trashViaRename(home, source string, identity *storage.CommandIdentity) (string, error) {
	trash := filepath.Join(home, ".Trash")
	if !under(source, home) || under(source, trash) {
		return "", ErrNotTrashable
	}
	if err := ensureTrashDirectory(trash, identity); err != nil {
		return "", err
	}
	destination, err := freeTrashName(trash, filepath.Base(source))
	if err != nil {
		return "", err
	}
	if err := renameInto(home, source, destination); err != nil {
		if errors.Is(err, os.ErrPermission) || isCrossDevice(err) {
			return "", ErrNotTrashable
		}
		return "", err
	}
	return destination, nil
}

func freeTrashName(trash, base string) (string, error) {
	stamp := time.Now().Format("20060102-150405")
	candidate := filepath.Join(trash, base+"-ratatouille-"+stamp)
	for suffix := 2; suffix < 1000; suffix++ {
		_, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
		candidate = filepath.Join(trash, fmt.Sprintf("%s-ratatouille-%s-%d", base, stamp, suffix))
	}
	return "", errors.New("could not find a free name in Trash")
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
		return refuse(path, "the Trash path is not a directory")
	}
	return nil
}

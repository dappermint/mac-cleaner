package scan

import (
	"fmt"
	"os"
	"strings"

	"github.com/dappermint/ratatouille/internal/exitcode"
	"github.com/dappermint/ratatouille/internal/storage"
)

type Options struct {
	Deep           bool
	Rootful        bool
	Surface        bool
	Verify         bool
	SkipItems      bool
	SurfaceRoot    string
	MinFileBytes   int64
	LargeFileLimit int
}

func (o Options) Validate() error {
	if o.Verify && os.Geteuid() != 0 {
		return exitcode.Errorf(exitcode.NeedsRoot, "--verify requires uid 0: sudo ratatouille surface --root --verify")
	}
	return nil
}

func Configure(home string, options Options, identity *storage.CommandIdentity) Scanner {
	scanner := NewScanner(home, options.Deep)
	scanner.Rootful = options.Rootful
	scanner.Surface = options.Surface
	scanner.Verify = options.Verify
	scanner.SkipItems = options.SkipItems
	scanner.SurfaceRoot = options.SurfaceRoot
	scanner.MinFileBytes = options.MinFileBytes
	scanner.LargeFileLimit = options.LargeFileLimit
	scanner.CommandIdentity = identity
	return scanner
}

// CommandName is what the operation log records for a cleanup run.
const CommandName = "clean"

func ConfirmationPhrase(items []Item) string {
	for _, item := range items {
		if item.Action != nil && item.Action.Kind == ActionEmptyTrash {
			return "empty trash"
		}
	}
	return CommandName
}

func ActionErrors(results []ActionResult) error {
	var failed []string
	for _, result := range results {
		if result.Error != nil {
			failed = append(failed, result.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d action(s) failed: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

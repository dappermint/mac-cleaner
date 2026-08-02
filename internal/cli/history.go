package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/dappermint/ratatouille/internal/history"
	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

func runHistoryCommand(home string, args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("history", flag.ContinueOnError)
	flags.SetOutput(errOut)
	jsonOutput := flags.Bool("json", false, "print the raw records")
	since := flags.String("since", "", "only entries newer than this, for example 7d")
	limit := flags.Int("limit", 50, "how many entries to print")
	run := flags.String("id", "", "only entries from one run")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("history does not take positional arguments")
	}

	span, err := history.ParseSince(*since)
	if err != nil {
		return err
	}
	entries, err := history.Read(home, history.Query{Since: span, Limit: *limit, Run: *run})
	if err != nil {
		return err
	}

	if *jsonOutput {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(entries)
	}
	printHistory(out, home, entries)
	return nil
}

func printHistory(out io.Writer, home string, entries []safety.Entry) {
	if len(entries) == 0 {
		fmt.Fprintf(out, "nothing recorded in %s\n", safety.LogPath(home))
		return
	}
	fmt.Fprintf(out, "%-20s %-9s %-22s %-9s %10s  %s\n", "when", "command", "item", "outcome", "size", "path")
	const continuation = "                                                                "
	for _, entry := range entries {
		size := ""
		if entry.Bytes > 0 {
			size = storage.HumanBytes(entry.Bytes)
		}
		outcome := string(entry.Outcome)
		if entry.DryRun {
			outcome = "dry-run"
		}
		fmt.Fprintf(out, "%-20s %-9s %-22s %-9s %10s  %s\n",
			entry.At.Local().Format("2006-01-02 15:04:05"),
			entry.Command, entry.Item, outcome, size, entry.Path)
		// A continuation line is indented under the row it belongs to rather
		// than padded to a magic column, which drifts the moment a width changes.
		if entry.Destination != "" {
			fmt.Fprintf(out, "%s in Trash as %s\n", continuation, filepath.Base(entry.Destination))
		}
		if entry.Error != "" {
			fmt.Fprintf(out, "%s %s\n", continuation, entry.Error)
		}
	}
	fmt.Fprintf(out, "\n%d entries from %s\n", len(entries), safety.LogPath(home))
	if recoverable := countRecoverable(entries); recoverable > 0 {
		fmt.Fprintf(out, "%d of them are still in Trash, which is not free space until it is emptied\n", recoverable)
	}
}

func countRecoverable(entries []safety.Entry) int {
	count := 0
	for _, entry := range entries {
		if entry.Recovery == safety.RecoveryTrash && entry.Outcome == safety.OutcomeOK && !entry.DryRun {
			count++
		}
	}
	return count
}

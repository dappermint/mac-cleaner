package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dappermint/ratatouille/internal/config"
	"github.com/dappermint/ratatouille/internal/installer"
	"github.com/dappermint/ratatouille/internal/keymap"
	"github.com/dappermint/ratatouille/internal/optimize"
	"github.com/dappermint/ratatouille/internal/purge"
	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
	"github.com/dappermint/ratatouille/internal/text"
)

func runPurgeCommand(ctx context.Context, home string, identity *storage.CommandIdentity, log *safety.Log, args []string, in io.Reader, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("purge", flag.ContinueOnError)
	flags.SetOutput(errOut)
	dryRun := flags.Bool("dry-run", false, "show what would be removed without changing anything")
	jsonOutput := flags.Bool("json", false, "print machine-readable json")
	minAge := flags.Duration("min-age", 7*24*3600e9, "leave projects touched more recently than this unselected")
	toTrash := flags.Bool("trash", false, "route to Trash instead of removing permanently")
	all := flags.Bool("all", false, "include projects touched inside the age window")
	yes := flags.Bool("yes", false, "skip the confirmation")
	if err := flags.Parse(args); err != nil {
		return err
	}

	artifacts, issues := purge.Find(ctx, home, purge.Options{MinAge: *minAge})
	if *jsonOutput {
		return writeJSON(out, "purge", map[string]any{"artifacts": artifacts, "issues": issues})
	}
	if len(artifacts) == 0 {
		fmt.Fprintf(out, "no build artifacts under %s\n", strings.Join(shortRoots(home, purge.Roots(home, purge.Options{})), ", "))
		fmt.Fprintf(out, "add your own search roots in %s\n", config.Path(home, config.PurgePathsFile))
		return nil
	}

	chosen := make([]purge.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Selected() || *all {
			chosen = append(chosen, artifact)
		}
	}

	fmt.Fprintf(out, "%10s  %-14s %-10s %s\n", "size", "kind", "untouched", "project")
	for _, artifact := range artifacts {
		mark := " "
		if artifact.Selected() || *all {
			mark = "•"
		}
		fmt.Fprintf(out, "%10s %s %-14s %-10s %s\n",
			storage.HumanBytes(artifact.Bytes), mark, artifact.Kind, since(artifact.Modified),
			storage.RelativeHome(home, artifact.Project))
	}
	for _, issue := range issues {
		fmt.Fprintf(out, "\nnote: %s\n", issue)
	}
	fmt.Fprintf(out, "\n%d of %d selected, %s\n", len(chosen), len(artifacts), storage.HumanBytes(purge.Total(chosen)))
	if !*all {
		fmt.Fprintln(out, "projects touched inside the age window are listed but unselected, --all includes them")
	}
	if len(chosen) == 0 {
		return nil
	}

	route := "permanently removed"
	if *toTrash {
		route = "moved to Trash"
	}
	fmt.Fprintf(out, "these will be %s\n", route)

	funnel := safety.NewFunnel(home, identity, *dryRun, log)
	if !*dryRun && !*yes {
		if err := confirmPhrase(in, out, "purge"); err != nil {
			return err
		}
	}
	removed, reclaimed, failures := purge.Remove(ctx, funnel, chosen, *toTrash)
	fmt.Fprintf(out, "\n%s\n", outcome(*dryRun, len(removed), reclaimed))
	return joinErrors(failures)
}

func runInstallerCommand(ctx context.Context, home string, identity *storage.CommandIdentity, log *safety.Log, args []string, in io.Reader, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("installer", flag.ContinueOnError)
	flags.SetOutput(errOut)
	dryRun := flags.Bool("dry-run", false, "show what would be removed without changing anything")
	jsonOutput := flags.Bool("json", false, "print machine-readable json")
	minSize := flags.String("min-size", "16MiB", "ignore anything smaller than this")
	yes := flags.Bool("yes", false, "skip the confirmation")
	if err := flags.Parse(args); err != nil {
		return err
	}
	floor, err := storage.ParseSize(*minSize)
	if err != nil {
		return err
	}

	files := installer.Find(ctx, home, installer.Options{MinSize: floor})
	if *jsonOutput {
		return writeJSON(out, "installers", map[string]any{"files": files})
	}
	if len(files) == 0 {
		fmt.Fprintln(out, "no installer files above the size floor")
		return nil
	}

	fmt.Fprintf(out, "%10s  %-10s %-12s %s\n", "size", "source", "downloaded", "file")
	for _, file := range files {
		fmt.Fprintf(out, "%10s  %-10s %-12s %s\n",
			storage.HumanBytes(file.Bytes), file.Source,
			file.Modified.Local().Format("2006-01-02"), text.Truncate(file.Name, 44))
	}
	fmt.Fprintf(out, "\n%d files, %s, all moved to Trash\n", len(files), storage.HumanBytes(installer.Total(files)))

	funnel := safety.NewFunnel(home, identity, *dryRun, log)
	if !*dryRun && !*yes {
		if err := confirmPhrase(in, out, "installer"); err != nil {
			return err
		}
	}
	removed, skipped, reclaimed, failures := installer.Remove(ctx, funnel, files)
	for _, note := range skipped {
		fmt.Fprintf(out, "skipped %s\n", note)
	}
	fmt.Fprintf(out, "\n%s\n", outcome(*dryRun, len(removed), reclaimed))
	return joinErrors(failures)
}

func runConfigCommand(home string, args []string, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New("config needs a subcommand: show, path, keys, whitelist, purge-paths")
	}
	switch args[0] {
	case "path":
		fmt.Fprintln(out, config.Dir(home))
		return nil
	case "show":
		return showConfig(home, out)
	case "whitelist":
		return showList(home, config.WhitelistFile, out,
			"one target id or absolute path pattern per line, never cleaned")
	case "optimize-whitelist":
		return showList(home, config.OptimizeWhitelistFile, out,
			"one task id per line, never run")
	case "purge-paths":
		return showList(home, config.PurgePathsFile, out,
			"one project search root per line, replacing the defaults")
	case "keys", "keymap":
		return showKeys(home, out)
	default:
		fmt.Fprintf(errOut, "unknown config subcommand %q\n", args[0])
		return errors.New("config needs one of: show, path, keys, whitelist, optimize-whitelist, purge-paths")
	}
}

// showKeys prints the resolved bindings rather than the preset's, so what it
// shows is what the interface will actually do.
func showKeys(home string, out io.Writer) error {
	settings, err := config.LoadSettings(home)
	if err != nil {
		return err
	}
	bindings, err := keymap.Load(settings)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "keymap %s, from %s\n\n", bindings.Name(), config.Path(home, config.SettingsFile))
	for _, mode := range []keymap.Mode{keymap.Normal, keymap.Visual_, keymap.Cmdline} {
		actions := bindings.Actions(mode)
		if len(actions) == 0 {
			continue
		}
		fmt.Fprintf(out, "[%s]\n", mode)
		for _, action := range actions {
			fmt.Fprintf(out, "  %-16s %-18s %s\n",
				action, strings.Join(bindings.Keys(mode, action), " "), keymap.Describe[action])
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintln(out, "rebind by adding to the config file:")
	fmt.Fprintln(out, "  keymap = vim")
	fmt.Fprintln(out, "  [keys]")
	fmt.Fprintln(out, "  mark = m")
	fmt.Fprintln(out, "  execute-marks = X")
	return nil
}

func showConfig(home string, out io.Writer) error {
	settings, err := config.LoadSettings(home)
	if err != nil {
		return err
	}
	preferences := config.PreferencesFrom(settings)
	fmt.Fprintf(out, "config directory  %s\n\n", config.Dir(home))
	fmt.Fprintf(out, "%-18s %-22s %s\n", "setting", "value", "source")
	for _, setting := range []struct {
		key   string
		value string
	}{
		{"keymap", preferences.Keymap},
		{"view", preferences.View},
		{"colour", preferences.Colour},
		{"depth", itoa(preferences.Depth)},
		{"status.interval", preferences.Interval.String()},
		{"purge.min-age", preferences.PurgeAge.String()},
		{"purge.trash", boolText(preferences.TrashOnly)},
	} {
		source := "default"
		if settings.Has(setting.key) {
			source = "config"
		}
		fmt.Fprintf(out, "%-18s %-22s %s\n", setting.key, setting.value, source)
	}
	fmt.Fprintln(out)
	for _, file := range []string{config.WhitelistFile, config.OptimizeWhitelistFile, config.PurgePathsFile} {
		path := config.Path(home, file)
		status := "not present, nothing is protected by it"
		if info, err := os.Stat(path); err == nil {
			status = fmt.Sprintf("%d bytes", info.Size())
		}
		fmt.Fprintf(out, "%-22s %s\n%-22s %s\n", file, path, "", status)
	}
	fmt.Fprintln(out, "\na whitelist can only remove something from a selection, never add one,")
	fmt.Fprintln(out, "and it cannot make a protected path cleanable")
	return nil
}

func showList(home, file string, out io.Writer, description string) error {
	path := config.Path(home, file)
	fmt.Fprintf(out, "%s\n%s\n\n", path, description)
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		fmt.Fprintln(out, "the file does not exist yet, create it to add entries")
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(contents)) == "" {
		fmt.Fprintln(out, "the file is empty")
		return nil
	}
	fmt.Fprint(out, string(contents))
	return nil
}

func runCompletionCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("completion needs a shell: fish, zsh or bash")
	}
	switch args[0] {
	case "fish":
		fmt.Fprint(out, fishCompletion)
	case "zsh":
		fmt.Fprint(out, zshCompletion)
	case "bash":
		fmt.Fprint(out, bashCompletion)
	default:
		return fmt.Errorf("no completion for %q, try fish, zsh or bash", args[0])
	}
	return nil
}

// runUpdateCommand prints the command that upgrades this install rather than
// rewriting the binary. A binary that overwrites itself inside a read-only Nix
// store path is wrong, and one that does it behind Homebrew's back leaves brew
// believing the old version is installed.
func runUpdateCommand(out io.Writer, executable string) {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		resolved = executable
	}
	switch {
	case strings.HasPrefix(resolved, "/nix/store/"):
		fmt.Fprintln(out, "installed through nix")
		fmt.Fprintln(out, "  nix profile upgrade ratatouille")
		fmt.Fprintln(out, "or, if it came from a flake input, update the input and rebuild")
	case strings.Contains(resolved, "/Cellar/") || strings.Contains(resolved, "/Caskroom/") ||
		strings.HasPrefix(resolved, "/opt/homebrew/") || strings.HasPrefix(resolved, "/usr/local/"):
		fmt.Fprintln(out, "installed through homebrew")
		fmt.Fprintln(out, "  brew upgrade ratatouille")
	default:
		fmt.Fprintf(out, "running from %s\n", resolved)
		fmt.Fprintln(out, "this tool does not update itself, install it through one of:")
		fmt.Fprintln(out, "  brew install dappermint/tap/ratatouille")
		fmt.Fprintln(out, "  nix profile install github:dappermint/ratatouille")
	}
}

const touchIDFile = "/etc/pam.d/sudo_local"

func runTouchIDCommand(args []string, out io.Writer) error {
	action := "status"
	if len(args) > 0 {
		action = args[0]
	}
	switch action {
	case "status":
		return touchIDStatus(out)
	case "enable", "disable":
		// Writing to /etc/pam.d needs root, and getting it wrong locks a user
		// out of sudo, so this prints the exact edit rather than making it.
		fmt.Fprintf(out, "%s Touch ID for sudo by editing %s\n\n", action, touchIDFile)
		if action == "enable" {
			fmt.Fprintln(out, "  sudo tee "+touchIDFile+" <<'EOF'")
			fmt.Fprintln(out, "  auth       sufficient     pam_tid.so")
			fmt.Fprintln(out, "  EOF")
		} else {
			fmt.Fprintln(out, "  sudo rm "+touchIDFile)
		}
		fmt.Fprintln(out, "\nthis tool prints the change rather than making it: a mistake in a pam")
		fmt.Fprintln(out, "file locks you out of sudo, and that is not a risk worth automating")
		return nil
	default:
		return fmt.Errorf("touchid takes status, enable or disable, not %q", action)
	}
}

func touchIDStatus(out io.Writer) error {
	if runtime.GOARCH != "arm64" {
		fmt.Fprintln(out, "Touch ID for sudo needs Apple silicon with a Touch ID sensor")
	}
	contents, err := os.ReadFile(touchIDFile)
	if os.IsNotExist(err) {
		fmt.Fprintf(out, "not enabled, %s does not exist\n", touchIDFile)
		return nil
	}
	if err != nil {
		return err
	}
	if strings.Contains(string(contents), "pam_tid.so") {
		fmt.Fprintf(out, "enabled, %s loads pam_tid.so\n", touchIDFile)
		return nil
	}
	fmt.Fprintf(out, "not enabled, %s exists but does not load pam_tid.so\n", touchIDFile)
	return nil
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func itoa(value int) string {
	return fmt.Sprintf("%d", value)
}

// since renders one consistent unit in the column rather than a date on some
// rows and the word "recent" on others.
func since(when time.Time) string {
	if when.IsZero() {
		return "unknown"
	}
	days := int(time.Since(when).Hours() / 24)
	switch {
	case days < 1:
		return "today"
	case days == 1:
		return "1 day"
	case days < 365:
		return fmt.Sprintf("%d days", days)
	default:
		return fmt.Sprintf("%.1f years", float64(days)/365)
	}
}

// outcome exists because "2 removed" after a dry run is a lie, and the
// difference between the two is the whole point of the flag.
func outcome(dryRun bool, count int, bytes int64) string {
	if dryRun {
		return fmt.Sprintf("dry run, %d items and %s would have been freed", count, storage.HumanBytes(bytes))
	}
	return fmt.Sprintf("%d removed, %s freed", count, storage.HumanBytes(bytes))
}

func confirmPhrase(in io.Reader, out io.Writer, phrase string) error {
	fmt.Fprintf(out, "\ntype %q to continue: ", phrase)
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(answer) != phrase {
		return errors.New("confirmation did not match, no changes made")
	}
	return nil
}

func joinErrors(failures []error) error {
	if len(failures) == 0 {
		return nil
	}
	messages := make([]string, 0, len(failures))
	for _, failure := range failures {
		messages = append(messages, failure.Error())
	}
	return errors.New(strings.Join(messages, "; "))
}

func shortRoots(home string, roots []string) []string {
	shown := make([]string, 0, len(roots))
	for _, root := range roots {
		shown = append(shown, storage.RelativeHome(home, root))
	}
	if len(shown) == 0 {
		return []string{"any known project directory"}
	}
	return shown
}

func runOptimizeCommand(ctx context.Context, home string, rootful bool, identity *storage.CommandIdentity, log *safety.Log, args []string, in io.Reader, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("optimize", flag.ContinueOnError)
	flags.SetOutput(errOut)
	list := flags.Bool("list", false, "show the tasks and what each one changes")
	dryRun := flags.Bool("dry-run", false, "say what would run without running it")
	jsonOutput := flags.Bool("json", false, "print machine-readable json")
	only := flags.String("only", "", "run only these task ids, comma separated")
	skip := flags.String("skip", "", "run everything except these task ids")
	yes := flags.Bool("yes", false, "skip the confirmation")
	if err := flags.Parse(args); err != nil {
		return err
	}

	tasks, err := optimize.Select(splitList(*only), append(splitList(*skip), flags.Args()...))
	if err != nil {
		return err
	}
	if *list {
		return listTasks(out, tasks, *jsonOutput)
	}

	env := optimize.NewEnv(home, rootful, identity, *dryRun)
	if !*dryRun && !*yes {
		fmt.Fprintf(out, "%d maintenance tasks will run, each one checked before it does anything\n", len(tasks))
		if err := confirmPhrase(in, out, "optimize"); err != nil {
			return err
		}
	}

	results := optimize.Run(ctx, env, tasks, log)
	if *jsonOutput {
		return writeJSON(out, "optimize-results", results)
	}
	for _, result := range results {
		fmt.Fprintf(out, "%-12s %-28s %s\n", result.Outcome, result.Name, result.Detail)
		if result.Error != "" {
			fmt.Fprintf(out, "%-12s %-28s %s\n", "", "", result.Error)
		}
	}
	counts := optimize.Summarize(results)
	fmt.Fprintf(out, "\n%d applied, %d unchanged, %d skipped, %d unavailable, %d failed\n",
		counts[optimize.OutcomeApplied], counts[optimize.OutcomeUnchanged],
		counts[optimize.OutcomeSkipped], counts[optimize.OutcomeUnavailable],
		counts[optimize.OutcomeFailed])
	if counts[optimize.OutcomeFailed] > 0 {
		return errors.New("some maintenance tasks failed")
	}
	return nil
}

func listTasks(out io.Writer, tasks []optimize.Task, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(out, "optimize-tasks", map[string]any{"tasks": tasks, "declined": optimize.DeclinedTasks()})
	}
	fmt.Fprintf(out, "%-24s %-6s %-12s %s\n", "id", "root", "reverses", "changes")
	for _, task := range tasks {
		root := "no"
		if task.NeedsRoot {
			root = "yes"
		}
		fmt.Fprintf(out, "%-24s %-6s %-12s %s\n", task.ID, root, task.Reverses, task.Changes)
	}
	declined := optimize.DeclinedTasks()
	if len(declined) > 0 {
		fmt.Fprintf(out, "\nnot offered, and why:\n")
		for _, entry := range declined {
			fmt.Fprintf(out, "  %-24s %s\n", entry.ID, entry.Reason)
		}
	}
	return nil
}

func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	trimmed := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			trimmed = append(trimmed, part)
		}
	}
	return trimmed
}

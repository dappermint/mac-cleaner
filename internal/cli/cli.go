package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dappermint/ratatouille/internal/config"
	"github.com/dappermint/ratatouille/internal/diagnostic"
	"github.com/dappermint/ratatouille/internal/exitcode"
	"github.com/dappermint/ratatouille/internal/external"
	"github.com/dappermint/ratatouille/internal/i18n"
	"github.com/dappermint/ratatouille/internal/keymap"
	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/scan"
	"github.com/dappermint/ratatouille/internal/storage"
	"github.com/dappermint/ratatouille/internal/text"
	"github.com/dappermint/ratatouille/internal/tui"
)

func Run(ctx context.Context, version string, args []string, in io.Reader, out, errOut io.Writer) (runErr error) {
	localizer := i18n.EnglishGB()
	if runtime.GOOS != "darwin" {
		return errors.New(localizer.T("cli.macos_only"))
	}
	debug, args := extractDebugFlag(args)
	if debug {
		ctx = diagnostic.WithContext(ctx, errOut)
		started := time.Now()
		command := "tui"
		if len(args) > 0 {
			command = args[0]
		}
		diagnostic.Printf(ctx, "command=%s event=start", command)
		defer func() {
			diagnostic.Printf(ctx, "command=%s event=finish duration=%s error=%v", command, time.Since(started).Round(time.Millisecond), runErr != nil)
		}()
	}
	rootful, args := extractRootFlag(args)
	if err := validateRootMode(rootful, os.Geteuid()); err != nil {
		return err
	}
	home, identity, err := scanIdentity(ctx, rootful)
	if err != nil {
		return err
	}
	log := safety.OpenLog(home, identity)
	settings, settingsErr := config.LoadSettings(home)
	if settingsErr != nil {
		fmt.Fprint(errOut, localizer.T("cli.config_error", settingsErr))
	}
	preferences := config.PreferencesFrom(settings)
	if selected, available := i18n.Load(preferences.Locale); available {
		localizer = selected
	} else {
		fmt.Fprint(errOut, localizer.T("cli.locale_fallback", preferences.Locale))
	}
	ctx = i18n.WithContext(ctx, localizer)

	if len(args) == 0 {
		if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
			printUsage(out, localizer)
			return nil
		}
		keys, keyErr := keymap.Load(settings)
		if keyErr != nil {
			return keyErr
		}
		return tui.Run(ctx, home, rootful, identity, safety.NewFunnel(home, identity, false, log), keys, config.Path(home, config.SettingsFile), out)
	}

	switch args[0] {
	case "scan":
		return runScanCommand(ctx, home, rootful, identity, args[1:], out, errOut)
	case "surface":
		return runSurfaceCommand(ctx, home, rootful, identity, args[1:], out, errOut)
	case "plan":
		return runPlanCommand(ctx, home, rootful, identity, args[1:], out, errOut)
	case "clean":
		return runCleanCommand(ctx, home, rootful, identity, log, args[1:], in, out, errOut)
	case "uninstall":
		return runUninstallCommand(ctx, home, rootful, identity, log, args[1:], in, out, errOut)
	case "purge":
		return runPurgeCommand(ctx, home, identity, log, args[1:], in, out, errOut)
	case "installer":
		return runInstallerCommand(ctx, home, identity, log, args[1:], in, out, errOut)
	case "optimize", "optimise":
		return runOptimizeCommand(ctx, home, rootful, identity, log, args[1:], in, out, errOut)
	case "config":
		return runConfigCommand(home, args[1:], out, errOut)
	case "completion":
		return runCompletionCommand(args[1:], out)
	case "touchid":
		return runTouchIDCommand(args[1:], out)
	case "update":
		runUpdateCommand(out, os.Args[0])
		return nil
	case "status":
		return runStatusCommand(ctx, isTerminal(os.Stdout), args[1:], out, errOut)
	case "history":
		return runHistoryCommand(home, args[1:], out, errOut)
	case "version", "--version", "-version":
		fmt.Fprintln(out, version)
		return nil
	case "help", "--help", "-h":
		printUsage(out, localizer)
		return nil
	default:
		return errors.New(localizer.T("cli.unknown_command", args[0]))
	}
}

func runScanCommand(ctx context.Context, home string, rootful bool, identity *storage.CommandIdentity, args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("scan", flag.ContinueOnError)
	flags.SetOutput(errOut)
	deep := flags.Bool("deep", false, "scan project build state")
	surface := flags.Bool("surface", false, "walk the whole data volume and account for every byte")
	verify := flags.Bool("verify", false, "run a live filesystem check, implies --surface")
	jsonOutput := flags.Bool("json", false, "print machine-readable json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("scan does not take item ids")
	}
	options := scan.Options{Deep: *deep, Rootful: rootful, Surface: *surface || *verify, Verify: *verify}
	if err := options.Validate(); err != nil {
		return err
	}
	report := scan.Configure(home, options, identity).Scan(ctx)
	if *jsonOutput {
		return writeJSON(out, "scan", report)
	}
	printReport(out, report)
	return nil
}

func runSurfaceCommand(ctx context.Context, home string, rootful bool, identity *storage.CommandIdentity, args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("surface", flag.ContinueOnError)
	flags.SetOutput(errOut)
	verify := flags.Bool("verify", false, "run a live filesystem check as well")
	jsonOutput := flags.Bool("json", false, "print machine-readable json")
	depth := flags.Int("depth", 3, "how many levels of the tree to print")
	files := flags.Bool("files", false, "list the largest files instead of the tree")
	minSize := flags.String("min-size", "100MiB", "the floor for the large file list")
	limit := flags.Int("limit", 40, "how many large files to print")
	positional, err := parsePositional(flags, args, 1)
	if err != nil {
		return err
	}
	argument := ""
	if len(positional) == 1 {
		argument = positional[0]
	}

	root, err := surfaceRoot(argument)
	if err != nil {
		return err
	}
	floor, err := storage.ParseSize(*minSize)
	if err != nil {
		return err
	}

	options := scan.Options{
		Rootful:      rootful,
		Surface:      true,
		Verify:       *verify,
		SkipItems:    true,
		SurfaceRoot:  root,
		MinFileBytes: floor,
	}
	if *files {
		options.LargeFileLimit = max(*limit, 1)
	} else {
		options.MinFileBytes = 0
	}
	if err := options.Validate(); err != nil {
		return err
	}

	report := scan.Configure(home, options, identity).Scan(ctx)
	if *jsonOutput {
		return writeJSON(out, "surface", report.Surface)
	}
	if *files {
		printLargeFiles(ctx, out, home, report, *limit)
		return nil
	}
	printSurface(out, report, *depth)
	return nil
}

// parsePositional works around the flag package stopping at the first
// non-flag token, which would leave "surface ~/Downloads --depth 2" with its
// flags unparsed. It peels positionals off one at a time and re-parses what is
// left, so the flag package still decides which tokens are flag values.
func parsePositional(flags *flag.FlagSet, args []string, limit int) ([]string, error) {
	var positional []string
	remaining := args
	for {
		if err := flags.Parse(remaining); err != nil {
			return nil, err
		}
		rest := flags.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		if len(positional) > limit {
			return nil, fmt.Errorf("expected at most %d arguments", limit)
		}
		remaining = rest[1:]
	}
}

// surfaceRoot resolves the optional path argument. An unreadable or missing
// path is an error here rather than an empty walk further down, so the message
// names what went wrong.
func surfaceRoot(argument string) (string, error) {
	if argument == "" {
		return "", nil
	}
	resolved, err := filepath.Abs(argument)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", resolved)
	}
	return resolved, nil
}

func printLargeFiles(ctx context.Context, out io.Writer, home string, report scan.Report, limit int) {
	surface := report.Surface
	if surface == nil || len(surface.LargeFiles) == 0 {
		fmt.Fprintln(out, i18n.FromContext(ctx).T("surface.no_large_files"))
		return
	}
	shown := surface.LargeFiles
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	for _, file := range shown {
		when := ""
		if !file.Modified.IsZero() {
			when = file.Modified.Local().Format("2006-01-02")
		}
		fmt.Fprintf(out, "%10s  %-10s  %s\n", storage.HumanBytes(file.Bytes), when, storage.RelativeHome(home, file.Path))
	}
	fmt.Fprintf(out, "\n%d files, %s walked in %s\n",
		len(shown), storage.HumanBytes(surface.Walked), text.Duration(surface.Elapsed))
}

func runPlanCommand(ctx context.Context, home string, rootful bool, identity *storage.CommandIdentity, args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	flags.SetOutput(errOut)
	deep := flags.Bool("deep", false, "scan project build state")
	allSafe := flags.Bool("all-safe", false, "select every safe action")
	if err := flags.Parse(args); err != nil {
		return err
	}
	report := scan.Configure(home, scan.Options{Deep: *deep, Rootful: rootful}, identity).Scan(ctx)
	items, err := resolveItems(report, flags.Args(), *allSafe)
	if err != nil {
		return err
	}
	fmt.Fprint(out, scan.PlanText(items))
	return nil
}

func runCleanCommand(ctx context.Context, home string, rootful bool, identity *storage.CommandIdentity, log *safety.Log, args []string, in io.Reader, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("clean", flag.ContinueOnError)
	flags.SetOutput(errOut)
	deep := flags.Bool("deep", false, "scan project build state")
	allSafe := flags.Bool("all-safe", false, "select every safe action")
	dryRun := flags.Bool("dry-run", false, "show actions without changing anything")
	externalPath := flags.String("external", "", "clean metadata from a directly mounted external volume")
	yes := flags.Bool("yes", false, "skip the normal confirmation")
	confirm := flags.String("confirm", "", "required phrase for irreversible automation")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *externalPath != "" {
		if *deep || *allSafe || len(flags.Args()) != 0 {
			return errors.New("--external cannot be combined with target ids, --deep, or --all-safe")
		}
		return runExternalClean(ctx, home, identity, log, *externalPath, *dryRun, *yes, in, out)
	}
	report := scan.Configure(home, scan.Options{Deep: *deep, Rootful: rootful}, identity).Scan(ctx)
	items, err := resolveItems(report, flags.Args(), *allSafe)
	if err != nil {
		return err
	}
	fmt.Fprint(out, scan.PlanText(items))
	if *dryRun {
		results := scan.ExecuteItems(ctx, safety.NewFunnel(home, identity, true, log), items, out)
		return scan.ActionErrors(results)
	}

	phrase := scan.ConfirmationPhrase(items)
	if *yes {
		if phrase == "empty trash" && *confirm != phrase {
			return errors.New("irreversible cleanup needs --confirm 'empty trash'")
		}
	} else {
		fmt.Fprintf(out, "\ntype %q to continue: ", phrase)
		answer, readErr := bufio.NewReader(in).ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if strings.TrimSpace(answer) != phrase {
			return errors.New("confirmation did not match, no changes made")
		}
	}

	results := scan.ExecuteItems(ctx, safety.NewFunnel(home, identity, false, log), items, out)
	return scan.ActionErrors(results)
}

func runExternalClean(ctx context.Context, home string, identity *storage.CommandIdentity, log *safety.Log, path string, dryRun, yes bool, in io.Reader, out io.Writer) error {
	scanCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	plan, err := external.Find(scanCtx, path, external.Options{})
	cancel()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "external volume %s\n", plan.Mount.Path)
	if len(plan.Items) == 0 {
		fmt.Fprintln(out, "  no removable metadata found")
		return nil
	}
	for _, item := range plan.Items {
		fmt.Fprintf(out, "  %10s  %-16s %s\n", storage.HumanBytes(item.Bytes), item.Kind, item.Path)
	}
	fmt.Fprintf(out, "\n%d items, %s, removed permanently\n", len(plan.Items), storage.HumanBytes(plan.Bytes()))
	if !dryRun && !yes {
		fmt.Fprint(out, "type \"external\" to continue: ")
		answer, readErr := bufio.NewReader(in).ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if strings.TrimSpace(answer) != "external" {
			return errors.New("confirmation did not match, no changes made")
		}
	}
	results, err := external.Remove(ctx, safety.NewFunnel(home, identity, dryRun, log), plan, external.Options{})
	for _, result := range results {
		verb := "removed"
		if result.DryRun {
			verb = "would remove"
		}
		fmt.Fprintf(out, "  %s  %s\n", verb, result.Path)
	}
	return err
}

func extractRootFlag(args []string) (bool, []string) {
	rootful := false
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--root" {
			rootful = true
			continue
		}
		filtered = append(filtered, arg)
	}
	return rootful, filtered
}

func extractDebugFlag(args []string) (bool, []string) {
	debug := false
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--debug" {
			debug = true
			continue
		}
		filtered = append(filtered, arg)
	}
	return debug, filtered
}

func validateRootMode(rootful bool, effectiveUID int) error {
	if rootful && effectiveUID != 0 {
		return exitcode.Errorf(exitcode.NeedsRoot, "--root requires uid 0: sudo ratatouille --root")
	}
	return nil
}

func scanIdentity(ctx context.Context, rootful bool) (string, *storage.CommandIdentity, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, err
	}
	if !rootful {
		return home, nil, nil
	}
	username := os.Getenv("SUDO_USER")
	uid, uidKnown := sudoID("SUDO_UID")
	gid, gidKnown := sudoID("SUDO_GID")
	if username == "" || username == "root" || !uidKnown || !gidKnown || !validUsername(username) {
		return home, nil, nil
	}
	candidate := filepath.Join("/Users", username)
	if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
		home = candidate
	}
	return home, &storage.CommandIdentity{
		UID:      uid,
		GID:      gid,
		Groups:   userGroups(ctx, username, gid),
		Username: username,
		Home:     home,
	}, nil
}

func sudoID(variable string) (uint32, bool) {
	value, err := strconv.ParseUint(os.Getenv(variable), 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(value), true
}

func userGroups(ctx context.Context, username string, primary uint32) []uint32 {
	output, err := exec.CommandContext(ctx, "/usr/bin/id", "-G", username).Output()
	if err != nil {
		return []uint32{primary}
	}
	groups := make([]uint32, 0, len(strings.Fields(string(output))))
	seen := make(map[uint32]bool)
	for _, field := range strings.Fields(string(output)) {
		value, parseErr := strconv.ParseUint(field, 10, 32)
		if parseErr != nil || seen[uint32(value)] {
			continue
		}
		group := uint32(value)
		seen[group] = true
		groups = append(groups, group)
	}
	if len(groups) == 0 {
		return []uint32{primary}
	}
	return groups
}

func validUsername(username string) bool {
	return username != "" && username != "." && username != ".." && filepath.Base(username) == username
}

func resolveItems(report scan.Report, args []string, allSafe bool) ([]scan.Item, error) {
	ids := make(map[string]bool)
	if allSafe {
		for _, item := range report.Items {
			if item.Risk == scan.RiskSafe && item.Selectable() {
				ids[item.ID] = true
			}
		}
	}
	for _, value := range args {
		for _, id := range strings.Split(value, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				ids[id] = true
			}
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("choose at least one item id or use --all-safe")
	}
	for id := range ids {
		item, ok := scan.ItemByID(report, id)
		if !ok {
			return nil, fmt.Errorf("unknown item id %q", id)
		}
		if !item.Selectable() {
			return nil, fmt.Errorf("item %q is not cleanable", id)
		}
	}
	items := scan.SelectedItems(report, ids)
	sort.SliceStable(items, func(a, b int) bool {
		return scan.RiskOrder(items[a].Risk) < scan.RiskOrder(items[b].Risk)
	})
	return items, nil
}

func printSurface(out io.Writer, report scan.Report, depth int) {
	surface := report.Surface
	if surface == nil || surface.Root == nil {
		fmt.Fprintln(out, "no surface was measured")
		return
	}
	printSurfaceNode(out, surface.Root, surface.Root.Total(), 0, depth)
	fmt.Fprintf(out, "\nwalked %s in %s across %d files, %d unreadable entries\n",
		storage.HumanBytes(surface.Walked), text.Duration(surface.Elapsed), surface.Files, surface.Denied)
	if report.Health != nil {
		printHealth(out, *report.Health)
	}
	for _, fault := range surface.Faults {
		fmt.Fprintf(out, "fault %s: %s\n", fault.Path, fault.Reason)
	}
}

func printSurfaceNode(out io.Writer, node *scan.SurfaceNode, parent int64, level, depth int) {
	share := ""
	if parent > 0 {
		share = fmt.Sprintf("%5.1f%%", float64(node.Total())/float64(parent)*100)
	}
	size := storage.HumanBytes(node.Bytes)
	if node.Bytes < 0 {
		size = "unknown"
	}
	fmt.Fprintf(out, "%-10s %6s  %s%s\n", size, share, strings.Repeat("  ", level), node.Name)
	if level >= depth {
		return
	}
	for _, child := range node.Children {
		printSurfaceNode(out, child, node.Total(), level+1, depth)
	}
}

func printHealth(out io.Writer, health scan.Health) {
	clean := 0
	fmt.Fprintf(out, "\nfilesystem health: %s (%s)\n", health.Level, health.Summary())
	for _, signal := range health.Signals {
		if signal.Level == scan.HealthOK && !strings.HasPrefix(signal.ID, "verify-") {
			clean++
			continue
		}
		fmt.Fprintf(out, "  %-8s %-28s %s\n", signal.Level, signal.Name, signal.Value)
		fmt.Fprintf(out, "           %s\n", signal.Detail)
	}
	if clean > 0 {
		fmt.Fprintf(out, "  %-8s %d further checks reported nothing\n", "ok", clean)
	}
	if !health.Verified {
		fmt.Fprintln(out, "  no live verify was run, add --verify for a filesystem check")
	}
}

// printReport leads with what can be acted on. Sorting everything into one
// table by category buries four selectable rows under twenty read-only ones,
// which is the opposite of what someone running a cleanup tool is looking for.
func printReport(out io.Writer, report scan.Report) {
	if report.Disk.Total > 0 {
		percent := float64(report.Disk.Free) / float64(report.Disk.Total) * 100
		fmt.Fprintf(out, "%s free of %s on %s (%.1f%%)\n",
			storage.HumanBytes(report.Disk.Free), storage.HumanBytes(report.Disk.Total), report.Disk.Path, percent)
	}
	if report.Disk.InUse > 0 {
		fmt.Fprintf(out, "%s in use by this volume, container %s\n", storage.HumanBytes(report.Disk.InUse), report.Disk.Container)
	}
	if report.Health != nil {
		fmt.Fprintf(out, "filesystem health %s\n", report.Health.Level)
	}

	actionable, inventory := splitItems(report.Items)

	fmt.Fprintf(out, "\ncleanup, %s across %d items\n\n", storage.HumanBytes(sumBytes(actionable)), len(actionable))
	if len(actionable) == 0 {
		fmt.Fprintln(out, "  nothing selectable was found")
	} else {
		fmt.Fprintf(out, "  %10s  %-11s %-18s %-22s %s\n", "size", "risk", "id", "category", "item")
		printGrouped(out, actionable)
		fmt.Fprintln(out, "\n  clean these with: rat clean --all-safe --dry-run")
	}

	if len(inventory) > 0 {
		fmt.Fprintf(out, "\ninventory, %s across %d items, none of it selectable\n\n",
			storage.HumanBytes(sumBytes(inventory)), len(inventory))
		fmt.Fprintf(out, "  %10s  %-24s %s\n", "size", "category", "item")
		for _, item := range inventory {
			fmt.Fprintf(out, "  %10s  %-24s %s\n",
				storage.HumanBytes(item.Bytes),
				text.Truncate(storage.DisplayCategory(item.Category), 24), item.Name)
		}
	}

	scope := "user"
	if report.Rootful {
		scope = "root"
	}
	fmt.Fprintf(out, "\nscope %s", scope)
	if len(report.Issues) > 0 {
		fmt.Fprintf(out, ", %d notes", len(report.Issues))
	}
	fmt.Fprintln(out)
}

// printGrouped puts the rows of one catalog target under a single header
// carrying its name and total, so a target that split into eight paths reads as
// one thing with eight parts rather than eight things with the same prefix.
func printGrouped(out io.Writer, items []scan.Item) {
	for _, group := range groupItems(items) {
		if len(group.items) < 2 {
			printItemRow(out, "", group.items[0], group.items[0].Name)
			continue
		}
		fmt.Fprintf(out, "  %10s  %-11s %-18s %-22s %s\n",
			storage.HumanBytes(group.bytes), group.items[0].Risk, group.id,
			text.Truncate(storage.DisplayCategory(group.items[0].Category), 22), groupName(group.items[0]))
		for _, item := range group.items {
			printItemRow(out, "   ", item, leafName(item))
		}
	}
}

type itemGroup struct {
	id    string
	bytes int64
	items []scan.Item
}

// groupItems collects the rows of one target together and orders the groups by
// their combined size. Ordering by the largest single row would put a target
// totalling 2 GiB below a standalone row of 700 MiB, which reads as a mistake.
func groupItems(items []scan.Item) []itemGroup {
	order := make([]string, 0, len(items))
	byTarget := make(map[string]*itemGroup, len(items))
	for index, item := range items {
		key := item.Target
		if key == "" {
			key = fmt.Sprintf("\x00standalone-%d", index)
		}
		group, seen := byTarget[key]
		if !seen {
			group = &itemGroup{id: item.Target}
			byTarget[key] = group
			order = append(order, key)
		}
		group.bytes += item.Bytes
		group.items = append(group.items, item)
	}

	groups := make([]itemGroup, 0, len(order))
	for _, key := range order {
		groups = append(groups, *byTarget[key])
	}
	sort.SliceStable(groups, func(a, b int) bool { return groups[a].bytes > groups[b].bytes })
	return groups
}

func printItemRow(out io.Writer, indent string, item scan.Item, label string) {
	fmt.Fprintf(out, "  %10s  %-11s %-18s %-22s %s%s\n",
		storage.HumanBytes(item.Bytes), item.Risk, text.Truncate(item.ID, 18),
		text.Truncate(storage.DisplayCategory(item.Category), 22), indent, label)
	if item.Unavailable != "" {
		fmt.Fprintf(out, "  %10s  unavailable: %s\n", "", item.Unavailable)
	}
}

// groupName and leafName undo the "target: leaf" naming for the grouped view.
// The full name stays on the item, because outside this view the prefix is what
// makes the row unambiguous.
func groupName(item scan.Item) string {
	if name, _, found := strings.Cut(item.Name, ": "); found {
		return name
	}
	return item.Name
}

func leafName(item scan.Item) string {
	if _, leaf, found := strings.Cut(item.Name, ": "); found {
		return leaf
	}
	return item.Name
}

// splitItems separates what a user can choose from what is only there to
// explain where the space went. Both are sorted biggest first, because size is
// what the eye is scanning for.
func splitItems(items []scan.Item) (actionable, inventory []scan.Item) {
	for _, item := range items {
		if item.Selectable() {
			actionable = append(actionable, item)
			continue
		}
		inventory = append(inventory, item)
	}
	sort.SliceStable(actionable, func(a, b int) bool { return actionable[a].Bytes > actionable[b].Bytes })
	sort.SliceStable(inventory, func(a, b int) bool { return inventory[a].Bytes > inventory[b].Bytes })
	return actionable, inventory
}

func sumBytes(items []scan.Item) int64 {
	var total int64
	for _, item := range items {
		if item.Bytes > 0 {
			total += item.Bytes
		}
	}
	return total
}

func printUsage(out io.Writer, localizer *i18n.Localizer) {
	fmt.Fprint(out, localizer.T("cli.usage"))
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

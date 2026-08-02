package cli

import (
	"bufio"
	"context"
	"encoding/json"
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

	"github.com/dappermint/mac-cleaner/internal/scan"
	"github.com/dappermint/mac-cleaner/internal/storage"
	"github.com/dappermint/mac-cleaner/internal/text"
	"github.com/dappermint/mac-cleaner/internal/tui"
)

func Run(ctx context.Context, version string, args []string, in io.Reader, out, errOut io.Writer) error {
	if runtime.GOOS != "darwin" {
		return errors.New("this tool only supports macOS")
	}
	rootful, args := extractRootFlag(args)
	if err := validateRootMode(rootful, os.Geteuid()); err != nil {
		return err
	}
	home, identity, err := scanIdentity(ctx, rootful)
	if err != nil {
		return err
	}

	if len(args) == 0 {
		if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
			printUsage(out)
			return nil
		}
		return tui.Run(ctx, home, rootful, identity, out)
	}

	switch args[0] {
	case "scan":
		return runScanCommand(ctx, home, rootful, identity, args[1:], out, errOut)
	case "surface":
		return runSurfaceCommand(ctx, home, rootful, identity, args[1:], out, errOut)
	case "plan":
		return runPlanCommand(ctx, home, rootful, identity, args[1:], out, errOut)
	case "clean":
		return runCleanCommand(ctx, home, rootful, identity, args[1:], in, out, errOut)
	case "version", "--version", "-version":
		fmt.Fprintln(out, version)
		return nil
	case "help", "--help", "-h":
		printUsage(out)
		return nil
	default:
		return fmt.Errorf("unknown command %q, try mac-cleaner help", args[0])
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
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
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
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("surface does not take item ids")
	}
	options := scan.Options{Rootful: rootful, Surface: true, Verify: *verify, SkipItems: true}
	if err := options.Validate(); err != nil {
		return err
	}
	report := scan.Configure(home, options, identity).Scan(ctx)
	if *jsonOutput {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report.Surface)
	}
	printSurface(out, report, *depth)
	return nil
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

func runCleanCommand(ctx context.Context, home string, rootful bool, identity *storage.CommandIdentity, args []string, in io.Reader, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("clean", flag.ContinueOnError)
	flags.SetOutput(errOut)
	deep := flags.Bool("deep", false, "scan project build state")
	allSafe := flags.Bool("all-safe", false, "select every safe action")
	dryRun := flags.Bool("dry-run", false, "show actions without changing anything")
	yes := flags.Bool("yes", false, "skip the normal confirmation")
	confirm := flags.String("confirm", "", "required phrase for irreversible automation")
	if err := flags.Parse(args); err != nil {
		return err
	}
	report := scan.Configure(home, scan.Options{Deep: *deep, Rootful: rootful}, identity).Scan(ctx)
	items, err := resolveItems(report, flags.Args(), *allSafe)
	if err != nil {
		return err
	}
	fmt.Fprint(out, scan.PlanText(items))
	if *dryRun {
		results := scan.ExecuteItems(ctx, home, items, true, out)
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

	results := scan.ExecuteItems(ctx, home, items, false, out)
	return scan.ActionErrors(results)
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

func validateRootMode(rootful bool, effectiveUID int) error {
	if rootful && effectiveUID != 0 {
		return errors.New("--root requires uid 0: sudo mac-cleaner --root")
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

func printReport(out io.Writer, report scan.Report) {
	if report.Disk.Total > 0 {
		percent := float64(report.Disk.Free) / float64(report.Disk.Total) * 100
		fmt.Fprintf(out, "%s free of %s on %s (%.1f%%)\n", storage.HumanBytes(report.Disk.Free), storage.HumanBytes(report.Disk.Total), report.Disk.Path, percent)
	}
	if report.Disk.InUse > 0 {
		fmt.Fprintf(out, "%s in use by this volume, container %s\n", storage.HumanBytes(report.Disk.InUse), report.Disk.Container)
	}
	fmt.Fprintln(out)
	if report.Health != nil {
		fmt.Fprintf(out, "filesystem health %s\n\n", report.Health.Level)
	}
	scope := "user"
	if report.Rootful {
		scope = "root"
	}
	fmt.Fprintf(out, "scope %s\n\n", scope)
	fmt.Fprintf(out, "%-25s %-21s %-11s %10s  %s\n", "id", "category", "risk", "size", "item")
	for _, item := range report.Items {
		fmt.Fprintf(out, "%-25s %-21s %-11s %10s  %s\n", item.ID, storage.DisplayCategory(item.Category), item.Risk, storage.HumanBytes(item.Bytes), item.Name)
		if item.Unavailable != "" {
			fmt.Fprintf(out, "  unavailable: %s\n", item.Unavailable)
		}
	}
	if len(report.Issues) > 0 {
		fmt.Fprintf(out, "\nnotes=%d\n", len(report.Issues))
	}
}

func printUsage(out io.Writer) {
	fmt.Fprint(out, `mac-cleaner

storage.Usage:
  mac-cleaner [--root]
  mac-cleaner scan [--root] [--deep] [--surface] [--verify] [--json]
  mac-cleaner surface [--root] [--verify] [--depth n] [--json]
  mac-cleaner plan [--root] [--deep] [--all-safe] <item-id>...
  mac-cleaner clean [--root] [--deep] [--all-safe] [--dry-run] <item-id>...

--root requires uid 0 and adds System Data, macOS and Other Users inventory
--surface accounts for every byte of the data volume, including what it cannot read
--verify runs a live filesystem check and requires uid 0
`)
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

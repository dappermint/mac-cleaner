package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
	"github.com/dappermint/ratatouille/internal/text"
	"github.com/dappermint/ratatouille/internal/uninstall"
)

func runUninstallCommand(ctx context.Context, home string, rootful bool, identity *storage.CommandIdentity, log *safety.Log, args []string, in io.Reader, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	flags.SetOutput(errOut)
	list := flags.Bool("list", false, "show installed apps and the names uninstall accepts")
	dryRun := flags.Bool("dry-run", false, "show what would be removed without changing anything")
	permanent := flags.Bool("permanent", false, "bypass Trash and remove immediately")
	leftoversOnly := flags.Bool("leftovers-only", false, "the app is already gone, remove only what it left")
	jsonOutput := flags.Bool("json", false, "print machine-readable json")
	yes := flags.Bool("yes", false, "skip the confirmation")
	if err := flags.Parse(args); err != nil {
		return err
	}

	env := uninstall.Env{Home: home, Rootful: rootful, Identity: identity}
	apps := uninstall.Inventory(ctx, env)

	if *list || flags.NArg() == 0 {
		return printApps(out, apps, *jsonOutput)
	}

	selected, err := resolveApps(apps, flags.Args())
	if err != nil {
		return err
	}

	funnel := safety.NewFunnel(home, identity, *dryRun, log)
	options := uninstall.Options{Permanent: *permanent, LeftoversOnly: *leftoversOnly, Rootful: rootful}

	if !*dryRun && !*yes {
		if err := confirmUninstall(ctx, env, apps, selected, in, out); err != nil {
			return err
		}
	}

	results := make([]uninstall.Result, 0, len(selected))
	for _, app := range selected {
		fmt.Fprintf(out, "\n%s %s\n", app.Name, app.Version)
		if cask := uninstall.BrewCask(ctx, app, identity); cask != "" {
			fmt.Fprintf(out, "  Homebrew cask %q owns this application\n", cask)
			if err := uninstall.BrewUninstall(ctx, funnel, cask, out); err != nil {
				return fmt.Errorf("uninstalling Homebrew cask %s: %w", cask, err)
			}
			caskOptions := options
			caskOptions.LeftoversOnly = true
			results = append(results, uninstall.Run(ctx, funnel, env, app, apps, caskOptions, out))
			continue
		}
		results = append(results, uninstall.Run(ctx, funnel, env, app, apps, options, out))
	}

	if *jsonOutput {
		return writeJSON(out, "uninstall-results", results)
	}
	return summarize(out, results, *dryRun)
}

func resolveApps(apps []uninstall.App, queries []string) ([]uninstall.App, error) {
	selected := make([]uninstall.App, 0, len(queries))
	for _, query := range queries {
		app, candidates := uninstall.Find(apps, query)
		if app.Path == "" {
			if len(candidates) == 0 {
				return nil, fmt.Errorf("no installed app matches %q, try ratatouille uninstall --list", query)
			}
			names := make([]string, 0, len(candidates))
			for _, candidate := range candidates {
				names = append(names, fmt.Sprintf("%s (%s)", candidate.Name, candidate.Path))
			}
			return nil, fmt.Errorf("%q matches %s, use a bundle id or absolute path", query, strings.Join(names, ", "))
		}
		selected = append(selected, app)
	}
	return selected, nil
}

func confirmUninstall(ctx context.Context, env uninstall.Env, apps, selected []uninstall.App, in io.Reader, out io.Writer) error {
	fmt.Fprintln(out, "this will remove:")
	for _, app := range selected {
		leftovers, skipped := uninstall.Leftovers(ctx, env, app, apps)
		var bytes int64
		for _, leftover := range leftovers {
			bytes += leftover.Bytes
		}
		fmt.Fprintf(out, "  %-28s %10s  the bundle and %d leftover files\n",
			app.Name, storage.HumanBytes(app.Bytes+bytes), len(leftovers))
		for _, skip := range skipped {
			fmt.Fprintf(out, "    keeping %s: %s\n", storage.RelativeHome(env.Home, skip.Path), skip.Reason)
		}
	}
	fmt.Fprint(out, "\ntype \"uninstall\" to continue: ")
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(answer) != "uninstall" {
		return errors.New("confirmation did not match, no changes made")
	}
	return nil
}

func printApps(out io.Writer, apps []uninstall.App, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(out, "applications", apps)
	}
	fmt.Fprintf(out, "%-32s %-38s %10s  %s\n", "name", "bundle", "size", "note")
	for _, app := range apps {
		note := ""
		if app.Protected {
			note = app.Reason
		}
		fmt.Fprintf(out, "%-32s %-38s %10s  %s\n",
			text.Truncate(app.Selector(), 32),
			text.Truncate(app.Bundle, 38),
			storage.HumanBytes(app.Bytes),
			note)
	}
	fmt.Fprintf(out, "\n%d applications, name one exactly to uninstall it\n", len(apps))
	return nil
}

func summarize(out io.Writer, results []uninstall.Result, dryRun bool) error {
	var bytes int64
	var failures []string
	for _, result := range results {
		bytes += result.Bytes
		failures = append(failures, result.Errors...)
	}
	if dryRun {
		fmt.Fprintf(out, "\ndry run, %s would be freed\n", storage.HumanBytes(bytes))
	} else {
		fmt.Fprintf(out, "\n%s freed\n", storage.HumanBytes(bytes))
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

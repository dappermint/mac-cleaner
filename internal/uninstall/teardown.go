package uninstall

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dappermint/ratatouille/internal/plist"
	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

const (
	// CommandName is what the operation log records for an uninstall run.
	CommandName    = "uninstall"
	launchTimeout  = 10 * time.Second
	confirmRetries = 20
)

type Options struct {
	Permanent     bool
	LeftoversOnly bool
	Rootful       bool
}

type Result struct {
	App            App        `json:"app"`
	BundleRemoved  bool       `json:"bundle_removed"`
	LeftoversFound []Leftover `json:"leftovers"`
	Removed        []string   `json:"removed"`
	Skipped        []Skip     `json:"skipped"`
	Errors         []string   `json:"errors,omitempty"`
	Bytes          int64      `json:"bytes"`
}

// Run performs the teardown in the only order that is safe: services first, so
// nothing is writing; then the bundle; then a confirmation that the bundle is
// actually gone; and only then the leftovers. Removing leftovers while the app
// is still installed is how an uninstaller deletes a working app's data.
func Run(ctx context.Context, funnel *safety.Funnel, env Env, app App, installed []App, options Options, out io.Writer) Result {
	result := Result{App: app}
	if app.Protected {
		result.Errors = append(result.Errors, app.Name+" is protected: "+app.Reason)
		return result
	}

	leftovers, skipped := Leftovers(ctx, env, app, installed)
	result.LeftoversFound = leftovers
	result.Skipped = skipped

	if !options.LeftoversOnly {
		for _, note := range stopServices(ctx, app, leftovers, funnel.DryRun()) {
			fmt.Fprintf(out, "  %s\n", note)
		}
		if err := removeBundle(ctx, funnel, app, options, &result, out); err != nil {
			result.Errors = append(result.Errors, err.Error())
			return result
		}
	}

	if !bundleGone(app, funnel.DryRun()) {
		result.Errors = append(result.Errors,
			app.Name+" is still installed, so its files were left alone")
		return result
	}
	result.BundleRemoved = true

	for _, leftover := range leftovers {
		request := safety.Request{Command: CommandName, Item: app.Bundle, Path: leftover.Path, Bytes: leftover.Bytes}
		outcome, err := remove(ctx, funnel, request, options)
		if err != nil {
			result.Errors = append(result.Errors, leftover.Path+": "+storage.CompactError(err))
			continue
		}
		if outcome.Outcome == safety.OutcomeSkipped {
			continue
		}
		result.Removed = append(result.Removed, leftover.Path)
		result.Bytes += leftover.Bytes
		fmt.Fprintf(out, "  %s  %s (%s)\n", verb(funnel.DryRun()), storage.RelativeHome(env.Home, leftover.Path), leftover.Kind)
	}
	return result
}

func verb(dryRun bool) string {
	if dryRun {
		return "would remove"
	}
	return "removed"
}

func removeBundle(ctx context.Context, funnel *safety.Funnel, app App, options Options, result *Result, out io.Writer) error {
	request := safety.Request{Command: CommandName, Item: app.Bundle, Path: app.Path, Bytes: app.Bytes}
	outcome, err := remove(ctx, funnel, request, options)
	if err != nil {
		return fmt.Errorf("removing %s: %w", app.Path, err)
	}
	if outcome.Outcome != safety.OutcomeSkipped {
		result.Removed = append(result.Removed, app.Path)
		result.Bytes += app.Bytes
	}
	fmt.Fprintf(out, "  %s  %s\n", verb(funnel.DryRun()), app.Path)
	return nil
}

func remove(ctx context.Context, funnel *safety.Funnel, request safety.Request, options Options) (safety.Result, error) {
	if options.Permanent {
		return funnel.Remove(ctx, request)
	}
	return funnel.Trash(ctx, request)
}

// bundleGone is the gate between removing the app and removing its data. A dry
// run reports the app as gone so the preview can show the whole plan, which is
// safe precisely because a dry run removes nothing.
func bundleGone(app App, dryRun bool) bool {
	if dryRun {
		return true
	}
	for range confirmRetries {
		if _, err := os.Lstat(app.Path); errors.Is(err, os.ErrNotExist) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// stopServices boots out the launch agents and daemons this app owns, so
// nothing is writing to the files about to be removed. It reports what it did
// rather than failing the uninstall, because a service that is already stopped
// is the normal case.
func stopServices(ctx context.Context, app App, leftovers []Leftover, dryRun bool) []string {
	var notes []string
	for _, leftover := range leftovers {
		if leftover.Kind != "launch agent" && leftover.Kind != "launch daemon" {
			continue
		}
		label := launchLabel(leftover.Path)
		if label == "" {
			continue
		}
		if dryRun {
			notes = append(notes, "would stop the service "+label)
			continue
		}
		if safety.NoAuth() {
			notes = append(notes, "left the service "+label+" running, launchctl is disabled")
			continue
		}
		domain := "gui/" + fmt.Sprint(os.Getuid())
		if leftover.Kind == "launch daemon" {
			domain = "system"
		}
		if _, err := storage.CaptureCommand(ctx, launchTimeout, "/bin/launchctl", "bootout", domain+"/"+label); err == nil {
			notes = append(notes, "stopped the service "+label)
		}
	}
	_ = app
	return notes
}

// launchLabel reads the service label out of the plist rather than assuming it
// matches the filename, because the two are allowed to differ.
func launchLabel(path string) string {
	dict, err := plist.ReadFile(path)
	if err != nil {
		return strings.TrimSuffix(filepath.Base(path), ".plist")
	}
	if label, ok := dict.String("Label"); ok && label != "" {
		return label
	}
	return strings.TrimSuffix(filepath.Base(path), ".plist")
}

// BrewCask reports the cask token that owns this app, if any. A brew-managed
// app has to be removed through brew, otherwise brew's own state still claims
// it is installed.
func BrewCask(ctx context.Context, app App) string {
	caskroom := "/opt/homebrew/Caskroom"
	if _, err := os.Stat(caskroom); err != nil {
		caskroom = "/usr/local/Caskroom"
		if _, err := os.Stat(caskroom); err != nil {
			return ""
		}
	}
	entries, err := os.ReadDir(caskroom)
	if err != nil {
		return ""
	}
	target := strings.ToLower(strings.TrimSuffix(filepath.Base(app.Path), appSuffix))
	for _, entry := range entries {
		if normalize(entry.Name()) == normalize(target) {
			return entry.Name()
		}
	}
	return ""
}

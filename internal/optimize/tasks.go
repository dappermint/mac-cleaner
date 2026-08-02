package optimize

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dappermint/ratatouille/internal/plist"
	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

const commandTimeout = 90 * time.Second

// All returns every task this tool is willing to run. A task earns its place by
// being explainable in one line, checkable before it runs, and either
// rebuildable or reversible afterwards. The list is deliberately shorter than
// what is technically possible; see Declined for what was left out and why.
var All = sync.OnceValue(func() []Task {
	return []Task{
		dnsFlush(),
		finderCacheRefresh(),
		launchServicesRebuild(),
		preventNetworkDSStore(),
		brokenPreferences(),
		orphanedLaunchAgents(),
		loginItemsAudit(),
		quarantineHistory(),
		legacyOverrides(),
		networkStackRefresh(),
		periodicMaintenance(),
	}
})

// Declined is part of the interface, not a comment. A maintenance tool that
// silently omits the risky half looks less capable than one that says which
// half it refused and why.
type Declined struct {
	ID     string
	Reason string
}

func DeclinedTasks() []Declined {
	return []Declined{
		{"sqlite-vacuum", "vacuuming Mail or Safari is not reversible, and copying the database first doubles the space needed at the moment free space is lowest"},
		{"spotlight-reindex", "a rebuild costs hours of battery and CPU, and slow search is rarely the index's fault"},
		{"permission-repair", "resetting home directory permissions breaks any deliberate setup, and there is no way to tell one from the other"},
		{"notification-cleanup", "the notification store is a live database with no documented offline format"},
		{"saved-state-cleanup", "already covered by the saved-application-state cleanup target, where it belongs"},
	}
}

func alwaysNeeded(context.Context, Env) (bool, string) { return true, "" }

func run(ctx context.Context, env Env, name string, args ...string) (string, error) {
	return storage.CaptureCommandAs(ctx, commandTimeout, env.Identity, name, args...)
}

func dnsFlush() Task {
	return Task{
		ID:          "dns-flush",
		Name:        "DNS cache",
		Description: "flush the resolver cache and restart mDNSResponder",
		Changes:     "empties the DNS cache, which repopulates on the next lookup",
		NeedsRoot:   true,
		Reverses:    Rebuildable,
		Probe:       alwaysNeeded,
		Run: func(ctx context.Context, env Env) (Outcome, string, error) {
			if _, err := run(ctx, env, "/usr/bin/dscacheutil", "-flushcache"); err != nil {
				return OutcomeFailed, "", err
			}
			// killall fails when the daemon is not running, which is not a
			// failure of the flush that already succeeded.
			if _, err := run(ctx, env, "/usr/bin/killall", "-HUP", "mDNSResponder"); err != nil {
				return OutcomeApplied, "cache flushed, mDNSResponder was not running", nil //nolint:nilerr // a missing daemon is not an error here
			}
			return OutcomeApplied, "cache flushed and mDNSResponder restarted", nil
		},
	}
}

func finderCacheRefresh() Task {
	return Task{
		ID:          "quicklook-cache",
		Name:        "QuickLook thumbnails",
		Description: "reset the QuickLook thumbnail cache",
		Changes:     "empties the thumbnail cache, which regenerates on demand",
		Reverses:    Rebuildable,
		Probe:       alwaysNeeded,
		Run: func(ctx context.Context, env Env) (Outcome, string, error) {
			if _, err := run(ctx, env, "/usr/bin/qlmanage", "-r", "cache"); err != nil {
				return OutcomeFailed, "", err
			}
			return OutcomeApplied, "thumbnails will regenerate as files are previewed", nil
		},
	}
}

const lsregister = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"

func launchServicesRebuild() Task {
	return Task{
		ID:          "launch-services",
		Name:        "Open With database",
		Description: "rebuild the file association database",
		Changes:     "rebuilds the Open With menu from the apps currently installed",
		Reverses:    Rebuildable,
		Probe: func(_ context.Context, _ Env) (bool, string) {
			if _, err := os.Stat(lsregister); err != nil {
				return false, "lsregister is not present on this system"
			}
			return true, ""
		},
		Run: func(ctx context.Context, env Env) (Outcome, string, error) {
			if _, err := run(ctx, env, lsregister, "-kill", "-r", "-domain", "local", "-domain", "system", "-domain", "user"); err != nil {
				return OutcomeFailed, "", err
			}
			return OutcomeApplied, "duplicate entries in the Open With menu should be gone", nil
		},
	}
}

func preventNetworkDSStore() Task {
	const domain = "com.apple.desktopservices"
	return Task{
		ID:          "no-network-dsstore",
		Name:        "Finder .DS_Store on network shares",
		Description: "stop Finder writing .DS_Store to network and USB volumes",
		Changes:     "sets two keys in " + domain,
		Reverses:    Reversible,
		Probe: func(ctx context.Context, env Env) (bool, string) {
			output, err := run(ctx, env, "/usr/bin/defaults", "read", domain, "DSDontWriteNetworkStores")
			if err == nil && strings.TrimSpace(output) == "1" {
				return false, "already set"
			}
			return true, ""
		},
		Run: func(ctx context.Context, env Env) (Outcome, string, error) {
			for _, key := range []string{"DSDontWriteNetworkStores", "DSDontWriteUSBStores"} {
				if _, err := run(ctx, env, "/usr/bin/defaults", "write", domain, key, "-bool", "true"); err != nil {
					return OutcomeFailed, "", err
				}
			}
			return OutcomeApplied, "takes effect after the next log out; undo with defaults delete " + domain, nil
		},
	}
}

// brokenPreferences uses the in-process plist reader. A preference file that
// will not parse is one macOS has already given up on, and the owning app
// recreates it with defaults.
func brokenPreferences() Task {
	return Task{
		ID:          "broken-preferences",
		Name:        "corrupt preference files",
		Description: "find preference files that no longer parse",
		Changes:     "moves unparseable plists to Trash, where the owning app recreates them",
		Reverses:    Rebuildable,
		Probe: func(_ context.Context, env Env) (bool, string) {
			if len(brokenPlists(env)) == 0 {
				return false, "every preference file parses"
			}
			return true, ""
		},
		Run: func(ctx context.Context, env Env) (Outcome, string, error) {
			broken := brokenPlists(env)
			if len(broken) == 0 {
				return OutcomeUnchanged, "every preference file parses", nil
			}
			funnel := safety.NewFunnel(env.Home, env.Identity, env.DryRun, nil)
			moved := 0
			for _, path := range broken {
				request := safety.Request{Command: CommandName, Item: "broken-preferences", Path: path}
				if _, err := funnel.Trash(ctx, request); err == nil {
					moved++
				}
			}
			return OutcomeApplied, fmt.Sprintf("%d unparseable files moved to Trash", moved), nil
		},
	}
}

func brokenPlists(env Env) []string {
	directory := filepath.Join(env.Home, "Library", "Preferences")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil
	}
	var broken []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".plist" {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, err := entry.Info()
		// An empty preference file is a write that never finished.
		if err != nil || info.Size() == 0 {
			broken = append(broken, path)
			continue
		}
		if _, err := plist.ReadFile(path); err != nil {
			broken = append(broken, path)
		}
	}
	return broken
}

// orphanedLaunchAgents reads the program path out of each agent and checks it
// still exists. Program and ProgramArguments are absolute paths only; anything
// else is not treated as a path at all.
func orphanedLaunchAgents() Task {
	return Task{
		ID:          "orphaned-launch-agents",
		Name:        "orphaned launch agents",
		Description: "find launch agents whose program is gone",
		Changes:     "moves agents pointing at missing programs to Trash",
		Reverses:    Rebuildable,
		Probe: func(_ context.Context, env Env) (bool, string) {
			if len(orphanedAgents(env)) == 0 {
				return false, "every launch agent points at a program that exists"
			}
			return true, ""
		},
		Run: func(ctx context.Context, env Env) (Outcome, string, error) {
			orphans := orphanedAgents(env)
			if len(orphans) == 0 {
				return OutcomeUnchanged, "every launch agent points at a program that exists", nil
			}
			funnel := safety.NewFunnel(env.Home, env.Identity, env.DryRun, nil)
			moved := 0
			for _, path := range orphans {
				request := safety.Request{Command: CommandName, Item: "orphaned-launch-agents", Path: path}
				if _, err := funnel.Trash(ctx, request); err == nil {
					moved++
				}
			}
			return OutcomeApplied, fmt.Sprintf("%d orphaned agents moved to Trash", moved), nil
		},
	}
}

func orphanedAgents(env Env) []string {
	directory := filepath.Join(env.Home, "Library", "LaunchAgents")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil
	}
	var orphans []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".plist" {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		program, ok := agentProgram(path)
		if !ok {
			continue
		}
		if _, err := os.Stat(program); os.IsNotExist(err) {
			orphans = append(orphans, path)
		}
	}
	return orphans
}

func agentProgram(path string) (string, bool) {
	dict, err := plist.ReadFile(path)
	if err != nil {
		// A plist that will not parse is the broken-preferences task's problem,
		// not this one's, and guessing at its contents is how a tool removes
		// something it never read.
		return "", false
	}
	if program, ok := dict.String("Program"); ok && filepath.IsAbs(program) {
		return program, true
	}
	arguments := dict.Strings("ProgramArguments")
	if len(arguments) > 0 && filepath.IsAbs(arguments[0]) {
		return arguments[0], true
	}
	return "", false
}

// loginItemsAudit only reports. Removing a login item means driving System
// Events through AppleScript, which prompts, and the finding is the useful part.
func loginItemsAudit() Task {
	return Task{
		ID:          "login-items",
		Name:        "login items",
		Description: "report login items whose target is missing",
		Changes:     "nothing, this one only looks",
		Reverses:    ReadOnly,
		Probe: func(_ context.Context, env Env) (bool, string) {
			if _, err := os.Stat(filepath.Join(env.Home, "Library", "Application Support", "com.apple.backgroundtaskmanagementagent")); err != nil {
				return false, "no background task database on this system"
			}
			return true, ""
		},
		Run: func(_ context.Context, env Env) (Outcome, string, error) {
			orphans := orphanedAgents(env)
			if len(orphans) == 0 {
				return OutcomeUnchanged, "nothing in LaunchAgents points at a missing program", nil
			}
			names := make([]string, 0, len(orphans))
			for _, path := range orphans {
				names = append(names, filepath.Base(path))
			}
			return OutcomeApplied, "review these: " + strings.Join(names, ", "), nil
		},
	}
}

func quarantineHistory() Task {
	return Task{
		ID:          "quarantine-history",
		Name:        "Gatekeeper download history",
		Description: "clear the record of which app downloaded what",
		Changes:     "empties the quarantine event database",
		Reverses:    Permanent,
		Probe: func(_ context.Context, env Env) (bool, string) {
			matches, _ := filepath.Glob(filepath.Join(env.Home, "Library", "Preferences", "com.apple.LaunchServices.QuarantineEventsV*"))
			if len(matches) == 0 {
				return false, "no quarantine database on this system"
			}
			return true, ""
		},
		Run: func(ctx context.Context, env Env) (Outcome, string, error) {
			matches, _ := filepath.Glob(filepath.Join(env.Home, "Library", "Preferences", "com.apple.LaunchServices.QuarantineEventsV*"))
			for _, path := range matches {
				if _, err := run(ctx, env, "/usr/bin/sqlite3", path, "delete from LSQuarantineEvent"); err != nil {
					return OutcomeFailed, "", err
				}
			}
			return OutcomeApplied, "download history cleared, this does not affect Gatekeeper itself", nil
		},
	}
}

func legacyOverrides() Task {
	const domain = "com.apple.frameworks.diskimages"
	return Task{
		ID:          "legacy-overrides",
		Name:        "legacy tweak overrides",
		Description: "remove disk image verification overrides left by old tweak tools",
		Changes:     "deletes the skip-verify keys, restoring Apple's default of verifying images",
		Reverses:    Reversible,
		Probe: func(ctx context.Context, env Env) (bool, string) {
			if _, err := run(ctx, env, "/usr/bin/defaults", "read", domain, "skip-verify"); err != nil {
				return false, "no override is set"
			}
			return true, ""
		},
		Run: func(ctx context.Context, env Env) (Outcome, string, error) {
			for _, key := range []string{"skip-verify", "skip-verify-locked", "skip-verify-remote"} {
				_, _ = run(ctx, env, "/usr/bin/defaults", "delete", domain, key)
			}
			return OutcomeApplied, "disk images will be verified again before mounting", nil
		},
	}
}

func networkStackRefresh() Task {
	return Task{
		ID:          "network-stack",
		Name:        "routing table and ARP cache",
		Description: "flush stale routes and ARP entries",
		Changes:     "empties the ARP cache, which repopulates from the network",
		NeedsRoot:   true,
		Reverses:    Rebuildable,
		Probe:       alwaysNeeded,
		Run: func(ctx context.Context, env Env) (Outcome, string, error) {
			if _, err := run(ctx, env, "/usr/sbin/arp", "-a", "-d"); err != nil {
				return OutcomeFailed, "", err
			}
			return OutcomeApplied, "ARP cache flushed", nil
		},
	}
}

// periodicMaintenance runs the scripts macOS is supposed to run itself. On a
// machine that sleeps rather than idles, they can go months without firing.
func periodicMaintenance() Task {
	return Task{
		ID:          "periodic-maintenance",
		Name:        "periodic maintenance scripts",
		Description: "run the daily, weekly and monthly scripts if they are stale",
		Changes:     "rotates logs and rebuilds locate and whatis databases",
		NeedsRoot:   true,
		Reverses:    Rebuildable,
		Probe: func(_ context.Context, _ Env) (bool, string) {
			info, err := os.Stat("/var/log/daily.out")
			if err != nil {
				return true, ""
			}
			if time.Since(info.ModTime()) < 7*24*time.Hour {
				return false, "the daily script ran within the last week"
			}
			return true, ""
		},
		Run: func(ctx context.Context, env Env) (Outcome, string, error) {
			if _, err := run(ctx, env, "/usr/sbin/periodic", "daily", "weekly", "monthly"); err != nil {
				return OutcomeFailed, "", err
			}
			return OutcomeApplied, "daily, weekly and monthly scripts completed", nil
		},
	}
}

func unknownTask(id string, all []Task) error {
	names := make([]string, 0, len(all))
	for _, task := range all {
		names = append(names, task.ID)
	}
	return fmt.Errorf("no optimize task named %q, try one of: %s", id, strings.Join(names, ", "))
}

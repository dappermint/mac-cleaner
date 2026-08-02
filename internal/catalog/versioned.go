package catalog

import (
	"context"
	"path/filepath"

	"github.com/dappermint/ratatouille/internal/plist"
	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
	"github.com/dappermint/ratatouille/internal/versioned"
)

type versionSpecs func(Env) []versioned.Spec

func versionedResolver(specifications versionSpecs) PathSpec {
	return Resolver(func(_ context.Context, env Env) []string {
		var paths []string
		for _, specification := range specifications(env) {
			plan, err := versioned.Resolve(specification)
			if err == nil {
				paths = append(paths, plan.Stale...)
			}
		}
		return paths
	})
}

func versionedGuard(specification func(Env, string) versioned.Spec) Guard {
	return Guard{
		Name: "still outside the active version retention set",
		Allow: func(_ context.Context, env Env, path string) (bool, string) {
			return versioned.StillStale(specification(env, path), path)
		},
	}
}

func OwnedByUserOrRoot() Guard {
	return Guard{
		Name: "owned by the invoking user or explicitly running as root",
		Allow: func(_ context.Context, env Env, path string) (bool, string) {
			if env.Rootful {
				return true, ""
			}
			return OwnedByUser().Allow(context.Background(), env, path)
		},
	}
}

func browserVersionTargets() []Target {
	type browser struct {
		id, name, app, framework, process string
	}
	browsers := []browser{
		{id: "chrome-old-versions", name: "old Chrome versions", app: "Google Chrome.app", framework: "Google Chrome Framework.framework", process: "Google Chrome"},
		{id: "brave-old-versions", name: "old Brave versions", app: "Brave Browser.app", framework: "Brave Browser Framework.framework", process: "Brave Browser"},
		{id: "edge-old-versions", name: "old Edge versions", app: "Microsoft Edge.app", framework: "Microsoft Edge Framework.framework", process: "Microsoft Edge"},
	}
	targets := make([]Target, 0, len(browsers))
	for _, item := range browsers {
		specs := func(env Env) []versioned.Spec {
			roots := []string{
				filepath.Join("/Applications", item.app),
				filepath.Join(env.Home, "Applications", item.app),
			}
			result := make([]versioned.Spec, 0, len(roots))
			for _, app := range roots {
				result = append(result, versioned.Spec{
					Root: filepath.Join(app, "Contents", "Frameworks", item.framework, "Versions"), ActiveLink: "Current",
				})
			}
			return result
		}
		targets = append(targets, Target{
			ID: item.id, Name: item.name, Group: GroupBrowsers,
			Category: storage.CategorySystemData, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
			Detail: "superseded browser frameworks, keeping the active release",
			Paths:  []PathSpec{versionedResolver(specs)},
			Guards: []Guard{
				ProcessNotRunning(item.process),
				versionedGuard(func(_ Env, path string) versioned.Spec {
					return versioned.Spec{Root: filepath.Dir(path), ActiveLink: "Current"}
				}),
				OwnedByUserOrRoot(),
			},
			MinBytes: 16 * mib,
			Evidence: "the framework Versions directory has a physical Current symlink, and only non-current version siblings are selected",
			NotTargets: []string{
				"the Current symlink and its target",
				"the browser profile, cookies, history, passwords, extensions, and application bundle outside the version sibling",
			},
		})
	}
	targets = append(targets, edgeUpdaterVersionTarget())
	return targets
}

func edgeUpdaterVersionTarget() Target {
	spec := func(env Env) versioned.Spec {
		return versioned.Spec{
			Root:      filepath.Join(env.Home, "Library", "Application Support", "Microsoft", "EdgeUpdater", "apps", "msedge-stable"),
			Installed: installedEdgeVersion(env.Home),
		}
	}
	return Target{
		ID: "edge-updater-old-versions", Name: "old Edge updater payloads", Group: GroupBrowsers,
		Category: storage.CategorySystemData, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
		Detail: "superseded updater payloads, preserving the installed release and pending updates",
		Paths: []PathSpec{versionedResolver(func(env Env) []versioned.Spec {
			return []versioned.Spec{spec(env)}
		})},
		Guards: []Guard{
			ProcessNotRunning("Microsoft Edge"),
			versionedGuard(func(env Env, _ string) versioned.Spec { return spec(env) }),
			OwnedByUser(),
		},
		MinBytes: 16 * mib,
		Evidence: "versioned updater children strictly older than the installed Edge release are stale; an equal or newer child is retained as a pending update",
		NotTargets: []string{
			"the installed Edge version and any newer staged update",
			"the Edge application, updater configuration, browser profiles, credentials, history, extensions, and cookies",
		},
	}
}

func installedEdgeVersion(home string) string {
	for _, app := range []string{
		"/Applications/Microsoft Edge.app",
		filepath.Join(home, "Applications", "Microsoft Edge.app"),
	} {
		dict, err := plist.ReadFile(filepath.Join(app, "Contents", "Info.plist"))
		if err != nil {
			continue
		}
		if version, ok := dict.String("CFBundleShortVersionString"); ok {
			return version
		}
	}
	return ""
}

func agentVersionTargets() []Target {
	type agent struct {
		id, name, root, active, process string
	}
	agents := []agent{
		{id: "claude-code-old-versions", name: "old Claude Code versions", root: ".local/share/claude/versions", active: ".local/bin/claude", process: "claude"},
		{id: "cursor-agent-old-versions", name: "old Cursor Agent versions", root: ".local/share/cursor-agent/versions", active: ".local/bin/cursor-agent", process: "cursor-agent"},
		{id: "copilot-cli-old-versions", name: "old GitHub Copilot CLI versions", root: ".copilot/pkg/universal", active: ".local/bin/copilot", process: "copilot"},
	}
	targets := make([]Target, 0, len(agents))
	for _, item := range agents {
		spec := func(env Env) versioned.Spec {
			return versioned.Spec{
				Root: filepath.Join(env.Home, item.root), ActiveLink: filepath.Join(env.Home, item.active), KeepPrevious: 1,
			}
		}
		targets = append(targets, Target{
			ID: item.id, Name: item.name, Group: GroupDeveloper,
			Category: storage.CategoryDeveloper, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
			Detail: "superseded agent binaries, keeping the active and previous releases",
			Paths: []PathSpec{versionedResolver(func(env Env) []versioned.Spec {
				return []versioned.Spec{spec(env)}
			})},
			Guards: []Guard{
				ProcessNotRunning(item.process),
				versionedGuard(func(env Env, _ string) versioned.Spec { return spec(env) }),
				OwnedByUser(),
			},
			MinBytes: 1 * mib,
			Evidence: "the launcher is a symlink to one physical version entry, and retention keeps that entry plus the next newest release",
			NotTargets: []string{
				"the active and immediately previous versions",
				"credentials, sessions, configuration, project state, and any directory outside the versions root",
			},
		})
	}
	return targets
}

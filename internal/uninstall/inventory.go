// Package uninstall removes an application and the files it left behind. The
// hard part is not the removal, it is deciding what belongs to the app. Every
// rule here is deliberately narrow: exact bundle id or exact app name, never a
// vendor prefix, never a team id, never a common word.
package uninstall

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dappermint/ratatouille/internal/plist"
	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

const (
	appSuffix       = ".app"
	sizeBudget      = 30 * time.Second
	inventoryBudget = 3 * time.Minute
)

type Scope string

const (
	// ScopeSystem is the sealed system volume. Listed for completeness, never
	// removable.
	ScopeSystem Scope = "system"
	// ScopeLocal is /Applications, shared by every user on the machine.
	ScopeLocal Scope = "local"
	// ScopeUser is ~/Applications.
	ScopeUser Scope = "user"
)

type App struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	Bundle     string    `json:"bundle"`
	Version    string    `json:"version,omitempty"`
	Scope      Scope     `json:"scope"`
	Bytes      int64     `json:"bytes"`
	Modified   time.Time `json:"modified,omitempty"`
	Protected  bool      `json:"protected"`
	Reason     string    `json:"reason,omitempty"`
	Executable string    `json:"-"`
}

// Selector is the exact string `uninstall` accepts for this app. Ambiguity is
// an error rather than a guess, so the selector is what --list prints.
func (a App) Selector() string {
	return a.Name
}

type Env struct {
	Home     string
	Rootful  bool
	Identity *storage.CommandIdentity
}

func roots(home string) []struct {
	path  string
	scope Scope
} {
	return []struct {
		path  string
		scope Scope
	}{
		{"/Applications", ScopeLocal},
		{"/Applications/Utilities", ScopeLocal},
		{filepath.Join(home, "Applications"), ScopeUser},
		{"/System/Applications", ScopeSystem},
		{"/System/Applications/Utilities", ScopeSystem},
	}
}

// Inventory lists every application bundle on the machine, sized. A system app
// is listed so the interface can say why it is not removable, rather than
// pretending it does not exist.
func Inventory(ctx context.Context, env Env) []App {
	ctx, cancel := context.WithTimeout(ctx, inventoryBudget)
	defer cancel()

	var apps []App
	seen := make(map[string]bool)
	for _, root := range roots(env.Home) {
		entries, err := os.ReadDir(root.path)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if filepath.Ext(entry.Name()) != appSuffix {
				continue
			}
			path := filepath.Join(root.path, entry.Name())
			if seen[path] {
				continue
			}
			seen[path] = true
			apps = append(apps, describe(path, root.scope))
		}
	}
	size(ctx, apps)
	sort.SliceStable(apps, func(a, b int) bool {
		if apps[a].Bytes != apps[b].Bytes {
			return apps[a].Bytes > apps[b].Bytes
		}
		return apps[a].Name < apps[b].Name
	})
	return apps
}

func describe(path string, scope Scope) App {
	app := App{
		Path:  path,
		Name:  strings.TrimSuffix(filepath.Base(path), appSuffix),
		Scope: scope,
	}
	if info, err := os.Lstat(path); err == nil {
		app.Modified = info.ModTime()
	}
	if dict, err := plist.ReadFile(filepath.Join(path, "Contents", "Info.plist")); err == nil {
		if bundle, ok := dict.String("CFBundleIdentifier"); ok {
			app.Bundle = bundle
		}
		if version, ok := dict.String("CFBundleShortVersionString"); ok {
			app.Version = version
		} else if version, ok := dict.String("CFBundleVersion"); ok {
			app.Version = version
		}
		if executable, ok := dict.String("CFBundleExecutable"); ok {
			app.Executable = executable
		}
	}
	app.Protected, app.Reason = protection(app)
	return app
}

func protection(app App) (bool, string) {
	if app.Scope == ScopeSystem {
		return true, "part of the sealed system volume"
	}
	if app.Bundle == "" {
		return true, "no bundle identifier, so nothing proves what its files are"
	}
	if safety.ProtectedFromUninstall(app.Bundle) {
		return true, "an Apple system component"
	}
	if err := safety.ValidateForDeletion(app.Path); err != nil {
		return true, "the safety validator refuses this path"
	}
	return false, ""
}

func size(ctx context.Context, apps []App) {
	workers := min(max(runtime.NumCPU()-1, 2), 8)
	queue := make(chan int)
	var waiter sync.WaitGroup
	for range workers {
		waiter.Add(1)
		go func() {
			defer waiter.Done()
			for index := range queue {
				pathCtx, cancel := context.WithTimeout(ctx, sizeBudget)
				usage, _ := storage.PathUsage(pathCtx, apps[index].Path)
				cancel()
				apps[index].Bytes = usage.Bytes
			}
		}()
	}
	for index := range apps {
		select {
		case queue <- index:
		case <-ctx.Done():
		}
	}
	close(queue)
	waiter.Wait()
}

// Find resolves what a user typed to exactly one app. A near miss is reported
// as a list of candidates rather than resolved by picking one.
func Find(apps []App, query string) (App, []App) {
	wanted := normalize(query)
	var partial []App
	for _, app := range apps {
		switch {
		case normalize(app.Name) == wanted, normalize(app.Bundle) == wanted:
			return app, nil
		case strings.Contains(normalize(app.Name), wanted):
			partial = append(partial, app)
		}
	}
	if len(partial) == 1 {
		return partial[0], nil
	}
	return App{}, partial
}

func normalize(value string) string {
	return strings.ToLower(strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.TrimSpace(value)))
}

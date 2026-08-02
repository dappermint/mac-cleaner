package catalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dappermint/ratatouille/internal/plist"
	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

const processTimeout = 5 * time.Second

// ProcessNotRunning keeps a cleanup off a tree an app is holding open. The
// process list is read once per scan into Env, so this costs a map lookup.
func ProcessNotRunning(names ...string) Guard {
	return Guard{
		Name: "process not running: " + strings.Join(names, ", "),
		Allow: func(_ context.Context, env Env, _ string) (bool, string) {
			if !env.ProcessesKnown {
				return false, "the process list is unavailable"
			}
			for _, name := range names {
				if env.Running(name) {
					return false, name + " is running"
				}
			}
			return true, ""
		},
	}
}

// OlderThan refuses anything modified recently. Age is the only evidence some
// targets have that they are not in use.
func OlderThan(age time.Duration) Guard {
	return Guard{
		Name: "older than " + age.String(),
		Allow: func(_ context.Context, env Env, path string) (bool, string) {
			info, err := os.Lstat(path)
			if err != nil {
				return true, ""
			}
			if env.Now.Sub(info.ModTime()) < age {
				return false, "modified in the last " + age.String()
			}
			return true, ""
		},
	}
}

// NotDataProtected reads a bundle id out of the path and checks it against the
// third-party protection table. Password managers, VPN clients and IM clients
// keep credentials and live databases beside their caches.
func NotDataProtected() Guard {
	return Guard{
		Name: "not a data-protected bundle",
		Allow: func(_ context.Context, _ Env, path string) (bool, string) {
			bundle := BundleFromPath(path)
			if bundle == "" {
				return true, ""
			}
			if safety.DataProtected(bundle) {
				return false, bundle + " holds credentials or live state"
			}
			if safety.ProtectedBundle(bundle) {
				return false, bundle + " is a system component"
			}
			return true, ""
		},
	}
}

// AppAbsent offers a path only when it can be positively tied to an application
// that is no longer installed.
//
// The word positively is the whole guard. An earlier version fell back to the
// directory's own name when it held no bundle id, and then treated "no
// installed app is called that" as proof the owner had been uninstalled. That
// is absence of evidence read as evidence of absence, with a delete on the end
// of it, and on a real machine it offered up ~/Library/Application
// Support/CloudDocs, which is iCloud Drive's local data, along with
// FileProvider and a row of vendor directories belonging to installed apps.
//
// So the only accepted evidence is a reverse-DNS bundle id in the path. A bare
// vendor or product directory is never enough, no matter how abandoned it
// looks. This finds fewer leftovers, which is the correct trade.
func AppAbsent() Guard {
	return Guard{
		Name: "the owning app is no longer installed",
		Allow: func(_ context.Context, env Env, path string) (bool, string) {
			if !env.InstalledKnown || len(env.Installed) == 0 {
				return false, "the installed application index is incomplete, so nothing can be proven absent"
			}
			bundle := BundleFromPath(path)
			if bundle == "" {
				return false, "not named for a bundle id, so nothing ties it to an application"
			}
			if !strings.HasPrefix(bundle, "com.") && !strings.HasPrefix(bundle, "org.") &&
				!strings.HasPrefix(bundle, "net.") && !strings.HasPrefix(bundle, "io.") &&
				!strings.HasPrefix(bundle, "dev.") && !strings.HasPrefix(bundle, "app.") &&
				!strings.HasPrefix(bundle, "ai.") && !strings.HasPrefix(bundle, "co.") {
				return false, bundle + " does not look like a bundle id"
			}
			if safety.ProtectedBundle(bundle) {
				return false, bundle + " is a system component"
			}
			if env.AppPresent(bundle) {
				return false, bundle + " is still installed"
			}
			return true, ""
		},
	}
}

// ContainerNotDataProtected reads the bundle id from the container directory
// rather than from the leaf, because the leaf here is always "Caches".
func ContainerNotDataProtected() Guard {
	return Guard{
		Name: "container is not a data-protected bundle",
		Allow: func(_ context.Context, _ Env, path string) (bool, string) {
			bundle := BundleFromContainer(path)
			if bundle == "" {
				return true, ""
			}
			if safety.DataProtected(bundle) {
				return false, bundle + " holds credentials or live state"
			}
			if safety.ProtectedBundle(bundle) {
				return false, bundle + " is a system component"
			}
			return true, ""
		},
	}
}

// ExcludeNames refuses a path whose own name is on the list. It exists so a
// glob can stay broad while a handful of known-sensitive siblings stay out.
func ExcludeNames(names ...string) Guard {
	return Guard{
		Name: "not one of: " + strings.Join(names, ", "),
		Allow: func(_ context.Context, _ Env, path string) (bool, string) {
			base := filepath.Base(path)
			if slices.Contains(names, base) {
				return false, base + " is excluded by name"
			}
			return true, ""
		},
	}
}

// OwnedByUser refuses another user's files unless the run is rootful and
// deliberately targeting them.
func OwnedByUser() Guard {
	return Guard{
		Name: "owned by the invoking user",
		Allow: func(_ context.Context, env Env, path string) (bool, string) {
			if env.Identity == nil {
				return true, ""
			}
			info, err := os.Lstat(path)
			if err != nil {
				return true, ""
			}
			if owner, ok := storage.FileOwner(info); ok && owner != env.Identity.UID {
				return false, "owned by another user"
			}
			return true, ""
		},
	}
}

// NeedsRoot marks a target that is only offered when the run has uid 0.
func NeedsRoot() Guard {
	return Guard{
		Name: "requires --root",
		Allow: func(_ context.Context, env Env, _ string) (bool, string) {
			if !env.Rootful {
				return false, "needs sudo --root"
			}
			return true, ""
		},
	}
}

// BundleFromPath reads the bundle id a cache or container directory is named
// after. Only a name that actually looks like a reverse-dns identifier counts;
// guessing from an arbitrary directory name is how a cleanup ends up matching
// on a vendor prefix.
func BundleFromPath(path string) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".savedState")
	base = strings.TrimSuffix(base, ".plist")
	parts := strings.Split(base, ".")
	if len(parts) < 3 {
		return ""
	}
	for _, part := range parts {
		if part == "" {
			return ""
		}
	}
	return base
}

// BundleFromContainer walks up to the container directory, which is the level
// named for the bundle, and reads the id from there.
func BundleFromContainer(path string) string {
	for current := filepath.Clean(path); current != "/" && current != "."; current = filepath.Dir(current) {
		parent := filepath.Base(filepath.Dir(current))
		if parent == "Containers" || parent == "Group Containers" {
			return BundleFromPath(current)
		}
	}
	return ""
}

// ReadInstalled indexes every installed application by bundle id and by display
// name, both lowercased. It is read once per scan, because the leftovers group
// asks about it for every candidate path.
func ReadInstalled(home string) map[string]bool {
	installed, _ := ReadInstalledState(home)
	return installed
}

func ReadInstalledState(home string) (map[string]bool, bool) {
	installed := make(map[string]bool)
	known := true
	roots := []string{
		"/Applications",
		filepath.Join(home, "Applications"),
		"/System/Applications",
	}
	for _, root := range roots {
		rootInfo, rootErr := os.Stat(root)
		if errors.Is(rootErr, os.ErrNotExist) {
			continue
		}
		if rootErr != nil || !rootInfo.IsDir() {
			known = false
			continue
		}
		walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				known = false
				return nil //nolint:nilerr // one unreadable app must not hide every other installed app
			}
			if path == root {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil || strings.Count(relative, string(filepath.Separator)) >= 4 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !entry.IsDir() || filepath.Ext(entry.Name()) != ".app" {
				return nil
			}
			name := strings.TrimSuffix(entry.Name(), ".app")
			installed[strings.ToLower(name)] = true
			dict, err := plist.ReadFile(filepath.Join(path, "Contents", "Info.plist"))
			if err != nil {
				known = false
				return filepath.SkipDir
			}
			if bundle, ok := dict.String("CFBundleIdentifier"); ok && bundle != "" {
				installed[strings.ToLower(bundle)] = true
				// A helper keeps its parent alive: com.example.app.helper must
				// not read as abandoned just because no .app is named that.
				installed[strings.ToLower(bundle)+".helper"] = true
			}
			return filepath.SkipDir
		})
		if walkErr != nil {
			known = false
		}
	}
	return installed, known
}

// ReadProcesses builds the process name set once per scan.
func ReadProcesses(ctx context.Context) (map[string]bool, bool) {
	processes := make(map[string]bool)
	output, err := storage.CaptureCommand(ctx, processTimeout, "/bin/ps", "-Axco", "command")
	if err != nil {
		return processes, false
	}
	for line := range strings.SplitSeq(output, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || name == "COMMAND" {
			continue
		}
		processes[strings.ToLower(name)] = true
	}
	return processes, true
}

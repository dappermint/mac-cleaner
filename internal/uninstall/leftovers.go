package uninstall

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

// plistSuffix is how a preferences or launch service entry is named.
const plistSuffix = ".plist"

// minNameLength is the floor for matching on an app's display name. Below it a
// name is too close to a common word to be evidence of anything.
const minNameLength = 4

// genericNames are words that appear as directory names for reasons that have
// nothing to do with the app that happens to share the name. Matching any of
// them would delete another vendor's data.
var genericNames = []string{
	"app", "apps", "application", "applications", "assistant", "backup", "backups",
	"browser", "cache", "caches", "chat", "client", "cloud", "code", "console",
	"data", "desktop", "dev", "docs", "editor", "files", "finder", "fonts", "game",
	"games", "helper", "home", "host", "images", "install", "installer", "library",
	"log", "logs", "mail", "manager", "media", "music", "network", "notes", "photo",
	"photos", "player", "plugins", "preferences", "print", "profile", "profiles",
	"remote", "screen", "scripts", "server", "service", "services", "settings",
	"setup", "share", "shared", "shell", "storage", "support", "sync", "system",
	"temp", "terminal", "test", "tools", "update", "updater", "user", "users",
	"utilities", "video", "web", "work",
}

// helperSuffixes are the endings an app's own helper bundles use. They extend an
// exact bundle id, they never replace it, so com.example.app.helper matches but
// com.example.other does not.
var helperSuffixes = []string{
	".helper", ".Helper", ".helper.app", ".updater", ".Updater", ".ShipIt",
	".framework", ".loginitem", ".LoginItem", ".agent", ".Agent", ".service",
	".xpc", ".finder", ".FinderSync", ".quicklook", ".QuickLook", ".notification",
}

type Evidence string

const (
	// EvidenceBundle is an exact bundle identifier match. The strongest
	// evidence there is.
	EvidenceBundle Evidence = "exact bundle id"
	// EvidenceHelper is an exact bundle identifier plus one of the app's own
	// documented helper suffixes.
	EvidenceHelper Evidence = "bundle id with a helper suffix"
	// EvidenceGroup is a team-id-prefixed group container whose remainder is
	// the exact bundle id.
	EvidenceGroup Evidence = "team-prefixed group container"
	// EvidenceName is an exact display-name match, allowed only in the two
	// locations where apps genuinely use display names.
	EvidenceName Evidence = "exact app name"
)

type Leftover struct {
	Path     string   `json:"path"`
	Kind     string   `json:"kind"`
	Evidence Evidence `json:"evidence"`
	Bytes    int64    `json:"bytes"`
}

type Skip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type location struct {
	directory string
	kind      string
	// byName allows display-name matching here. Only two locations get it,
	// because only these two are where apps actually use their display name.
	byName bool
	// suffix is appended to the bundle id to form the entry name.
	suffix string
	// root marks a location outside the user's home, which needs uid 0.
	root bool
}

func locations(home string) []location {
	return []location{
		{directory: filepath.Join(home, "Library", "Application Support"), kind: "application support", byName: true},
		{directory: filepath.Join(home, "Library", "Caches"), kind: "cache"},
		{directory: filepath.Join(home, "Library", "Containers"), kind: "container"},
		{directory: filepath.Join(home, "Library", "Group Containers"), kind: "group container"},
		{directory: filepath.Join(home, "Library", "Preferences"), kind: "preferences", suffix: plistSuffix},
		{directory: filepath.Join(home, "Library", "HTTPStorages"), kind: "http storage"},
		{directory: filepath.Join(home, "Library", "WebKit"), kind: "webkit storage"},
		{directory: filepath.Join(home, "Library", "Cookies"), kind: "cookies", suffix: ".binarycookies"},
		{directory: filepath.Join(home, "Library", "Saved Application State"), kind: "saved state", suffix: ".savedState"},
		{directory: filepath.Join(home, "Library", "Application Scripts"), kind: "application scripts"},
		{directory: filepath.Join(home, "Library", "Logs"), kind: "logs", byName: true},
		{directory: filepath.Join(home, "Library", "LaunchAgents"), kind: "launch agent", suffix: plistSuffix},
		{directory: "/Library/LaunchAgents", kind: "launch agent", suffix: plistSuffix, root: true},
		{directory: "/Library/LaunchDaemons", kind: "launch daemon", suffix: plistSuffix, root: true},
		{directory: "/Library/Application Support", kind: "application support", byName: true, root: true},
		{directory: "/Library/Caches", kind: "cache", root: true},
	}
}

// Leftovers finds what an app left behind. It returns what it will remove and,
// separately, what it looked at and refused, so the refusals are reviewable
// rather than invisible.
func Leftovers(ctx context.Context, env Env, app App, installed []App) ([]Leftover, []Skip) {
	if app.Bundle == "" {
		return nil, []Skip{{Path: app.Path, Reason: "no bundle identifier, so nothing can be matched to it"}}
	}
	siblings := sharedIdentity(app, installed)

	var found []Leftover
	var skipped []Skip
	for _, place := range locations(env.Home) {
		if place.root && !env.Rootful {
			continue
		}
		entries, err := os.ReadDir(place.directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			path := filepath.Join(place.directory, entry.Name())
			evidence, ok := match(entry.Name(), place, app)
			if !ok {
				continue
			}
			if owner, taken := siblings[strings.ToLower(entry.Name())]; taken {
				skipped = append(skipped, Skip{Path: path, Reason: "also claimed by " + owner})
				continue
			}
			if err := safety.ValidateForDeletion(path); err != nil {
				skipped = append(skipped, Skip{Path: path, Reason: "the safety validator refuses it"})
				continue
			}
			if safety.DataProtected(bundleOf(entry.Name())) {
				skipped = append(skipped, Skip{Path: path, Reason: "holds credentials or live state"})
				continue
			}
			usage, _ := storage.PathUsage(ctx, path)
			found = append(found, Leftover{Path: path, Kind: place.kind, Evidence: evidence, Bytes: usage.Bytes})
		}
	}

	slices.SortStableFunc(found, func(a, b Leftover) int {
		return strings.Compare(a.Path, b.Path)
	})
	return found, skipped
}

// match is the whole decision. Everything it accepts is either the exact bundle
// id, that id plus one of the app's own helper suffixes, a team-prefixed group
// container ending in that id, or the exact display name in one of the two
// locations where display names are actually used.
func match(entry string, place location, app App) (Evidence, bool) {
	name := strings.TrimSuffix(entry, place.suffix)
	if place.suffix != "" && name == entry {
		return "", false
	}

	if name == app.Bundle {
		return EvidenceBundle, true
	}
	for _, suffix := range helperSuffixes {
		if name == app.Bundle+suffix {
			return EvidenceHelper, true
		}
	}
	if teamPrefixed(name, app.Bundle) {
		return EvidenceGroup, true
	}
	if place.byName && nameMatches(name, app.Name) {
		return EvidenceName, true
	}
	return "", false
}

// teamPrefixed accepts <TEAMID>.<exact bundle id>, which is how a group
// container is named. The prefix has to look like a team id, ten characters of
// uppercase and digits, so a vendor-wide prefix cannot pass as one.
func teamPrefixed(name, bundle string) bool {
	suffix := "." + bundle
	if !strings.HasSuffix(name, suffix) {
		return false
	}
	prefix := strings.TrimSuffix(name, suffix)
	if len(prefix) != 10 {
		return false
	}
	for _, character := range prefix {
		isUpper := character >= 'A' && character <= 'Z'
		isDigit := character >= '0' && character <= '9'
		if !isUpper && !isDigit {
			return false
		}
	}
	return true
}

// nameMatches allows the app's display name and the obvious spacing variants of
// it, but only above the length floor and never a generic word.
func nameMatches(entry, appName string) bool {
	if len(appName) < minNameLength {
		return false
	}
	if isGeneric(appName) || isGeneric(entry) {
		return false
	}
	return normalize(entry) == normalize(appName)
}

func isGeneric(value string) bool {
	return slices.Contains(genericNames, strings.ToLower(strings.TrimSpace(value)))
}

// sharedIdentity maps every entry name another installed app would also claim.
// If two apps can claim the same file, neither may remove it, which covers a
// second copy under /Volumes, an inverse-name variant, and two apps that
// genuinely share a helper.
func sharedIdentity(app App, installed []App) map[string]string {
	shared := make(map[string]string)
	for _, other := range installed {
		if other.Path == app.Path {
			continue
		}
		if other.Bundle != "" {
			claim(shared, other.Bundle, other.Name)
			for _, suffix := range helperSuffixes {
				claim(shared, other.Bundle+suffix, other.Name)
			}
			claim(shared, other.Bundle+".plist", other.Name)
			claim(shared, other.Bundle+".savedState", other.Name)
			claim(shared, other.Bundle+".binarycookies", other.Name)
		}
		if other.Name != "" {
			claim(shared, other.Name, other.Name)
		}
	}
	return shared
}

func claim(shared map[string]string, key, owner string) {
	shared[strings.ToLower(key)] = owner
}

func bundleOf(entry string) string {
	name := entry
	for _, suffix := range []string{plistSuffix, ".savedState", ".binarycookies"} {
		name = strings.TrimSuffix(name, suffix)
	}
	if strings.Count(name, ".") < 2 {
		return ""
	}
	return name
}

// Package catalog holds the cleanup targets as data rather than as code. The
// interesting properties of a target, its risk, its evidence, its guards and
// what it deliberately does not touch, have to be readable by the interface,
// by the json output, and by a test. A function body is none of those things.
package catalog

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/dappermint/ratatouille/internal/config"
	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

type Risk string

const (
	RiskSafe        Risk = "safe"
	RiskReview      Risk = "review"
	RiskDestructive Risk = "destructive"
	RiskProtected   Risk = "protected"
	RiskInfo        Risk = "info"
)

type Group string

const (
	GroupUserEssentials Group = "user essentials"
	GroupAppCaches      Group = "app caches"
	GroupBrowsers       Group = "browsers"
	GroupCloudOffice    Group = "cloud and office"
	GroupDeveloper      Group = "developer tools"
	GroupApps           Group = "apps and utilities"
	GroupVirtualization Group = "virtualization"
	GroupLeftovers      Group = "app leftovers"
	GroupDeviceBackups  Group = "device backups and firmware"
	GroupSystem         Group = "system"
)

// GroupOrder is the order the interface lists groups in.
var GroupOrder = []Group{
	GroupUserEssentials,
	GroupAppCaches,
	GroupBrowsers,
	GroupCloudOffice,
	GroupDeveloper,
	GroupApps,
	GroupVirtualization,
	GroupLeftovers,
	GroupDeviceBackups,
	GroupSystem,
}

type PathKind int

const (
	// PathHome is a literal path under the invoking user's home.
	PathHome PathKind = iota
	// PathGlob is a filepath.Match pattern under the home unless it is absolute.
	PathGlob
	// PathAbsolute is a literal path outside any home.
	PathAbsolute
	// PathResolver computes its own paths, for trees whose names carry a
	// version or a device identifier.
	PathResolver
)

type PathSpec struct {
	Kind    PathKind
	Pattern string
	Resolve func(ctx context.Context, env Env) []string
}

func Home(pattern string) PathSpec { return PathSpec{Kind: PathHome, Pattern: pattern} }
func Glob(pattern string) PathSpec { return PathSpec{Kind: PathGlob, Pattern: pattern} }
func Absolute(path string) PathSpec {
	return PathSpec{Kind: PathAbsolute, Pattern: path}
}
func Resolver(resolve func(ctx context.Context, env Env) []string) PathSpec {
	return PathSpec{Kind: PathResolver, Resolve: resolve}
}

// Env is everything a guard or a resolver is allowed to know. It is built once
// per scan so a guard cannot turn into a process spawn in a tight loop.
type Env struct {
	Home      string
	Rootful   bool
	Identity  *storage.CommandIdentity
	Whitelist *config.Whitelist
	Processes map[string]bool
	// ProcessesKnown distinguishes a genuinely empty process list from a failed
	// read. Process-sensitive cleanup must refuse when this is false.
	ProcessesKnown bool
	Now            time.Time

	// Installed is every bundle id and app name currently on the machine. The
	// leftovers group needs to know what is gone, and the only way to know that
	// is to know what is still here.
	Installed map[string]bool
	// InstalledKnown is true only when every configured application root and
	// every app bundle in scope was readable.
	InstalledKnown bool
}

// AppPresent reports whether an app with this bundle id or display name is
// still installed. An empty index means the scan never built one, in which case
// nothing is treated as absent, because guessing wrong here removes the data of
// a working app.
func (e Env) AppPresent(name string) bool {
	if !e.InstalledKnown || len(e.Installed) == 0 {
		return true
	}
	return e.Installed[strings.ToLower(name)]
}

func (e Env) Running(name string) bool {
	return e.Processes[strings.ToLower(name)]
}

// Guard is a condition that has to hold before a path is offered, and again
// before it is removed. Preview and execution call the same function, which is
// the only way the two can be kept from disagreeing.
type Guard struct {
	Name  string
	Allow func(ctx context.Context, env Env, path string) (bool, string)
}

type Target struct {
	ID       string
	Name     string
	Group    Group
	Category storage.Category
	Risk     Risk
	Recovery safety.Recovery
	Detail   string
	Paths    []PathSpec
	Guards   []Guard

	// MinBytes keeps noise out of the list. A target below it is measured but
	// not offered.
	MinBytes int64

	// Split gives a path its own selectable row once it is big enough to be
	// worth deciding about on its own. Everything below SplitMinBytes stays in
	// one aggregate row, the same way the surface view names what it can and
	// sums the rest.
	Split         bool
	SplitMinBytes int64

	// Sweep marks a broad catch-all whose paths overlap more specific targets.
	// Sweeps claim last, so the target that actually knows what a directory is
	// wins it, and the sweep reports only the remainder.
	Sweep bool

	// Evidence says what proves this path is what we think it is.
	Evidence      string
	Qualification EvidenceStatus
	Observations  []Observation
	// NotTargets are the sibling paths deliberately excluded, and why. Required
	// for anything above RiskSafe.
	NotTargets []string
}

type EvidenceStatus string

const (
	EvidenceObserved EvidenceStatus = "observed"
	EvidencePending  EvidenceStatus = "pending"
)

type Observation struct {
	Product    string    `json:"product"`
	Version    string    `json:"version"`
	MacOS      string    `json:"macos"`
	Bytes      int64     `json:"bytes"`
	ObservedAt time.Time `json:"observed_at"`
}

// Allows runs the guard chain. The whitelist is consulted last, so a user
// cannot use it to unblock something the earlier guards refused.
func (t Target) Allows(ctx context.Context, env Env, path string) (bool, string) {
	for _, guard := range t.Guards {
		if ok, reason := guard.Allow(ctx, env, path); !ok {
			return false, reason
		}
	}
	if env.Whitelist.Blocks(t.ID, path) {
		return false, "whitelisted"
	}
	if err := safety.ValidateForDeletion(path); err != nil {
		return false, "refused by the safety validator"
	}
	return true, ""
}

// Expand turns the path specs into absolute paths. It does not check whether
// they exist; that is the sizing pass.
func (t Target) Expand(ctx context.Context, env Env) []string {
	var paths []string
	for _, spec := range t.Paths {
		switch spec.Kind {
		case PathHome:
			paths = append(paths, filepath.Join(env.Home, spec.Pattern))
		case PathAbsolute:
			paths = append(paths, filepath.Clean(spec.Pattern))
		case PathGlob:
			pattern := spec.Pattern
			if !filepath.IsAbs(pattern) {
				pattern = filepath.Join(env.Home, pattern)
			}
			matches, err := filepath.Glob(pattern)
			if err != nil {
				continue
			}
			paths = append(paths, matches...)
		case PathResolver:
			if spec.Resolve != nil {
				paths = append(paths, spec.Resolve(ctx, env)...)
			}
		}
	}
	return storage.UniqueStrings(paths)
}

func RiskOrder(risk Risk) int {
	switch risk {
	case RiskSafe:
		return 0
	case RiskReview:
		return 1
	case RiskDestructive:
		return 2
	case RiskProtected:
		return 3
	default:
		return 4
	}
}

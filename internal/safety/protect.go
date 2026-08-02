package safety

import (
	_ "embed"
	"path"
	"strings"
	"sync"
)

//go:embed data/protection.txt
var protectionData string

type tables struct {
	protectedPaths     []string
	systemFast         []string
	systemCritical     map[string]bool
	appleUninstallable map[string]bool
	dataProtected      []string
}

var loadTables = sync.OnceValue(func() tables {
	sections := parseSections(protectionData)
	return tables{
		protectedPaths:     sections["protected-paths"],
		systemFast:         sections["system-fast"],
		systemCritical:     toSet(sections["system-critical"]),
		appleUninstallable: toSet(sections["apple-uninstallable"]),
		dataProtected:      sections["data-protected"],
	}
})

func parseSections(raw string) map[string][]string {
	sections := make(map[string][]string)
	current := ""
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = line[1 : len(line)-1]
			if _, ok := sections[current]; !ok {
				sections[current] = nil
			}
			continue
		}
		if current == "" {
			continue
		}
		sections[current] = append(sections[current], line)
	}
	return sections
}

func toSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func protectedPathReason(candidate string) string {
	for _, protected := range loadTables().protectedPaths {
		if under(candidate, protected) {
			return "the path is inside protected " + protected
		}
	}
	return ""
}

// ProtectedPaths is the literal list, for the health and actions views to
// explain why something visible is not selectable.
func ProtectedPaths() []string {
	return append([]string(nil), loadTables().protectedPaths...)
}

// ProtectedBundle is the cleanup-time fast path. A miss here means leftover
// files, not a removed system component, so a blanket pattern is correct.
func ProtectedBundle(bundle string) bool {
	return matchesAny(loadTables().systemFast, bundle)
}

// ProtectedFromUninstall is the uninstall-time check. A miss here would let a
// user uninstall Finder, so it is an exhaustive list of exact ids rather than a
// pattern, minus the Apple apps a user actually installed themselves.
func ProtectedFromUninstall(bundle string) bool {
	loaded := loadTables()
	if loaded.appleUninstallable[bundle] {
		return false
	}
	if loaded.systemCritical[bundle] {
		return true
	}
	return strings.HasPrefix(bundle, "com.apple.")
}

// DataProtected covers third-party apps whose caches sit beside credentials,
// sessions, or a live database.
func DataProtected(bundle string) bool {
	return matchesAny(loadTables().dataProtected, bundle)
}

func matchesAny(patterns []string, bundle string) bool {
	if bundle == "" {
		return false
	}
	for _, pattern := range patterns {
		if matched, err := path.Match(pattern, bundle); err == nil && matched {
			return true
		}
	}
	return false
}

// SystemCriticalBundles is what the monthly drift audit compares against.
func SystemCriticalBundles() []string {
	loaded := loadTables()
	bundles := make([]string, 0, len(loaded.systemCritical))
	for bundle := range loaded.systemCritical {
		bundles = append(bundles, bundle)
	}
	return bundles
}

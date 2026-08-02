// Package safety owns every removal this binary performs. Nothing outside it
// may call os.Remove, os.RemoveAll, os.Rename, or shell out to rm; the
// forbidigo linter enforces that.
package safety

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type Refusal struct {
	Path   string
	Reason string
}

func (r *Refusal) Error() string {
	return "refusing " + r.Path + ": " + r.Reason
}

func Refused(err error) bool {
	var refusal *Refusal
	return errors.As(err, &refusal)
}

func refuse(path, reason string) error {
	return &Refusal{Path: path, Reason: reason}
}

// dataVolume is where the APFS data volume mounts. Firmlinks make
// /System/Volumes/Data/Users and /Users the same directory, so a path that
// arrives through the mount point has to validate as the path it really is.
const dataVolume = "/System/Volumes/Data"

var allowedRoots = []string{
	"/private/tmp",
	"/private/var/log",
	"/private/var/folders",
	"/private/var/db/diagnostics",
	"/System/Library/Caches/com.apple.coresymbolicationd/data",
}

var deniedRoots = []string{
	"/bin",
	"/sbin",
	"/usr",
	"/System",
	"/Library/Extensions",
	"/etc",
	"/private/etc",
	"/var/db",
	"/private/var/db",
	"/dev",
	"/cores",
	"/.vol",
	"/Network",
}

var deniedExceptions = []string{
	"/usr/local",
}

var bareRoots = []string{
	"/",
	"/Applications",
	"/Library",
	"/Users",
	"/Volumes",
	"/opt",
	"/private",
	"/private/var",
	"/private/var/root",
	"/tmp",
	"/usr/local",
	"/var",
	"/var/root",
}

var libraryBareRoots = []string{
	"Application Support",
	"Caches",
	"Containers",
	"Group Containers",
	"LaunchAgents",
	"LaunchDaemons",
	"Logs",
	"Preferences",
	"PrivilegedHelperTools",
	"Frameworks",
	"Extensions",
	"Keychains",
}

var homeBareRoots = []string{
	"Applications",
	"Desktop",
	"Documents",
	"Downloads",
	"Library",
	"Movies",
	"Music",
	"Pictures",
	"Public",
	".Trash",
	".config",
	".ssh",
	".gnupg",
}

// Normalize folds the data volume mount point into the root namespace and
// cleans the result. It does not resolve symlinks.
func Normalize(path string) string {
	if path == dataVolume {
		return "/"
	}
	if strings.HasPrefix(path, dataVolume+"/") {
		path = path[len(dataVolume):]
	}
	return filepath.Clean(path)
}

// ValidateForDeletion decides whether a path may be handed to the removal
// funnel. Every check is independent and any one of them refusing kills the
// operation. Symlink resolution can only take permission away: a resolved path
// never grants what the literal path lacked.
func ValidateForDeletion(path string) error {
	if err := validateSyntax(path); err != nil {
		return err
	}
	normalized := Normalize(path)
	if err := denyPredicates(normalized); err != nil {
		return err
	}
	for _, resolved := range resolutions(normalized) {
		if err := denyPredicates(resolved); err != nil {
			return refuse(path, "resolves to "+resolved+", "+reasonOf(err))
		}
	}
	return nil
}

func reasonOf(err error) string {
	var refusal *Refusal
	if errors.As(err, &refusal) {
		return refusal.Reason
	}
	return err.Error()
}

func validateSyntax(path string) error {
	if path == "" {
		return refuse(path, "the path is empty")
	}
	if !filepath.IsAbs(path) {
		return refuse(path, "the path is not absolute")
	}
	for _, char := range path {
		if char < 32 || char == 127 {
			return refuse(path, "the path contains a control character")
		}
	}
	if slices.Contains(components(path), "..") {
		return refuse(path, "the path contains a .. component")
	}
	return nil
}

// resolutions returns every form of the path that a removal could actually
// reach: the leaf with its own symlink followed, and the leaf under a
// canonicalized parent. Both are checked because an ancestor link redirects a
// removal while leaving the literal path string innocent.
func resolutions(path string) []string {
	var resolved []string
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if target, err := os.Readlink(path); err == nil {
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(path), target)
			}
			resolved = append(resolved, Normalize(target))
		}
	}
	if hasSymlinkAncestor(path) {
		if parent, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
			resolved = append(resolved, Normalize(filepath.Join(parent, filepath.Base(path))))
		}
	}
	return resolved
}

// hasSymlinkAncestor keeps the common case fork-free: canonicalizing every
// path costs a stat storm, and almost no ancestor is a link.
func hasSymlinkAncestor(path string) bool {
	current := filepath.Dir(path)
	for current != "/" && current != "." {
		info, err := os.Lstat(current)
		if err != nil {
			return false
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return true
		}
		current = filepath.Dir(current)
	}
	return false
}

func denyPredicates(path string) error {
	if isAllowed(path) {
		return nil
	}
	if reason := deniedRootReason(path); reason != "" {
		return refuse(path, reason)
	}
	if isBareRoot(path) {
		return refuse(path, "this is a root directory, its children may be removable but it is not")
	}
	if reason := protectedPathReason(path); reason != "" {
		return refuse(path, reason)
	}
	return nil
}

func isAllowed(path string) bool {
	for _, root := range allowedRoots {
		if strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

func deniedRootReason(path string) string {
	for _, exception := range deniedExceptions {
		if under(path, exception) {
			return ""
		}
	}
	for _, root := range deniedRoots {
		if under(path, root) {
			return "the path is inside " + root
		}
	}
	return ""
}

func isBareRoot(path string) bool {
	if slices.Contains(bareRoots, path) {
		return true
	}
	parts := components(path)
	switch {
	case len(parts) == 2 && (parts[0] == "Users" || parts[0] == "Volumes"):
		return true
	case len(parts) == 2 && parts[0] == "Library":
		return slices.Contains(libraryBareRoots, parts[1])
	case len(parts) == 3 && parts[0] == "Users":
		return slices.Contains(homeBareRoots, parts[2])
	case len(parts) == 4 && parts[0] == "Users" && parts[2] == "Library":
		return slices.Contains(libraryBareRoots, parts[3])
	}
	return false
}

func under(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

// components splits without cleaning, because cleaning would resolve away the
// .. components the syntax check exists to find.
func components(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

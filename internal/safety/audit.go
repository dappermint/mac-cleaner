package safety

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dappermint/ratatouille/internal/plist"
)

// SystemAppRoots are where macOS keeps the applications it ships.
var SystemAppRoots = []string{
	"/System/Applications",
	"/System/Applications/Utilities",
	"/System/Library/CoreServices",
	"/System/Library/CoreServices/Applications",
}

// AuditSystemBundles reports every bundle macOS ships that the protection
// tables do not name. A new macOS release can add apps or change their casing,
// and the runtime com.apple.* blanket would quietly cover both, so the drift is
// invisible without something that looks for it on purpose.
func AuditSystemBundles() (missing []string, checked int) {
	known := make(map[string]bool)
	for _, bundle := range SystemCriticalBundles() {
		known[bundle] = true
	}

	seen := make(map[string]bool)
	for _, root := range SystemAppRoots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if filepath.Ext(entry.Name()) != ".app" {
				continue
			}
			dict, err := plist.ReadFile(filepath.Join(root, entry.Name(), "Contents", "Info.plist"))
			if err != nil {
				continue
			}
			bundle, ok := dict.String("CFBundleIdentifier")
			if !ok || bundle == "" || seen[bundle] {
				continue
			}
			seen[bundle] = true
			checked++
			// Casing matters: macOS has shipped com.apple.bootcampassistant
			// alongside the older com.apple.BootCampAssistant, and a
			// case-sensitive table misses one of them.
			if !known[bundle] {
				missing = append(missing, bundle)
			}
		}
	}
	sort.Strings(missing)
	return missing, checked
}

// AuditReport renders the drift for a CI log or an issue body.
func AuditReport(missing []string, checked int) string {
	if len(missing) == 0 {
		return "checked " + itoa(checked) + " system bundles, all named in the protection table"
	}
	var builder strings.Builder
	builder.WriteString("checked ")
	builder.WriteString(itoa(checked))
	builder.WriteString(" system bundles, ")
	builder.WriteString(itoa(len(missing)))
	builder.WriteString(" not named in the protection table.\n")
	builder.WriteString("add these to [system-critical] in internal/safety/data/protection.txt,\n")
	builder.WriteString("exactly as printed, because the table is case sensitive:\n\n")
	for _, bundle := range missing {
		builder.WriteString(bundle)
		builder.WriteByte('\n')
	}
	return builder.String()
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

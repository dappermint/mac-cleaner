package catalog

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

const (
	finderMetadataLimit  = 20_000
	finderTraversalLimit = 250_000
)

func parityUserTargets() []Target {
	return []Target{
		{
			ID: "finder-metadata", Name: "Finder metadata", Group: GroupUserEssentials,
			Category: storage.CategorySystemData, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
			Detail: "per-folder Finder display metadata, recreated when the folder is viewed",
			Paths: []PathSpec{Resolver(func(_ context.Context, env Env) []string {
				return finderMetadata(env.Home)
			})},
			Guards: []Guard{OwnedByUser()}, MinBytes: 1 * mib,
			Evidence: ".DS_Store is Finder's rebuildable per-directory display metadata; traversal is physical-device bounded and capped",
			NotTargets: []string{
				"files other than .DS_Store, symlinked directories, mounted volumes, and anything beyond the item cap",
			},
		},
		{
			ID: "handoff-pasteboard-cache", Name: "Handoff clipboard cache", Group: GroupUserEssentials,
			Category: storage.CategorySystemData, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
			Detail: "expired Universal Clipboard transfer buffers",
			Paths:  []PathSpec{Glob("Library/Group Containers/group.com.apple.coreservices.useractivityd/shared-pasteboard/*")},
			Guards: []Guard{OlderThan(time.Hour), OwnedByUser()}, MinBytes: 1 * mib,
			Evidence: "direct children of useractivityd's shared-pasteboard staging directory are ephemeral transfer buffers, with the latest hour retained",
			NotTargets: []string{
				"clipboard buffers modified in the last hour and every other useractivityd group-container path",
			},
		},
		{
			ID: "messages-preview-caches", Name: "Messages preview caches", Group: GroupUserEssentials,
			Category: storage.CategorySystemData, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
			Detail: "generated attachment previews and sticker cache entries",
			Paths: []PathSpec{
				Glob("Library/Messages/StickerCache/*"),
				Glob("Library/Messages/Caches/Previews/Attachments/*"),
				Glob("Library/Messages/Caches/Previews/StickerCache/*"),
			},
			Guards: []Guard{ProcessNotRunning("Messages"), OwnedByUser()}, MinBytes: 8 * mib,
			Evidence: "only the named preview and sticker cache children are selected, outside the Messages databases and attachment originals",
			NotTargets: []string{
				"chat databases, original attachments, accounts, identities, drafts, and every Messages path outside the three cache roots",
			},
		},
	}
}

func finderMetadata(home string) []string {
	rootInfo, err := os.Stat(home)
	if err != nil {
		return nil
	}
	rootDevice := deviceNumber(rootInfo)
	paths := make([]string, 0, 256)
	visited := 0
	_ = filepath.WalkDir(home, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if len(paths) >= finderMetadataLimit {
			return fs.SkipAll
		}
		if path == home {
			return nil
		}
		visited++
		if visited > finderTraversalLimit {
			return fs.SkipAll
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			info, infoErr := entry.Info()
			if infoErr != nil || deviceNumber(info) != rootDevice {
				return filepath.SkipDir
			}
			if entry.Name() == ".Trash" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == ".DS_Store" {
			paths = append(paths, path)
		}
		return nil
	})
	return paths
}

func deviceNumber(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Dev)
	}
	return 0
}

func officeTargets() []Target {
	return []Target{{
		ID: "office-app-caches", Name: "office application caches", Group: GroupCloudOffice,
		Category: storage.CategorySystemData, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
		Detail: "generated caches, temporary files, and logs from office applications",
		Paths: []PathSpec{
			Home("Library/Caches/com.microsoft.Word"), Home("Library/Caches/com.microsoft.Excel"),
			Glob("Library/Caches/com.microsoft.Powerpoint/*"), Glob("Library/Caches/com.microsoft.Outlook/*"),
			Glob("Library/Caches/com.apple.iWork.*"), Home("Library/Caches/com.kingsoft.wpsoffice.mac"),
			Glob("Library/Caches/org.mozilla.thunderbird/*"),
			Glob("Library/Containers/com.microsoft.Word/Data/Library/Caches/*"), Glob("Library/Containers/com.microsoft.Word/Data/tmp/*"), Glob("Library/Containers/com.microsoft.Word/Data/Library/Logs/*"),
			Glob("Library/Containers/com.microsoft.Excel/Data/Library/Caches/*"), Glob("Library/Containers/com.microsoft.Excel/Data/tmp/*"), Glob("Library/Containers/com.microsoft.Excel/Data/Library/Logs/*"),
		},
		Guards:   []Guard{ProcessNotRunning("Microsoft Word", "Microsoft Excel", "Microsoft PowerPoint", "Microsoft Outlook", "Pages", "Numbers", "Keynote", "Thunderbird"), OwnedByUser()},
		MinBytes: 16 * mib,
		Evidence: "each path is explicitly named Caches, tmp, or Logs inside an office app's cache or sandbox hierarchy",
		NotTargets: []string{
			"documents, mail stores, accounts, templates, add-ins, preferences, recovery data, and container roots",
		},
	}}
}

func virtualizationCacheTargets() []Target {
	return []Target{{
		ID: "utm-caches", Name: "UTM caches", Group: GroupVirtualization,
		Category: storage.CategoryDeveloper, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
		Detail: "UTM application, sandbox, and temporary caches",
		Paths: []PathSpec{
			Glob("Library/Caches/com.utmapp.UTM/*"),
			Glob("Library/Containers/com.utmapp.UTM/Data/Library/Caches/*"),
			Glob("Library/Containers/com.utmapp.UTM/Data/tmp/*"),
		},
		Guards: []Guard{ProcessNotRunning("UTM"), OwnedByUser()}, MinBytes: 16 * mib,
		Evidence: "only direct children of UTM's named cache and temporary roots are selected",
		NotTargets: []string{
			"virtual machine bundles, disk images, saved state, configuration, downloads, and the UTM container root",
		},
	}}
}

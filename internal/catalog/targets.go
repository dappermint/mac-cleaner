package catalog

import (
	"sync"
	"time"

	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

const (
	mib = int64(1024 * 1024)
	gib = 1024 * mib
)

// unmeasured marks a target whose reclaimed bytes have not been recorded on a
// real machine yet. It is not a placeholder to leave in place: the catalog rule
// is that a target ships with a measured value, and TestMeasuredValues reports
// how much of the catalog still owes one.
const unmeasured = "unmeasured"

// All returns the catalog in display order. Two rules apply to every entry:
// Evidence has to say what proves the path is what we claim, and anything above
// RiskSafe has to name the sibling paths it deliberately leaves alone.
var All = sync.OnceValue(func() []Target {
	targets := make([]Target, 0, 32)
	targets = append(targets, userEssentials()...)
	targets = append(targets, appCaches()...)
	targets = append(targets, browsers()...)
	targets = append(targets, cloudAndOffice()...)
	targets = append(targets, developerTools()...)
	targets = append(targets, deviceBackups()...)
	return targets
})

func userEssentials() []Target {
	return []Target{
		{
			ID:       "user-caches",
			Name:     "per-app cache",
			Group:    GroupUserEssentials,
			Category: storage.CategorySystemData,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "cache directories apps rebuild on next launch",
			Paths: []PathSpec{
				Glob("Library/Caches/*"),
				Glob(".cache/*"),
			},
			Guards: []Guard{
				// These four have an owning tool with its own cleanup command,
				// and that command is already a separate action. Counting them
				// here as well would count the same bytes twice.
				ExcludeNames("Homebrew", "go-build", "uv", "nix-cache-staging", "ratatouille"),
				NotDataProtected(),
				OwnedByUser(),
			},
			MinBytes:      16 * mib,
			Sweep:         true,
			Split:         true,
			SplitMinBytes: 128 * mib,
			Evidence:      "each entry is a direct child of a cache directory whose whole purpose is rebuildable data",
			Measured:      unmeasured,
			NotTargets: []string{
				"anything matching the data-protected table, which is where password managers and IM clients keep live state",
				"com.apple.* entries, which the fast protection table blankets",
				"caches owned by a tool that has its own cleanup command, which would otherwise be counted twice",
			},
		},
		{
			ID:       "user-logs",
			Name:     "application logs",
			Group:    GroupUserEssentials,
			Category: storage.CategorySystemData,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "log directories apps recreate when they next write",
			Paths:    []PathSpec{Glob("Library/Logs/*")},
			Guards: []Guard{
				ExcludeNames("ratatouille", "DiagnosticReports", "CrashReporter"),
				NotDataProtected(),
				OwnedByUser(),
			},
			MinBytes:      8 * mib,
			Sweep:         true,
			Split:         true,
			SplitMinBytes: 32 * mib,
			Evidence:      "each entry is a direct child of the per-user log directory",
			Measured:      unmeasured,
			NotTargets: []string{
				"DiagnosticReports, which is the crash history a support case needs",
				"this tool's own operation log",
			},
		},
		{
			ID:       "crash-reporter-state",
			Name:     "crash reporter state",
			Group:    GroupUserEssentials,
			Category: storage.CategorySystemData,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "per-app crash counters, rebuilt on the next crash",
			Paths:    []PathSpec{Home("Library/Application Support/CrashReporter")},
			Guards:   []Guard{OwnedByUser()},
			MinBytes: 1 * mib,
			Evidence: "a fixed Apple path holding only counter plists",
			Measured: unmeasured,
		},
		{
			ID:       "saved-application-state",
			Name:     "saved window state",
			Group:    GroupUserEssentials,
			Category: storage.CategorySystemData,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "reopen-windows state for apps not used in a month",
			Paths:    []PathSpec{Glob("Library/Saved Application State/*.savedState")},
			Guards:   []Guard{OlderThan(30 * 24 * time.Hour), NotDataProtected(), OwnedByUser()},
			MinBytes: 1 * mib,
			Evidence: "the .savedState suffix is the documented shape of this directory",
			Measured: unmeasured,
			NotTargets: []string{
				"state touched in the last 30 days, which belongs to an app the user still has open sessions in",
			},
		},
		{
			ID:       "incomplete-downloads",
			Name:     "incomplete downloads",
			Group:    GroupUserEssentials,
			Category: storage.CategoryDocuments,
			Risk:     RiskReview,
			Recovery: safety.RecoveryTrash,
			Detail:   "partial transfers that no longer have a resumable source",
			Paths: []PathSpec{
				Glob("Downloads/*.download"),
				Glob("Downloads/*.crdownload"),
				Glob("Downloads/*.part"),
				Glob("Downloads/*.partial"),
			},
			Guards:   []Guard{OlderThan(7 * 24 * time.Hour), OwnedByUser()},
			MinBytes: 1 * mib,
			Evidence: "the suffix is written by the downloader itself and removed on completion",
			Measured: unmeasured,
			NotTargets: []string{
				"anything modified in the last week, which may still be an active transfer",
				"completed downloads, which are user documents",
			},
		},
	}
}

func appCaches() []Target {
	return []Target{
		{
			ID:            "container-caches",
			Name:          "sandbox container caches",
			Group:         GroupAppCaches,
			Category:      storage.CategorySystemData,
			Risk:          RiskSafe,
			Recovery:      safety.RecoveryTrash,
			Detail:        "the cache directory inside each sandboxed app's container",
			Paths:         []PathSpec{Glob("Library/Containers/*/Data/Library/Caches")},
			Guards:        []Guard{ContainerNotDataProtected(), OwnedByUser()},
			MinBytes:      16 * mib,
			Split:         true,
			SplitMinBytes: 128 * mib,
			Evidence:      "the sandbox layout puts rebuildable data at exactly this path inside every container",
			Measured:      unmeasured,
			NotTargets: []string{
				"Data/Library/Application Support and Data/Documents, which are the app's real state",
			},
		},
		{
			ID:            "group-container-caches",
			Name:          "group container caches",
			Group:         GroupAppCaches,
			Category:      storage.CategorySystemData,
			Risk:          RiskSafe,
			Recovery:      safety.RecoveryTrash,
			Detail:        "the cache directory inside each app group container",
			Paths:         []PathSpec{Glob("Library/Group Containers/*/Library/Caches")},
			Guards:        []Guard{ContainerNotDataProtected(), OwnedByUser()},
			MinBytes:      16 * mib,
			Split:         true,
			SplitMinBytes: 128 * mib,
			Evidence:      "the group container layout mirrors the sandbox layout",
			Measured:      unmeasured,
			NotTargets: []string{
				"the group container root, which is where shared app databases live",
			},
		},
	}
}

func browsers() []Target {
	return []Target{
		chromiumBrowser("chrome", "Google Chrome", "Library/Caches/Google/Chrome", "Library/Application Support/Google/Chrome"),
		chromiumBrowser("edge", "Microsoft Edge", "Library/Caches/Microsoft Edge", "Library/Application Support/Microsoft Edge"),
		chromiumBrowser("brave", "Brave Browser", "Library/Caches/BraveSoftware/Brave-Browser", "Library/Application Support/BraveSoftware/Brave-Browser"),
		chromiumBrowser("vivaldi", "Vivaldi", "Library/Caches/Vivaldi", "Library/Application Support/Vivaldi"),
		chromiumBrowser("arc", "Arc", "Library/Caches/company.thebrowser.Browser", "Library/Application Support/Arc/User Data"),
		{
			ID:       "firefox-cache",
			Name:     "Firefox cache",
			Group:    GroupBrowsers,
			Category: storage.CategorySystemData,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "the network and startup caches, rebuilt on next browse",
			Paths: []PathSpec{
				Glob("Library/Caches/Firefox/Profiles/*/cache2"),
				Glob("Library/Caches/Firefox/Profiles/*/startupCache"),
			},
			Guards:   []Guard{ProcessNotRunning("firefox"), OwnedByUser()},
			MinBytes: 8 * mib,
			Evidence: "cache2 and startupCache are Firefox's own names for its rebuildable stores, and they sit outside the profile directory that holds the user's data",
			Measured: unmeasured,
			NotTargets: []string{
				"Application Support/Firefox/Profiles, which holds cookies, logins, history and extensions",
			},
		},
	}
}

// chromiumBrowser builds the same target shape for every Chromium fork. They
// share a profile layout, so one description covers all of them and the
// per-browser difference is only where the root sits.
func chromiumBrowser(id, process, cacheRoot, profileRoot string) Target {
	return Target{
		ID:       id + "-cache",
		Name:     process + " cache",
		Group:    GroupBrowsers,
		Category: storage.CategorySystemData,
		Risk:     RiskSafe,
		Recovery: safety.RecoveryTrash,
		Detail:   "network, code and GPU caches, rebuilt on next browse",
		Paths: []PathSpec{
			Home(cacheRoot),
			Glob(profileRoot + "/*/Service Worker/CacheStorage"),
			Glob(profileRoot + "/*/Code Cache"),
			Glob(profileRoot + "/*/GPUCache"),
			Glob(profileRoot + "/*/Application Cache"),
		},
		Guards:   []Guard{ProcessNotRunning(process), OwnedByUser()},
		MinBytes: 8 * mib,
		Evidence: "these four directory names are Chromium's own rebuildable caches and are recreated on the next page load",
		Measured: unmeasured,
		NotTargets: []string{
			"Login Data, Cookies, Local Storage, History, Bookmarks and Extensions, which are the profile itself",
			"Service Worker/Database, which holds registrations rather than cached responses",
		},
	}
}

func cloudAndOffice() []Target {
	return []Target{
		{
			ID:       "dropbox-cache",
			Name:     "Dropbox cache",
			Group:    GroupCloudOffice,
			Category: storage.CategorySystemData,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "the local sync cache, refetched on demand",
			Paths:    []PathSpec{Home("Library/Caches/com.getdropbox.dropbox")},
			Guards:   []Guard{ProcessNotRunning("Dropbox"), OwnedByUser()},
			MinBytes: 16 * mib,
			Evidence: "a bundle-id-named cache directory belonging to a client that can refetch from the server",
			Measured: unmeasured,
		},
		{
			ID:       "onedrive-cache",
			Name:     "OneDrive cache",
			Group:    GroupCloudOffice,
			Category: storage.CategorySystemData,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "the local sync cache, refetched on demand",
			Paths:    []PathSpec{Home("Library/Caches/com.microsoft.OneDrive")},
			Guards:   []Guard{ProcessNotRunning("OneDrive"), OwnedByUser()},
			MinBytes: 16 * mib,
			Evidence: "a bundle-id-named cache directory belonging to a client that can refetch from the server",
			Measured: unmeasured,
		},
		{
			ID:       "google-drive-cache",
			Name:     "Google Drive cache",
			Group:    GroupCloudOffice,
			Category: storage.CategorySystemData,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "the local sync cache, refetched on demand",
			Paths:    []PathSpec{Home("Library/Caches/com.google.GoogleDrive")},
			Guards:   []Guard{ProcessNotRunning("Google Drive"), OwnedByUser()},
			MinBytes: 16 * mib,
			Evidence: "a bundle-id-named cache directory belonging to a client that can refetch from the server",
			Measured: unmeasured,
		},
	}
}

func developerTools() []Target {
	return []Target{
		{
			ID:       "xcode-derived-data",
			Name:     "Xcode derived data",
			Group:    GroupDeveloper,
			Category: storage.CategoryDeveloper,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "build products and indexes, rebuilt by the next build",
			Paths:    []PathSpec{Home("Library/Developer/Xcode/DerivedData")},
			Guards:   []Guard{ProcessNotRunning("Xcode", "xcodebuild"), OwnedByUser()},
			MinBytes: 64 * mib,
			Evidence: "Xcode's own documented location for regenerable build output",
			Measured: unmeasured,
		},
		{
			ID:       "xcode-device-support",
			Name:     "Xcode device support",
			Group:    GroupDeveloper,
			Category: storage.CategoryDeveloper,
			Risk:     RiskReview,
			Recovery: safety.RecoveryTrash,
			Detail:   "symbol sets per iOS version, re-copied when a device reconnects",
			Paths: []PathSpec{
				Glob("Library/Developer/Xcode/iOS DeviceSupport/*"),
				Glob("Library/Developer/Xcode/watchOS DeviceSupport/*"),
				Glob("Library/Developer/Xcode/tvOS DeviceSupport/*"),
			},
			Guards:        []Guard{ProcessNotRunning("Xcode"), OwnedByUser()},
			MinBytes:      64 * mib,
			Split:         true,
			SplitMinBytes: 256 * mib,
			Evidence:      "each entry is named for the OS build it was copied from and is regenerated on the next device attach",
			Measured:      unmeasured,
			NotTargets: []string{
				"the DeviceSupport root itself, so a partially removed set still has a home",
				"Archives, which are shipping artifacts rather than build intermediates",
			},
		},
		{
			ID:            "xcode-archives",
			Name:          "Xcode archives",
			Group:         GroupDeveloper,
			Category:      storage.CategoryDeveloper,
			Risk:          RiskReview,
			Recovery:      safety.RecoveryTrash,
			Detail:        "archived builds, which may be the only copy of a shipped binary",
			Paths:         []PathSpec{Glob("Library/Developer/Xcode/Archives/*")},
			Guards:        []Guard{OlderThan(180 * 24 * time.Hour), ProcessNotRunning("Xcode"), OwnedByUser()},
			MinBytes:      64 * mib,
			Split:         true,
			SplitMinBytes: 256 * mib,
			Evidence:      "a dated directory under Xcode's archive root",
			Measured:      unmeasured,
			NotTargets: []string{
				"anything under six months old, because an archive is how a crash report gets symbolicated",
			},
		},
		{
			ID:       "coresimulator-caches",
			Name:     "simulator caches",
			Group:    GroupDeveloper,
			Category: storage.CategoryDeveloper,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "simulator dyld and asset caches, rebuilt on next boot",
			Paths: []PathSpec{
				Home("Library/Developer/CoreSimulator/Caches"),
				Home("Library/Logs/CoreSimulator"),
			},
			Guards:   []Guard{ProcessNotRunning("Simulator", "simdiskimaged"), OwnedByUser()},
			MinBytes: 16 * mib,
			Evidence: "CoreSimulator's own cache and log roots, separate from Devices which holds simulator state",
			Measured: unmeasured,
			NotTargets: []string{
				"CoreSimulator/Devices, which is the simulator's installed apps and data",
			},
		},
		{
			ID:       "jetbrains-logs",
			Name:     "JetBrains logs and caches",
			Group:    GroupDeveloper,
			Category: storage.CategoryDeveloper,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "IDE index caches and logs, rebuilt on next open",
			Paths: []PathSpec{
				Glob("Library/Caches/JetBrains/*"),
				Glob("Library/Logs/JetBrains/*"),
			},
			Guards:        []Guard{OwnedByUser()},
			MinBytes:      32 * mib,
			Split:         true,
			SplitMinBytes: 128 * mib,
			Evidence:      "JetBrains splits caches and logs out of Application Support, which is where its settings and credentials live",
			Measured:      unmeasured,
			NotTargets: []string{
				"Application Support/JetBrains, which holds licences, settings and keymaps",
			},
		},
		{
			ID:       "gradle-daemon",
			Name:     "Gradle daemon state",
			Group:    GroupDeveloper,
			Category: storage.CategoryDeveloper,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "daemon logs and worker scratch, recreated per build",
			Paths: []PathSpec{
				Home(".gradle/daemon"),
				Home(".gradle/workers"),
				Home(".gradle/native"),
			},
			Guards:   []Guard{ProcessNotRunning("java", "gradle"), OwnedByUser()},
			MinBytes: 8 * mib,
			Evidence: "Gradle recreates these three directories on the next invocation",
			Measured: unmeasured,
			NotTargets: []string{
				".gradle/caches, which holds resolved dependencies that would have to be redownloaded",
			},
		},
		{
			ID:       "maven-repository",
			Name:     "Maven repository",
			Group:    GroupDeveloper,
			Category: storage.CategoryDeveloper,
			Risk:     RiskReview,
			Recovery: safety.RecoveryTrash,
			Detail:   "downloaded artifacts, refetched on next build with a network",
			Paths:    []PathSpec{Home(".m2/repository")},
			Guards:   []Guard{ProcessNotRunning("java", "mvn"), OwnedByUser()},
			MinBytes: 256 * mib,
			Evidence: "Maven's default local repository, which it repopulates from the configured remotes",
			Measured: unmeasured,
			NotTargets: []string{
				".m2/settings.xml, which holds repository credentials",
				"anything installed locally with mvn install and not published anywhere, which this cannot distinguish and is why the tier is review",
			},
		},
		{
			ID:       "python-caches",
			Name:     "Python tool caches",
			Group:    GroupDeveloper,
			Category: storage.CategoryDeveloper,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "wheel and package download caches",
			Paths: []PathSpec{
				Home("Library/Caches/pip"),
				Home(".cache/pip"),
				Home(".conda/pkgs"),
			},
			Guards:   []Guard{OwnedByUser()},
			MinBytes: 16 * mib,
			Evidence: "each is the documented cache location of its tool, refetched from the index on demand",
			Measured: unmeasured,
		},
		{
			ID:       "javascript-caches",
			Name:     "JavaScript tool caches",
			Group:    GroupDeveloper,
			Category: storage.CategoryDeveloper,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "package manager and build tool download caches",
			Paths: []PathSpec{
				Home("Library/Caches/Yarn"),
				Home(".bun/install/cache"),
				Home("Library/Caches/node-gyp"),
				Home("Library/Caches/electron"),
				Home("Library/Caches/ms-playwright"),
				Home("Library/Caches/Cypress"),
			},
			Guards:   []Guard{OwnedByUser()},
			MinBytes: 16 * mib,
			Evidence: "each is the documented cache location of its tool and is refetched on demand",
			Measured: unmeasured,
			NotTargets: []string{
				"the npm cache, which the npm cache verify action already owns, so counting it here would double count it",
			},
		},
		{
			ID:       "rust-caches",
			Name:     "Rust tool caches",
			Group:    GroupDeveloper,
			Category: storage.CategoryDeveloper,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "crate download cache, refetched from the registry",
			Paths: []PathSpec{
				Home(".cargo/registry/cache"),
				Home(".cargo/registry/src"),
			},
			Guards:   []Guard{ProcessNotRunning("cargo", "rustc"), OwnedByUser()},
			MinBytes: 32 * mib,
			Evidence: "cargo repopulates both from the registry, and neither holds a local-only crate",
			Measured: unmeasured,
			NotTargets: []string{
				".cargo/registry/index, which is the registry metadata cargo needs to resolve offline",
				".cargo/bin, which holds installed binaries",
			},
		},
		{
			ID:       "swift-caches",
			Name:     "Swift package cache",
			Group:    GroupDeveloper,
			Category: storage.CategoryDeveloper,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "resolved package checkouts, refetched on next resolve",
			Paths:    []PathSpec{Home("Library/Caches/org.swift.swiftpm")},
			Guards:   []Guard{ProcessNotRunning("Xcode", "swift"), OwnedByUser()},
			MinBytes: 16 * mib,
			Evidence: "SwiftPM's own shared cache, which it repopulates from the package URLs in the manifest",
			Measured: unmeasured,
		},
	}
}

func deviceBackups() []Target {
	return []Target{
		{
			ID:            "ios-device-backups",
			Name:          "iOS device backups",
			Group:         GroupDeviceBackups,
			Category:      storage.CategoryIOSFiles,
			Risk:          RiskReview,
			Recovery:      safety.RecoveryTrash,
			Detail:        "local device backups, which may be the only copy",
			Paths:         []PathSpec{Glob("Library/Application Support/MobileSync/Backup/*")},
			Guards:        []Guard{OlderThan(90 * 24 * time.Hour), OwnedByUser()},
			MinBytes:      1 * gib,
			Split:         true,
			SplitMinBytes: 1 * gib,
			Evidence:      "each entry is one device backup, named for its device identifier",
			Measured:      unmeasured,
			NotTargets: []string{
				"anything under 90 days old, because a recent backup is usually the restore point for a device still in use",
				"the Backup root itself",
			},
		},
		{
			ID:       "device-firmware",
			Name:     "cached device firmware",
			Group:    GroupDeviceBackups,
			Category: storage.CategoryIOSFiles,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "downloaded IPSW images, refetched when an update is applied",
			Paths: []PathSpec{
				Glob("Library/iTunes/iPhone Software Updates/*"),
				Glob("Library/iTunes/iPad Software Updates/*"),
				Glob("Library/iTunes/iPod Software Updates/*"),
			},
			Guards:        []Guard{OwnedByUser()},
			MinBytes:      256 * mib,
			Split:         true,
			SplitMinBytes: 256 * mib,
			Evidence:      "an IPSW is a download Apple serves again on demand",
			Measured:      unmeasured,
		},
	}
}

package catalog

import (
	"sync"
	"time"

	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

const (
	mib           = int64(1024 * 1024)
	gib           = 1024 * mib
	observedMacOS = "27.0"
	macOSProduct  = "macOS"
)

// All returns the catalog in display order. Two rules apply to every entry:
// Evidence has to say what proves the path is what we claim, and anything above
// RiskSafe has to name the sibling paths it deliberately leaves alone.
var All = sync.OnceValue(func() []Target {
	targets := make([]Target, 0, 99)
	targets = append(targets, userEssentials()...)
	targets = append(targets, parityUserTargets()...)
	targets = append(targets, appCaches()...)
	targets = append(targets, browsers()...)
	targets = append(targets, browserVersionTargets()...)
	targets = append(targets, cloudAndOffice()...)
	targets = append(targets, officeTargets()...)
	targets = append(targets, developerTools()...)
	targets = append(targets, additionalDeveloperTools()...)
	targets = append(targets, xcodeDocumentationTarget())
	targets = append(targets, obsoleteEditorExtensions())
	targets = append(targets, agentVersionTargets()...)
	targets = append(targets, appsAndUtilities()...)
	targets = append(targets, virtualization()...)
	targets = append(targets, virtualizationCacheTargets()...)
	targets = append(targets, deviceBackups()...)
	targets = append(targets, leftovers()...)
	targets = append(targets, system()...)
	qualify(targets)
	return targets
})

func qualify(targets []Target) {
	observedAt := time.Date(2026, time.August, 2, 18, 37, 11, 0, time.UTC)
	observations := map[string]Observation{
		"javascript-caches":    {Product: "Bun", Version: "1.3.13", MacOS: observedMacOS, Bytes: 734470144, ObservedAt: observedAt},
		"python-caches":        {Product: "pip", Version: "26.1.2", MacOS: observedMacOS, Bytes: 47742976, ObservedAt: observedAt},
		"user-caches":          {Product: macOSProduct, Version: observedMacOS, MacOS: observedMacOS, Bytes: 2195988480, ObservedAt: observedAt},
		"user-logs":            {Product: macOSProduct, Version: observedMacOS, MacOS: observedMacOS, Bytes: 139718656, ObservedAt: observedAt},
		"orphaned-caches":      {Product: macOSProduct, Version: observedMacOS, MacOS: observedMacOS, Bytes: 220053504, ObservedAt: observedAt},
		"orphaned-app-support": {Product: macOSProduct, Version: observedMacOS, MacOS: observedMacOS, Bytes: 149553152, ObservedAt: observedAt},
	}
	for index := range targets {
		observation, ok := observations[targets[index].ID]
		if !ok {
			targets[index].Qualification = EvidencePending
			continue
		}
		targets[index].Qualification = EvidenceObserved
		targets[index].Observations = []Observation{observation}
	}
}

// leftovers are the directories an uninstalled app left behind. Every target
// here is guarded by AppAbsent, which refuses outright when the installed index
// could not be built, because "I could not tell" must never read as "it is
// gone".
func leftovers() []Target {
	shared := []string{
		"anything whose owning app is still installed, which is that app's business",
		"any path whose bundle id is in the data-protected table",
		"anything at all when the installed application index came back empty",
	}
	place := func(id, name, pattern, detail string) Target {
		return Target{
			ID:            id,
			Name:          name,
			Group:         GroupLeftovers,
			Category:      storage.CategorySystemData,
			Risk:          RiskReview,
			Recovery:      safety.RecoveryTrash,
			Detail:        detail,
			Paths:         []PathSpec{Glob(pattern)},
			Guards:        []Guard{AppAbsent(), NotDataProtected(), OwnedByUser()},
			MinBytes:      8 * mib,
			Split:         true,
			SplitMinBytes: 64 * mib,
			Evidence:      "the directory is named for a bundle id or app that is no longer installed anywhere this tool looks",
			NotTargets:    shared,
		}
	}
	return []Target{
		place("orphaned-app-support", "orphaned application support",
			"Library/Application Support/*", "state belonging to an app that is gone"),
		place("orphaned-caches", "orphaned caches",
			"Library/Caches/*", "caches belonging to an app that is gone"),
		place("orphaned-containers", "orphaned containers",
			"Library/Containers/*", "sandbox containers belonging to an app that is gone"),
		place("orphaned-saved-state", "orphaned window state",
			"Library/Saved Application State/*.savedState", "window state belonging to an app that is gone"),
		place("orphaned-http-storage", "orphaned web storage",
			"Library/HTTPStorages/*", "cookies and local storage belonging to an app that is gone"),
	}
}

// system targets need uid 0. They are offered rather than hidden without it, so
// the interface can say what running as root would add.
func system() []Target {
	return []Target{
		{
			ID:            "system-caches",
			Name:          "system caches",
			Group:         GroupSystem,
			Category:      storage.CategorySystemData,
			Risk:          RiskSafe,
			Recovery:      safety.RecoveryTrash,
			Detail:        "machine-wide caches, rebuilt on demand",
			Paths:         []PathSpec{Glob("/Library/Caches/*")},
			Guards:        []Guard{NeedsRoot(), NotDataProtected()},
			MinBytes:      32 * mib,
			Split:         true,
			SplitMinBytes: 128 * mib,
			Evidence:      "the machine-wide equivalent of the per-user cache directory, reserved for rebuildable data",
			NotTargets: []string{
				"/Library/Caches/com.apple.* entries, which the fast protection table blankets",
				"/System/Library/Caches, which is on the sealed volume",
			},
		},
		{
			ID:       "system-logs",
			Name:     "system logs",
			Group:    GroupSystem,
			Category: storage.CategorySystemData,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "rotated system logs, already superseded",
			Paths: []PathSpec{
				Glob("/private/var/log/*.gz"),
				Glob("/private/var/log/*.[0-9]"),
				Glob("/private/var/log/*.old"),
			},
			Guards:   []Guard{NeedsRoot(), OlderThan(14 * 24 * time.Hour)},
			MinBytes: 8 * mib,
			Evidence: "a numeric, .gz or .old suffix is what newsyslog appends when it rotates a log it has finished with",
			NotTargets: []string{
				"the live log each rotated file came from, which has no suffix",
				"/private/var/db/diagnostics, which is the unified log store",
			},
		},
		{
			ID:            "system-diagnostic-reports",
			Name:          "diagnostic reports",
			Group:         GroupSystem,
			Category:      storage.CategorySystemData,
			Risk:          RiskReview,
			Recovery:      safety.RecoveryTrash,
			Detail:        "crash and panic reports older than 90 days",
			Paths:         []PathSpec{Glob("/Library/Logs/DiagnosticReports/*")},
			Guards:        []Guard{NeedsRoot(), OlderThan(90 * 24 * time.Hour)},
			MinBytes:      8 * mib,
			Split:         true,
			SplitMinBytes: 64 * mib,
			Evidence:      "each entry is one dated report and nothing reads them but a human diagnosing a fault",
			NotTargets: []string{
				"anything under 90 days old, because a recent crash report is the evidence for a fault still being investigated",
				"the per-user DiagnosticReports directory, which the user-logs target already excludes by name",
			},
		},
		{
			ID:       "system-icon-cache",
			Name:     "icon services cache",
			Group:    GroupSystem,
			Category: storage.CategorySystemData,
			Risk:     RiskSafe,
			Recovery: safety.RecoveryTrash,
			Detail:   "the machine-wide icon cache, rebuilt on demand",
			Paths:    []PathSpec{Absolute("/Library/Caches/com.apple.iconservices.store")},
			Guards:   []Guard{NeedsRoot()},
			MinBytes: 8 * mib,
			Evidence: "an Apple-owned cache whose whole contents are regenerated from application bundles",
		},
	}
}

// virtualization images are large, slow to rebuild and often the only copy, so
// nothing here is ever safe tier.
func virtualization() []Target {
	return []Target{
		{
			ID:            "container-images",
			Name:          "container image caches",
			Group:         GroupVirtualization,
			Category:      storage.CategoryDeveloper,
			Risk:          RiskReview,
			Recovery:      safety.RecoveryTrash,
			Detail:        "pulled image layers, refetched from the registry",
			Paths:         []PathSpec{Home(".orbstack/log"), Home("Library/Caches/colima"), Home(".lima/_images")},
			Guards:        []Guard{ProcessNotRunning("OrbStack", "colima", "limactl"), OwnedByUser()},
			MinBytes:      256 * mib,
			Split:         true,
			SplitMinBytes: 512 * mib,
			Evidence:      "image layers and logs a container runtime repopulates from its registry",
			NotTargets: []string{
				"the virtual machine disk images themselves, which hold anything written inside the machine",
				"docker's own storage, which the docker system prune action owns",
			},
		},
		{
			ID:       "simulator-runtimes",
			Name:     "unused simulator runtimes",
			Group:    GroupVirtualization,
			Category: storage.CategoryDeveloper,
			Risk:     RiskReview,
			Recovery: safety.RecoveryTrash,
			Detail:   "downloaded simulator runtimes, refetched from Apple",
			Paths:    []PathSpec{Glob("Library/Developer/CoreSimulator/Volumes/*")},
			Guards:   []Guard{ProcessNotRunning("Simulator", "Xcode", "simdiskimaged"), OwnedByUser()},
			MinBytes: 512 * mib,
			Evidence: "each entry is a mounted runtime image Xcode re-downloads on demand",
			NotTargets: []string{
				"CoreSimulator/Devices, which holds the simulators' installed apps and data",
				"any runtime currently in use, which is why the Simulator process guard is there",
			},
		},
	}
}

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
			Sweep:         true,
			Split:         true,
			SplitMinBytes: 128 * mib,
			Evidence:      "the sandbox layout puts rebuildable data at exactly this path inside every container",
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
			Sweep:         true,
			Split:         true,
			SplitMinBytes: 128 * mib,
			Evidence:      "the group container layout mirrors the sandbox layout",
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
		chromiumBrowser("chromium", "Chromium", "Library/Caches/Chromium", "Library/Application Support/Chromium"),
		chromiumBrowser("opera", "Opera", "Library/Caches/com.operasoftware.Opera", "Library/Application Support/com.operasoftware.Opera"),
		chromiumBrowser("sidekick", "Sidekick", "Library/Caches/Sidekick", "Library/Application Support/Sidekick"),
		chromiumBrowser("comet", "Comet", "Library/Caches/Comet", "Library/Application Support/Comet"),
		chromiumBrowser("dia", "Dia", "Library/Caches/Dia", "Library/Application Support/Dia"),
		{
			ID: "orion-cache", Name: "Orion cache", Group: GroupBrowsers,
			Category: storage.CategorySystemData, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
			Detail: "browser network and WebKit caches, rebuilt on next browse",
			Paths:  []PathSpec{Home("Library/Caches/com.kagi.kagimacOS")},
			Guards: []Guard{ProcessNotRunning("Orion"), OwnedByUser()}, MinBytes: 8 * mib,
			Evidence:   "Orion's bundle-id-named directory under the per-user Caches root",
			NotTargets: []string{"Orion profiles, cookies, history, bookmarks, passwords, extensions, and WebKit website data"},
		},
		{
			ID: "zen-cache", Name: "Zen cache", Group: GroupBrowsers,
			Category: storage.CategorySystemData, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
			Detail: "Firefox-derived network and startup caches, rebuilt on next browse",
			Paths:  []PathSpec{Glob("Library/Application Support/zen/Profiles/*/cache2"), Glob("Library/Application Support/zen/Profiles/*/startupCache"), Home("Library/Caches/zen")},
			Guards: []Guard{ProcessNotRunning("zen"), OwnedByUser()}, MinBytes: 8 * mib,
			Evidence:   "cache2 and startupCache are Firefox-family rebuildable stores outside login, history, cookie, and extension databases",
			NotTargets: []string{"the Zen profile root, logins, cookies, history, bookmarks, extensions, and preferences"},
		},
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
		},
	}
}

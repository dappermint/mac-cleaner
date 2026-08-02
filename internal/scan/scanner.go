package scan

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dappermint/ratatouille/internal/storage"
)

const (
	defaultCacheMinimum = int64(256 * 1024 * 1024)
	defaultDataMinimum  = int64(512 * 1024 * 1024)
)

var nixGCCountPattern = regexp.MustCompile(`(?m)^([0-9]+) store paths would be deleted$`)

type Scanner struct {
	Home            string
	Deep            bool
	Rootful         bool
	Surface         bool
	Verify          bool
	SkipCommands    bool
	SkipSystem      bool
	SkipItems       bool
	CommandTimeout  time.Duration
	CacheMinimum    int64
	DataMinimum     int64
	ArtifactMinimum int64
	SystemMinimum   int64
	CommandIdentity *storage.CommandIdentity
	Progress        func(ScanProgress)
	rootPaths       *rootInventoryPaths
}

type ScanStageState string

const (
	ScanQueued  ScanStageState = "queued"
	ScanRunning ScanStageState = "running"
	ScanDone    ScanStageState = "done"
)

type ScanProgress struct {
	ID      string
	Name    string
	Detail  string
	State   ScanStageState
	Total   int
	Items   int
	Bytes   int64
	Unknown int
	Issues  int
	Disk    *storage.Disk
	Started time.Time
	Elapsed time.Duration
}

type scanCollector struct {
	id      string
	name    string
	detail  string
	collect func(context.Context) scanResult
}

type scanResult struct {
	items  []Item
	issues []string
}

type pathCandidate struct {
	path     string
	bytes    int64
	modified time.Time
	denied   int64
}

type inventoryTarget struct {
	path     string
	category storage.Category
}

type userDataTarget struct {
	path     string
	name     string
	category storage.Category
}

type rootInventoryPaths struct {
	library  string
	variable string
	system   string
	users    string
	fixed    []inventoryTarget
}

func NewScanner(home string, deep bool) Scanner {
	return Scanner{
		Home:            filepath.Clean(home),
		Deep:            deep,
		CommandTimeout:  15 * time.Second,
		CacheMinimum:    defaultCacheMinimum,
		DataMinimum:     defaultDataMinimum,
		ArtifactMinimum: defaultCacheMinimum,
		SystemMinimum:   64 * 1024 * 1024,
	}
}

func (s Scanner) Scan(ctx context.Context) Report {
	report := Report{GeneratedAt: time.Now(), Home: s.Home, Rootful: s.Rootful}
	collectors := s.collectors()
	totalStages := len(collectors) + 1
	if s.Surface {
		totalStages++
	}
	if s.Verify {
		totalStages++
	}
	s.emit(ScanProgress{
		ID:     volumeStageID,
		Name:   volumeStageName,
		Detail: volumeStageDetail,
		State:  ScanQueued,
		Total:  totalStages,
	})
	if s.Surface {
		s.emit(ScanProgress{
			ID:     SurfaceStageID,
			Name:   surfaceStageName,
			Detail: surfaceStageDetail,
			State:  ScanQueued,
			Total:  totalStages,
		})
	}
	if s.Verify {
		s.emit(ScanProgress{
			ID:     verifyStageID,
			Name:   "filesystem verify",
			Detail: verifyStageDetail,
			State:  ScanQueued,
			Total:  totalStages,
		})
	}
	for _, collector := range collectors {
		s.emit(ScanProgress{
			ID:     collector.id,
			Name:   collector.name,
			Detail: collector.detail,
			State:  ScanQueued,
			Total:  totalStages,
		})
	}

	volumeStarted := time.Now()
	s.emit(ScanProgress{
		ID:      volumeStageID,
		Name:    volumeStageName,
		Detail:  volumeStageDetail,
		State:   ScanRunning,
		Total:   totalStages,
		Started: volumeStarted,
	})
	diskPath := storage.DataVolume
	if _, err := os.Stat(diskPath); err != nil {
		diskPath = "/"
	}
	if disk, err := storage.DiskUsage(diskPath); err == nil {
		report.Disk = disk
		diskCopy := disk
		s.emit(ScanProgress{
			ID:      volumeStageID,
			Name:    volumeStageName,
			Detail:  volumeStageDetail,
			State:   ScanDone,
			Total:   totalStages,
			Disk:    &diskCopy,
			Started: volumeStarted,
			Elapsed: time.Since(volumeStarted),
		})
	} else {
		report.Issues = append(report.Issues, "disk storage.Usage: "+err.Error())
		s.emit(ScanProgress{
			ID:      volumeStageID,
			Name:    volumeStageName,
			Detail:  volumeStageDetail,
			State:   ScanDone,
			Total:   totalStages,
			Issues:  1,
			Started: volumeStarted,
			Elapsed: time.Since(volumeStarted),
		})
	}

	var surfaceWait sync.WaitGroup
	var surface Surface
	var health Health
	if s.Surface {
		surfaceWait.Add(1)
		go func() {
			defer surfaceWait.Done()
			surface, health = s.runSurfaceStage(ctx, totalStages)
		}()
	}

	results := make(chan scanResult, len(collectors))
	semaphore := make(chan struct{}, 4)
	var wait sync.WaitGroup
	for _, collector := range collectors {
		wait.Add(1)
		go func(collector scanCollector) {
			defer wait.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			started := time.Now()
			s.emit(ScanProgress{
				ID:      collector.id,
				Name:    collector.name,
				Detail:  collector.detail,
				State:   ScanRunning,
				Total:   totalStages,
				Started: started,
			})
			result := collector.collect(ctx)
			bytes, unknown := scanResultSize(result)
			s.emit(ScanProgress{
				ID:      collector.id,
				Name:    collector.name,
				Detail:  collector.detail,
				State:   ScanDone,
				Total:   totalStages,
				Items:   len(result.items),
				Bytes:   bytes,
				Unknown: unknown,
				Issues:  len(result.issues),
				Started: started,
				Elapsed: time.Since(started),
			})
			results <- result
		}(collector)
	}
	wait.Wait()
	close(results)

	for result := range results {
		report.Items = append(report.Items, result.items...)
		report.Issues = append(report.Issues, result.issues...)
	}
	surfaceWait.Wait()
	if s.Surface {
		report.Surface = &surface
		report.Health = &health
		report.Issues = append(report.Issues, surface.Issues...)
		report.Disk.InUse = surface.Claimed
		if len(surface.Containers) > 0 {
			report.Disk.Container = containerHolding(surface.Containers, report.Disk.Path)
		}
	}
	report.Issues = storage.UniqueStrings(report.Issues)
	report.Sort()
	return report
}

const (
	volumeStageID      = "volume"
	volumeStageName    = "data volume"
	volumeStageDetail  = "reading capacity and physical free space"
	surfaceStageName   = "disk surface"
	groupSupported     = "supported cleanup"
	estimateAllocated  = "allocated bytes"
	SurfaceStageID     = "surface"
	surfaceStageDetail = "walking every readable byte of the data volume"
	verifyStageID      = "verify"
	verifyStageDetail  = "asking diskutil to check the live filesystem"
)

func (s Scanner) runSurfaceStage(ctx context.Context, totalStages int) (Surface, Health) {
	started := time.Now()
	s.emit(ScanProgress{
		ID:      SurfaceStageID,
		Name:    surfaceStageName,
		Detail:  surfaceStageDetail,
		State:   ScanRunning,
		Total:   totalStages,
		Started: started,
	})
	surface := s.buildSurface(ctx, func(files, bytes int64) {
		s.emit(ScanProgress{
			ID:      SurfaceStageID,
			Name:    surfaceStageName,
			Detail:  surfaceStageDetail,
			State:   ScanRunning,
			Total:   totalStages,
			Items:   int(files),
			Bytes:   bytes,
			Started: started,
		})
	})
	s.emit(ScanProgress{
		ID:      SurfaceStageID,
		Name:    surfaceStageName,
		Detail:  surfaceStageDetail,
		State:   ScanDone,
		Total:   totalStages,
		Items:   int(surface.Files),
		Bytes:   surface.Walked,
		Issues:  len(surface.Issues),
		Started: started,
		Elapsed: time.Since(started),
	})

	dataPath := DataVolumePath(surface.Mounts)
	health := EvaluateHealth(surface, dataPath)
	if s.Verify {
		verifyStarted := time.Now()
		s.emit(ScanProgress{
			ID:      verifyStageID,
			Name:    "filesystem verify",
			Detail:  verifyStageDetail,
			State:   ScanRunning,
			Total:   totalStages,
			Started: verifyStarted,
		})
		signals := VerifySignals(ctx, surface.Containers, dataPath)
		health.Signals = append(health.Signals, signals...)
		health.Verified = true
		for _, signal := range signals {
			health.Level = WorseLevel(health.Level, signal.Level)
		}
		s.emit(ScanProgress{
			ID:      verifyStageID,
			Name:    "filesystem verify",
			Detail:  verifyStageDetail,
			State:   ScanDone,
			Total:   totalStages,
			Items:   len(signals),
			Started: verifyStarted,
			Elapsed: time.Since(verifyStarted),
		})
	}
	return surface, health
}

func containerHolding(containers []storage.Container, path string) string {
	for _, container := range containers {
		if container.Holds(path) {
			return container.Reference
		}
	}
	return ""
}

func (s Scanner) collectors() []scanCollector {
	if s.SkipItems {
		return nil
	}
	collectors := []scanCollector{
		{
			id:      "app-data",
			name:    "app data",
			detail:  "sizing Application Support and sandbox containers",
			collect: s.collectLargeData,
		},
		{
			id:      "app-caches",
			name:    "app caches",
			detail:  "finding large per-app cache directories",
			collect: s.collectAppCaches,
		},
		{
			id:      "downloads",
			name:    "downloads",
			detail:  "checking large top-level downloads",
			collect: s.collectDownloads,
		},
		{
			id:      "trash",
			name:    "Trash",
			detail:  "measuring items already waiting for deletion",
			collect: s.collectTrash,
		},
	}
	if s.Deep {
		collectors = append(collectors, scanCollector{
			id:      "project-builds",
			name:    "project builds",
			detail:  "walking projects for marked rebuildable directories",
			collect: s.collectBuildArtifacts,
		})
	}
	if !s.SkipSystem {
		collectors = append(collectors, scanCollector{
			id:      "installed-apps",
			name:    "installed apps",
			detail:  "sizing large application bundles for visibility",
			collect: s.collectInstalledApps,
		})
	}
	if s.Rootful && !s.SkipSystem {
		collectors = append(collectors,
			scanCollector{
				id:      "system-data",
				name:    "System Data",
				detail:  "indexing /Library, /private/var and package stores",
				collect: s.collectSystemData,
			},
			scanCollector{
				id:      "macos",
				name:    "macOS",
				detail:  "measuring the sealed system tree",
				collect: s.collectMacOS,
			},
			scanCollector{
				id:      "other-users",
				name:    "Other Users",
				detail:  "indexing other home directories and Shared",
				collect: s.collectOtherUsers,
			},
		)
	}
	if !s.SkipCommands {
		collectors = append(collectors,
			scanCollector{
				id:      "homebrew",
				name:    "homebrew",
				detail:  "asking brew for its supported cleanup preview",
				collect: s.collectHomebrew,
			},
			scanCollector{
				id:      "uv",
				name:    "uv cache",
				detail:  "measuring the uv package cache",
				collect: s.collectUV,
			},
			scanCollector{
				id:      "npm",
				name:    "npm cache",
				detail:  "measuring npm's content-addressed cache",
				collect: s.collectNPM,
			},
			scanCollector{
				id:      "go",
				name:    "go caches",
				detail:  "measuring build, test and module caches",
				collect: s.collectGo,
			},
			scanCollector{
				id:      "nix",
				name:    "nix store",
				detail:  "asking lix which store paths are unreachable",
				collect: s.collectNix,
			},
			scanCollector{
				id:      "docker",
				name:    "docker",
				detail:  "checking unused objects without starting the daemon",
				collect: s.collectDocker,
			},
		)
	}
	return collectors
}

func (s Scanner) emit(progress ScanProgress) {
	if s.Progress != nil {
		s.Progress(progress)
	}
}

func (s Scanner) captureCommand(ctx context.Context, command string, args ...string) (string, error) {
	return storage.CaptureCommandAs(ctx, s.CommandTimeout, s.CommandIdentity, command, args...)
}

func scanResultSize(result scanResult) (int64, int) {
	var bytes int64
	var unknown int
	for _, item := range result.items {
		if item.Bytes < 0 {
			unknown++
			continue
		}
		bytes += item.Bytes
	}
	return bytes, unknown
}

func (s Scanner) collectHomebrew(ctx context.Context) scanResult {
	brew, err := exec.LookPath("brew")
	if err != nil {
		return scanResult{}
	}
	output, err := s.captureCommand(ctx, brew, "cleanup", "--prune=all", "--dry-run")
	if err != nil {
		return scanResult{issues: []string{"homebrew preview: " + storage.CompactError(err)}}
	}
	bytes := storage.ParseLargestSize(output)
	if bytes == 0 {
		return scanResult{}
	}
	return scanResult{items: []Item{{
		ID:       "homebrew-cleanup",
		Name:     "homebrew leftovers",
		Group:    groupSupported,
		Category: storage.CategoryDeveloper,
		Detail:   "old bottles, cask downloads and stale formula versions, using Homebrew's own cleanup command",
		Source:   "brew cleanup --prune=all --dry-run",
		Risk:     RiskSafe,
		Bytes:    bytes,
		Estimate: "homebrew dry-run estimate",
		Action: &Action{
			Kind:      ActionCommand,
			Command:   brew,
			Args:      []string{"cleanup", "--prune=all"},
			Immediate: true,
			Identity:  s.CommandIdentity,
		},
	}}}
}

func (s Scanner) collectUV(ctx context.Context) scanResult {
	uv, err := exec.LookPath("uv")
	if err != nil {
		return scanResult{}
	}
	cacheDir := filepath.Join(s.Home, ".cache", "uv")
	if output, commandErr := s.captureCommand(ctx, uv, "cache", "dir"); commandErr == nil {
		if candidate := strings.TrimSpace(output); filepath.IsAbs(candidate) {
			cacheDir = candidate
		}
	}
	measured, err := storage.PathUsage(ctx, cacheDir)
	if err != nil || measured.Bytes == 0 {
		return scanResult{}
	}
	return scanResult{items: []Item{{
		ID:       "uv-cache-prune",
		Name:     "unused uv cache entries",
		Group:    groupSupported,
		Category: storage.CategoryDeveloper,
		Detail:   "unused package cache and disposable centralized environments, using uv's concurrency-safe prune",
		Source:   storage.RelativeHome(s.Home, cacheDir),
		Risk:     RiskSafe,
		Bytes:    measured.Bytes,
		Estimate: "current cache size, prune may reclaim less",
		Action: &Action{
			Kind:      ActionCommand,
			Command:   uv,
			Args:      []string{"cache", "prune"},
			Immediate: true,
			Identity:  s.CommandIdentity,
		},
	}}}
}

func (s Scanner) collectNPM(ctx context.Context) scanResult {
	npm, err := exec.LookPath("npm")
	if err != nil {
		return scanResult{}
	}
	cacheDir := filepath.Join(s.Home, ".npm")
	if output, commandErr := s.captureCommand(ctx, npm, "config", "get", "cache"); commandErr == nil {
		if candidate := strings.TrimSpace(output); filepath.IsAbs(candidate) {
			cacheDir = candidate
		}
	}
	measured, err := storage.PathUsage(ctx, cacheDir)
	if err != nil || measured.Bytes == 0 {
		return scanResult{}
	}
	return scanResult{items: []Item{{
		ID:       "npm-cache-verify",
		Name:     "unneeded npm cache entries",
		Group:    groupSupported,
		Category: storage.CategoryDeveloper,
		Detail:   "integrity-checks npm's content cache and garbage-collects unneeded entries without forcing a full wipe",
		Source:   storage.RelativeHome(s.Home, cacheDir),
		Risk:     RiskSafe,
		Bytes:    measured.Bytes,
		Estimate: "current cache size, verify may reclaim less",
		Action: &Action{
			Kind:      ActionCommand,
			Command:   npm,
			Args:      []string{"cache", "verify"},
			Immediate: true,
			Identity:  s.CommandIdentity,
		},
	}}}
}

func (s Scanner) collectGo(ctx context.Context) scanResult {
	goCommand, err := exec.LookPath("go")
	if err != nil {
		return scanResult{}
	}
	output, err := s.captureCommand(ctx, goCommand, "env", "GOCACHE", "GOMODCACHE")
	if err != nil {
		return scanResult{issues: []string{"go cache paths: " + storage.CompactError(err)}}
	}
	paths := strings.Fields(output)
	if len(paths) < 2 {
		return scanResult{issues: []string{"go cache paths: unexpected go env output"}}
	}
	var items []Item
	if measured, measureErr := storage.PathUsage(ctx, paths[0]); measureErr == nil && measured.Bytes > 0 {
		items = append(items, Item{
			ID:       "go-build-cache",
			Name:     "go build and test cache",
			Group:    groupSupported,
			Category: storage.CategoryDeveloper,
			Detail:   "rebuildable compiler and test outputs, using go clean",
			Source:   storage.RelativeHome(s.Home, paths[0]),
			Risk:     RiskSafe,
			Bytes:    measured.Bytes,
			Estimate: "current cache size",
			Action: &Action{
				Kind:      ActionCommand,
				Command:   goCommand,
				Args:      []string{"clean", "-cache", "-testcache"},
				Immediate: true,
				Identity:  s.CommandIdentity,
			},
		})
	}
	if measured, measureErr := storage.PathUsage(ctx, paths[1]); measureErr == nil && measured.Bytes > 0 {
		items = append(items, Item{
			ID:       "go-module-cache",
			Name:     "go module download cache",
			Group:    "developer caches",
			Category: storage.CategoryDeveloper,
			Detail:   "downloaded dependency sources, rebuildable with network access but expensive to fetch again",
			Source:   storage.RelativeHome(s.Home, paths[1]),
			Risk:     RiskReview,
			Bytes:    measured.Bytes,
			Estimate: "current cache size",
			Action: &Action{
				Kind:      ActionCommand,
				Command:   goCommand,
				Args:      []string{"clean", "-modcache"},
				Immediate: true,
				Identity:  s.CommandIdentity,
			},
		})
	}
	return scanResult{items: items}
}

func (s Scanner) collectNix(ctx context.Context) scanResult {
	nix, err := exec.LookPath("nix")
	if err != nil {
		return scanResult{}
	}
	output, err := s.captureCommand(ctx, nix, "store", "gc", "--dry-run")
	if err != nil {
		return scanResult{issues: []string{"nix gc preview: " + storage.CompactError(err)}}
	}
	count := parseNixGCCount(output)
	if count == 0 && strings.Contains(output, "0 store paths would be deleted") {
		return scanResult{}
	}
	if count < 0 {
		return scanResult{issues: []string{"nix gc preview: unexpected dry-run summary"}}
	}
	return scanResult{items: []Item{{
		ID:       "nix-store-gc",
		Name:     "unreachable nix store paths",
		Group:    groupSupported,
		Category: storage.CategorySystemData,
		Detail:   fmt.Sprintf("%d store paths with no garbage-collector root, current profiles and rollback generations remain protected", count),
		Source:   "nix store gc --dry-run",
		Risk:     RiskSafe,
		Bytes:    -1,
		Estimate: fmt.Sprintf("%d paths, lix reports size only after cleanup", count),
		Action: &Action{
			Kind:      ActionCommand,
			Command:   nix,
			Args:      []string{"store", "gc"},
			Immediate: true,
			Identity:  s.CommandIdentity,
		},
	}}}
}

func parseNixGCCount(output string) int {
	match := nixGCCountPattern.FindStringSubmatch(output)
	if len(match) != 2 {
		return -1
	}
	count, err := strconv.Atoi(match[1])
	if err != nil {
		return -1
	}
	return count
}

func (s Scanner) collectDocker(ctx context.Context) scanResult {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return scanResult{}
	}
	output, err := s.captureCommand(ctx, docker, "system", "df", "--format", "{{.Reclaimable}}")
	if err != nil {
		return scanResult{}
	}
	bytes := storage.SumSizes(output)
	if bytes == 0 {
		return scanResult{}
	}
	return scanResult{items: []Item{{
		ID:       "docker-system-prune",
		Name:     "unused docker objects",
		Group:    "developer caches",
		Category: storage.CategoryDeveloper,
		Detail:   "stopped containers, unused networks, dangling images and build cache, volumes and tagged images are kept",
		Source:   "docker system df",
		Risk:     RiskReview,
		Bytes:    bytes,
		Estimate: "docker reclaimable estimate, prune may reclaim less",
		Action: &Action{
			Kind:      ActionCommand,
			Command:   docker,
			Args:      []string{"system", "prune", "--force"},
			Immediate: true,
			Identity:  s.CommandIdentity,
		},
	}}}
}

func (s Scanner) collectTrash(ctx context.Context) scanResult {
	trash := filepath.Join(s.Home, ".Trash")
	measured, err := storage.PathUsage(ctx, trash)
	if err != nil || measured.Bytes == 0 {
		return scanResult{}
	}
	return scanResult{items: []Item{{
		ID:       "empty-trash",
		Name:     "empty Trash permanently",
		Group:    "irreversible",
		Category: storage.CategoryTrash,
		Detail:   "permanently deletes everything currently in ~/.Trash, including items placed there outside this tool",
		Source:   "~/.Trash",
		Risk:     RiskDestructive,
		Bytes:    measured.Bytes,
		Estimate: estimateAllocated,
		Action: &Action{
			Kind:      ActionEmptyTrash,
			Paths:     []string{trash},
			Immediate: true,
			Identity:  s.CommandIdentity,
		},
	}}}
}

func (s Scanner) collectAppCaches(ctx context.Context) scanResult {
	roots := []string{
		filepath.Join(s.Home, "Library", "Caches"),
		filepath.Join(s.Home, ".cache"),
	}
	excluded := map[string]bool{
		filepath.Join(s.Home, "Library", "Caches", "Homebrew"): true,
		filepath.Join(s.Home, "Library", "Caches", "go-build"): true,
		filepath.Join(s.Home, ".cache", "uv"):                  true,
		filepath.Join(s.Home, ".cache", "nix-cache-staging"):   true,
	}
	var candidates []pathCandidate
	var issues []string
	for _, root := range roots {
		found, foundIssues := largestChildren(ctx, root, s.CacheMinimum, 12, excluded)
		candidates = append(candidates, found...)
		issues = append(issues, foundIssues...)
	}
	sortCandidates(candidates)
	if len(candidates) > 12 {
		candidates = candidates[:12]
	}
	items := make([]Item, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, Item{
			ID:       pathID("cache", candidate.path),
			Name:     friendlyCacheName(filepath.Base(candidate.path)),
			Group:    "app caches",
			Category: storage.CategorySystemData,
			Detail:   "app-owned cache, close the related app first; this is moved to Trash so it remains recoverable until Trash is emptied",
			Source:   storage.RelativeHome(s.Home, candidate.path),
			Risk:     RiskReview,
			Bytes:    candidate.bytes,
			Modified: optionalTime(candidate.modified),
			Estimate: estimateAllocated,
			Action: &Action{
				Kind:      ActionTrash,
				Paths:     []string{candidate.path},
				Immediate: false,
				Identity:  s.CommandIdentity,
			},
		})
	}
	return scanResult{items: items, issues: issues}
}

func (s Scanner) collectDownloads(ctx context.Context) scanResult {
	downloads := filepath.Join(s.Home, "Downloads")
	candidates, issues := largestChildren(ctx, downloads, s.CacheMinimum, 16, nil)
	items := make([]Item, 0, len(candidates))
	for _, candidate := range candidates {
		age := "modified " + candidate.modified.Format("2006-01-02")
		items = append(items, Item{
			ID:       pathID("download", candidate.path),
			Name:     filepath.Base(candidate.path),
			Group:    "large downloads",
			Category: storage.CategoryDocuments,
			Detail:   "large top-level download, " + age + "; moved to Trash and never selected automatically",
			Source:   storage.RelativeHome(s.Home, candidate.path),
			Risk:     RiskReview,
			Bytes:    candidate.bytes,
			Modified: optionalTime(candidate.modified),
			Estimate: estimateAllocated,
			Action: &Action{
				Kind:      ActionTrash,
				Paths:     []string{candidate.path},
				Immediate: false,
				Identity:  s.CommandIdentity,
			},
		})
	}
	return scanResult{items: items, issues: issues}
}

func (s Scanner) collectLargeData(ctx context.Context) scanResult {
	targets := s.categoryDataTargets()
	excluded := make(map[string]bool, len(targets))
	categoryByPath := make(map[string]storage.Category, len(targets))
	nameByPath := make(map[string]string, len(targets))
	for _, target := range targets {
		excluded[target.path] = true
		categoryByPath[target.path] = target.category
		nameByPath[target.path] = target.name
	}
	roots := []string{
		filepath.Join(s.Home, "Library", "Application Support"),
		filepath.Join(s.Home, "Library", "Containers"),
		filepath.Join(s.Home, "Library", "Group Containers"),
	}
	var candidates []pathCandidate
	var issues []string
	for _, root := range roots {
		found, foundIssues := largestChildren(ctx, root, s.DataMinimum, 10, excluded)
		candidates = append(candidates, found...)
		issues = append(issues, foundIssues...)
	}
	for _, target := range targets {
		info, err := os.Lstat(target.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			issues = append(issues, target.path+": "+storage.CompactError(err))
			continue
		}
		measured, err := storage.PathUsage(ctx, target.path)
		if err != nil && !errors.Is(err, context.Canceled) {
			issues = append(issues, target.path+": "+storage.CompactError(err))
			continue
		}
		if measured.Denied > 0 {
			issues = append(issues, fmt.Sprintf("%s: %d permission-denied entries skipped", target.path, measured.Denied))
		}
		if measured.Bytes >= s.DataMinimum {
			candidates = append(candidates, pathCandidate{
				path:     target.path,
				bytes:    measured.Bytes,
				modified: info.ModTime(),
				denied:   measured.Denied,
			})
		}
	}
	sortCandidates(candidates)
	if len(candidates) > 24 {
		candidates = candidates[:24]
	}
	items := make([]Item, 0, len(candidates))
	for _, candidate := range candidates {
		name := nameByPath[candidate.path]
		if name == "" {
			name = friendlyDataName(filepath.Base(candidate.path))
		}
		category := categoryByPath[candidate.path]
		if category == "" {
			category = storage.CategoryForUserData(candidate.path)
		}
		items = append(items, Item{
			ID:       pathID("data", candidate.path),
			Name:     name,
			Group:    "protected app data",
			Category: category,
			Detail:   "real app data, games, downloads or account state; inspect and remove it from the owning app",
			Source:   storage.RelativeHome(s.Home, candidate.path),
			Risk:     RiskProtected,
			Bytes:    candidate.bytes,
			Modified: optionalTime(candidate.modified),
			Estimate: estimateAllocated,
		})
	}
	return scanResult{items: items, issues: issues}
}

func (s Scanner) categoryDataTargets() []userDataTarget {
	targets := []userDataTarget{
		{path: filepath.Join(s.Home, "Library", "Mail"), name: "Mail data", category: storage.CategoryMail},
		{path: filepath.Join(s.Home, "Library", "Messages"), name: "Messages data", category: storage.CategoryMessages},
		{path: filepath.Join(s.Home, "Library", "Mobile Documents"), name: "iCloud Drive local data", category: storage.CategoryICloudDrive},
		{path: filepath.Join(s.Home, "Library", "Application Support", "MobileSync"), name: "iOS backups and firmware", category: storage.CategoryIOSFiles},
		{path: filepath.Join(s.Home, "Music", "Music"), name: "Music library", category: storage.CategoryMusic},
	}
	pictures := filepath.Join(s.Home, "Pictures")
	if entries, err := os.ReadDir(pictures); err == nil {
		for _, entry := range entries {
			if strings.HasSuffix(strings.ToLower(entry.Name()), ".photoslibrary") {
				targets = append(targets, userDataTarget{
					path:     filepath.Join(pictures, entry.Name()),
					name:     entry.Name(),
					category: storage.CategoryPhotos,
				})
			}
		}
	}
	return targets
}

func (s Scanner) collectInstalledApps(ctx context.Context) scanResult {
	candidates, issues := largestChildren(ctx, "/Applications", 1024*1024*1024, 8, nil)
	items := make([]Item, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, Item{
			ID:       pathID("app", candidate.path),
			Name:     filepath.Base(candidate.path),
			Group:    "installed apps",
			Category: storage.CategoryApplications,
			Detail:   "large application bundle; uninstall it through its package manager or vendor workflow",
			Source:   candidate.path,
			Risk:     RiskProtected,
			Bytes:    candidate.bytes,
			Modified: optionalTime(candidate.modified),
			Estimate: estimateAllocated,
		})
	}
	return scanResult{items: items, issues: issues}
}

func (s Scanner) collectSystemData(ctx context.Context) scanResult {
	paths := s.inventoryPaths()
	categoryByPath := make(map[string]storage.Category)
	var candidates []pathCandidate
	var issues []string
	for _, root := range []string{paths.library, paths.variable} {
		found, foundIssues := largestChildren(ctx, root, s.SystemMinimum, 32, nil)
		candidates = append(candidates, found...)
		issues = append(issues, foundIssues...)
		for _, candidate := range found {
			categoryByPath[candidate.path] = systemCategory(candidate.path, paths.library)
		}
	}
	for _, target := range paths.fixed {
		if ctx.Err() != nil {
			break
		}
		info, err := os.Lstat(target.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			issues = append(issues, target.path+": "+storage.CompactError(err))
			continue
		}
		measured, err := storage.PathUsage(ctx, target.path)
		if err != nil && !errors.Is(err, context.Canceled) {
			issues = append(issues, target.path+": "+storage.CompactError(err))
			continue
		}
		if measured.Denied > 0 {
			issues = append(issues, fmt.Sprintf("%s: %d permission-denied entries skipped", target.path, measured.Denied))
		}
		if measured.Bytes < s.SystemMinimum {
			continue
		}
		candidates = append(candidates, pathCandidate{
			path:     target.path,
			bytes:    measured.Bytes,
			modified: info.ModTime(),
			denied:   measured.Denied,
		})
		categoryByPath[target.path] = target.category
	}
	sortCandidates(candidates)
	if len(candidates) > 48 {
		candidates = candidates[:48]
	}
	items := make([]Item, 0, len(candidates))
	for _, candidate := range candidates {
		category := categoryByPath[candidate.path]
		if category == "" {
			category = storage.CategorySystemData
		}
		items = append(items, Item{
			ID:       pathID("system", candidate.path),
			Name:     systemInventoryName(candidate.path, paths),
			Group:    "root inventory",
			Category: category,
			Detail:   "allocated blocks in a root-owned tree",
			Source:   candidate.path,
			Risk:     RiskProtected,
			Bytes:    candidate.bytes,
			Modified: optionalTime(candidate.modified),
			Estimate: estimateAllocated,
		})
	}
	return scanResult{items: items, issues: issues}
}

func (s Scanner) collectMacOS(ctx context.Context) scanResult {
	root := s.inventoryPaths().system
	measured, err := storage.PathUsageExcluding(ctx, root, []string{filepath.Join(root, "Volumes")})
	if errors.Is(err, os.ErrNotExist) {
		return scanResult{}
	}
	if err != nil {
		return scanResult{issues: []string{root + ": " + storage.CompactError(err)}}
	}
	var issues []string
	if measured.Denied > 0 {
		issues = append(issues, fmt.Sprintf("%s: %d permission-denied entries skipped", root, measured.Denied))
	}
	if measured.Bytes == 0 {
		return scanResult{issues: issues}
	}
	return scanResult{items: []Item{{
		ID:       pathID("macos", root),
		Name:     "system tree",
		Group:    "root inventory",
		Category: storage.CategoryMacOS,
		Detail:   "sealed macOS applications and system files",
		Source:   root,
		Risk:     RiskProtected,
		Bytes:    measured.Bytes,
		Estimate: estimateAllocated,
	}}, issues: issues}
}

func (s Scanner) collectOtherUsers(ctx context.Context) scanResult {
	root := s.inventoryPaths().users
	excluded := map[string]bool{filepath.Clean(s.Home): true}
	candidates, issues := largestChildren(ctx, root, s.SystemMinimum, 24, excluded)
	items := make([]Item, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, Item{
			ID:       pathID("other-user", candidate.path),
			Name:     filepath.Base(candidate.path),
			Group:    "root inventory",
			Category: storage.CategoryOtherUsers,
			Detail:   "allocated blocks owned by another user or shared at system scope",
			Source:   candidate.path,
			Risk:     RiskProtected,
			Bytes:    candidate.bytes,
			Modified: optionalTime(candidate.modified),
			Estimate: estimateAllocated,
		})
	}
	return scanResult{items: items, issues: issues}
}

func (s Scanner) inventoryPaths() rootInventoryPaths {
	if s.rootPaths != nil {
		return *s.rootPaths
	}
	return rootInventoryPaths{
		library:  "/Library",
		variable: "/private/var",
		system:   "/System",
		users:    "/Users",
		fixed: []inventoryTarget{
			{path: "/nix/store", category: storage.CategorySystemData},
			{path: "/opt/homebrew", category: storage.CategoryDeveloper},
			{path: "/usr/local", category: storage.CategoryDeveloper},
		},
	}
}

func systemCategory(path, libraryRoot string) storage.Category {
	relative, err := filepath.Rel(libraryRoot, path)
	if err == nil && relative != "." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		first := strings.Split(relative, string(filepath.Separator))[0]
		switch first {
		case "Developer":
			return storage.CategoryDeveloper
		case "Audio":
			return storage.CategoryMusicCreate
		}
	}
	return storage.CategorySystemData
}

func systemInventoryName(path string, roots rootInventoryPaths) string {
	for _, root := range []string{roots.library, roots.variable} {
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != "." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return filepath.Base(root) + "/" + relative
		}
	}
	return strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator))
}

func (s Scanner) collectBuildArtifacts(ctx context.Context) scanResult {
	roots := []string{
		filepath.Join(s.Home, "Documents"),
		filepath.Join(s.Home, "Code"),
		filepath.Join(s.Home, "Projects"),
		filepath.Join(s.Home, "Developer"),
		filepath.Join(s.Home, "src"),
		filepath.Join(s.Home, "work"),
	}
	var candidates []pathCandidate
	var issues []string
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if walkErr != nil {
				if errors.Is(walkErr, fs.ErrPermission) {
					return filepath.SkipDir
				}
				return nil
			}
			if !entry.IsDir() {
				return nil
			}
			base := entry.Name()
			if base == ".git" || base == ".Trash" || base == "Library" {
				return filepath.SkipDir
			}
			kind := buildArtifactKind(path)
			if kind == "" {
				return nil
			}
			measured, err := storage.PathUsage(ctx, path)
			if err == nil && measured.Bytes >= s.ArtifactMinimum {
				info, _ := entry.Info()
				candidate := pathCandidate{path: path, bytes: measured.Bytes, denied: measured.Denied}
				if info != nil {
					candidate.modified = info.ModTime()
				}
				candidates = append(candidates, candidate)
			}
			return filepath.SkipDir
		})
		if walkErr != nil && !errors.Is(walkErr, context.Canceled) {
			issues = append(issues, "deep scan "+storage.RelativeHome(s.Home, root)+": "+storage.CompactError(walkErr))
		}
	}
	sortCandidates(candidates)
	if len(candidates) > 24 {
		candidates = candidates[:24]
	}
	items := make([]Item, 0, len(candidates))
	for _, candidate := range candidates {
		kind := buildArtifactKind(candidate.path)
		project := storage.RelativeHome(s.Home, filepath.Dir(candidate.path))
		items = append(items, Item{
			ID:       pathID("build", candidate.path),
			Name:     kind + " in " + project,
			Group:    "project build state",
			Category: storage.CategoryDeveloper,
			Detail:   "rebuildable project-local state, moved to Trash so the project can be checked before permanent deletion",
			Source:   storage.RelativeHome(s.Home, candidate.path),
			Risk:     RiskReview,
			Bytes:    candidate.bytes,
			Modified: optionalTime(candidate.modified),
			Estimate: estimateAllocated,
			Action: &Action{
				Kind:      ActionTrash,
				Paths:     []string{candidate.path},
				Immediate: false,
				Identity:  s.CommandIdentity,
			},
		})
	}
	return scanResult{items: items, issues: issues}
}

func largestChildren(ctx context.Context, root string, minimum int64, limit int, excluded map[string]bool) ([]pathCandidate, []string) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		return nil, nil
	}
	if err != nil {
		return nil, []string{storage.RelativeHome(filepath.Dir(filepath.Dir(root)), root) + ": " + storage.CompactError(err)}
	}
	type candidateResult struct {
		candidate pathCandidate
		err       error
	}
	results := make(chan candidateResult, len(entries))
	semaphore := make(chan struct{}, 4)
	var wait sync.WaitGroup
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if excluded != nil && excluded[path] {
			continue
		}
		wait.Add(1)
		go func(path string, entry fs.DirEntry) {
			defer wait.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			measured, measureErr := storage.PathUsage(ctx, path)
			candidate := pathCandidate{path: path, bytes: measured.Bytes, denied: measured.Denied}
			if info, infoErr := entry.Info(); infoErr == nil {
				candidate.modified = info.ModTime()
			}
			results <- candidateResult{candidate: candidate, err: measureErr}
		}(path, entry)
	}
	wait.Wait()
	close(results)

	var candidates []pathCandidate
	var issues []string
	var denied int64
	for result := range results {
		if result.err != nil && !errors.Is(result.err, context.Canceled) {
			issues = append(issues, result.candidate.path+": "+storage.CompactError(result.err))
			continue
		}
		if result.candidate.bytes >= minimum {
			candidates = append(candidates, result.candidate)
		}
		denied += result.candidate.denied
	}
	if denied > 0 {
		issues = append(issues, fmt.Sprintf("%s: %d permission-denied entries skipped", root, denied))
	}
	sortCandidates(candidates)
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, issues
}

func sortCandidates(candidates []pathCandidate) {
	sort.SliceStable(candidates, func(a, b int) bool {
		if candidates[a].bytes != candidates[b].bytes {
			return candidates[a].bytes > candidates[b].bytes
		}
		return candidates[a].path < candidates[b].path
	})
}

func buildArtifactKind(path string) string {
	base := filepath.Base(path)
	parent := filepath.Dir(path)
	switch base {
	case "node_modules":
		if regularFile(filepath.Join(parent, "package.json")) {
			return "node_modules"
		}
	case "target":
		if regularFile(filepath.Join(parent, "Cargo.toml")) {
			return "rust target"
		}
	case ".direnv":
		if regularFile(filepath.Join(parent, ".envrc")) {
			return "direnv environment"
		}
	case ".venv":
		if regularFile(filepath.Join(path, "pyvenv.cfg")) {
			return "python environment"
		}
	}
	return ""
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func pathID(prefix, path string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(path)))
	return fmt.Sprintf("%s-%x", prefix, digest[:5])
}

func friendlyCacheName(name string) string {
	known := map[string]string{
		"com.spotify.client":     "spotify cache",
		"company.thebrowser.dia": "dia browser cache",
		"org.swift.swiftpm":      "swift package cache",
		"com.apple.wallpaper":    "wallpaper cache",
		"discordptb":             "discord ptb cache",
		"discordcanary":          "discord canary cache",
	}
	if friendly, ok := known[name]; ok {
		return friendly
	}
	return name + " cache"
}

func friendlyDataName(name string) string {
	known := map[string]string{
		"Steam":                            "steam library and app data",
		"com.franke.Whisky":                "whisky bottles and app data",
		"6N38VWS5BX.ru.keepcoder.Telegram": "telegram account data and cached media",
		"HUAQ24HBR6.dev.orbstack":          "orbstack machines and container data",
		"Zed":                              "zed extensions and language tooling",
		"discordptb":                       "discord ptb app data",
		"Dia":                              "dia browser profile",
		"CloudDocs":                        "icloud drive local data",
	}
	if friendly, ok := known[name]; ok {
		return friendly
	}
	return name
}

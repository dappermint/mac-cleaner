package catalog

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dappermint/ratatouille/internal/config"
	"github.com/dappermint/ratatouille/internal/storage"
)

const (
	defaultPathBudget = 20 * time.Second
	defaultBudget     = 3 * time.Minute
)

type Skip struct {
	Path   string
	Reason string
}

// Measurement is one path and what it actually costs. The per-path breakdown is
// kept rather than summed away, because a 500 MiB cache is worth its own row
// while forty small ones are not.
type Measurement struct {
	Path     string
	Bytes    int64
	Files    int64
	Denied   int64
	Modified time.Time
}

type Candidate struct {
	Target       Target
	Measurements []Measurement
	Skipped      []Skip
	Partial      bool
}

func (c Candidate) Bytes() int64 {
	var total int64
	for _, measurement := range c.Measurements {
		total += measurement.Bytes
	}
	return total
}

func (c Candidate) Denied() int64 {
	var total int64
	for _, measurement := range c.Measurements {
		total += measurement.Denied
	}
	return total
}

func (c Candidate) Paths() []string {
	paths := make([]string, 0, len(c.Measurements))
	for _, measurement := range c.Measurements {
		paths = append(paths, measurement.Path)
	}
	return paths
}

func (c Candidate) Modified() time.Time {
	var newest time.Time
	for _, measurement := range c.Measurements {
		if measurement.Modified.After(newest) {
			newest = measurement.Modified
		}
	}
	return newest
}

func (c Candidate) Offered() bool {
	return len(c.Measurements) > 0 && c.Bytes() >= c.Target.MinBytes
}

// Split separates the measurements worth their own row from the remainder. A
// target that does not opt in returns everything as the remainder, which is the
// single aggregate row.
func (c Candidate) Split() (individual []Measurement, remainder []Measurement) {
	if !c.Target.Split {
		return nil, c.Measurements
	}
	for _, measurement := range c.Measurements {
		if measurement.Bytes >= c.Target.SplitMinBytes {
			individual = append(individual, measurement)
			continue
		}
		remainder = append(remainder, measurement)
	}
	return individual, remainder
}

type Options struct {
	Workers    int
	PathBudget time.Duration
	Budget     time.Duration
}

func (o Options) workers() int {
	if o.Workers > 0 {
		return o.Workers
	}
	workers := runtime.NumCPU() - 1
	if workers < 2 {
		workers = 2
	}
	if workers > 8 {
		workers = 8
	}
	return workers
}

func (o Options) pathBudget() time.Duration {
	if o.PathBudget > 0 {
		return o.PathBudget
	}
	return defaultPathBudget
}

func (o Options) budget() time.Duration {
	if o.Budget > 0 {
		return o.Budget
	}
	return defaultBudget
}

// NewEnv reads everything the guards share, once. A guard that shells out per
// path turns a catalog pass into a process storm.
func NewEnv(ctx context.Context, home string, rootful bool, identity *storage.CommandIdentity) Env {
	whitelist, _ := config.LoadWhitelist(home, config.WhitelistFile)
	return Env{
		Home:      filepath.Clean(home),
		Rootful:   rootful,
		Identity:  identity,
		Whitelist: whitelist,
		Processes: ReadProcesses(ctx),
		Installed: ReadInstalled(filepath.Clean(home)),
		Now:       time.Now(),
	}
}

// Resolve expands every target, drops what the guards refuse, and measures what
// is left. A path already claimed by an earlier target is dropped rather than
// counted twice, so the totals the interface prints add up.
func Resolve(ctx context.Context, env Env, targets []Target, options Options) []Candidate {
	ctx, cancel := context.WithTimeout(ctx, options.budget())
	defer cancel()

	type job struct {
		targetIndex int
		path        string
		exclude     []string
	}
	candidates := make([]Candidate, len(targets))
	for index, target := range targets {
		candidates[index] = Candidate{Target: target}
	}

	claims := newClaims()
	var jobs []job
	// Specific targets claim before broad sweeps, so the Chrome target with its
	// process guard wins the Chrome cache rather than the generic sweep over
	// Library/Caches taking it first.
	for _, index := range claimOrder(targets) {
		target := targets[index]
		for _, path := range target.Expand(ctx, env) {
			cleaned := filepath.Clean(path)
			if owner, taken := claims.coveredBy(cleaned); taken {
				candidates[index].Skipped = append(candidates[index].Skipped,
					Skip{Path: cleaned, Reason: "already counted under " + owner})
				continue
			}
			if _, err := os.Lstat(cleaned); err != nil {
				continue
			}
			if ok, reason := target.Allows(ctx, env, cleaned); !ok {
				candidates[index].Skipped = append(candidates[index].Skipped, Skip{Path: cleaned, Reason: reason})
				continue
			}
			// A sweep that contains an already-claimed subtree still gets a row,
			// but it is measured without those bytes so the totals stay honest.
			exclude := claims.within(cleaned)
			claims.add(cleaned, target.ID)
			jobs = append(jobs, job{targetIndex: index, path: cleaned, exclude: exclude})
		}
	}

	var mutex sync.Mutex
	queue := make(chan job)
	var waiter sync.WaitGroup
	for range options.workers() {
		waiter.Add(1)
		go func() {
			defer waiter.Done()
			for item := range queue {
				usage, modified, partial := measure(ctx, item.path, item.exclude, options.pathBudget())
				mutex.Lock()
				candidate := &candidates[item.targetIndex]
				candidate.Measurements = append(candidate.Measurements, Measurement{
					Path:     item.path,
					Bytes:    usage.Bytes,
					Files:    usage.Files,
					Denied:   usage.Denied,
					Modified: modified,
				})
				candidate.Partial = candidate.Partial || partial
				mutex.Unlock()
			}
		}()
	}
	for _, item := range jobs {
		select {
		case queue <- item:
		case <-ctx.Done():
		}
	}
	close(queue)
	waiter.Wait()

	for index := range candidates {
		measurements := candidates[index].Measurements
		sort.SliceStable(measurements, func(a, b int) bool {
			if measurements[a].Bytes != measurements[b].Bytes {
				return measurements[a].Bytes > measurements[b].Bytes
			}
			return measurements[a].Path < measurements[b].Path
		})
	}
	return candidates
}

func measure(ctx context.Context, path string, exclude []string, budget time.Duration) (storage.Usage, time.Time, bool) {
	pathCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	modified := time.Time{}
	if info, err := os.Lstat(path); err == nil {
		modified = info.ModTime()
	}
	usage, err := storage.PathUsageExcluding(pathCtx, path, exclude)
	return usage, modified, err != nil
}

// claims tracks which target owns which path, so no byte is attributed twice.
type claims struct {
	owners map[string]string
	order  []string
}

func newClaims() *claims {
	return &claims{owners: make(map[string]string)}
}

// coveredBy reports whether this path is already accounted for, either because
// it was claimed outright or because an ancestor of it was.
func (c *claims) coveredBy(path string) (string, bool) {
	for _, claimed := range c.order {
		if path == claimed || strings.HasPrefix(path, claimed+string(filepath.Separator)) {
			return c.owners[claimed], true
		}
	}
	return "", false
}

// within returns the already-claimed paths that sit inside this one.
func (c *claims) within(path string) []string {
	var inside []string
	prefix := path + string(filepath.Separator)
	for _, claimed := range c.order {
		if strings.HasPrefix(claimed, prefix) {
			inside = append(inside, claimed)
		}
	}
	return inside
}

func (c *claims) add(path, owner string) {
	c.owners[path] = owner
	c.order = append(c.order, path)
}

// claimOrder puts the specific targets ahead of the broad sweeps while leaving
// the display order alone.
func claimOrder(targets []Target) []int {
	order := make([]int, 0, len(targets))
	for index, target := range targets {
		if !target.Sweep {
			order = append(order, index)
		}
	}
	for index, target := range targets {
		if target.Sweep {
			order = append(order, index)
		}
	}
	return order
}

// RecheckPaths runs the same guard chain again immediately before removal. The
// preview and the deletion have to agree, and the only way to guarantee that is
// to call the same function twice rather than to write the condition twice.
func RecheckPaths(ctx context.Context, env Env, target Target, paths []string) ([]string, []Skip) {
	allowed := make([]string, 0, len(paths))
	var skipped []Skip
	for _, path := range paths {
		if _, err := os.Lstat(path); err != nil {
			skipped = append(skipped, Skip{Path: path, Reason: "already gone"})
			continue
		}
		if ok, reason := target.Allows(ctx, env, path); !ok {
			skipped = append(skipped, Skip{Path: path, Reason: reason})
			continue
		}
		allowed = append(allowed, path)
	}
	return allowed, skipped
}

func ByID(id string) (Target, bool) {
	for _, target := range All() {
		if target.ID == id {
			return target, true
		}
	}
	return Target{}, false
}

// Describe renders the one line the interface shows under a target.
func (c Candidate) Describe(home string) string {
	paths := c.Paths()
	if len(paths) == 0 {
		return c.Target.Detail
	}
	shown := make([]string, 0, 4)
	for index, path := range paths {
		if index == 3 {
			shown = append(shown, "and more")
			break
		}
		shown = append(shown, storage.RelativeHome(home, path))
	}
	return strings.Join(shown, ", ")
}

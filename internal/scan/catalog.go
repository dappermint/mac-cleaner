package scan

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dappermint/ratatouille/internal/catalog"
	"github.com/dappermint/ratatouille/internal/storage"
)

func (s Scanner) collectCatalog(ctx context.Context) scanResult {
	env := catalog.NewEnv(ctx, s.Home, s.Rootful, s.CommandIdentity)
	candidates := catalog.Resolve(ctx, env, catalog.All(), catalog.Options{})

	result := scanResult{}
	for _, candidate := range candidates {
		if !candidate.Offered() {
			continue
		}
		result.items = append(result.items, s.catalogItems(candidate)...)
	}
	return result
}

// catalogItems turns one target into the rows the actions view shows. A path
// big enough to decide about on its own gets its own row; the rest are summed
// into one, so the list stays readable without hiding anything.
func (s Scanner) catalogItems(candidate catalog.Candidate) []Item {
	individual, remainder := candidate.Split()
	items := make([]Item, 0, len(individual)+1)

	for _, measurement := range individual {
		item := s.catalogItem(candidate.Target, []catalog.Measurement{measurement})
		item.ID = pathID(candidate.Target.ID, measurement.Path)
		item.Name = candidate.Target.Name + ": " + friendlyPathName(measurement.Path)
		items = append(items, item)
	}

	if len(remainder) > 0 {
		item := s.catalogItem(candidate.Target, remainder)
		if len(individual) > 0 {
			item.Name = candidate.Target.Name + ": " + fmt.Sprintf("%d smaller", len(remainder))
			item.ID = candidate.Target.ID + "-rest"
		}
		items = append(items, item)
	}
	return items
}

func (s Scanner) catalogItem(target catalog.Target, measurements []catalog.Measurement) Item {
	var bytes, denied int64
	paths := make([]string, 0, len(measurements))
	pathBytes := make([]int64, 0, len(measurements))
	newest := optionalTime(measurements[0].Modified)
	for _, measurement := range measurements {
		bytes += measurement.Bytes
		denied += measurement.Denied
		paths = append(paths, measurement.Path)
		pathBytes = append(pathBytes, measurement.Bytes)
		if newest == nil || measurement.Modified.After(*newest) {
			newest = optionalTime(measurement.Modified)
		}
	}

	estimate := estimateAllocated
	if denied > 0 {
		estimate = fmt.Sprintf("allocated bytes, %d entries unreadable", denied)
	}
	return Item{
		ID:       target.ID,
		Target:   target.ID,
		Name:     target.Name,
		Group:    string(target.Group),
		Category: target.Category,
		Detail:   target.Detail,
		Source:   sourceList(s.Home, paths),
		Risk:     Risk(target.Risk),
		Bytes:    bytes,
		Modified: newest,
		Estimate: estimate,
		Action: &Action{
			Kind:      ActionTrash,
			Paths:     paths,
			PathBytes: pathBytes,
			Identity:  s.CommandIdentity,
		},
	}
}

// friendlyPathName gives a bundle-id directory a name a person recognises.
func friendlyPathName(path string) string {
	base := filepath.Base(path)
	if base == "Caches" {
		if bundle := catalog.BundleFromContainer(path); bundle != "" {
			base = bundle
		}
	}
	return strings.TrimSuffix(friendlyCacheName(base), " cache")
}

func sourceList(home string, paths []string) string {
	shown := make([]string, 0, 4)
	for index, path := range paths {
		if index == 3 {
			shown = append(shown, fmt.Sprintf("and %d more", len(paths)-3))
			break
		}
		shown = append(shown, storage.RelativeHome(home, path))
	}
	return strings.Join(shown, ", ")
}

// recheckCatalogPaths runs the target's own guard chain a second time,
// immediately before removal. The preview and the deletion use the same
// function rather than two copies of the same condition, which is the only way
// they cannot drift apart.
func recheckCatalogPaths(ctx context.Context, env catalog.Env, item Item) ([]string, []string) {
	if item.Target == "" || item.Action == nil {
		return item.Action.Paths, nil
	}
	target, ok := catalog.ByID(item.Target)
	if !ok {
		return nil, []string{item.Target + " is no longer in the catalog"}
	}
	allowed, skipped := catalog.RecheckPaths(ctx, env, target, item.Action.Paths)
	reasons := make([]string, 0, len(skipped))
	for _, skip := range skipped {
		reasons = append(reasons, storage.RelativeHome(env.Home, skip.Path)+": "+skip.Reason)
	}
	return allowed, reasons
}

func hasCatalogItem(items []Item) bool {
	for _, item := range items {
		if item.Target != "" {
			return true
		}
	}
	return false
}

// CatalogGroups lists the groups present in a report, in catalog order, so the
// interface does not have to know the ordering rule.
func CatalogGroups(report Report) []string {
	present := make(map[string]bool)
	for _, item := range report.Items {
		if item.Target != "" {
			present[item.Group] = true
		}
	}
	groups := make([]string, 0, len(present))
	for _, group := range catalog.GroupOrder {
		if present[string(group)] {
			groups = append(groups, string(group))
		}
	}
	return groups
}

// CatalogExplain renders why a target exists, for the detail pane.
func CatalogExplain(id string) string {
	target, ok := catalog.ByID(id)
	if !ok {
		return ""
	}
	lines := []string{"evidence: " + target.Evidence}
	if len(target.NotTargets) > 0 {
		lines = append(lines, "leaves alone: "+strings.Join(target.NotTargets, "; "))
	}
	return strings.Join(lines, "\n")
}

package tui

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/scan"
	"github.com/dappermint/ratatouille/internal/storage"
)

// markableKinds are the node kinds that stand for something on disk. Every
// other kind is an accounting row: a remainder, the unaccounted gap, a
// foreign volume, free space. Those describe bytes rather than owning them,
// and there is nothing to remove.
func markable(node *scan.SurfaceNode) (bool, string) {
	if node == nil {
		return false, "nothing here"
	}
	switch node.Kind {
	case scan.NodeDirectory:
	case scan.NodeRemainder:
		return false, "this row sums the directories too small to name, open its parent and pick one"
	case scan.NodeUnwalked, scan.NodeUnreadable:
		return false, "this row is space no readable file explains, there is nothing to select"
	case scan.NodeForeign:
		return false, "this is another volume, select it under its own volume row"
	case scan.NodeContainer, scan.NodeVolume, scan.NodeSurface:
		return false, "a container or volume is not something to delete"
	default:
		return false, "this row is not a directory"
	}
	if node.Path == "" {
		return false, "this row has no path of its own"
	}
	if err := safety.ValidateForDeletion(node.Path); err != nil {
		return false, refusalReason(err)
	}
	return true, ""
}

func refusalReason(err error) string {
	var refusal *safety.Refusal
	if errors.As(err, &refusal) {
		return refusal.Reason
	}
	return err.Error()
}

// toggleMark marks or unmarks the directory under the cursor. A directory
// inside an already-marked one is refused rather than marked, because the
// running total has to be the bytes actually reclaimed and not the same bytes
// counted at two depths.
func (state *tuiState) toggleMark() {
	rows := state.surfaceRows()
	index := state.cursor()
	if index < 0 || index >= len(rows) {
		return
	}
	node := rows[index].node

	if state.marked[node.Path] {
		delete(state.marked, node.Path)
		state.notice = "unmarked " + storage.RelativeHome(state.report.Home, node.Path)
		return
	}
	if ok, reason := markable(node); !ok {
		state.notice = reason
		return
	}
	if owner, covered := state.markedAncestor(node.Path); covered {
		state.notice = "already covered by " + storage.RelativeHome(state.report.Home, owner)
		return
	}
	for _, marked := range state.markedPaths() {
		if under(marked, node.Path) {
			delete(state.marked, marked)
		}
	}
	if state.marked == nil {
		state.marked = make(map[string]bool)
	}
	state.marked[node.Path] = true
	state.markedBytes[node.Path] = node.Total()
	state.notice = "marked " + storage.RelativeHome(state.report.Home, node.Path)
}

func (state *tuiState) markedAncestor(path string) (string, bool) {
	for marked := range state.marked {
		if marked != path && under(path, marked) {
			return marked, true
		}
	}
	return "", false
}

func under(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func (state *tuiState) markedPaths() []string {
	paths := make([]string, 0, len(state.marked))
	for path := range state.marked {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (state *tuiState) markedTotal() int64 {
	var total int64
	for path := range state.marked {
		total += state.markedBytes[path]
	}
	return total
}

func (state *tuiState) clearMarks() {
	state.marked = make(map[string]bool)
	state.markedBytes = make(map[string]int64)
}

// markedItem folds the marked directories into one reviewable action, so the
// explorer's removals go through exactly the same confirmation, funnel and
// operation log as everything else in the tool.
func (state *tuiState) markedItem(identity *storage.CommandIdentity) (scan.Item, bool) {
	paths := state.markedPaths()
	if len(paths) == 0 {
		return scan.Item{}, false
	}
	sizes := make([]int64, 0, len(paths))
	for _, path := range paths {
		sizes = append(sizes, state.markedBytes[path])
	}
	name := "explorer selection"
	if len(paths) == 1 {
		name = storage.RelativeHome(state.report.Home, paths[0])
	}
	return scan.Item{
		ID:       "explorer-selection",
		Name:     name,
		Group:    "explorer",
		Category: storage.CategoryUnclassified,
		Detail:   "directories marked in the surface view, moved to Trash so they stay recoverable",
		Source:   strings.Join(shortPaths(state.report.Home, paths), ", "),
		Risk:     scan.RiskReview,
		Bytes:    state.markedTotal(),
		Estimate: "allocated bytes as measured during the walk",
		Action: &scan.Action{
			Kind:      scan.ActionTrash,
			Paths:     paths,
			PathBytes: sizes,
			Identity:  identity,
		},
	}, true
}

func shortPaths(home string, paths []string) []string {
	shown := make([]string, 0, 4)
	for index, path := range paths {
		if index == 3 {
			shown = append(shown, "and more")
			break
		}
		shown = append(shown, storage.RelativeHome(home, path))
	}
	return shown
}

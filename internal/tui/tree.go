package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dappermint/ratatouille/internal/scan"
	"github.com/dappermint/ratatouille/internal/storage"
	"github.com/dappermint/ratatouille/internal/text"
)

type surfaceRow struct {
	node       *scan.SurfaceNode
	key        string
	depth      int
	parent     int64
	parentName string
	open       bool
}

func surfaceRows(root *scan.SurfaceNode, expanded map[string]bool) []surfaceRow {
	if root == nil {
		return nil
	}
	var rows []surfaceRow
	var walk func(node *scan.SurfaceNode, key string, depth int, parent int64, parentName string)
	walk = func(node *scan.SurfaceNode, key string, depth int, parent int64, parentName string) {
		open := expanded[key] && len(node.Children) > 0
		rows = append(rows, surfaceRow{node: node, key: key, depth: depth, parent: parent, parentName: parentName, open: open})
		if !open {
			return
		}
		for _, child := range node.Children {
			walk(child, key+"/"+child.Name, depth+1, node.Total(), node.Name)
		}
	}
	walk(root, root.Name, 0, root.Total(), "")
	return rows
}

func defaultExpansion(root *scan.SurfaceNode, dataPath string, depth int) map[string]bool {
	expanded := make(map[string]bool)
	if root == nil {
		return expanded
	}
	expanded[root.Name] = true
	for _, container := range root.Children {
		containerKey := root.Name + "/" + container.Name
		if !hasChildAt(container, dataPath) {
			continue
		}
		expanded[containerKey] = true
		for _, volume := range container.Children {
			if volume.Path != dataPath {
				continue
			}
			key := containerKey + "/" + volume.Name
			expanded[key] = true
			node := volume
			for level := 0; level < depth; level++ {
				largest := largestChild(node)
				if largest == nil {
					break
				}
				key += "/" + largest.Name
				expanded[key] = true
				node = largest
			}
		}
	}
	return expanded
}

func hasChildAt(n *scan.SurfaceNode, path string) bool {
	if n == nil {
		return false
	}
	for _, child := range n.Children {
		if child.Path == path {
			return true
		}
	}
	return false
}

func largestChild(node *scan.SurfaceNode) *scan.SurfaceNode {
	var best *scan.SurfaceNode
	for _, child := range node.Children {
		if child.Kind != scan.NodeDirectory && child.Kind != scan.NodeVolume {
			continue
		}
		if best == nil || child.Total() > best.Total() {
			best = child
		}
	}
	return best
}

func (state *tuiState) surfaceRows() []surfaceRow {
	if state.report.Surface == nil {
		return nil
	}
	return surfaceRows(state.report.Surface.Root, state.expanded)
}

func (state *tuiState) toggleSurfaceRow() {
	rows := state.surfaceRows()
	index := state.cursor()
	if index < 0 || index >= len(rows) {
		return
	}
	row := rows[index]
	if len(row.node.Children) == 0 {
		state.notice = surfaceLeafNotice(row.node)
		return
	}
	state.expanded[row.key] = !state.expanded[row.key]
}

func (state *tuiState) collapseSurfaceRow() {
	rows := state.surfaceRows()
	index := state.cursor()
	if index < 0 || index >= len(rows) {
		return
	}
	row := rows[index]
	if state.expanded[row.key] && len(row.node.Children) > 0 {
		state.expanded[row.key] = false
		return
	}
	for position := index - 1; position >= 0; position-- {
		if rows[position].depth < row.depth {
			state.setCursor(position)
			return
		}
	}
}

func surfaceLeafNotice(node *scan.SurfaceNode) string {
	switch node.Kind {
	case scan.NodeUnreadable:
		return "size unknown, grant Full Disk Access or rerun with sudo --root"
	case scan.NodeUnwalked:
		return "claimed by the volume, attributed to no readable file"
	case scan.NodeForeign:
		return "separate volume, listed under its own container row"
	default:
		return "no deeper detail was retained for this branch"
	}
}

func surfaceColor(kind scan.NodeKind) string {
	switch kind {
	case scan.NodeContainer, scan.NodeVolume, scan.NodeSurface:
		return colorCyan
	case scan.NodeFree:
		return colorMint
	case scan.NodeUnwalked, scan.NodeOverhead:
		return colorAmber
	case scan.NodeUnreadable:
		return colorCoral
	case scan.NodeRemainder, scan.NodeForeign:
		return colorFog
	default:
		return colorInk
	}
}

func surfaceSize(node *scan.SurfaceNode) string {
	if node.Kind == scan.NodeForeign {
		return "elsewhere"
	}
	if node.Bytes < 0 {
		return "unknown"
	}
	return storage.HumanBytes(node.Bytes)
}

func surfaceShare(node *scan.SurfaceNode, parent int64) string {
	if parent <= 0 || node.Bytes < 0 {
		return ""
	}
	return fmt.Sprintf("%.1f%%", float64(node.Total())/float64(parent)*100)
}

func (state *tuiState) narrowLine(focused bool, color, label string, width int) string {
	if width <= 2 {
		return text.Truncate(text.Clean(label), width)
	}
	cursor := " "
	if focused {
		cursor = state.paint(colorCyan, "┃")
	}
	return cursor + " " + state.paint(color, text.Truncate(text.Clean(label), width-2))
}

func (state *tuiState) surfaceLine(row surfaceRow, focused bool, width int) string {
	if width < 24 {
		return state.narrowLine(focused, surfaceColor(row.node.Kind), row.node.Name+" "+surfaceSize(row.node), width)
	}
	sizeWidth := 10
	shareWidth := 7
	barWidth := 0
	if width >= 96 {
		barWidth = 14
	}
	nameWidth := surfaceNameWidth(width, sizeWidth, shareWidth, barWidth)
	if nameWidth < 12 {
		barWidth = 0
		nameWidth = surfaceNameWidth(width, sizeWidth, shareWidth, barWidth)
	}
	if nameWidth < 8 {
		shareWidth = 0
		nameWidth = surfaceNameWidth(width, sizeWidth, shareWidth, barWidth)
	}
	if nameWidth < 1 {
		nameWidth = 1
	}

	cursor := " "
	if focused {
		cursor = state.paint(colorCyan, "┃")
	}
	glyph := "·"
	if len(row.node.Children) > 0 {
		glyph = "▸"
		if row.open {
			glyph = "▾"
		}
	}
	if state.marked[row.node.Path] && row.node.Path != "" {
		glyph = "✗"
	} else if owner, covered := state.markedAncestor(row.node.Path); covered && owner != "" && row.node.Path != "" {
		glyph = "·"
	}
	depth := row.depth
	if depth > 8 {
		depth = 8
	}
	label := strings.Repeat("  ", depth) + glyph + " " + text.Clean(row.node.Name)
	color := surfaceColor(row.node.Kind)
	if row.node.Path != "" && state.marked[row.node.Path] {
		color = colorAmber
	}
	line := cursor + " " + state.paint(color, text.PadRight(text.Truncate(label, nameWidth), nameWidth)) +
		" " + state.paint(color, text.PadLeft(surfaceSize(row.node), sizeWidth))
	if shareWidth > 0 {
		line += " " + state.paint(colorFog, text.PadLeft(surfaceShare(row.node, row.parent), shareWidth))
	}
	if barWidth > 0 {
		line += " " + state.shareBar(row.node, row.parent, barWidth)
	}
	return line
}

func surfaceNameWidth(width, sizeWidth, shareWidth, barWidth int) int {
	remaining := width - 3 - sizeWidth
	if shareWidth > 0 {
		remaining -= shareWidth + 1
	}
	if barWidth > 0 {
		remaining -= barWidth + 1
	}
	return remaining
}

func (state *tuiState) shareBar(node *scan.SurfaceNode, parent int64, width int) string {
	if parent <= 0 || node.Bytes < 0 || width <= 0 {
		return strings.Repeat(" ", max(width, 0))
	}
	filled := int(float64(width) * float64(node.Total()) / float64(parent))
	if filled > width {
		filled = width
	}
	if filled == 0 && node.Total() > 0 {
		filled = 1
	}
	return state.paint(surfaceColor(node.Kind), strings.Repeat("▇", filled)) +
		state.paint(colorFog, strings.Repeat("·", width-filled))
}

func (state *tuiState) surfaceInspector(width, lineCount int) []string {
	rows := state.surfaceRows()
	index := state.cursor()
	if index < 0 || index >= len(rows) {
		return padLines([]string{state.paint(colorFog, text.Truncate("no surface was measured", width))}, lineCount)
	}
	row := rows[index]
	meta := surfaceSize(row.node)
	if share := surfaceShare(row.node, row.parent); share != "" {
		owner := row.parentName
		if owner == "" {
			owner = "total"
		}
		meta += " / " + share + " of " + text.Clean(owner)
	}
	if row.node.Category != "" {
		meta += " / " + storage.DisplayCategory(row.node.Category)
	}
	lines := []string{state.bold(colorInk, text.Truncate(text.JoinEdges(text.Clean(row.node.Name), meta, width), width))}
	if lineCount <= 1 {
		return lines
	}
	detail := row.node.Detail
	if detail == "" {
		detail = surfaceNodeDetail(row.node)
	}
	lines = append(lines, state.paint(colorFog, text.Truncate(text.Clean(detail), width)))
	if lineCount <= 2 {
		return lines
	}
	source := row.node.Path
	if source == "" {
		source = "derived from apfs volume totals"
	}
	lines = append(lines, state.paint(colorFog, text.Truncate("path    "+text.Clean(source), width)))
	if lineCount > 3 {
		lines = append(lines, state.paint(colorFog, text.Truncate(surfaceCounts(row.node), width)))
	}
	return padLines(lines, lineCount)
}

func surfaceNodeDetail(node *scan.SurfaceNode) string {
	switch node.Kind {
	case scan.NodeRemainder:
		return "everything at this level too small to keep its own row, plus loose files"
	case scan.NodeDirectory:
		return "measured by summing allocated blocks under this directory"
	default:
		return "reported by the filesystem rather than measured file by file"
	}
}

func surfaceCounts(node *scan.SurfaceNode) string {
	parts := make([]string, 0, 3)
	if node.Files > 0 {
		parts = append(parts, fmt.Sprintf("files=%d", node.Files))
	}
	if node.Entries > 0 {
		parts = append(parts, fmt.Sprintf("folded=%d", node.Entries))
	}
	parts = append(parts, "kind="+string(node.Kind))
	return strings.Join(parts, "  ")
}

type healthRow struct {
	level  scan.HealthLevel
	name   string
	value  string
	detail string
	source string
}

func (state *tuiState) healthRows() []healthRow {
	var rows []healthRow
	if state.report.Health != nil {
		signals := append([]scan.HealthSignal(nil), state.report.Health.Signals...)
		sort.SliceStable(signals, func(a, b int) bool {
			return scan.HealthOrder(signals[a].Level) < scan.HealthOrder(signals[b].Level)
		})
		for _, signal := range signals {
			rows = append(rows, healthRow{
				level:  signal.Level,
				name:   signal.Name,
				value:  signal.Value,
				detail: signal.Detail,
				source: signal.Source,
			})
		}
	}
	if state.report.Surface != nil {
		for _, fault := range state.report.Surface.Faults {
			level := scan.HealthWatch
			if fault.Hardware {
				level = scan.HealthAlarm
			}
			rows = append(rows, healthRow{
				level:  level,
				name:   "fault",
				value:  fault.Reason,
				detail: fault.Path,
				source: "surface walk",
			})
		}
	}
	for _, issue := range state.report.Issues {
		rows = append(rows, healthRow{
			level:  scan.HealthUnknown,
			name:   "scan note",
			value:  issue,
			detail: "the scan could not complete this check, so its bytes are not in any total",
			source: "scan",
		})
	}
	return rows
}

func healthColor(level scan.HealthLevel) string {
	switch level {
	case scan.HealthAlarm:
		return colorCoral
	case scan.HealthWatch:
		return colorAmber
	case scan.HealthUnknown:
		return colorFog
	default:
		return colorMint
	}
}

func (state *tuiState) healthLine(row healthRow, focused bool, width int) string {
	levelWidth := 7
	nameWidth := 26
	if width < 64 {
		nameWidth = 18
	}
	valueWidth := width - levelWidth - nameWidth - 4
	if valueWidth < 1 {
		return state.narrowLine(focused, healthColor(row.level), string(row.level)+" "+row.name, width)
	}
	cursor := " "
	if focused {
		cursor = state.paint(colorCyan, "┃")
	}
	return cursor + " " +
		state.paint(healthColor(row.level), text.PadRight(string(row.level), levelWidth)) + " " +
		state.paint(colorInk, text.PadRight(text.Truncate(text.Clean(row.name), nameWidth), nameWidth)) + " " +
		state.paint(colorFog, text.Truncate(text.Clean(row.value), valueWidth))
}

func (state *tuiState) healthInspector(width, lineCount int) []string {
	rows := state.healthRows()
	index := state.cursor()
	if index < 0 || index >= len(rows) {
		return padLines([]string{state.paint(colorFog, text.Truncate("no health signals were gathered", width))}, lineCount)
	}
	row := rows[index]
	lines := []string{state.bold(colorInk, text.Truncate(text.JoinEdges(text.Clean(row.name), string(row.level), width), width))}
	if lineCount <= 1 {
		return lines
	}
	lines = append(lines, state.paint(healthColor(row.level), text.Truncate(text.Clean(row.value), width)))
	if lineCount <= 2 {
		return lines
	}
	for _, wrapped := range text.Wrap(text.Clean(row.detail), width) {
		if len(lines) >= lineCount-1 {
			break
		}
		lines = append(lines, state.paint(colorFog, text.Truncate(wrapped, width)))
	}
	if len(lines) < lineCount {
		lines = append(lines, state.paint(colorFog, text.Truncate("source  "+text.Clean(row.source), width)))
	}
	return padLines(lines, lineCount)
}

func padLines(lines []string, count int) []string {
	for len(lines) < count {
		lines = append(lines, "")
	}
	if len(lines) > count {
		lines = lines[:count]
	}
	return lines
}

package main

import (
	"fmt"
	"sort"
	"strings"
)

type surfaceRow struct {
	node       *SurfaceNode
	key        string
	depth      int
	parent     int64
	parentName string
	open       bool
}

func surfaceRows(root *SurfaceNode, expanded map[string]bool) []surfaceRow {
	if root == nil {
		return nil
	}
	var rows []surfaceRow
	var walk func(node *SurfaceNode, key string, depth int, parent int64, parentName string)
	walk = func(node *SurfaceNode, key string, depth int, parent int64, parentName string) {
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

func defaultExpansion(root *SurfaceNode, dataPath string) map[string]bool {
	expanded := make(map[string]bool)
	if root == nil {
		return expanded
	}
	expanded[root.Name] = true
	for _, container := range root.Children {
		containerKey := root.Name + "/" + container.Name
		if !container.hasChildAt(dataPath) {
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
			for level := 0; level < 2; level++ {
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

func (n *SurfaceNode) hasChildAt(path string) bool {
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

func largestChild(node *SurfaceNode) *SurfaceNode {
	var best *SurfaceNode
	for _, child := range node.Children {
		if child.Kind != NodeDirectory && child.Kind != NodeVolume {
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

func surfaceLeafNotice(node *SurfaceNode) string {
	switch node.Kind {
	case NodeUnreadable:
		return "size unknown, grant Full Disk Access or rerun with sudo --root"
	case NodeUnwalked:
		return "claimed by the volume, attributed to no readable file"
	case NodeForeign:
		return "separate volume, listed under its own container row"
	default:
		return "no deeper detail was retained for this branch"
	}
}

func surfaceColor(kind NodeKind) string {
	switch kind {
	case NodeContainer, NodeVolume, NodeSurface:
		return "cyan"
	case NodeFree:
		return "mint"
	case NodeUnwalked, NodeOverhead:
		return "amber"
	case NodeUnreadable:
		return "coral"
	case NodeRemainder, NodeForeign:
		return "fog"
	default:
		return "ink"
	}
}

func surfaceSize(node *SurfaceNode) string {
	if node.Kind == NodeForeign {
		return "elsewhere"
	}
	if node.Bytes < 0 {
		return "unknown"
	}
	return humanBytes(node.Bytes)
}

func surfaceShare(node *SurfaceNode, parent int64) string {
	if parent <= 0 || node.Bytes < 0 {
		return ""
	}
	return fmt.Sprintf("%.1f%%", float64(node.Total())/float64(parent)*100)
}

func (state *tuiState) narrowLine(focused bool, color, label string, width int) string {
	if width <= 2 {
		return truncate(cleanDisplay(label), width)
	}
	cursor := " "
	if focused {
		cursor = state.paint("cyan", "›")
	}
	return cursor + " " + state.paint(color, truncate(cleanDisplay(label), width-2))
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
		cursor = state.paint("cyan", "›")
	}
	glyph := "·"
	if len(row.node.Children) > 0 {
		glyph = "▸"
		if row.open {
			glyph = "▾"
		}
	}
	depth := row.depth
	if depth > 8 {
		depth = 8
	}
	label := strings.Repeat("  ", depth) + glyph + " " + cleanDisplay(row.node.Name)
	color := surfaceColor(row.node.Kind)
	line := cursor + " " + state.paint(color, padRight(truncate(label, nameWidth), nameWidth)) +
		" " + state.paint(color, padLeft(surfaceSize(row.node), sizeWidth))
	if shareWidth > 0 {
		line += " " + state.paint("fog", padLeft(surfaceShare(row.node, row.parent), shareWidth))
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

func (state *tuiState) shareBar(node *SurfaceNode, parent int64, width int) string {
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
		state.paint("fog", strings.Repeat("·", width-filled))
}

func (state *tuiState) surfaceInspector(width, lineCount int) []string {
	rows := state.surfaceRows()
	index := state.cursor()
	if index < 0 || index >= len(rows) {
		return padLines([]string{state.paint("fog", truncate("no surface was measured", width))}, lineCount)
	}
	row := rows[index]
	meta := surfaceSize(row.node)
	if share := surfaceShare(row.node, row.parent); share != "" {
		owner := row.parentName
		if owner == "" {
			owner = "total"
		}
		meta += " / " + share + " of " + cleanDisplay(owner)
	}
	if row.node.Category != "" {
		meta += " / " + displayCategory(row.node.Category)
	}
	lines := []string{state.bold("ink", truncate(joinEdges(cleanDisplay(row.node.Name), meta, width), width))}
	if lineCount <= 1 {
		return lines
	}
	detail := row.node.Detail
	if detail == "" {
		detail = surfaceNodeDetail(row.node)
	}
	lines = append(lines, state.paint("fog", truncate(cleanDisplay(detail), width)))
	if lineCount <= 2 {
		return lines
	}
	source := row.node.Path
	if source == "" {
		source = "derived from apfs volume totals"
	}
	lines = append(lines, state.paint("fog", truncate("path    "+cleanDisplay(source), width)))
	if lineCount > 3 {
		lines = append(lines, state.paint("fog", truncate(surfaceCounts(row.node), width)))
	}
	return padLines(lines, lineCount)
}

func surfaceNodeDetail(node *SurfaceNode) string {
	switch node.Kind {
	case NodeRemainder:
		return "everything at this level too small to keep its own row, plus loose files"
	case NodeDirectory:
		return "measured by summing allocated blocks under this directory"
	default:
		return "reported by the filesystem rather than measured file by file"
	}
}

func surfaceCounts(node *SurfaceNode) string {
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
	level  HealthLevel
	name   string
	value  string
	detail string
	source string
}

func (state *tuiState) healthRows() []healthRow {
	var rows []healthRow
	if state.report.Health != nil {
		signals := append([]HealthSignal(nil), state.report.Health.Signals...)
		sort.SliceStable(signals, func(a, b int) bool {
			return healthOrder(signals[a].Level) < healthOrder(signals[b].Level)
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
			level := HealthWatch
			if fault.Hardware {
				level = HealthAlarm
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
			level:  HealthUnknown,
			name:   "scan note",
			value:  issue,
			detail: "the scan could not complete this check, so its bytes are not in any total",
			source: "scan",
		})
	}
	return rows
}

func healthColor(level HealthLevel) string {
	switch level {
	case HealthAlarm:
		return "coral"
	case HealthWatch:
		return "amber"
	case HealthUnknown:
		return "fog"
	default:
		return "mint"
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
		cursor = state.paint("cyan", "›")
	}
	return cursor + " " +
		state.paint(healthColor(row.level), padRight(string(row.level), levelWidth)) + " " +
		state.paint("ink", padRight(truncate(cleanDisplay(row.name), nameWidth), nameWidth)) + " " +
		state.paint("fog", truncate(cleanDisplay(row.value), valueWidth))
}

func (state *tuiState) healthInspector(width, lineCount int) []string {
	rows := state.healthRows()
	index := state.cursor()
	if index < 0 || index >= len(rows) {
		return padLines([]string{state.paint("fog", truncate("no health signals were gathered", width))}, lineCount)
	}
	row := rows[index]
	lines := []string{state.bold("ink", truncate(joinEdges(cleanDisplay(row.name), string(row.level), width), width))}
	if lineCount <= 1 {
		return lines
	}
	lines = append(lines, state.paint(healthColor(row.level), truncate(cleanDisplay(row.value), width)))
	if lineCount <= 2 {
		return lines
	}
	for _, wrapped := range wrapText(cleanDisplay(row.detail), width) {
		if len(lines) >= lineCount-1 {
			break
		}
		lines = append(lines, state.paint("fog", truncate(wrapped, width)))
	}
	if len(lines) < lineCount {
		lines = append(lines, state.paint("fog", truncate("source  "+cleanDisplay(row.source), width)))
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

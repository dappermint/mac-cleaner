package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dappermint/mac-cleaner/internal/scan"
	"github.com/dappermint/mac-cleaner/internal/storage"
	"github.com/dappermint/mac-cleaner/internal/text"
)

const (
	ansiReset      = "\x1b[0m"
	ansiBold       = "\x1b[1m"
	ansiClear      = "\x1b[2J\x1b[H"
	ansiHideCursor = "\x1b[?25l"
	ansiShowCursor = "\x1b[?25h"
)

const (
	colorInk   = "ink"
	colorFog   = "fog"
	colorCyan  = "cyan"
	colorMint  = "mint"
	colorAmber = "amber"
	colorCoral = "coral"
)

var tuiColors = map[string]string{
	"ink":   "\x1b[38;2;226;232;240m",
	"fog":   "\x1b[38;2;148;163;184m",
	"cyan":  "\x1b[38;2;56;189;248m",
	"mint":  "\x1b[38;2;74;222;128m",
	"amber": "\x1b[38;2;251;191;36m",
	"coral": "\x1b[38;2;251;113;133m",
}

var tuiFilters = []scan.Risk{"", scan.RiskSafe, scan.RiskReview, scan.RiskDestructive, scan.RiskProtected}

type rawTerminal struct {
	saved string
	raw   bool
}

func (terminal *rawTerminal) Enter() error {
	state := exec.Command("/bin/stty", "-g")
	state.Stdin = os.Stdin
	output, err := state.Output()
	if err != nil {
		return fmt.Errorf("read terminal state: %w", err)
	}
	terminal.saved = strings.TrimSpace(string(output))
	command := exec.Command("/bin/stty", "raw", "-echo")
	command.Stdin = os.Stdin
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("enter raw terminal mode: %s", strings.TrimSpace(string(output)))
	}
	terminal.raw = true
	return nil
}

func (terminal *rawTerminal) Restore() {
	if !terminal.raw || terminal.saved == "" {
		return
	}
	command := exec.Command("/bin/stty", terminal.saved)
	command.Stdin = os.Stdin
	_ = command.Run()
	terminal.raw = false
}

type launchState struct {
	label   string
	started time.Time
	order   []string
	stages  map[string]scan.ScanProgress
	disk    storage.Disk
	color   bool
	rootful bool
}

func tuiScanOptions(rootful bool) scan.Options {
	return scan.Options{Deep: true, Rootful: rootful, Surface: true}
}

func scanWithLaunch(ctx context.Context, home string, options scan.Options, identity *storage.CommandIdentity, renderer *screenRenderer, resize <-chan os.Signal, label string) (scan.Report, error) {
	updates := make(chan scan.ScanProgress, 64)
	reports := make(chan scan.Report, 1)
	scanner := scan.Configure(home, options, identity)
	scanner.Progress = func(progress scan.ScanProgress) {
		select {
		case updates <- progress:
		case <-ctx.Done():
		}
	}
	state := launchState{
		label:   label,
		started: time.Now(),
		stages:  make(map[string]scan.ScanProgress),
		color:   os.Getenv("NO_COLOR") == "",
		rootful: options.Rootful,
	}

	state.render(renderer)
	go func() {
		reports <- scanner.Scan(ctx)
	}()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case progress := <-updates:
			state.apply(progress)
			for {
				select {
				case progress = <-updates:
					state.apply(progress)
				default:
					state.render(renderer)
					goto next
				}
			}
		case report := <-reports:
			for {
				select {
				case progress := <-updates:
					state.apply(progress)
				default:
					state.render(renderer)
					if ctx.Err() != nil {
						return scan.Report{}, ctx.Err()
					}
					return report, nil
				}
			}
		case <-ticker.C:
			state.render(renderer)
		case <-resize:
			renderer.Resize()
			state.render(renderer)
		case <-ctx.Done():
			return scan.Report{}, ctx.Err()
		}
	next:
	}
}

func (state *launchState) apply(progress scan.ScanProgress) {
	if _, exists := state.stages[progress.ID]; !exists {
		state.order = append(state.order, progress.ID)
	}
	state.stages[progress.ID] = progress
	if progress.Disk != nil {
		state.disk = *progress.Disk
	}
}

func (state *launchState) render(renderer *screenRenderer) {
	height, width := renderer.Size()
	if width < 1 {
		width = 1
	}
	now := time.Now()
	hostname, _ := os.Hostname()
	title := "mac-cleaner"
	if hostname != "" {
		title += " / " + text.Clean(hostname)
	}
	scope := "user"
	if state.rootful {
		scope = "root"
	}
	lines := []string{
		state.bold(colorCyan, text.Truncate(text.JoinEdges(title, "scan / "+scope, width), width)),
		state.paint(colorInk, text.Truncate(text.JoinEdges(state.label, "elapsed "+text.Duration(now.Sub(state.started)), width), width)),
	}

	if state.disk.Total > 0 {
		freePercent := float64(state.disk.Free) / float64(state.disk.Total) * 100
		pressure := spaceLabel(freePercent)
		diskLine := fmt.Sprintf("%s free of %s  %s", storage.HumanBytes(state.disk.Free), storage.HumanBytes(state.disk.Total), pressure)
		lines = append(lines, state.paint(spaceColor(freePercent), text.Truncate(diskLine, width)))
		rail := (&tuiState{color: state.color}).storageRail(width, state.disk.Free, state.disk.Total, 0)
		lines = append(lines, rail)
	} else {
		lines = append(lines,
			state.paint(colorFog, text.Truncate("reading physical capacity from the data volume", width)),
			state.paint(colorFog, strings.Repeat("·", width)),
		)
	}

	done, running := state.counts()
	progressLine := fmt.Sprintf("stages=%d/%d", done, len(state.order))
	if running > 0 {
		progressLine += fmt.Sprintf("  running=%d", running)
	}
	lines = append(lines, state.paint(colorFog, text.Truncate(progressLine, width)))

	stageRows := height - len(lines) - 1
	if stageRows < 1 {
		stageRows = 1
	}
	for _, id := range state.visibleStages(stageRows) {
		lines = append(lines, state.stageLine(state.stages[id], now, width))
	}
	for len(lines) < height-1 {
		lines = append(lines, "")
	}
	footer := "read-only scan  ctrl-c abort"
	lines = append(lines, state.paint(colorFog, text.Truncate(footer, width)))
	if len(lines) > height {
		lines = lines[:height]
	}
	renderer.Render(lines)
}

func (state *launchState) counts() (done, running int) {
	for _, progress := range state.stages {
		switch progress.State {
		case scan.ScanDone:
			done++
		case scan.ScanRunning:
			running++
		}
	}
	return done, running
}

func (state *launchState) visibleStages(limit int) []string {
	if len(state.order) <= limit {
		return state.order
	}
	focus := 0
	for index, id := range state.order {
		if state.stages[id].State != scan.ScanQueued {
			focus = index
		}
	}
	start := focus - limit + 1
	if start < 0 {
		start = 0
	}
	if start+limit > len(state.order) {
		start = len(state.order) - limit
	}
	return state.order[start : start+limit]
}

func (state *launchState) stageLine(progress scan.ScanProgress, now time.Time, width int) string {
	prefix := "·"
	color := colorFog
	summary := progress.Detail
	switch progress.State {
	case scan.ScanRunning:
		prefix = "●"
		color = colorCyan
		if progress.Items > 0 || progress.Bytes > 0 {
			summary = fmt.Sprintf("%s files / %s so far", text.Count(progress.Items), storage.HumanBytes(progress.Bytes))
		}
	case scan.ScanDone:
		prefix = "✓"
		color = colorMint
		summary = completedStageSummary(progress)
	}
	elapsed := ""
	if progress.State == scan.ScanRunning && !progress.Started.IsZero() {
		elapsed = text.Duration(now.Sub(progress.Started))
	} else if progress.State == scan.ScanDone {
		elapsed = text.Duration(progress.Elapsed)
	}

	if width < 44 {
		plain := fmt.Sprintf("%s %s  %s", prefix, progress.Name, summary)
		return state.paint(color, text.Truncate(plain, width))
	}
	nameWidth := 15
	elapsedWidth := 7
	summaryWidth := width - nameWidth - elapsedWidth - 5
	if summaryWidth < 1 {
		summaryWidth = 1
	}
	return state.paint(color, prefix) + " " +
		state.paint(color, text.PadRight(text.Truncate(progress.Name, nameWidth), nameWidth)) + "  " +
		state.paint(colorFog, text.PadRight(text.Truncate(summary, summaryWidth), summaryWidth)) + " " +
		state.paint(color, text.PadLeft(elapsed, elapsedWidth))
}

func completedStageSummary(progress scan.ScanProgress) string {
	if progress.ID == "volume" && progress.Disk != nil {
		return storage.HumanBytes(progress.Disk.Total) + " mapped"
	}
	if progress.ID == scan.SurfaceStageID {
		return fmt.Sprintf("%s files / %s accounted", text.Count(progress.Items), storage.HumanBytes(progress.Bytes))
	}
	if progress.Issues > 0 && progress.Items == 0 {
		return fmt.Sprintf("notes=%d", progress.Issues)
	}
	if progress.Items == 0 {
		return "0 items"
	}
	parts := []string{fmt.Sprintf("items=%d", progress.Items)}
	if progress.Bytes > 0 {
		parts = append(parts, storage.HumanBytes(progress.Bytes))
	}
	if progress.Unknown > 0 {
		parts = append(parts, fmt.Sprintf("unknown=%d", progress.Unknown))
	}
	if progress.Issues > 0 {
		parts = append(parts, fmt.Sprintf("notes=%d", progress.Issues))
	}
	return strings.Join(parts, " / ")
}

func (state *launchState) paint(color, value string) string {
	return paintText(state.color, color, value)
}

func (state *launchState) bold(color, value string) string {
	return boldText(state.color, color, value)
}

type tuiView int

const (
	viewSurface tuiView = iota
	viewActions
	viewHealth
)

var tuiViewOrder = []tuiView{viewSurface, viewActions, viewHealth}

func (v tuiView) String() string {
	switch v {
	case viewSurface:
		return "surface"
	case viewActions:
		return "actions"
	default:
		return "health"
	}
}

type tuiState struct {
	report      scan.Report
	selected    map[string]bool
	view        tuiView
	cursors     [3]int
	offsets     [3]int
	expanded    map[string]bool
	filterIndex int
	notice      string
	color       bool
	rootful     bool
}

func (state *tuiState) cursor() int {
	return state.cursors[state.view]
}

func (state *tuiState) setCursor(value int) {
	state.cursors[state.view] = value
}

func (state *tuiState) offset() int {
	return state.offsets[state.view]
}

func (state *tuiState) setOffset(value int) {
	state.offsets[state.view] = value
}

func Run(ctx context.Context, home string, rootful bool, identity *storage.CommandIdentity, out io.Writer) error {
	renderer := newScreenRenderer(out)
	renderer.Enter()
	defer renderer.Exit()
	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)

	report, err := scanWithLaunch(ctx, home, tuiScanOptions(rootful), identity, renderer, resize, "indexing storage")
	if err != nil {
		return err
	}

	terminal := &rawTerminal{}
	if err := terminal.Enter(); err != nil {
		return err
	}
	defer terminal.Restore()

	state := tuiState{
		report:   report,
		selected: make(map[string]bool),
		expanded: defaultExpansion(surfaceRoot(report), report.Disk.Path),
		color:    os.Getenv("NO_COLOR") == "",
		rootful:  rootful,
	}
	state.view = state.openingView()
	keys, resumeKeys := readKeyStream(ctx)

	for {
		state.render(renderer)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-resize:
			renderer.Resize()
			continue
		case event := <-keys:
			if event.err != nil {
				if errors.Is(event.err, io.EOF) {
					return nil
				}
				return event.err
			}
			switch event.key {
			case "q", "ctrl-c":
				return nil
			case "up", "k":
				if state.cursor() > 0 {
					state.setCursor(state.cursor() - 1)
				}
			case "down", "j":
				if state.cursor()+1 < state.rowCount() {
					state.setCursor(state.cursor() + 1)
				}
			case "tab":
				state.cycleFilter()
			case "v":
				state.cycleView()
			case "1":
				state.view = viewSurface
			case "2":
				state.view = viewActions
			case "3":
				state.view = viewHealth
			case "right", "l":
				if state.view == viewSurface {
					state.toggleSurfaceRow()
				}
			case "left", "h":
				if state.view == viewSurface {
					state.collapseSurfaceRow()
				}
			case "space":
				if state.view == viewSurface {
					state.toggleSurfaceRow()
					break
				}
				state.toggleCurrent()
			case "a":
				state.toggleSafe()
			case "enter":
				if state.view == viewSurface {
					state.toggleSurfaceRow()
					break
				}
				if state.view == viewActions {
					state.showDetails(renderer)
				}
			case "?":
				state.showHelp(renderer)
			case "c":
				items := scan.SelectedItems(state.report, state.selected)
				if len(items) == 0 {
					state.notice = "mark=0"
					break
				}
				terminal.Restore()
				renderer.Exit()
				confirmed, confirmErr := confirmInteractive(items, out)
				if confirmErr != nil {
					return confirmErr
				}
				if !confirmed {
					fmt.Fprint(out, "\nenter: return ")
					_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
					renderer.Enter()
					if err := terminal.Enter(); err != nil {
						return err
					}
					state.notice = "unchanged"
					break
				}
				results := scan.ExecuteItems(ctx, home, items, false, out)
				if err := scan.ActionErrors(results); err != nil {
					fmt.Fprintf(out, "\n%s\n", err)
				}
				fmt.Fprint(out, "\nenter: rescan ")
				_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
				renderer.Enter()
				report, scanErr := scanWithLaunch(ctx, home, tuiScanOptions(rootful), identity, renderer, resize, "indexing storage")
				if scanErr != nil {
					return scanErr
				}
				state.reset(report)
				if err := terminal.Enter(); err != nil {
					return err
				}
			case "r":
				terminal.Restore()
				report, scanErr := scanWithLaunch(ctx, home, tuiScanOptions(rootful), identity, renderer, resize, "indexing storage")
				if scanErr != nil {
					return scanErr
				}
				state.reset(report)
				if err := terminal.Enter(); err != nil {
					return err
				}
			}
			select {
			case resumeKeys <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

type keyResult struct {
	key string
	err error
}

func readKeyStream(ctx context.Context) (<-chan keyResult, chan<- struct{}) {
	keys := make(chan keyResult)
	resume := make(chan struct{})
	go func() {
		for {
			key, err := readKey()
			select {
			case keys <- keyResult{key: key, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
			select {
			case <-resume:
			case <-ctx.Done():
				return
			}
		}
	}()
	return keys, resume
}

func (state *tuiState) reset(report scan.Report) {
	state.report = report
	state.selected = make(map[string]bool)
	state.cursors = [3]int{}
	state.offsets = [3]int{}
	state.filterIndex = 0
	state.notice = ""
	state.expanded = defaultExpansion(surfaceRoot(report), report.Disk.Path)
	state.view = state.openingView()
}

func (state *tuiState) render(renderer *screenRenderer) {
	height, width := renderer.Size()
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}

	inspectorLines := 4
	if height < 20 {
		inspectorLines = 2
	}
	if height < 14 {
		inspectorLines = 0
	}
	inspectorBlock := 0
	if inspectorLines > 0 {
		inspectorBlock = inspectorLines + 1
	}

	header := state.headerLines(width, height >= 10)
	visibleRows := height - len(header) - inspectorBlock - 2
	if visibleRows < 1 {
		visibleRows = 1
	}

	rowCount := state.rowCount()
	state.clampCursor(rowCount)
	state.adjustOffset(rowCount, visibleRows)

	lines := make([]string, 0, height)
	lines = append(lines, header...)
	lines = append(lines, state.bodyLines(width, visibleRows)...)
	for len(lines) < len(header)+visibleRows {
		lines = append(lines, "")
	}
	if inspectorLines > 0 {
		lines = append(lines, state.paint(colorFog, strings.Repeat("─", width)))
		lines = append(lines, state.inspectorLines(width, inspectorLines)...)
	}
	lines = append(lines, state.statusLine(width))
	lines = append(lines, state.paint(colorFog, text.Truncate(keyGuide(width, state.view), width)))
	if len(lines) > height {
		lines = lines[:height]
	}
	renderer.Render(lines)
}

func (state *tuiState) headerLines(width int, full bool) []string {
	hostname, _ := os.Hostname()
	title := "mac-cleaner"
	if hostname != "" {
		title += " / " + text.Clean(hostname)
	}
	scope := "user"
	if state.rootful {
		scope = "root"
	}
	lines := []string{state.bold(colorCyan, text.Truncate(text.JoinEdges(title, state.view.String()+" / "+scope, width), width))}

	chosen := scan.SelectedItems(state.report, state.selected)
	direct, toTrash, _ := scan.SelectionTotals(chosen)
	lines = append(lines, state.capacityLine(width, direct))
	lines = append(lines, state.capacityRail(width, direct))

	switch state.view {
	case viewSurface:
		lines = append(lines, state.surfaceSummary(width))
	case viewHealth:
		lines = append(lines, state.healthSummary(width))
	default:
		lines = append(lines, state.selectionLine(chosen, direct, toTrash, width))
	}
	if !full {
		return lines
	}
	switch state.view {
	case viewActions:
		lines = append(lines, state.filterLine(state.filteredIndices(), width))
		lines = append(lines, state.tableHeader(width))
	case viewSurface:
		lines = append(lines, state.paint(colorFog, text.Truncate(surfaceLegend(state.report), width)))
	}
	return lines
}

func (state *tuiState) capacityLine(width int, reclaim int64) string {
	disk := state.report.Disk
	if disk.Total <= 0 {
		return state.paint(colorFog, text.Truncate("physical disk capacity unavailable", width))
	}
	freePercent := float64(disk.Free) / float64(disk.Total) * 100
	label := "disk"
	if disk.Container != "" {
		label = disk.Container
	}
	line := fmt.Sprintf("%s  %s capacity  %s free  %s", label, storage.HumanBytes(disk.Total), storage.HumanBytes(disk.Free), spaceLabel(freePercent))
	if disk.InUse > 0 {
		line = fmt.Sprintf("%s  %s capacity  data %s  other %s  free %s  %s",
			label, storage.HumanBytes(disk.Total), storage.HumanBytes(disk.InUse),
			storage.HumanBytes(disk.Total-disk.Free-disk.InUse), storage.HumanBytes(disk.Free), spaceLabel(freePercent))
	}
	if reclaim > 0 {
		line += "  reclaim=" + storage.HumanBytes(reclaim)
	}
	return state.paint(spaceColor(freePercent), text.Truncate(line, width))
}

func (state *tuiState) capacityRail(width int, reclaim int64) string {
	container := state.activeContainer()
	if container == nil {
		return state.storageRail(width, state.report.Disk.Free, state.report.Disk.Total, reclaim)
	}
	return state.containerRail(width, *container, reclaim)
}

func (state *tuiState) activeContainer() *storage.Container {
	if state.report.Surface == nil {
		return nil
	}
	for index, container := range state.report.Surface.Containers {
		if container.Holds(state.report.Disk.Path) {
			return &state.report.Surface.Containers[index]
		}
	}
	return nil
}

func (state *tuiState) containerRail(width int, container storage.Container, reclaim int64) string {
	if width <= 0 || container.Ceiling <= 0 {
		return ""
	}
	freePercent := float64(container.Free) / float64(container.Ceiling) * 100
	type segment struct {
		bytes int64
		glyph string
		color string
	}
	var segments []segment
	for _, volume := range container.Volumes {
		if volume.InUse <= 0 {
			continue
		}
		if scan.HasRole(volume, "Data") {
			claimed := volume.InUse
			if reclaim > claimed {
				reclaim = claimed
			}
			segments = append(segments,
				segment{bytes: claimed - reclaim, glyph: "█", color: spaceColor(freePercent)},
				segment{bytes: reclaim, glyph: "▓", color: colorMint},
			)
			continue
		}
		segments = append(segments, segment{bytes: volume.InUse, glyph: "▓", color: colorFog})
	}
	if unattributed := container.Unattributed(); unattributed > 0 {
		segments = append(segments, segment{bytes: unattributed, glyph: "▒", color: colorAmber})
	}

	var rendered strings.Builder
	used := 0
	for _, entry := range segments {
		cells := int(float64(width) * float64(entry.bytes) / float64(container.Ceiling))
		if cells == 0 && entry.bytes > 0 && used < width {
			cells = 1
		}
		if used+cells > width {
			cells = width - used
		}
		if cells <= 0 {
			continue
		}
		rendered.WriteString(state.paint(entry.color, strings.Repeat(entry.glyph, cells)))
		used += cells
	}
	if used < width {
		rendered.WriteString(state.paint(colorFog, strings.Repeat("░", width-used)))
	}
	return rendered.String()
}

func surfaceLegend(report scan.Report) string {
	if report.Surface == nil {
		return "no surface measurement in this report"
	}
	surface := report.Surface
	parts := []string{
		"walked=" + storage.HumanBytes(surface.Walked),
		"files=" + text.Count(int(surface.Files)),
		"in=" + text.Duration(surface.Elapsed),
	}
	if surface.Claimed > surface.Walked {
		parts = append(parts, "unaccounted="+storage.HumanBytes(surface.Claimed-surface.Walked))
	}
	if surface.Denied > 0 {
		parts = append(parts, fmt.Sprintf("unreadable=%d", surface.Denied))
	}
	return strings.Join(parts, "  ")
}

func (state *tuiState) surfaceSummary(width int) string {
	if state.report.Surface == nil {
		return state.paint(colorFog, text.Truncate("run with --surface to account for every byte", width))
	}
	surface := state.report.Surface
	covered := float64(100)
	if surface.Claimed > 0 {
		covered = float64(surface.Walked) / float64(surface.Claimed) * 100
	}
	color := colorMint
	if covered < 95 {
		color = colorAmber
	}
	line := fmt.Sprintf("data volume explained %.1f%%  %s of %s", covered, storage.HumanBytes(surface.Walked), storage.HumanBytes(surface.Claimed))
	return state.paint(color, text.Truncate(line, width))
}

func (state *tuiState) healthSummary(width int) string {
	if state.report.Health == nil {
		return state.paint(colorFog, text.Truncate("no health signals were gathered", width))
	}
	health := *state.report.Health
	line := "filesystem " + string(health.Level) + "  " + health.Summary()
	if !health.Verified {
		line += "  no live verify"
	}
	return state.paint(healthColor(health.Level), text.Truncate(line, width))
}

func (state *tuiState) rowCount() int {
	switch state.view {
	case viewSurface:
		return len(state.surfaceRows())
	case viewHealth:
		return len(state.healthRows())
	default:
		return len(state.filteredIndices())
	}
}

func (state *tuiState) bodyLines(width, visibleRows int) []string {
	offset := state.offset()
	cursor := state.cursor()
	lines := make([]string, 0, visibleRows)
	switch state.view {
	case viewSurface:
		rows := state.surfaceRows()
		if len(rows) == 0 {
			return []string{state.paint(colorFog, text.Truncate("  no surface was measured", width))}
		}
		for position := offset; position < len(rows) && position < offset+visibleRows; position++ {
			lines = append(lines, state.surfaceLine(rows[position], position == cursor, width))
		}
	case viewHealth:
		rows := state.healthRows()
		if len(rows) == 0 {
			return []string{state.paint(colorFog, text.Truncate("  no health signals were gathered", width))}
		}
		for position := offset; position < len(rows) && position < offset+visibleRows; position++ {
			lines = append(lines, state.healthLine(rows[position], position == cursor, width))
		}
	default:
		indices := state.filteredIndices()
		if len(indices) == 0 {
			return []string{state.paint(colorFog, text.Truncate("  no rows", width))}
		}
		for position := offset; position < len(indices) && position < offset+visibleRows; position++ {
			lines = append(lines, state.itemLine(state.report.Items[indices[position]], position == cursor, width))
		}
	}
	return lines
}

func (state *tuiState) inspectorLines(width, lineCount int) []string {
	switch state.view {
	case viewSurface:
		return state.surfaceInspector(width, lineCount)
	case viewHealth:
		return state.healthInspector(width, lineCount)
	default:
		return state.inspector(width, lineCount)
	}
}

func (state *tuiState) cycleView() {
	state.view = tuiViewOrder[(int(state.view)+1)%len(tuiViewOrder)]
	state.notice = "view=" + state.view.String()
}

func (state *tuiState) openingView() tuiView {
	if surfaceRoot(state.report) == nil {
		return viewActions
	}
	return viewSurface
}

func (state *tuiState) filteredIndices() []int {
	filter := tuiFilters[state.filterIndex]
	indices := make([]int, 0, len(state.report.Items))
	for index, item := range state.report.Items {
		if filter == "" || item.Risk == filter {
			indices = append(indices, index)
		}
	}
	return indices
}

func (state *tuiState) focusedItem() (scan.Item, bool) {
	indices := state.filteredIndices()
	cursor := state.cursors[viewActions]
	if cursor < 0 || cursor >= len(indices) {
		return scan.Item{}, false
	}
	return state.report.Items[indices[cursor]], true
}

func (state *tuiState) cycleFilter() {
	if state.view != viewActions {
		state.view = viewActions
	}
	state.filterIndex = (state.filterIndex + 1) % len(tuiFilters)
	state.setCursor(0)
	state.setOffset(0)
	state.notice = "risk=" + filterLabel(tuiFilters[state.filterIndex])
}

func (state *tuiState) clampCursor(count int) {
	if count == 0 {
		state.setCursor(0)
		state.setOffset(0)
		return
	}
	if state.cursor() < 0 {
		state.setCursor(0)
	}
	if state.cursor() >= count {
		state.setCursor(count - 1)
	}
}

func (state *tuiState) adjustOffset(count, visibleRows int) {
	if state.cursor() < state.offset() {
		state.setOffset(state.cursor())
	}
	if state.cursor() >= state.offset()+visibleRows {
		state.setOffset(state.cursor() - visibleRows + 1)
	}
	maxOffset := count - visibleRows
	if maxOffset < 0 {
		maxOffset = 0
	}
	if state.offset() > maxOffset {
		state.setOffset(maxOffset)
	}
}

func (state *tuiState) selectionLine(chosen []scan.Item, direct, toTrash int64, width int) string {
	parts := []string{
		fmt.Sprintf("mark=%d", len(chosen)),
		"reclaim=" + storage.HumanBytes(direct),
		"trash=" + storage.HumanBytes(toTrash),
	}
	if unknown := scan.UnknownSizeCount(chosen); unknown > 0 {
		parts = append(parts, fmt.Sprintf("unknown=%d", unknown))
	}
	color := colorFog
	if len(chosen) > 0 {
		color = colorMint
	}
	return state.paint(color, text.Truncate(strings.Join(parts, "  "), width))
}

func (state *tuiState) filterLine(indices []int, width int) string {
	label := filterLabel(tuiFilters[state.filterIndex])
	plain := fmt.Sprintf("risk=%s  items=%d  %s", label, len(indices), riskSummary(state.report))
	return state.paint(colorFog, text.Truncate(plain, width))
}

func (state *tuiState) tableHeader(width int) string {
	if width < 52 {
		return state.paint(colorFog, text.Truncate("      size  item", width))
	}
	categoryWidth := 14
	if width >= 80 {
		categoryWidth = 20
	}
	header := "      " + text.PadRight("category", categoryWidth) + " " + text.PadLeft("size", 9) + "  item"
	return state.paint(colorFog, text.Truncate(header, width))
}

func (state *tuiState) itemLine(item scan.Item, focused bool, width int) string {
	if width < 24 {
		return state.narrowLine(focused, riskColor(item.Risk), item.Name+" "+storage.HumanBytes(item.Bytes), width)
	}
	cursor := " "
	if focused {
		cursor = state.paint(colorCyan, "›")
	}
	mark := " · "
	if item.Selectable() {
		mark = "[ ]"
		if state.selected[item.ID] {
			mark = state.paint(colorMint, "[x]")
		}
	}
	size := text.PadLeft(storage.HumanBytes(item.Bytes), 9)
	if width < 52 {
		nameWidth := width - 17
		if nameWidth < 1 {
			nameWidth = 1
		}
		return cursor + " " + mark + " " + size + "  " + text.Truncate(text.Clean(item.Name), nameWidth)
	}
	categoryWidth := 14
	if width >= 80 {
		categoryWidth = 20
	}
	badge := state.paint(riskColor(item.Risk), text.PadRight(text.Truncate(storage.DisplayCategory(item.Category), categoryWidth), categoryWidth))
	nameWidth := width - categoryWidth - 18
	if nameWidth < 1 {
		nameWidth = 1
	}
	return cursor + " " + mark + " " + badge + " " + size + "  " + text.Truncate(text.Clean(item.Name), nameWidth)
}

func (state *tuiState) inspector(width, lineCount int) []string {
	item, ok := state.focusedItem()
	if !ok {
		lines := []string{state.paint(colorFog, text.Truncate("no rows", width))}
		for len(lines) < lineCount {
			lines = append(lines, "")
		}
		return lines
	}
	meta := storage.HumanBytes(item.Bytes) + " / " + storage.DisplayCategory(item.Category) + " / " + string(item.Risk)
	lines := []string{state.bold(colorInk, text.Truncate(text.JoinEdges(text.Clean(item.Name), meta, width), width))}
	if lineCount == 1 {
		return lines
	}
	if lineCount == 2 {
		summary := "source " + inspectorSource(item) + "  /  " + inspectorAction(item)
		return append(lines, state.paint(colorFog, text.Truncate(summary, width)))
	}
	lines = append(lines, state.paint(colorFog, text.Truncate(text.Clean(item.Detail), width)))
	lines = append(lines, state.paint(colorFog, text.Truncate("source  "+inspectorSource(item), width)))
	if len(lines) < lineCount {
		lines = append(lines, state.paint(riskColor(item.Risk), text.Truncate("action  "+inspectorAction(item), width)))
	}
	return lines
}

func inspectorSource(item scan.Item) string {
	if item.Source != "" {
		return text.Clean(item.Source)
	}
	if item.Group != "" {
		return text.Clean(item.Group)
	}
	return "not reported"
}

func inspectorAction(item scan.Item) string {
	if item.Unavailable != "" {
		return "unavailable, " + text.Clean(item.Unavailable)
	}
	if item.Action == nil {
		return "read-only"
	}
	return text.Clean(item.Action.Display())
}

func (state *tuiState) statusLine(width int) string {
	if state.notice != "" {
		notice := state.notice
		state.notice = ""
		return state.paint(colorAmber, text.Truncate(notice, width))
	}
	if len(state.report.Issues) > 0 {
		return state.paint(colorFog, text.Truncate(fmt.Sprintf("scope=%s  notes=%d  allocated-blocks", state.scope(), len(state.report.Issues)), width))
	}
	return state.paint(colorFog, text.Truncate("scope="+state.scope()+"  notes=0  allocated-blocks", width))
}

func (state *tuiState) scope() string {
	if state.rootful {
		return "root"
	}
	return "user"
}

func (state *tuiState) toggleCurrent() {
	item, ok := state.focusedItem()
	if !ok {
		return
	}
	if !item.Selectable() {
		state.notice = string(item.Risk) + ": read-only"
		return
	}
	state.selected[item.ID] = !state.selected[item.ID]
}

func (state *tuiState) toggleSafe() {
	allSelected := true
	count := 0
	for _, item := range state.report.Items {
		if item.Risk == scan.RiskSafe && item.Selectable() {
			count++
			allSelected = allSelected && state.selected[item.ID]
		}
	}
	if count == 0 {
		state.notice = "safe=0"
		return
	}
	for _, item := range state.report.Items {
		if item.Risk == scan.RiskSafe && item.Selectable() {
			state.selected[item.ID] = !allSelected
		}
	}
}

func (state *tuiState) showDetails(renderer *screenRenderer) {
	item, ok := state.focusedItem()
	if !ok {
		return
	}
	height, width := renderer.Size()
	if width < 12 {
		width = 12
	}
	lines := []string{
		state.bold(colorCyan, text.Truncate(text.Clean(item.Name), width)),
		"",
		text.Truncate("category   "+storage.DisplayCategory(item.Category), width),
		"risk       " + state.riskBadge(item.Risk),
		text.Truncate("size       "+storage.HumanBytes(item.Bytes), width),
		text.Truncate("estimate   "+text.Clean(item.Estimate), width),
	}
	if item.Source != "" {
		lines = append(lines, text.Truncate("source     "+text.Clean(item.Source), width))
	}
	if item.Modified != nil {
		lines = append(lines, "modified   "+item.Modified.Format("2006-01-02 15:04"))
	}
	lines = append(lines, "")
	for _, line := range text.Wrap(text.Clean(item.Detail), width-2) {
		lines = append(lines, state.paint(colorFog, line))
	}
	if item.Action != nil {
		lines = append(lines, "", state.bold(riskColor(item.Risk), "action"))
		for _, line := range text.Wrap(text.Clean(item.Action.Display()), width-2) {
			lines = append(lines, "  "+line)
		}
	} else {
		lines = append(lines, "", state.paint(colorFog, "read-only"))
	}
	if item.Unavailable != "" {
		lines = append(lines, "", state.paint(colorCoral, text.Truncate("unavailable: "+text.Clean(item.Unavailable), width)))
	}
	if len(lines) > height-1 {
		lines = lines[:height-1]
	}
	for len(lines) < height-1 {
		lines = append(lines, "")
	}
	lines = append(lines, state.paint(colorFog, text.Truncate("any key: return", width)))
	renderer.Render(lines)
	_, _ = readKey()
	renderer.Invalidate()
}

func (state *tuiState) showHelp(renderer *screenRenderer) {
	height, width := renderer.Size()
	plain := []string{
		"keymap",
		"",
		"j/k  move                     v  next view",
		"1  surface   2  actions   3  health",
		"",
		"surface view",
		"h/l or arrows  fold and unfold a branch",
		"every level sums to its parent, remainders and unreadable trees included",
		"",
		"actions view",
		"tab  next risk filter        space  mark",
		"a  mark safe set             enter  inspect",
		"c  execute marked set        r  rescan",
		"",
		"safe         supported command",
		"review       explicit mark",
		"destructive  exact confirmation",
		"protected    read-only",
		"",
		"health view lists device, container and walk signals worst first",
		"",
		"--root       System Data, macOS, Other Users & Shared",
		"--verify     live filesystem check, root only",
	}
	lines := make([]string, 0, height)
	for index, line := range plain {
		line = text.Truncate(line, width)
		if index == 0 {
			line = state.bold(colorCyan, line)
		}
		lines = append(lines, line)
	}
	if len(lines) > height-1 {
		lines = lines[:height-1]
	}
	for len(lines) < height-1 {
		lines = append(lines, "")
	}
	lines = append(lines, state.paint(colorFog, text.Truncate("any key: return", width)))
	renderer.Render(lines)
	_, _ = readKey()
	renderer.Invalidate()
}

func confirmInteractive(items []scan.Item, out io.Writer) (bool, error) {
	fmt.Fprint(out, scan.PlanText(items))
	phrase := scan.ConfirmationPhrase(items)
	fmt.Fprintf(out, "\ntype %q to continue: ", phrase)
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if strings.TrimSpace(answer) != phrase {
		fmt.Fprintln(out, "aborted")
		return false, nil
	}
	return true, nil
}

func (state *tuiState) riskBadge(risk scan.Risk) string {
	return state.paint(riskColor(risk), string(risk))
}

func riskColor(risk scan.Risk) string {
	switch risk {
	case scan.RiskSafe:
		return colorMint
	case scan.RiskReview:
		return colorAmber
	case scan.RiskDestructive:
		return colorCoral
	case scan.RiskProtected:
		return colorFog
	default:
		return colorCyan
	}
}

func (state *tuiState) storageRail(width int, free, total, reclaim int64) string {
	if total <= 0 || width <= 0 {
		return ""
	}
	if free < 0 {
		free = 0
	}
	if free > total {
		free = total
	}
	used := total - free
	if reclaim < 0 {
		reclaim = 0
	}
	if reclaim > used {
		reclaim = used
	}
	remaining := used - reclaim
	usedCells := int(float64(width) * float64(remaining) / float64(total))
	reclaimCells := int(float64(width) * float64(reclaim) / float64(total))
	if reclaim > 0 && reclaimCells == 0 && width > 1 {
		reclaimCells = 1
		if usedCells+reclaimCells > width {
			usedCells = width - reclaimCells
		}
	}
	if usedCells+reclaimCells > width {
		usedCells = width - reclaimCells
	}
	freeCells := width - usedCells - reclaimCells
	freePercent := float64(free) / float64(total) * 100
	return state.paint(spaceColor(freePercent), strings.Repeat("█", usedCells)) +
		state.paint(colorMint, strings.Repeat("▓", reclaimCells)) +
		state.paint(colorFog, strings.Repeat("░", freeCells))
}

func riskSummary(report scan.Report) string {
	parts := make([]string, 0, 5)
	for _, risk := range []scan.Risk{scan.RiskSafe, scan.RiskReview, scan.RiskDestructive, scan.RiskProtected, scan.RiskInfo} {
		count := 0
		var bytes int64
		unknown := false
		for _, item := range report.Items {
			if item.Risk != risk {
				continue
			}
			count++
			if item.Bytes < 0 {
				unknown = true
			} else {
				bytes += item.Bytes
			}
		}
		if count == 0 {
			continue
		}
		part := fmt.Sprintf("%s=%d/%s", risk, count, storage.HumanBytes(bytes))
		if unknown {
			part += "+"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "  ")
}

func filterLabel(risk scan.Risk) string {
	if risk == "" {
		return "all"
	}
	return string(risk)
}

func spaceColor(freePercent float64) string {
	if freePercent < 5 {
		return colorCoral
	}
	if freePercent < 10 {
		return colorAmber
	}
	return colorCyan
}

func spaceLabel(freePercent float64) string {
	switch {
	case freePercent < 5:
		return fmt.Sprintf("critical  free=%.1f%%", freePercent)
	case freePercent < 10:
		return fmt.Sprintf("tight  free=%.1f%%", freePercent)
	default:
		return fmt.Sprintf("healthy  free=%.1f%%", freePercent)
	}
}

func keyGuide(width int, view tuiView) string {
	if view == viewSurface {
		if width >= 100 {
			return "jk move  hl fold  v view  1 surface 2 actions 3 health  r scan  ? keys  q quit"
		}
		if width >= 72 {
			return "jk move  hl fold  v view  r scan  ? keys  q quit"
		}
		return "jk move  hl fold  v view  q quit"
	}
	if view == viewHealth {
		if width >= 72 {
			return "jk move  v view  r scan  ? keys  q quit"
		}
		return "jk move  v view  q quit"
	}
	if width >= 100 {
		return "jk move  tab risk  space mark  a safe  enter inspect  c execute  v view  r scan  q quit"
	}
	if width >= 72 {
		return "jk move  tab risk  space mark  enter inspect  c execute  v view  q quit"
	}
	return "jk move  tab risk  space mark  c run  q quit"
}

func surfaceRoot(report scan.Report) *scan.SurfaceNode {
	if report.Surface == nil {
		return nil
	}
	return report.Surface.Root
}

func (state *tuiState) paint(color, value string) string {
	return paintText(state.color, color, value)
}

func (state *tuiState) bold(color, value string) string {
	return boldText(state.color, color, value)
}

func paintText(enabled bool, color, value string) string {
	if !enabled || value == "" {
		return value
	}
	return tuiColors[color] + value + ansiReset
}

func boldText(enabled bool, color, value string) string {
	if !enabled || value == "" {
		return value
	}
	return tuiColors[color] + ansiBold + value + ansiReset
}

func terminalSize() (height, width int) {
	command := exec.Command("/bin/stty", "size")
	command.Stdin = os.Stdin
	output, err := command.Output()
	if err == nil {
		fields := strings.Fields(string(output))
		if len(fields) == 2 {
			height, _ = strconv.Atoi(fields[0])
			width, _ = strconv.Atoi(fields[1])
		}
	}
	if height <= 0 {
		height = 24
	}
	if width <= 0 {
		width = 80
	}
	return height, width
}

func readKey() (string, error) {
	buffer := make([]byte, 1)
	if _, err := os.Stdin.Read(buffer); err != nil {
		return "", err
	}
	switch buffer[0] {
	case 3:
		return "ctrl-c", nil
	case 9:
		return "tab", nil
	case '\r', '\n':
		return "enter", nil
	case ' ':
		return "space", nil
	case 27:
		sequence := make([]byte, 2)
		if _, err := io.ReadFull(os.Stdin, sequence); err != nil {
			return "", err
		}
		if sequence[0] == '[' {
			switch sequence[1] {
			case 'A':
				return "up", nil
			case 'B':
				return "down", nil
			case 'C':
				return "right", nil
			case 'D':
				return "left", nil
			}
		}
		return "escape", nil
	default:
		return string(buffer), nil
	}
}

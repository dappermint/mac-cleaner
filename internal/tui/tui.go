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

	"github.com/dappermint/ratatouille/internal/i18n"
	"github.com/dappermint/ratatouille/internal/keymap"
	"github.com/dappermint/ratatouille/internal/metrics"
	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/scan"
	"github.com/dappermint/ratatouille/internal/storage"
	"github.com/dappermint/ratatouille/internal/text"
	"github.com/dappermint/ratatouille/internal/uninstall"
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
	label     string
	started   time.Time
	order     []string
	stages    map[string]scan.ScanProgress
	disk      storage.Disk
	color     bool
	rootful   bool
	cached    *scan.Report
	localizer *i18n.Localizer
}

func tuiScanOptions(rootful bool) scan.Options {
	return scan.Options{Deep: true, Rootful: rootful, Surface: true, MinFileBytes: 512 * 1024 * 1024, LargeFileLimit: 100}
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
		label:     label,
		started:   time.Now(),
		stages:    make(map[string]scan.ScanProgress),
		color:     os.Getenv("NO_COLOR") == "",
		rootful:   options.Rootful,
		localizer: i18n.FromContext(ctx),
	}
	if cached, err := scan.LoadCachedReport(home, options.Rootful); err == nil {
		state.cached = &cached
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
	title := "ratatouille"
	if hostname != "" {
		title += " / " + text.Clean(hostname)
	}
	scope := state.localizer.T("tui.scope.user")
	if state.rootful {
		scope = state.localizer.T("tui.scope.root")
	}
	lines := []string{
		state.bold(colorCyan, headerText(title, "scan / "+scope, width)),
		state.paint(colorInk, text.Truncate(text.JoinEdges(state.label, state.localizer.T("tui.launch.elapsed", text.Duration(now.Sub(state.started))), width), width)),
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
			state.paint(colorFog, text.Truncate(state.localizer.T("tui.launch.reading_capacity"), width)),
			state.paint(colorFog, strings.Repeat("·", width)),
		)
	}

	done, running := state.counts()
	progressLine := fmt.Sprintf("stages=%d/%d", done, len(state.order))
	if running > 0 {
		progressLine += fmt.Sprintf("  running=%d", running)
	}
	lines = append(lines, state.paint(colorFog, text.Truncate(progressLine, width)))
	if state.cached != nil && surfaceRoot(*state.cached) != nil && height-len(lines) > 5 {
		age := time.Since(state.cached.GeneratedAt)
		lines = append(lines, state.paint(colorFog, text.Truncate(state.localizer.T("tui.launch.cached", text.Duration(age)), width)))
		preview := tuiState{
			report: *state.cached, expanded: defaultExpansion(surfaceRoot(*state.cached), state.cached.Disk.Path), color: state.color, localizer: state.localizer,
		}
		rows := preview.surfaceRows()
		limit := min(len(rows), min(6, height-len(lines)-3))
		for index := 0; index < limit; index++ {
			lines = append(lines, preview.surfaceLine(rows[index], false, width))
		}
	}

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
	footer := state.localizer.T("tui.launch.footer")
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
	viewApps
	viewHealth
	viewStatus
)

var tuiViewOrder = []tuiView{viewSurface, viewActions, viewApps, viewHealth, viewStatus}

func (v tuiView) String() string {
	switch v {
	case viewSurface:
		return "surface"
	case viewActions:
		return "actions"
	case viewApps:
		return "apps"
	case viewHealth:
		return "health"
	default:
		return "status"
	}
}

type tuiState struct {
	report       scan.Report
	selected     map[string]bool
	view         tuiView
	cursors      [5]int
	offsets      [5]int
	expanded     map[string]bool
	marked       map[string]bool
	markedBytes  map[string]int64
	keys         *keymap.Map
	configPath   string
	mode         keymap.Mode
	pending      string
	command      string
	anchor       int
	filterIndex  int
	notice       string
	color        bool
	rootful      bool
	apps         []uninstall.App
	selectedApps map[string]bool
	status       metrics.Snapshot
	tracker      *metrics.Tracker
	localizer    *i18n.Localizer
}

func (state *tuiState) t(key string) string {
	if state.localizer == nil {
		state.localizer = i18n.EnglishGB()
	}
	return state.localizer.T(key)
}

func (state *tuiState) viewName() string {
	return state.t("tui.view." + state.view.String())
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

func Run(ctx context.Context, home string, rootful bool, identity *storage.CommandIdentity, funnel *safety.Funnel, keys *keymap.Map, configPath string, out io.Writer) error {
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

	tracker := metrics.NewTracker()
	state := tuiState{
		report:       report,
		selected:     make(map[string]bool),
		expanded:     defaultExpansion(surfaceRoot(report), report.Disk.Path),
		marked:       make(map[string]bool),
		markedBytes:  make(map[string]int64),
		keys:         keys,
		configPath:   configPath,
		mode:         keymap.Normal,
		anchor:       -1,
		color:        os.Getenv("NO_COLOR") == "",
		rootful:      rootful,
		apps:         uninstall.Inventory(ctx, uninstall.Env{Home: home, Rootful: rootful, Identity: identity}),
		selectedApps: make(map[string]bool),
		status:       tracker.Observe(metrics.Collect(ctx)),
		tracker:      tracker,
		localizer:    i18n.FromContext(ctx),
	}
	state.view = state.openingView()
	presses, resumeKeys := readKeyStream(ctx)
	statusTicker := time.NewTicker(2 * time.Second)
	defer statusTicker.Stop()

	for {
		state.render(renderer)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-resize:
			renderer.Resize()
			continue
		case <-statusTicker.C:
			if state.view == viewStatus {
				state.status = state.tracker.Observe(metrics.Collect(ctx))
			}
			continue
		case event := <-presses:
			if event.err != nil {
				if errors.Is(event.err, io.EOF) {
					return nil
				}
				return event.err
			}
			// A key press resolves to a named action first, so the bindings
			// live in one table a user can rebind rather than in this switch.
			action, pending := state.keys.Lookup(state.mode, state.pending, event.key)
			if pending {
				state.pending += event.key
				break
			}
			state.pending = ""
			if state.mode == keymap.Cmdline {
				done, quit := state.commandKey(action, event.key)
				if quit {
					return nil
				}
				if !done {
					break
				}
			}

			switch action {
			case keymap.Quit:
				return nil
			case keymap.Up:
				state.moveBy(-1)
			case keymap.Down:
				state.moveBy(1)
			case keymap.Top:
				state.setCursor(0)
			case keymap.Bottom:
				state.setCursor(state.rowCount() - 1)
			case keymap.HalfPageDown:
				state.moveBy(state.pageStep(renderer))
			case keymap.HalfPageUp:
				state.moveBy(-state.pageStep(renderer))
			case keymap.NextFilter:
				state.cycleFilter()
			case keymap.NextView:
				state.cycleView()
			case keymap.ViewSurface:
				state.view = viewSurface
			case keymap.ViewActions:
				state.view = viewActions
			case keymap.ViewApps:
				state.view = viewApps
			case keymap.ViewHealth:
				state.view = viewHealth
			case keymap.ViewStatus:
				state.view = viewStatus
			case keymap.Unfold:
				if state.view == viewSurface {
					state.toggleSurfaceRow()
				}
			case keymap.Fold:
				if state.view == viewSurface {
					state.collapseSurfaceRow()
				}
			case keymap.Visual:
				state.enterVisual()
			case keymap.Command:
				state.enterCommand()
			case keymap.Escape:
				state.leaveMode()
			case keymap.ClearMarks:
				state.clearMarks()
				state.notice = "marks cleared"
			case keymap.Toggle:
				if state.view == viewSurface {
					state.toggleSurfaceRow()
					break
				}
				if state.view == viewApps {
					state.toggleApp()
					break
				}
				state.applyToRange(state.toggleAt)
			case keymap.ToggleSafe:
				state.toggleSafe()
			case keymap.Mark:
				if state.view == viewSurface {
					state.applyToRange(state.markAt)
				}
			case keymap.Details:
				if state.view == viewSurface {
					state.toggleSurfaceRow()
					break
				}
				if state.view == viewActions {
					state.showDetails(renderer)
				}
			case keymap.Help:
				state.showHelp(renderer)
			case keymap.Execute, keymap.ExecuteMarks:
				if state.view == viewApps {
					if err := state.executeApps(ctx, home, rootful, identity, funnel, terminal, renderer, out); err != nil {
						return err
					}
					break
				}
				var items []scan.Item
				if action == keymap.ExecuteMarks {
					marked, ok := state.markedItem(identity)
					if !ok {
						state.notice = "nothing marked, press " + state.keyFor(keymap.Mark) + " on a directory in the surface view"
						break
					}
					items = []scan.Item{marked}
				} else {
					items = scan.SelectedItems(state.report, state.selected)
				}
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
				results := scan.ExecuteItems(ctx, funnel, items, out)
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
				state.apps = uninstall.Inventory(ctx, uninstall.Env{Home: home, Rootful: rootful, Identity: identity})
				if err := terminal.Enter(); err != nil {
					return err
				}
			case keymap.Rescan:
				if state.view == viewStatus {
					state.status = state.tracker.Observe(metrics.Collect(ctx))
					break
				}
				if state.view == viewApps {
					state.apps = uninstall.Inventory(ctx, uninstall.Env{Home: home, Rootful: rootful, Identity: identity})
					break
				}
				terminal.Restore()
				report, scanErr := scanWithLaunch(ctx, home, tuiScanOptions(rootful), identity, renderer, resize, "indexing storage")
				if scanErr != nil {
					return scanErr
				}
				state.reset(report)
				state.apps = uninstall.Inventory(ctx, uninstall.Env{Home: home, Rootful: rootful, Identity: identity})
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
	state.cursors = [5]int{}
	state.offsets = [5]int{}
	state.filterIndex = 0
	state.notice = ""
	state.expanded = defaultExpansion(surfaceRoot(report), report.Disk.Path)
	state.clearMarks()
	state.mode = keymap.Normal
	state.pending = ""
	state.command = ""
	state.anchor = -1
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
	title := "ratatouille"
	if hostname != "" {
		title += " / " + text.Clean(hostname)
	}
	scope := state.t("tui.scope.user")
	if state.rootful {
		scope = state.t("tui.scope.root")
	}
	lines := []string{state.bold(colorCyan, headerText(title, state.viewName()+" / "+scope, width))}

	chosen := scan.SelectedItems(state.report, state.selected)
	direct, toTrash, _ := scan.SelectionTotals(chosen)
	lines = append(lines, state.capacityLine(width, direct))
	lines = append(lines, state.capacityRail(width, direct))

	switch state.view {
	case viewSurface:
		lines = append(lines, state.surfaceSummary(width))
	case viewApps:
		lines = append(lines, state.appSummary(width))
	case viewHealth:
		lines = append(lines, state.healthSummary(width))
	case viewStatus:
		lines = append(lines, state.liveStatusSummary(width))
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
	case viewApps:
		lines = append(lines, state.appTableHeader(width))
	case viewStatus:
		lines = append(lines, state.paint(colorFog, text.Truncate("live sample, refreshed every 2s", width)))
	}
	return lines
}

func headerText(left, right string, width int) string {
	if width > 1 {
		width--
	}
	return text.JoinEdges(left, right, width)
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
	// A marked set is the thing the user is about to act on, so while one
	// exists it takes the line the coverage figure would otherwise have.
	if count := len(state.marked); count > 0 {
		line := fmt.Sprintf("marked=%d  trash=%s  x to execute, d to unmark",
			count, storage.HumanBytes(state.markedTotal()))
		return state.paint(colorAmber, text.Truncate(line, width))
	}
	line := fmt.Sprintf("data volume explained %.1f%%  %s of %s", covered, storage.HumanBytes(surface.Walked), storage.HumanBytes(surface.Claimed))
	if len(state.report.Insights) > 0 {
		line += fmt.Sprintf("  insights=%d", len(state.report.Insights))
	}
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
	case viewApps:
		return len(state.apps)
	case viewStatus:
		return len(state.statusRows())
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
			return []string{state.paint(colorFog, text.Truncate("  "+state.t("tui.empty.surface"), width))}
		}
		for position := offset; position < len(rows) && position < offset+visibleRows; position++ {
			lines = append(lines, state.surfaceLine(rows[position], position == cursor, width))
		}
	case viewHealth:
		rows := state.healthRows()
		if len(rows) == 0 {
			return []string{state.paint(colorFog, text.Truncate("  "+state.t("tui.empty.health"), width))}
		}
		for position := offset; position < len(rows) && position < offset+visibleRows; position++ {
			lines = append(lines, state.healthLine(rows[position], position == cursor, width))
		}
	case viewApps:
		if len(state.apps) == 0 {
			return []string{state.paint(colorFog, text.Truncate("  "+state.t("tui.apps.none"), width))}
		}
		for position := offset; position < len(state.apps) && position < offset+visibleRows; position++ {
			lines = append(lines, state.appLine(state.apps[position], position == cursor, width))
		}
	case viewStatus:
		rows := state.statusRows()
		for position := offset; position < len(rows) && position < offset+visibleRows; position++ {
			lines = append(lines, state.statusMetricLine(rows[position], position == cursor, width))
		}
	default:
		indices := state.filteredIndices()
		if len(indices) == 0 {
			return []string{state.paint(colorFog, text.Truncate("  "+state.t("tui.empty.rows"), width))}
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
	case viewApps:
		return state.appInspector(width, lineCount)
	case viewStatus:
		return state.statusInspector(width, lineCount)
	default:
		return state.inspector(width, lineCount)
	}
}

func (state *tuiState) cycleView() {
	state.view = tuiViewOrder[(int(state.view)+1)%len(tuiViewOrder)]
	state.notice = "view=" + state.viewName()
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
	status := "scope=" + state.scope() + "  notes=0  allocated-blocks"
	if len(state.report.Issues) > 0 {
		status = fmt.Sprintf("scope=%s  notes=%d  allocated-blocks", state.scope(), len(state.report.Issues))
	}
	if mode := state.modeLabel(); mode != "" {
		colour := colorFog
		if state.mode != keymap.Normal {
			colour = colorAmber
		}
		return state.paint(colour, text.Truncate(mode+"  "+status, width))
	}
	return state.paint(colorFog, text.Truncate(status, width))
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
	// The help is generated from the live keymap. A hardcoded list tells a user
	// who rebound something to press a key that does nothing.
	plain := []string{"keymap: " + state.keys.Name(), ""}
	for _, action := range state.keys.Actions(keymap.Normal) {
		keys := strings.Join(state.keys.Keys(keymap.Normal, action), "  ")
		plain = append(plain, fmt.Sprintf("%-14s %s", keys, keymap.Describe[action]))
	}
	if state.keys.Modal() {
		plain = append(plain, "", "visual mode")
		for _, action := range state.keys.Actions(keymap.Visual_) {
			keys := strings.Join(state.keys.Keys(keymap.Visual_, action), "  ")
			plain = append(plain, fmt.Sprintf("%-14s %s over the selected rows", keys, keymap.Describe[action]))
		}
		plain = append(plain, "", "command line", ":surface :actions :apps :health :status :clear :marks :q")
	}
	plain = append(plain,
		"",
		"safe         supported command or proven evidence",
		"review       explicit mark",
		"destructive  exact confirmation",
		"protected    read-only",
		"",
		"every level sums to its parent, remainders and unreadable trees included",
		"bindings live in "+state.configPath,
	)

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
			return "jk move  hl fold  v view  1 surface 2 actions 3 apps 4 health 5 status  r scan  ? keys  q quit"
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
	if view == viewApps {
		if width >= 72 {
			return "jk move  space mark  c uninstall  v view  r scan  ? keys  q quit"
		}
		return "jk move  space mark  c uninstall  q quit"
	}
	if view == viewStatus {
		return "jk move  v view  r sample  ? keys  q quit"
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
	case 2:
		return "ctrl-b", nil
	case 3:
		return "ctrl-c", nil
	case 4:
		return "ctrl-d", nil
	case 6:
		return "ctrl-f", nil
	case 9:
		return "tab", nil
	case 21:
		return "ctrl-u", nil
	case 23:
		return "ctrl-w", nil
	case 127, 8:
		return "backspace", nil
	case '\r', '\n':
		return "enter", nil
	case ' ':
		return "space", nil
	case 27:
		return readEscape()
	default:
		return string(buffer), nil
	}
}

// readEscape distinguishes a bare Escape from the start of an arrow sequence.
// Waiting unconditionally for two more bytes makes Escape hang until the next
// key, which is unusable once there are modes to leave.
func readEscape() (string, error) {
	if err := os.Stdin.SetReadDeadline(time.Now().Add(escapeWindow)); err != nil {
		// A stdin that cannot take a deadline falls back to the blocking read,
		// which is what this did before modes existed.
		sequence := make([]byte, 2)
		if _, err := io.ReadFull(os.Stdin, sequence); err != nil {
			return "", err
		}
		return arrowFrom(sequence), nil
	}
	defer func() { _ = os.Stdin.SetReadDeadline(time.Time{}) }()

	sequence := make([]byte, 2)
	if _, err := io.ReadFull(os.Stdin, sequence); err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return "escape", nil
		}
		return "", err
	}
	return arrowFrom(sequence), nil
}

func arrowFrom(sequence []byte) string {
	if sequence[0] == '[' {
		switch sequence[1] {
		case 'A':
			return "up"
		case 'B':
			return "down"
		case 'C':
			return "right"
		case 'D':
			return "left"
		}
	}
	return "escape"
}

const escapeWindow = 40 * time.Millisecond

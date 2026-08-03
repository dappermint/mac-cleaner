package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/dappermint/ratatouille/internal/keymap"
	"github.com/dappermint/ratatouille/internal/text"
)

type keyHint struct {
	key   string
	label string
}

func (state *tuiState) navigationLine(width int) string {
	position := state.positionLabel()
	available := width
	if position != "" {
		available -= utf8.RuneCountInString(position) + 2
	}
	if available < 1 {
		return state.paint(colorFog, text.Truncate(position, width))
	}

	plain, rendered := state.fullNavigation()
	if utf8.RuneCountInString(plain) > available {
		plain, rendered = state.compactNavigation(available)
	}
	if position == "" {
		return rendered
	}
	spaces := width - utf8.RuneCountInString(plain) - utf8.RuneCountInString(position)
	if spaces < 1 {
		spaces = 1
	}
	return rendered + strings.Repeat(" ", spaces) + state.paint(colorFog, position)
}

func (state *tuiState) fullNavigation() (string, string) {
	plainTabs := make([]string, 0, len(tuiViewOrder))
	renderedTabs := make([]string, 0, len(tuiViewOrder))
	for _, view := range tuiViewOrder {
		label := state.viewLabel(view)
		if key := state.boundKey(viewAction(view)); key != "" {
			label = key + " " + label
		}
		plain := label
		if view == state.view {
			plain = "[" + label + "]"
			plainTabs = append(plainTabs, plain)
			renderedTabs = append(renderedTabs, state.bold(colorCyan, plain))
			continue
		}
		plainTabs = append(plainTabs, plain)
		renderedTabs = append(renderedTabs, state.paint(colorFog, plain))
	}
	return strings.Join(plainTabs, "  "), strings.Join(renderedTabs, "  ")
}

func (state *tuiState) compactNavigation(width int) (string, string) {
	active := "[" + state.viewName() + "]"
	next := state.boundKey(keymap.NextView)
	plain := active
	if next != "" {
		candidate := active + "  " + next + " next view"
		if utf8.RuneCountInString(candidate) <= width {
			plain = candidate
		}
	}
	plain = text.Truncate(plain, width)
	return plain, state.bold(colorCyan, plain)
}

func viewAction(view tuiView) keymap.Action {
	switch view {
	case viewSurface:
		return keymap.ViewSurface
	case viewActions:
		return keymap.ViewActions
	case viewApps:
		return keymap.ViewApps
	case viewHealth:
		return keymap.ViewHealth
	default:
		return keymap.ViewStatus
	}
}

func (state *tuiState) positionLabel() string {
	count := state.rowCount()
	if count == 0 {
		return "0/0"
	}
	cursor := state.cursor()
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= count {
		cursor = count - 1
	}
	return fmt.Sprintf("%d/%d", cursor+1, count)
}

func (state *tuiState) keyGuide(width int) string {
	hints := state.viewHints()
	for len(hints) > 1 && hintWidth(hints) > width {
		hints = append(hints[:len(hints)-2], hints[len(hints)-1])
	}
	if len(hints) == 0 {
		return ""
	}
	if hintWidth(hints) > width {
		return state.paint(colorFog, text.Truncate(hints[0].key+" "+hints[0].label, width))
	}

	parts := make([]string, 0, len(hints))
	for _, hint := range hints {
		parts = append(parts, state.bold(colorCyan, hint.key)+" "+state.paint(colorFog, hint.label))
	}
	return strings.Join(parts, "  ")
}

func (state *tuiState) viewHints() []keyHint {
	hints := []keyHint{state.pairedHint(keymap.Down, keymap.Up, "move")}
	switch state.view {
	case viewSurface:
		hints = append(hints,
			state.pairedHint(keymap.Fold, keymap.Unfold, "fold"),
			state.actionHint(keymap.Mark, "mark"),
			state.actionHint(keymap.ExecuteMarks, "trash"),
		)
	case viewActions:
		hints = append(hints,
			state.actionHint(keymap.NextFilter, "risk"),
			state.actionHint(keymap.Toggle, "mark"),
			state.actionHint(keymap.Details, "inspect"),
			state.actionHint(keymap.Execute, "run"),
		)
	case viewApps:
		hints = append(hints,
			state.actionHint(keymap.Toggle, "mark"),
			state.actionHint(keymap.Execute, "uninstall"),
			state.actionHint(keymap.Rescan, "refresh"),
		)
	case viewStatus:
		hints = append(hints, state.actionHint(keymap.Rescan, "sample"))
	}
	if state.view != viewApps && state.view != viewStatus {
		hints = append(hints, state.actionHint(keymap.Rescan, "scan"))
	}
	hints = append(hints,
		state.actionHint(keymap.NextView, "view"),
		state.actionHint(keymap.Help, "keys"),
		state.actionHint(keymap.Quit, "quit"),
	)

	filtered := hints[:0]
	for _, hint := range hints {
		if hint.key != "" {
			filtered = append(filtered, hint)
		}
	}
	return filtered
}

func (state *tuiState) actionHint(action keymap.Action, label string) keyHint {
	return keyHint{key: state.boundKey(action), label: label}
}

func (state *tuiState) pairedHint(first, second keymap.Action, label string) keyHint {
	left := state.boundKey(first)
	right := state.boundKey(second)
	switch {
	case left == "":
		return keyHint{key: right, label: label}
	case right == "":
		return keyHint{key: left, label: label}
	default:
		return keyHint{key: left + "/" + right, label: label}
	}
}

func (state *tuiState) boundKey(action keymap.Action) string {
	keys := state.keys.Keys(keymap.Normal, action)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

func hintWidth(hints []keyHint) int {
	width := 0
	for index, hint := range hints {
		if index > 0 {
			width += 2
		}
		width += utf8.RuneCountInString(hint.key) + 1 + utf8.RuneCountInString(hint.label)
	}
	return width
}

func (state *tuiState) inspectorRule(width int) string {
	label := " details "
	if width <= utf8.RuneCountInString(label)+1 {
		return state.paint(colorFog, strings.Repeat("─", max(width, 0)))
	}
	return state.paint(colorFog, "─"+label+strings.Repeat("─", width-utf8.RuneCountInString(label)-1))
}

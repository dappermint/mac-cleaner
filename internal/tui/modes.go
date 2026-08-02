package tui

import (
	"strings"

	"github.com/dappermint/ratatouille/internal/keymap"
)

// enterVisual starts a range selection anchored at the current row. It is a
// no-op under a keymap without modes, so the default map never lands in a state
// its bindings cannot leave.
func (state *tuiState) enterVisual() {
	if !state.keys.Modal() {
		return
	}
	state.mode = keymap.Visual_
	state.anchor = state.cursor()
	state.notice = "visual, move to extend and " + state.keyFor(keymap.Mark) + " to act"
}

func (state *tuiState) enterCommand() {
	if !state.keys.Modal() {
		return
	}
	state.mode = keymap.Cmdline
	state.command = ""
}

func (state *tuiState) leaveMode() {
	switch state.mode {
	case keymap.Visual_:
		state.notice = ""
	case keymap.Cmdline:
		state.command = ""
	default:
		// Escape in normal mode is the one that clears a pending selection,
		// which is the only thing left to cancel.
		if len(state.marked) > 0 {
			state.clearMarks()
			state.notice = "marks cleared"
		}
	}
	state.mode = keymap.Normal
	state.anchor = -1
}

// visualRange is the rows the current selection covers, inclusive. Outside
// visual mode it is just the row under the cursor.
func (state *tuiState) visualRange() (int, int) {
	cursor := state.cursor()
	if state.mode != keymap.Visual_ || state.anchor < 0 {
		return cursor, cursor
	}
	if state.anchor <= cursor {
		return state.anchor, cursor
	}
	return cursor, state.anchor
}

// applyToRange runs an action over every row in the visual range, then leaves
// visual mode. Acting on a range and then staying in it invites acting twice.
func (state *tuiState) applyToRange(apply func(index int)) {
	first, last := state.visualRange()
	saved := state.cursor()
	for index := first; index <= last; index++ {
		apply(index)
	}
	state.setCursor(saved)
	if state.mode == keymap.Visual_ {
		count := last - first + 1
		state.mode = keymap.Normal
		state.anchor = -1
		if count > 1 {
			state.notice = pluralRows(count)
		}
	}
}

func pluralRows(count int) string {
	if count == 1 {
		return "1 row"
	}
	return itoa(count) + " rows"
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func (state *tuiState) markAt(index int) {
	saved := state.cursor()
	state.setCursor(index)
	state.toggleMark()
	state.setCursor(saved)
}

func (state *tuiState) toggleAt(index int) {
	saved := state.cursor()
	state.setCursor(index)
	state.toggleCurrent()
	state.setCursor(saved)
}

func (state *tuiState) moveBy(delta int) {
	target := state.cursor() + delta
	if target < 0 {
		target = 0
	}
	if limit := state.rowCount() - 1; target > limit {
		target = limit
	}
	if target < 0 {
		target = 0
	}
	state.setCursor(target)
}

// pageStep is half the visible rows, which is what a half-page movement means
// on a screen rather than a fixed number of lines.
func (state *tuiState) pageStep(renderer *screenRenderer) int {
	height, _ := renderer.Size()
	step := (height - 8) / 2
	if step < 1 {
		return 1
	}
	return step
}

// commandKey feeds a key into the command line. It returns whether the command
// was submitted and whether it asked to quit.
func (state *tuiState) commandKey(action keymap.Action, key string) (bool, bool) {
	switch action {
	case keymap.Escape:
		state.leaveMode()
		return false, false
	case keymap.Confirm:
		command := strings.TrimSpace(state.command)
		state.mode = keymap.Normal
		state.command = ""
		return state.runCommand(command)
	}
	switch key {
	case "backspace", "delete":
		if state.command != "" {
			state.command = state.command[:len(state.command)-1]
		}
	case "space":
		state.command += " "
	default:
		if len(key) == 1 {
			state.command += key
		}
	}
	return false, false
}

// runCommand handles the : line. The vocabulary is deliberately tiny and maps
// onto things the keys already do, so it is a second way to reach them rather
// than a second set of behaviour.
func (state *tuiState) runCommand(command string) (bool, bool) {
	switch command {
	case "", "q", "quit":
		return false, command != ""
	case "surface", "1":
		state.view = viewSurface
	case "actions", "2":
		state.view = viewActions
	case "health", "3":
		state.view = viewHealth
	case "clear":
		state.clearMarks()
		state.notice = "marks cleared"
	case "marks":
		state.notice = pluralRows(len(state.marked)) + " marked"
	case "r", "rescan":
		state.notice = "press " + state.keyFor(keymap.Rescan) + " to rescan"
	case "c", "clean", "x":
		state.notice = "press " + state.keyFor(keymap.Execute) + " to run the selection"
	case "help", "h":
		state.notice = "press " + state.keyFor(keymap.Help) + " for the keymap"
	default:
		state.notice = "no command " + command
	}
	return true, false
}

// keyFor names a key for a message, so a rebound keymap never tells the user to
// press something that does nothing.
func (state *tuiState) keyFor(action keymap.Action) string {
	keys := state.keys.Keys(keymap.Normal, action)
	if len(keys) == 0 {
		return "the bound key"
	}
	return keys[0]
}

// modeLabel is the indicator the status line shows. A keymap without modes
// shows nothing, because there is nothing to be in.
func (state *tuiState) modeLabel() string {
	if !state.keys.Modal() {
		return ""
	}
	switch state.mode {
	case keymap.Visual_:
		return "VISUAL"
	case keymap.Cmdline:
		return ":" + state.command
	default:
		if state.pending != "" {
			return state.pending
		}
		return "NORMAL"
	}
}

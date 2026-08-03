// Package keymap turns key presses into named actions. The interface binds
// actions rather than keys, so a user can rebind anything and a vim user can
// have modes without either of them being a second copy of the key handling.
package keymap

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dappermint/ratatouille/internal/config"
)

type Action string

const (
	None Action = ""

	Quit    Action = "quit"
	Help    Action = "help"
	Rescan  Action = "rescan"
	Escape  Action = "escape"
	Confirm Action = "confirm"

	Up           Action = "up"
	Down         Action = "down"
	Top          Action = "top"
	Bottom       Action = "bottom"
	HalfPageDown Action = "half-page-down"
	HalfPageUp   Action = "half-page-up"

	Fold   Action = "fold"
	Unfold Action = "unfold"

	NextView    Action = "next-view"
	ViewSurface Action = "view-surface"
	ViewActions Action = "view-actions"
	ViewApps    Action = "view-apps"
	ViewHealth  Action = "view-health"
	ViewStatus  Action = "view-status"
	NextFilter  Action = "next-filter"

	Toggle       Action = "toggle"
	ToggleSafe   Action = "toggle-safe"
	Mark         Action = "mark"
	ClearMarks   Action = "clear-marks"
	Execute      Action = "execute"
	ExecuteMarks Action = "execute-marks"
	Details      Action = "details"

	Visual  Action = "visual"
	Command Action = "command"
)

// Mode is which set of bindings is live. The default keymap only ever uses
// Normal; vim adds the other two.
type Mode string

const (
	Normal  Mode = "normal"
	Visual_ Mode = "visual"
	Cmdline Mode = "command"
)

func (m Mode) String() string { return string(m) }

// Describe is the one-line explanation shown in help and in `config keys`.
var Describe = map[Action]string{
	Quit: "leave", Help: "this list", Rescan: "scan again", Escape: "cancel",
	Confirm: "confirm", Up: "up one row", Down: "down one row",
	Top: "first row", Bottom: "last row",
	HalfPageDown: "down half a screen", HalfPageUp: "up half a screen",
	Fold: "fold a branch", Unfold: "unfold a branch",
	NextView: "next view", ViewSurface: "surface view", ViewActions: "actions view",
	ViewApps: "apps view", ViewHealth: "health view", ViewStatus: "status view", NextFilter: "next risk filter",
	Toggle: "select the row", ToggleSafe: "select every safe action",
	Mark: "mark a directory for Trash", ClearMarks: "unmark everything",
	Execute: "run the selection", ExecuteMarks: "trash the marked set",
	Details: "inspect the row", Visual: "visual mode", Command: "command line",
}

// Order is the order actions appear in help, grouped the way a person reads
// them rather than alphabetically.
var Order = []Action{
	Up, Down, Top, Bottom, HalfPageDown, HalfPageUp,
	Fold, Unfold, NextView, ViewSurface, ViewActions, ViewApps, ViewHealth, ViewStatus, NextFilter,
	Toggle, ToggleSafe, Mark, ClearMarks, Execute, ExecuteMarks, Details,
	Visual, Command, Rescan, Help, Confirm, Escape, Quit,
}

type Map struct {
	name     string
	bindings map[Mode]map[string]Action
	// prefixes holds the first key of every multi-key binding, so the reader
	// knows to wait for a second press rather than reporting no match.
	prefixes map[Mode]map[string]bool
}

// Every accessor tolerates a nil map. A caller that has not loaded a keymap
// yet should render an interface with no bindings, not crash.
func (m *Map) Name() string {
	if m == nil {
		return "none"
	}
	return m.name
}

// Modal reports whether this keymap has anything beyond normal mode. The
// interface uses it to decide whether a mode indicator is worth the space.
func (m *Map) Modal() bool {
	if m == nil {
		return false
	}
	return len(m.bindings[Visual_]) > 0 || len(m.bindings[Cmdline]) > 0
}

// Lookup resolves a key press. Pending is the keys already typed toward a
// multi-key binding, empty for the common case. It returns the action, and
// whether the sequence so far is the start of a longer binding.
func (m *Map) Lookup(mode Mode, pending, key string) (Action, bool) {
	if m == nil {
		return None, false
	}
	sequence := key
	if pending != "" {
		sequence = pending + key
	}
	if action, ok := m.bindings[mode][sequence]; ok {
		return action, false
	}
	// Normal-mode bindings stay live in visual mode unless visual overrides
	// them, so movement does not have to be declared twice.
	if mode == Visual_ {
		if action, ok := m.bindings[Normal][sequence]; ok {
			return action, false
		}
	}
	if m.prefixes[mode][sequence] || (mode == Visual_ && m.prefixes[Normal][sequence]) {
		return None, true
	}
	return None, false
}

// Keys returns every key bound to an action in a mode, for display.
func (m *Map) Keys(mode Mode, action Action) []string {
	if m == nil {
		return nil
	}
	var keys []string
	for key, bound := range m.bindings[mode] {
		if bound == action {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(a, b int) bool {
		if len(keys[a]) != len(keys[b]) {
			return len(keys[a]) < len(keys[b])
		}
		return keys[a] < keys[b]
	})
	return keys
}

func (m *Map) Actions(mode Mode) []Action {
	if m == nil {
		return nil
	}
	seen := make(map[Action]bool)
	for _, action := range m.bindings[mode] {
		seen[action] = true
	}
	ordered := make([]Action, 0, len(seen))
	for _, action := range Order {
		if seen[action] {
			ordered = append(ordered, action)
		}
	}
	return ordered
}

// Ambiguous reports any key that is both a complete binding and the start of a
// longer one. Such a key resolves on the first press, so the longer binding is
// unreachable. It is exported so a test can hold the line.
func (m *Map) Ambiguous() []string {
	if m == nil {
		return nil
	}
	var found []string
	for mode, bindings := range m.bindings {
		for key := range bindings {
			if m.prefixes[mode][key] {
				found = append(found, string(mode)+":"+key)
			}
		}
	}
	sort.Strings(found)
	return found
}

func newMap(name string) *Map {
	return &Map{
		name:     name,
		bindings: map[Mode]map[string]Action{Normal: {}, Visual_: {}, Cmdline: {}},
		prefixes: map[Mode]map[string]bool{Normal: {}, Visual_: {}, Cmdline: {}},
	}
}

func (m *Map) bind(mode Mode, action Action, keys ...string) {
	for _, key := range keys {
		m.bindings[mode][key] = action
		for length := 1; length < len(key); length++ {
			// Only whole-key prefixes count: "gg" makes "g" a prefix, but a
			// single named key like "left" must not make "l" one.
			if isChord(key) {
				m.prefixes[mode][key[:length]] = true
			}
		}
	}
}

// namedKeys are the multi-character names the decoder emits for a single
// press. Without an explicit list, "left" looks exactly like a chord and turns
// "l" into a prefix that swallows the unfold binding.
var namedKeys = map[string]bool{
	"up": true, "down": true, "left": true, "right": true,
	"tab": true, "enter": true, "space": true, "escape": true,
	"backspace": true, "delete": true,
}

// isChord reports whether a binding is several single-character presses rather
// than one named key such as "enter" or "ctrl-d".
func isChord(key string) bool {
	if len(key) < 2 || namedKeys[key] || strings.Contains(key, "-") {
		return false
	}
	for _, character := range key {
		if character > 127 || character == ' ' {
			return false
		}
	}
	return true
}

// Default is the keymap for someone who has never used vi. Arrows work, hjkl
// works, and there are no modes to be in.
func Default() *Map {
	m := newMap("default")
	m.bind(Normal, Up, "up", "k")
	m.bind(Normal, Down, "down", "j")
	m.bind(Normal, Top, "g")
	m.bind(Normal, Bottom, "G")
	m.bind(Normal, HalfPageDown, "ctrl-d")
	m.bind(Normal, HalfPageUp, "ctrl-u")
	m.bind(Normal, Unfold, "right", "l")
	m.bind(Normal, Fold, "left", "h")
	m.bind(Normal, NextView, "v")
	m.bind(Normal, ViewSurface, "1")
	m.bind(Normal, ViewActions, "2")
	m.bind(Normal, ViewApps, "3")
	m.bind(Normal, ViewHealth, "4")
	m.bind(Normal, ViewStatus, "5")
	m.bind(Normal, NextFilter, "tab")
	m.bind(Normal, Toggle, "space")
	m.bind(Normal, ToggleSafe, "a")
	m.bind(Normal, Mark, "d")
	m.bind(Normal, Execute, "c")
	m.bind(Normal, ExecuteMarks, "x")
	m.bind(Normal, Details, "enter")
	m.bind(Normal, Rescan, "r")
	m.bind(Normal, Help, "?")
	m.bind(Normal, Escape, "escape")
	m.bind(Normal, Quit, "q", "ctrl-c")
	return m
}

// Vim adds the movement and the two extra modes a vi user reaches for without
// thinking. Everything from the default map still works, so this is an
// addition rather than a separate dialect to learn.
func Vim() *Map {
	m := Default()
	m.name = "vim"
	// Bare g does nothing in vim; it only exists as the start of gg. Leaving
	// the default's single-key binding in place would resolve on the first
	// press and the chord could never fire.
	clear(m, Normal, Top)
	m.bind(Normal, Top, "gg")
	m.bind(Normal, HalfPageDown, "ctrl-d", "ctrl-f")
	m.bind(Normal, HalfPageUp, "ctrl-u", "ctrl-b")
	m.bind(Normal, Visual, "V", "v")
	m.bind(Normal, NextView, "ctrl-w")
	m.bind(Normal, Command, ":")
	m.bind(Normal, Mark, "d")
	m.bind(Normal, ClearMarks, "u")
	m.bind(Normal, Execute, "c")
	m.bind(Normal, ExecuteMarks, "x", "ZZ")

	// Visual mode keeps normal movement and swaps what the action keys mean.
	m.bind(Visual_, Toggle, "space", "enter")
	m.bind(Visual_, Mark, "d")
	m.bind(Visual_, Escape, "escape", "V", "v")
	m.bind(Visual_, Quit, "ctrl-c")

	m.bind(Cmdline, Confirm, "enter")
	m.bind(Cmdline, Escape, "escape", "ctrl-c")
	return m
}

// Preset resolves a keymap by name.
func Preset(name string) (*Map, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "default", "plain":
		return Default(), nil
	case "vim", "vi":
		return Vim(), nil
	default:
		return nil, fmt.Errorf("no keymap named %q, try default or vim", name)
	}
}

// Load builds the keymap from settings: a preset, then any [keys] overrides.
// An override replaces the preset's keys for that action rather than adding to
// them, so a user who rebinds something does not silently keep the old key too.
func Load(settings *config.Settings) (*Map, error) {
	preferences := config.PreferencesFrom(settings)
	m, err := Preset(preferences.Keymap)
	if err != nil {
		return nil, err
	}
	for _, mode := range []Mode{Normal, Visual_, Cmdline} {
		section := "keys"
		if mode != Normal {
			section = "keys." + string(mode)
		}
		for _, action := range Order {
			key := section + "." + string(action)
			if !settings.Has(key) {
				continue
			}
			clear(m, mode, action)
			m.bind(mode, action, settings.List(key)...)
		}
	}
	m.rebuildPrefixes()
	if ambiguous := m.Ambiguous(); len(ambiguous) > 0 {
		return nil, fmt.Errorf("ambiguous key bindings: %s", strings.Join(ambiguous, ", "))
	}
	return m, nil
}

func clear(m *Map, mode Mode, action Action) {
	for key, bound := range m.bindings[mode] {
		if bound == action {
			delete(m.bindings[mode], key)
		}
	}
}

func (m *Map) rebuildPrefixes() {
	m.prefixes = map[Mode]map[string]bool{Normal: {}, Visual_: {}, Cmdline: {}}
	for mode, bindings := range m.bindings {
		for key := range bindings {
			if !isChord(key) {
				continue
			}
			for length := 1; length < len(key); length++ {
				m.prefixes[mode][key[:length]] = true
			}
		}
	}
}

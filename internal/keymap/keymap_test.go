package keymap

import (
	"testing"

	"github.com/dappermint/ratatouille/internal/config"
)

func settings(t *testing.T, contents string) *config.Settings {
	t.Helper()
	parsed, err := config.ParseSettings(contents)
	if err != nil {
		t.Fatalf("parsing settings: %v", err)
	}
	return parsed
}

func lookup(t *testing.T, m *Map, mode Mode, keys ...string) Action {
	t.Helper()
	pending := ""
	for _, key := range keys {
		action, more := m.Lookup(mode, pending, key)
		if more {
			pending += key
			continue
		}
		return action
	}
	t.Fatalf("the sequence %v never resolved, still pending %q", keys, pending)
	return None
}

// hjkl and the arrows both work in the default map, because a user who has
// never touched vi should not have to learn anything to move.
func TestDefaultMovesWithArrowsAndHJKL(t *testing.T) {
	m := Default()
	cases := map[string]Action{
		"k": Up, "up": Up,
		"j": Down, "down": Down,
		"h": Fold, "left": Fold,
		"l": Unfold, "right": Unfold,
	}
	for key, want := range cases {
		if got := lookup(t, m, Normal, key); got != want {
			t.Errorf("%q = %q, want %q", key, got, want)
		}
	}
}

func TestDefaultHasNoModes(t *testing.T) {
	if Default().Modal() {
		t.Error("the default map is modal, so a user could get into a mode with no way out")
	}
}

func TestVimIsModal(t *testing.T) {
	if !Vim().Modal() {
		t.Error("the vim map is not modal")
	}
}

// gg has to wait for the second g rather than firing on the first.
func TestChordsWaitForTheSecondKey(t *testing.T) {
	m := Vim()
	action, more := m.Lookup(Normal, "", "g")
	if !more {
		t.Fatalf("g resolved immediately to %q instead of waiting", action)
	}
	if got := lookup(t, m, Normal, "g", "g"); got != Top {
		t.Errorf("gg = %q, want %q", got, Top)
	}
}

// A named key must never be treated as the prefix of a chord, or "left" would
// swallow "l".
func TestNamedKeysAreNotChordPrefixes(t *testing.T) {
	m := Vim()
	if action, more := m.Lookup(Normal, "", "left"); more || action != Fold {
		t.Errorf("left = %q pending=%v, want fold immediately", action, more)
	}
	if action, more := m.Lookup(Normal, "", "l"); more || action != Unfold {
		t.Errorf("l = %q pending=%v, want unfold immediately", action, more)
	}
	if action, more := m.Lookup(Normal, "", "ctrl-d"); more || action != HalfPageDown {
		t.Errorf("ctrl-d = %q pending=%v", action, more)
	}
}

// Movement is declared once. Visual mode inherits it rather than repeating it,
// which is what keeps the two from drifting apart.
func TestVisualInheritsNormalMovement(t *testing.T) {
	m := Vim()
	if got := lookup(t, m, Visual_, "j"); got != Down {
		t.Errorf("j in visual = %q, want %q", got, Down)
	}
	if got := lookup(t, m, Visual_, "escape"); got != Escape {
		t.Errorf("escape in visual = %q", got)
	}
}

// Every mode needs a way out, or a user can be trapped in it.
func TestEveryModeCanBeLeft(t *testing.T) {
	m := Vim()
	for _, mode := range []Mode{Normal, Visual_, Cmdline} {
		if len(m.Keys(mode, Escape)) == 0 && len(m.Keys(mode, Quit)) == 0 {
			t.Errorf("%s mode has no escape and no quit", mode)
		}
	}
}

// A key that both completes a binding and starts a longer one resolves on the
// first press, leaving the longer binding unreachable.
func TestNoBindingIsAlsoAPrefix(t *testing.T) {
	for _, m := range []*Map{Default(), Vim()} {
		if found := m.Ambiguous(); len(found) > 0 {
			t.Errorf("%s has keys that are both a binding and a prefix: %v", m.Name(), found)
		}
	}
}

func TestPresetNames(t *testing.T) {
	for _, name := range []string{"", "default", "vim", "vi", "VIM"} {
		if _, err := Preset(name); err != nil {
			t.Errorf("Preset(%q): %v", name, err)
		}
	}
	if _, err := Preset("emacs"); err == nil {
		t.Error("an unknown preset was accepted")
	}
}

func TestLoadAppliesThePresetFromSettings(t *testing.T) {
	m, err := Load(settings(t, "keymap = vim\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Name() != "vim" {
		t.Errorf("name = %q, want vim", m.Name())
	}
	if !m.Modal() {
		t.Error("the vim preset was not applied")
	}
}

// An override replaces the preset's keys for that action. Adding to them would
// leave the old key working, which is not what rebinding means.
func TestOverrideReplacesRatherThanAdds(t *testing.T) {
	m, err := Load(settings(t, "[keys]\nmark = m\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := lookup(t, m, Normal, "m"); got != Mark {
		t.Errorf("m = %q, want %q", got, Mark)
	}
	if action, _ := m.Lookup(Normal, "", "d"); action == Mark {
		t.Error("the original key still marks after being rebound")
	}
	if keys := m.Keys(Normal, Mark); len(keys) != 1 || keys[0] != "m" {
		t.Errorf("mark is bound to %v, want only m", keys)
	}
}

func TestOverrideAcceptsSeveralKeys(t *testing.T) {
	m, err := Load(settings(t, "[keys]\nquit = q, Q, ctrl-c\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, key := range []string{"q", "Q", "ctrl-c"} {
		if got := lookup(t, m, Normal, key); got != Quit {
			t.Errorf("%q = %q, want quit", key, got)
		}
	}
}

func TestOverrideCanTargetAMode(t *testing.T) {
	m, err := Load(settings(t, "keymap = vim\n[keys.visual]\nmark = M\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := lookup(t, m, Visual_, "M"); got != Mark {
		t.Errorf("M in visual = %q, want mark", got)
	}
	// The normal-mode binding is untouched by a visual-mode override.
	if got := lookup(t, m, Normal, "d"); got != Mark {
		t.Errorf("d in normal = %q, want mark", got)
	}
}

func TestUnknownKeymapIsAnError(t *testing.T) {
	if _, err := Load(settings(t, "keymap = dvorak\n")); err == nil {
		t.Error("an unknown keymap name was accepted")
	}
}

// Every action a keymap binds needs a description, or help renders a blank.
func TestEveryBoundActionIsDescribed(t *testing.T) {
	for _, m := range []*Map{Default(), Vim()} {
		for _, mode := range []Mode{Normal, Visual_, Cmdline} {
			for _, action := range m.Actions(mode) {
				if Describe[action] == "" {
					t.Errorf("%s: %q has no description", m.Name(), action)
				}
			}
		}
	}
}

// Actions() reads from Order, so an action missing from Order is invisible in
// help and in config keys even though it works.
func TestEveryBoundActionIsInOrder(t *testing.T) {
	inOrder := make(map[Action]bool, len(Order))
	for _, action := range Order {
		inOrder[action] = true
	}
	for _, m := range []*Map{Default(), Vim()} {
		for _, mode := range []Mode{Normal, Visual_, Cmdline} {
			for key, action := range m.bindings[mode] {
				if !inOrder[action] {
					t.Errorf("%s binds %q to %q, which is not in Order and so never shown", m.Name(), key, action)
				}
			}
		}
	}
}

func TestNilMapIsSafe(t *testing.T) {
	var m *Map
	if m.Modal() {
		t.Error("a nil map reported itself modal")
	}
	if action, more := m.Lookup(Normal, "", "j"); action != None || more {
		t.Error("a nil map resolved a key")
	}
	if m.Keys(Normal, Up) != nil || m.Actions(Normal) != nil {
		t.Error("a nil map returned bindings")
	}
	if m.Name() != "none" {
		t.Errorf("name = %q", m.Name())
	}
}

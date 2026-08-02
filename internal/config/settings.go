package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const SettingsFile = "config"

// Settings is the parsed contents of ~/.config/ratatouille/config. The format is
// deliberately the smallest thing that can express what is needed: optional
// [section] headers, key = value lines, # comments. A dependency to parse a
// config file is a poor trade for a tool that otherwise has none.
type Settings struct {
	values map[string]string
	order  []string
	path   string
}

func LoadSettings(home string) (*Settings, error) {
	settings := &Settings{values: map[string]string{}, path: Path(home, SettingsFile)}
	contents, err := os.ReadFile(settings.path)
	if os.IsNotExist(err) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	return settings, settings.parse(string(contents))
}

func ParseSettings(contents string) (*Settings, error) {
	settings := &Settings{values: map[string]string{}}
	return settings, settings.parse(contents)
}

func (s *Settings) parse(contents string) error {
	section := ""
	for number, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			return fmt.Errorf("%s line %d: expected key = value, got %q", s.path, number+1, line)
		}
		key = strings.TrimSpace(key)
		if section != "" {
			key = section + "." + key
		}
		if key == "" {
			return fmt.Errorf("%s line %d: the key is empty", s.path, number+1)
		}
		if _, seen := s.values[key]; !seen {
			s.order = append(s.order, key)
		}
		s.values[key] = strings.TrimSpace(value)
	}
	return nil
}

func (s *Settings) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Keys returns the settings that were actually written, in file order, so
// `config show` can say which values came from the file and which are defaults.
func (s *Settings) Keys() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.order...)
}

func (s *Settings) Has(key string) bool {
	if s == nil {
		return false
	}
	_, ok := s.values[key]
	return ok
}

func (s *Settings) String(key, fallback string) string {
	if s == nil {
		return fallback
	}
	if value, ok := s.values[key]; ok && value != "" {
		return value
	}
	return fallback
}

// List splits a comma separated value. It is how a key gets more than one
// binding without inventing a nested syntax.
func (s *Settings) List(key string) []string {
	raw := s.String(key, "")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			values = append(values, part)
		}
	}
	return values
}

func (s *Settings) Bool(key string, fallback bool) bool {
	switch strings.ToLower(s.String(key, "")) {
	case "true", "yes", "on", "1":
		return true
	case "false", "no", "off", "0":
		return false
	default:
		return fallback
	}
}

func (s *Settings) Int(key string, fallback int) int {
	value, err := strconv.Atoi(s.String(key, ""))
	if err != nil {
		return fallback
	}
	return value
}

func (s *Settings) Duration(key string, fallback time.Duration) time.Duration {
	raw := s.String(key, "")
	if raw == "" {
		return fallback
	}
	// Accept the day and week suffixes a person actually types, which
	// time.ParseDuration does not.
	unit := raw[len(raw)-1]
	if unit == 'd' || unit == 'w' {
		count, err := strconv.Atoi(raw[:len(raw)-1])
		if err != nil {
			return fallback
		}
		span := 24 * time.Hour
		if unit == 'w' {
			span = 7 * 24 * time.Hour
		}
		return time.Duration(count) * span
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return value
}

// Preferences are the settings that are not keybindings. Each one exists
// because no single default is right for everyone, which is the bar a setting
// has to clear before it is added.
type Preferences struct {
	Keymap    string        // default or vim
	View      string        // which view opens first
	Colour    string        // auto, always, never
	Depth     int           // default tree depth for surface
	Interval  time.Duration // status sampling interval
	PurgeAge  time.Duration // how recent a project has to be to stay unselected
	TrashOnly bool          // route purge to Trash rather than removing outright
}

func LoadPreferences(home string) (Preferences, *Settings, error) {
	settings, err := LoadSettings(home)
	return PreferencesFrom(settings), settings, err
}

func PreferencesFrom(settings *Settings) Preferences {
	return Preferences{
		Keymap:    settings.String("keymap", "default"),
		View:      settings.String("view", "auto"),
		Colour:    settings.String("colour", settings.String("color", "auto")),
		Depth:     settings.Int("depth", 3),
		Interval:  settings.Duration("status.interval", 2*time.Second),
		PurgeAge:  settings.Duration("purge.min-age", 7*24*time.Hour),
		TrashOnly: settings.Bool("purge.trash", false),
	}
}

// UseColour resolves the three-way colour setting against whether the output is
// a terminal, so NO_COLOR and a pipe are both honoured without the caller
// repeating the rule.
func (p Preferences) UseColour(terminal bool) bool {
	switch strings.ToLower(p.Colour) {
	case "always":
		return true
	case "never":
		return false
	default:
		return terminal && os.Getenv("NO_COLOR") == ""
	}
}

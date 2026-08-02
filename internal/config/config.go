// Package config reads the files a user edits by hand. A whitelist can only
// ever remove something from a selection; it can never make a protected path
// cleanable.
package config

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const EnvDir = "RATATOUILLE_CONFIG_DIR"

const (
	WhitelistFile         = "whitelist"
	OptimizeWhitelistFile = "optimize-whitelist"
	PurgePathsFile        = "purge-paths"
)

func Dir(home string) string {
	if override := os.Getenv(EnvDir); override != "" {
		return override
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "ratatouille")
	}
	return filepath.Join(home, ".config", "ratatouille")
}

func Path(home, file string) string {
	return filepath.Join(Dir(home), file)
}

// Whitelist holds target ids and path patterns the user has protected.
type Whitelist struct {
	ids      map[string]bool
	patterns []string
}

func LoadWhitelist(home, file string) (*Whitelist, error) {
	list := &Whitelist{ids: make(map[string]bool)}
	lines, err := readLines(Path(home, file))
	if err != nil {
		return list, err
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "/") || strings.HasPrefix(line, "~") {
			list.patterns = append(list.patterns, expand(home, line))
			continue
		}
		list.ids[line] = true
	}
	return list, nil
}

// Blocks reports whether the user has protected this target or this path. An
// empty whitelist blocks nothing, which is the common case.
func (w *Whitelist) Blocks(id, path string) bool {
	if w == nil {
		return false
	}
	if id != "" && w.ids[id] {
		return true
	}
	if path == "" {
		return false
	}
	cleaned := filepath.Clean(path)
	for _, pattern := range w.patterns {
		if cleaned == pattern || strings.HasPrefix(cleaned, pattern+string(filepath.Separator)) {
			return true
		}
		if matched, err := filepath.Match(pattern, cleaned); err == nil && matched {
			return true
		}
	}
	return false
}

func (w *Whitelist) Empty() bool {
	return w == nil || (len(w.ids) == 0 && len(w.patterns) == 0)
}

func (w *Whitelist) Entries() []string {
	if w == nil {
		return nil
	}
	entries := make([]string, 0, len(w.ids)+len(w.patterns))
	for id := range w.ids {
		entries = append(entries, id)
	}
	entries = append(entries, w.patterns...)
	return entries
}

func PurgePaths(home string) ([]string, error) {
	lines, err := readLines(Path(home, PurgePathsFile))
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(lines))
	for _, line := range lines {
		paths = append(paths, expand(home, line))
	}
	return paths, nil
}

func expand(home, value string) string {
	if value == "~" {
		return home
	}
	if strings.HasPrefix(value, "~/") {
		return filepath.Join(home, value[2:])
	}
	return filepath.Clean(value)
}

func readLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines, scanner.Err()
}

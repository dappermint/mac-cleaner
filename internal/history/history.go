// Package history reads back the operation log. There is no undo command:
// restoring an arbitrary path set automatically is a good way to overwrite
// something newer with something older. The log says where the files went and
// the user decides.
package history

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dappermint/ratatouille/internal/safety"
)

const defaultLimit = 50

type Query struct {
	Since time.Duration
	Limit int
	Run   string
}

// Read returns the most recent entries first, across the live log and every
// rotated generation it needs to satisfy the query.
func Read(home string, query Query) ([]safety.Entry, error) {
	if query.Limit <= 0 {
		query.Limit = defaultLimit
	}
	cutoff := time.Time{}
	if query.Since > 0 {
		cutoff = time.Now().Add(-query.Since)
	}

	entries := []safety.Entry{}
	for _, path := range generations(safety.LogPath(home)) {
		found, err := readFile(path)
		if err != nil {
			return nil, err
		}
		entries = append(entries, found...)
	}

	filtered := entries[:0]
	for _, entry := range entries {
		if query.Run != "" && entry.Run != query.Run {
			continue
		}
		if !cutoff.IsZero() && entry.At.Before(cutoff) {
			continue
		}
		filtered = append(filtered, entry)
	}
	sort.SliceStable(filtered, func(a, b int) bool {
		return filtered[a].At.After(filtered[b].At)
	})
	if len(filtered) > query.Limit {
		filtered = filtered[:query.Limit]
	}
	return filtered, nil
}

func generations(path string) []string {
	paths := []string{path}
	for generation := 1; generation <= 5; generation++ {
		candidate := fmt.Sprintf("%s.%d", path, generation)
		if _, err := os.Stat(candidate); err == nil {
			paths = append(paths, candidate)
		}
	}
	return paths
}

func readFile(path string) ([]safety.Entry, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var entries []safety.Entry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry safety.Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
}

// ParseSince accepts what a person types: 30m, 24h, 7d, 2w.
func ParseSince(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	unit := value[len(value)-1]
	if unit == 'd' || unit == 'w' {
		count, err := strconv.Atoi(value[:len(value)-1])
		if err != nil {
			return 0, fmt.Errorf("cannot read %q as a duration", value)
		}
		span := 24 * time.Hour
		if unit == 'w' {
			span = 7 * 24 * time.Hour
		}
		return time.Duration(count) * span, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("cannot read %q as a duration", value)
	}
	return duration, nil
}

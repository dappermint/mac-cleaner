package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dappermint/ratatouille/internal/safety"
)

func writeLog(t *testing.T, entries ...safety.Entry) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(safety.EnvLog, filepath.Join(home, "operations.jsonl"))
	t.Setenv(safety.EnvNoLog, "")
	log := safety.OpenLog(home, nil)
	for _, entry := range entries {
		log.Record(entry)
	}
	if err := log.LastError(); err != nil {
		t.Fatalf("writing the log: %v", err)
	}
	return home
}

func TestReadReturnsNewestFirst(t *testing.T) {
	now := time.Now().UTC()
	home := writeLog(t,
		safety.Entry{At: now.Add(-2 * time.Hour), Command: "clean", Item: "old"},
		safety.Entry{At: now.Add(-1 * time.Minute), Command: "clean", Item: "new"},
	)

	entries, err := Read(home, Query{})
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Item != "new" {
		t.Errorf("the newest entry is %q, want %q", entries[0].Item, "new")
	}
}

func TestReadFiltersBySinceAndRun(t *testing.T) {
	now := time.Now().UTC()
	home := writeLog(t,
		safety.Entry{At: now.Add(-48 * time.Hour), Command: "clean", Item: "ancient"},
		safety.Entry{At: now.Add(-1 * time.Hour), Command: "clean", Item: "recent"},
	)

	entries, err := Read(home, Query{Since: 24 * time.Hour})
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if len(entries) != 1 || entries[0].Item != "recent" {
		t.Fatalf("since did not filter: %+v", entries)
	}

	entries, err = Read(home, Query{Run: "no-such-run"})
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("an unknown run returned %d entries", len(entries))
	}
}

func TestReadHonoursTheLimit(t *testing.T) {
	now := time.Now().UTC()
	seed := make([]safety.Entry, 0, 10)
	for index := range 10 {
		seed = append(seed, safety.Entry{At: now.Add(-time.Duration(index) * time.Minute), Command: "clean"})
	}
	home := writeLog(t, seed...)

	entries, err := Read(home, Query{Limit: 3})
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("got %d entries, want 3", len(entries))
	}
}

func TestReadOnAMissingLogIsNotAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv(safety.EnvLog, filepath.Join(home, "nothing-here.jsonl"))
	entries, err := Read(home, Query{})
	if err != nil {
		t.Fatalf("a missing log returned an error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a missing log returned %d entries", len(entries))
	}
}

func TestReadSkipsCorruptLines(t *testing.T) {
	home := writeLog(t, safety.Entry{At: time.Now().UTC(), Command: "clean", Item: "good"})
	path := safety.LogPath(home)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("opening the log: %v", err)
	}
	if _, err := file.WriteString("this is not json\n"); err != nil {
		t.Fatalf("appending: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	entries, err := Read(home, Query{})
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if len(entries) != 1 || entries[0].Item != "good" {
		t.Errorf("a corrupt line changed the result: %+v", entries)
	}
}

func TestParseSince(t *testing.T) {
	cases := map[string]time.Duration{
		"":    0,
		"30m": 30 * time.Minute,
		"24h": 24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
		"2w":  14 * 24 * time.Hour,
	}
	for input, want := range cases {
		got, err := ParseSince(input)
		if err != nil {
			t.Fatalf("ParseSince(%q): %v", input, err)
		}
		if got != want {
			t.Errorf("ParseSince(%q) = %s, want %s", input, got, want)
		}
	}
	if _, err := ParseSince("yesterday"); err == nil {
		t.Error("an unparseable duration was accepted")
	}
}

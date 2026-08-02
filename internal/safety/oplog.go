package safety

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dappermint/ratatouille/internal/storage"
)

const (
	logRotateBytes = 16 << 20
	logGenerations = 5
	LogFileName    = "operations.jsonl"
)

type Outcome string

const (
	OutcomeOK      Outcome = "ok"
	OutcomeFailed  Outcome = "failed"
	OutcomeRefused Outcome = "refused"
	OutcomeSkipped Outcome = "skipped"
)

type Recovery string

const (
	RecoveryTrash       Recovery = "trash"
	RecoveryRebuildable Recovery = "rebuildable"
	RecoveryPermanent   Recovery = "permanent"
	RecoveryNone        Recovery = "none"
)

type Kind string

const (
	KindTrash      Kind = "trash"
	KindRemove     Kind = "remove"
	KindEmptyTrash Kind = "empty-trash"
	KindCommand    Kind = "command"
)

type Entry struct {
	At          time.Time `json:"at"`
	Run         string    `json:"run"`
	Command     string    `json:"command"`
	Item        string    `json:"item,omitempty"`
	Kind        Kind      `json:"kind"`
	Path        string    `json:"path,omitempty"`
	Destination string    `json:"destination,omitempty"`
	Bytes       int64     `json:"bytes,omitempty"`
	Outcome     Outcome   `json:"outcome"`
	Recovery    Recovery  `json:"recoverable,omitempty"`
	DryRun      bool      `json:"dry_run,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// Log is the only diagnostic channel this tool has. It never leaves the
// machine and it never records file contents.
type Log struct {
	mutex    sync.Mutex
	path     string
	run      string
	identity *storage.CommandIdentity
	disabled bool
	lastErr  error
}

func LogPath(home string) string {
	if override := os.Getenv(EnvLog); override != "" {
		return override
	}
	return filepath.Join(home, "Library", "Logs", "ratatouille", LogFileName)
}

func OpenLog(home string, identity *storage.CommandIdentity) *Log {
	return &Log{
		path:     LogPath(home),
		run:      newRunID(),
		identity: identity,
		disabled: loggingDisabled(),
	}
}

func (l *Log) Run() string {
	if l == nil {
		return ""
	}
	return l.run
}

func (l *Log) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// LastError reports a logging failure for --debug. A log that cannot be
// written never fails the operation it was describing.
func (l *Log) LastError() error {
	if l == nil {
		return nil
	}
	l.mutex.Lock()
	defer l.mutex.Unlock()
	return l.lastErr
}

func (l *Log) Record(entry Entry) {
	if l == nil || l.disabled {
		return
	}
	if entry.At.IsZero() {
		entry.At = time.Now().UTC()
	}
	entry.Run = l.run

	l.mutex.Lock()
	defer l.mutex.Unlock()
	if err := l.append(entry); err != nil {
		l.lastErr = err
	}
}

func (l *Log) append(entry Entry) error {
	if err := l.ensureDirectory(); err != nil {
		return err
	}
	if err := l.rotate(); err != nil {
		return err
	}
	file, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	l.chown(l.path)
	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = file.Write(append(encoded, '\n'))
	return err
}

func (l *Log) ensureDirectory() error {
	directory := filepath.Dir(l.path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	l.chown(directory)
	return nil
}

// chown keeps a sudo run from leaving root-owned logs in a user's home.
func (l *Log) chown(path string) {
	if l.identity == nil {
		return
	}
	_ = os.Chown(path, int(l.identity.UID), int(l.identity.GID))
}

func (l *Log) rotate() error {
	info, err := os.Stat(l.path)
	if err != nil || info.Size() < logRotateBytes {
		return nil //nolint:nilerr // a missing log is the normal first-write case
	}
	oldest := fmt.Sprintf("%s.%d", l.path, logGenerations)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) { //nolint:forbidigo // log rotation of a path this package owns
		return err
	}
	for generation := logGenerations - 1; generation >= 1; generation-- {
		from := fmt.Sprintf("%s.%d", l.path, generation)
		to := fmt.Sprintf("%s.%d", l.path, generation+1)
		if _, err := os.Stat(from); err == nil {
			if err := os.Rename(from, to); err != nil { //nolint:forbidigo // log rotation of a path this package owns
				return err
			}
		}
	}
	return os.Rename(l.path, l.path+".1") //nolint:forbidigo // log rotation of a path this package owns
}

func newRunID() string {
	buffer := make([]byte, 4)
	if _, err := rand.Read(buffer); err != nil {
		return time.Now().UTC().Format("20060102T150405Z")
	}
	return time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(buffer)
}

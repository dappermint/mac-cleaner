package safety

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/dappermint/ratatouille/internal/storage"
)

type Request struct {
	Command string
	Item    string
	Path    string
	Bytes   int64
}

type Result struct {
	Path        string
	Destination string
	Bytes       int64
	Outcome     Outcome
	Recovery    Recovery
	DryRun      bool
	PutBack     bool
}

// Funnel is the single place a file leaves the disk. Every feature package
// holds one; nothing else may remove anything.
type Funnel struct {
	home     string
	identity *storage.CommandIdentity
	dryRun   bool
	log      *Log
}

func NewFunnel(home string, identity *storage.CommandIdentity, dryRun bool, log *Log) *Funnel {
	return &Funnel{
		home:     home,
		identity: identity,
		dryRun:   dryRun || DryRunFromEnv(),
		log:      log,
	}
}

func (f *Funnel) DryRun() bool {
	return f.dryRun
}

func (f *Funnel) Log() *Log {
	return f.log
}

func (f *Funnel) Home() string {
	return f.home
}

func (f *Funnel) Identity() *storage.CommandIdentity {
	return f.identity
}

// Trash prefers Finder, which gives a real Put Back, and falls back to a
// rename into the user's Trash, which does not. It never falls back to a
// permanent removal.
func (f *Funnel) Trash(ctx context.Context, request Request) (Result, error) {
	result := Result{Path: request.Path, Bytes: request.Bytes, Recovery: RecoveryTrash, DryRun: f.dryRun}
	if err := ValidateForDeletion(request.Path); err != nil {
		return f.finish(request, KindTrash, result, err)
	}
	if _, err := os.Lstat(request.Path); err != nil {
		result.Outcome = OutcomeSkipped
		return f.finish(request, KindTrash, result, nil)
	}
	if f.dryRun {
		return f.finish(request, KindTrash, result, nil)
	}

	if err := trashViaFinder(ctx, request.Path); err == nil {
		result.PutBack = true
		return f.finish(request, KindTrash, result, nil)
	}
	destination, err := trashViaRename(f.home, request.Path, f.identity)
	if err != nil {
		return f.finish(request, KindTrash, result, err)
	}
	result.Destination = destination
	return f.finish(request, KindTrash, result, nil)
}

// Remove is permanent. Callers reach for it only when the target is
// rebuildable or when the user asked for it in as many words.
func (f *Funnel) Remove(ctx context.Context, request Request) (Result, error) {
	result := Result{Path: request.Path, Bytes: request.Bytes, Recovery: RecoveryPermanent, DryRun: f.dryRun}
	if err := ValidateForDeletion(request.Path); err != nil {
		return f.finish(request, KindRemove, result, err)
	}
	if _, err := os.Lstat(request.Path); err != nil {
		result.Outcome = OutcomeSkipped
		return f.finish(request, KindRemove, result, nil)
	}
	if f.dryRun {
		return f.finish(request, KindRemove, result, nil)
	}
	return f.finish(request, KindRemove, result, removeWithin(ctx, request.Path))
}

// EmptyTrash removes the contents of the user's Trash, not the Trash itself.
func (f *Funnel) EmptyTrash(ctx context.Context, request Request) (Result, error) {
	trash := filepath.Join(f.home, ".Trash")
	result := Result{Path: trash, Bytes: request.Bytes, Recovery: RecoveryPermanent, DryRun: f.dryRun}
	request.Path = trash

	if err := validateTrashRoot(f.home, trash); err != nil {
		return f.finish(request, KindEmptyTrash, result, err)
	}
	entries, err := os.ReadDir(trash)
	if errors.Is(err, os.ErrNotExist) {
		result.Outcome = OutcomeSkipped
		return f.finish(request, KindEmptyTrash, result, nil)
	}
	if err != nil {
		return f.finish(request, KindEmptyTrash, result, err)
	}
	if f.dryRun {
		return f.finish(request, KindEmptyTrash, result, nil)
	}

	root, err := openDirectory(ctx, trash)
	if err != nil {
		return f.finish(request, KindEmptyTrash, result, err)
	}
	defer func() { _ = root.Close() }()
	for _, entry := range entries {
		if err := root.RemoveAll(entry.Name()); err != nil {
			return f.finish(request, KindEmptyTrash, result, err)
		}
	}
	return f.finish(request, KindEmptyTrash, result, nil)
}

// RecordCommand logs a cleanup that a tool performed through its own command,
// where no path passed through this funnel.
func (f *Funnel) RecordCommand(request Request, display string, err error) {
	entry := Entry{
		Command:  request.Command,
		Item:     request.Item,
		Kind:     KindCommand,
		Path:     display,
		Bytes:    request.Bytes,
		Outcome:  OutcomeOK,
		Recovery: RecoveryRebuildable,
		DryRun:   f.dryRun,
	}
	if err != nil {
		entry.Outcome = OutcomeFailed
		entry.Error = storage.CompactError(err)
	}
	f.log.Record(entry)
}

func (f *Funnel) finish(request Request, kind Kind, result Result, err error) (Result, error) {
	if result.Outcome == "" {
		result.Outcome = OutcomeOK
	}
	entry := Entry{
		Command:     request.Command,
		Item:        request.Item,
		Kind:        kind,
		Path:        result.Path,
		Destination: result.Destination,
		Bytes:       result.Bytes,
		Outcome:     result.Outcome,
		Recovery:    result.Recovery,
		DryRun:      result.DryRun,
	}
	if err != nil {
		entry.Outcome = OutcomeFailed
		if Refused(err) {
			entry.Outcome = OutcomeRefused
		}
		entry.Error = storage.CompactError(err)
		result.Outcome = entry.Outcome
	}
	f.log.Record(entry)
	return result, err
}

// removeWithin never resolves the target by name again. It opens the parent
// once, checks that the directory it got is the one validation saw, and
// removes the leaf relative to that handle, so nothing can be swapped
// underneath the removal.
func removeWithin(ctx context.Context, path string) error {
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return refuse(path, "there is no leaf to remove")
	}
	root, err := openParent(ctx, path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.RemoveAll(base)
}

func openParent(ctx context.Context, path string) (*os.Root, error) {
	return openDirectory(ctx, filepath.Dir(path))
}

func openDirectory(ctx context.Context, directory string) (*os.Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	before, err := os.Stat(directory)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	if !sameFile(before, after) {
		_ = root.Close()
		return nil, refuse(directory, "the directory changed between the check and the removal")
	}
	return root, nil
}

func sameFile(left, right os.FileInfo) bool {
	leftStat, leftOK := left.Sys().(*syscall.Stat_t)
	rightStat, rightOK := right.Sys().(*syscall.Stat_t)
	if !leftOK || !rightOK {
		return os.SameFile(left, right)
	}
	return leftStat.Dev == rightStat.Dev && leftStat.Ino == rightStat.Ino
}

func validateTrashRoot(home, trash string) error {
	if home == "" || len(components(home)) < 2 {
		return refuse(home, "this is not a plausible home directory")
	}
	if trash != filepath.Join(home, ".Trash") {
		return refuse(trash, "this is not the Trash of the invoking user")
	}
	info, err := os.Lstat(trash)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return refuse(trash, "the Trash path is not a directory")
	}
	return nil
}

// renameInto performs the move relative to a handle on the home directory, so
// neither side of the rename can be redirected by a symlink that appears after
// validation.
func renameInto(home, source, destination string) error {
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	from, err := filepath.Rel(home, source)
	if err != nil {
		return err
	}
	to, err := filepath.Rel(home, destination)
	if err != nil {
		return err
	}
	if err := root.Rename(from, to); err != nil {
		return fmt.Errorf("moving %s to Trash: %w", source, err)
	}
	return nil
}

func isCrossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

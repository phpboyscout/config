package config

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
)

// File modes for staged and committed configuration.
//
// Configuration routinely holds credentials, so a file this module creates is
// owner-only rather than inheriting a permissive default. A file that already
// exists keeps whatever mode its owner chose — that is their decision, not
// ours.
const (
	configFileMode = 0o600
	configDirMode  = 0o750
)

// Write errors.
var (
	// ErrConflict is returned when a source changed between being read and
	// being written, so committing would silently discard someone else's work.
	ErrConflict = errors.New("config: source changed since it was read")

	// ErrNotWritable is returned when a change is routed at a backend that
	// cannot persist.
	ErrNotWritable = errors.New("config: backend is not writable")

	// ErrPartialCommit is returned when a multi-source commit fails partway
	// and could not be fully rolled back. It always names what is in which
	// state: a caller must never be left guessing.
	ErrPartialCommit = errors.New("config: commit partially applied")
)

// asEdit renders a change as the edit a backend applies, for the internal
// bookkeeping that needs one directly.
func (c Change) asEdit() Edit {
	return Edit{Path: c.Path, Value: c.Value, Remove: c.Remove}
}

// Edit is one change addressed at a specific document of a backend.
type Edit struct {
	// Document is the document index within the source.
	Document int
	Path     string
	Value    any
	Remove   bool
}

// WritableBackend is a backend that can persist changes.
//
// It is separate from Backend because reading and writing are genuinely
// different problems. A backend that can be read is not thereby able to
// handle atomicity, conflict detection or rollback, and pretending otherwise
// would make every caller check capabilities at runtime instead of the type
// system checking them once.
type WritableBackend interface {
	Backend

	// Prepare stages edits without making them visible. It must not modify
	// the source: everything it does has to be abandonable.
	Prepare(ctx context.Context, edits []Edit) (Pending, error)
}

// Pending is staged work that has not yet been made visible.
//
// The three-phase shape — prepare, verify, commit — exists so that the
// expensive and failure-prone part happens while nothing is visible, and the
// window in which a partially applied set could be observed is as short as the
// backend can make it.
type Pending interface {
	// Layers is what the backend will contribute once committed. This is what
	// lets the Store build the next snapshot directly from the content it just
	// wrote, rather than re-reading and hoping it gets the same answer.
	Layers() []Layer

	// Verify reports whether the source is still as it was when Prepare ran.
	Verify(ctx context.Context) error

	// Commit makes the staged content visible.
	Commit(ctx context.Context) error

	// Rollback restores the source to its pre-commit state. Best effort: it is
	// called when a later commit in the same batch failed.
	Rollback(ctx context.Context) error

	// Discard abandons staged work that was never committed.
	Discard(ctx context.Context) error
}

// filePending is staged content for one file.
type filePending struct {
	backend *codecBackend
	// original is the file's content when Prepare ran, retained so a commit
	// that fails partway through a batch can be undone.
	original []byte
	// existed records whether the file was there when this write was prepared,
	// so a rollback of a newly created file removes it rather than restoring an
	// empty one.
	existed bool
	// loadedExist records whether the file was there when the backend last
	// read it, which is what the fingerprint describes. A file absent at load
	// and created by someone else before this write has the two disagree, and
	// pairing the load-time hash with prepare-time existence would leave the
	// backend rejecting every later write.
	loadedExist bool
	// fingerprint is a hash of the original content. Comparing content rather
	// than modification time avoids depending on a filesystem's timestamp
	// granularity, which varies and is coarse on some in-memory
	// implementations.
	fingerprint [32]byte
	staged      []byte
	layers      []Layer
	// target is the path actually written: the backend's path, or whatever it
	// points at when it is a symlink.
	target   string
	tempPath string
}

func (p *filePending) Layers() []Layer { return p.layers }

// Verify re-reads the source and compares it with what Prepare saw.
//
// This is the defence against a change landing between routing and commit —
// another process, or a human with the file open. It cannot prevent the race,
// only detect it, which is the most that is portably achievable without lock
// files.
func (p *filePending) Verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	current, _, err := p.backend.read()
	if err != nil {
		return err
	}

	if sha256.Sum256(current) != p.fingerprint {
		return fmt.Errorf("%w: %s", ErrConflict, p.backend.path)
	}

	return nil
}

// Commit writes the staged content and moves it into place.
//
// Writing to a temporary file in the same directory and renaming means a
// reader never observes a half-written configuration, and a crash leaves
// either the old file or the new one rather than a truncated hybrid.
func (p *filePending) Commit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := p.writeTemp(); err != nil {
		return err
	}

	if err := p.backend.fs.Rename(p.tempPath, p.target); err != nil {
		_ = p.backend.fs.Remove(p.tempPath)

		return fmt.Errorf("config: committing %s: %w", p.target, err)
	}

	// The content just written is now what this backend knows the file to hold.
	// Advancing the fingerprint here is what distinguishes our own writes from
	// foreign ones: the Store never re-reads after committing — it builds the
	// next snapshot from the staged layers — so nothing else would ever move it
	// on, and the second write to any file would be reported as a conflict with
	// the first.
	p.backend.loaded = sha256.Sum256(p.staged)
	p.backend.loadedExist = true

	return nil
}

func (p *filePending) writeTemp() error {
	if dir := filepath.Dir(p.target); dir != "" && dir != "." {
		if err := p.backend.fs.MkdirAll(dir, configDirMode); err != nil {
			return fmt.Errorf("config: preparing %s: %w", dir, err)
		}
	}

	if err := p.backend.fs.WriteFile(p.tempPath, p.staged, p.mode()); err != nil {
		return fmt.Errorf("config: staging %s: %w", p.backend.path, err)
	}

	return nil
}

// mode is the permission set the committed file should carry.
//
// A file this module creates is owner-only, because configuration routinely
// holds credentials and inheriting a permissive default would leak them. A file
// that already exists keeps whatever mode its owner chose — that is their
// decision, not ours, and staging through a temporary file must not quietly
// tighten it.
func (p *filePending) mode() fs.FileMode {
	if !p.existed {
		return configFileMode
	}

	info, err := p.backend.fs.Stat(p.target)
	if err != nil {
		return configFileMode
	}

	return info.Mode().Perm()
}

// Rollback restores what was there before the commit.
//
// Both halves of "before" matter. Commit advances the backend's record of what
// the file holds, and the Store never re-reads after committing, so restoring
// the bytes while leaving that record pointing at the rolled-back content would
// leave the backend permanently unusable: every later write would be rejected
// as a conflict with a change nobody made.
func (p *filePending) Rollback(_ context.Context) error {
	defer p.restoreFingerprint()

	if !p.existed {
		// The file is ours; removing it returns the world to how it was.
		if err := p.backend.fs.Remove(p.target); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("config: rolling back %s: %w", p.backend.path, err)
		}

		return nil
	}

	if err := p.backend.fs.WriteFile(p.target, p.original, p.mode()); err != nil {
		return fmt.Errorf("config: rolling back %s: %w", p.backend.path, err)
	}

	return nil
}

// restoreFingerprint returns the backend's record of the file to what it was
// when this write was prepared.
//
// Deferred rather than conditional: if the restore write failed, the file's
// state is unknown, and the fingerprint from prepare is still the better of the
// two answers. It describes what was last known to be there, so a genuine
// foreign change is still caught, where a fingerprint describing content that
// was never successfully written would reject everything.
func (p *filePending) restoreFingerprint() {
	p.backend.loaded = p.fingerprint
	p.backend.loadedExist = p.loadedExist
}

func (p *filePending) Discard(_ context.Context) error {
	if err := p.backend.fs.Remove(p.tempPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("config: discarding staged %s: %w", p.backend.path, err)
	}

	return nil
}

// stagingCounter distinguishes concurrent staging files within one process.
var stagingCounter atomic.Uint64

// stagingPath returns a staging path unique to this write.
//
// A fixed name derived from the target let two writers stage over each other:
// the second's content lands in the first's temporary file, and the first
// renames it into place believing it wrote its own. The conflict check cannot
// see that, because it inspects the target rather than the staging file.
func stagingPath(target string) string {
	return fmt.Sprintf("%s.%d-%d.yamldoc-tmp", target, os.Getpid(), stagingCounter.Add(1))
}

// resolveTarget follows a symlink to the file it points at.
//
// Committing is a rename over the target path, and rename replaces the link
// itself rather than writing through it. A configuration file managed by a
// dotfile tool is routinely a symlink into a tracked repository, so writing to
// one used to silently replace the link with a regular file and leave the real
// file untouched — the user's change absent from the repository they keep it
// in, and the link they rely on gone.
func resolveTarget(filesystem FS, path string) string {
	reader, ok := filesystem.(LinkReader)
	if !ok {
		return path
	}

	seen := map[string]bool{}
	current := path

	for range 16 {
		if seen[current] {
			return path
		}

		seen[current] = true

		dest, err := reader.Readlink(current)
		if err != nil {
			return current
		}

		if !filepath.IsAbs(dest) {
			dest = filepath.Join(filepath.Dir(current), dest)
		}

		current = dest
	}

	return current
}

package config

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/spf13/afero"
	"gitlab.com/phpboyscout/go/yamldoc"
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
	backend *fileBackend
	// original is the file's content when Prepare ran, retained so a commit
	// that fails partway through a batch can be undone.
	original []byte
	// existed records whether the file was there at all, so a rollback of a
	// newly created file removes it rather than restoring an empty one.
	existed bool
	// fingerprint is a hash of the original content. Comparing content rather
	// than modification time avoids depending on a filesystem's timestamp
	// granularity, which varies and is coarse on some in-memory
	// implementations.
	fingerprint [32]byte
	staged      []byte
	layers      []Layer
	tempPath    string
}

// Prepare applies edits to the file's documents and stages the result.
func (b *fileBackend) Prepare(ctx context.Context, edits []Edit) (Pending, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	original, existed, err := b.read()
	if err != nil {
		return nil, err
	}

	// The comparison point is what was read at load, so a change that landed
	// between then and now is caught. Fingerprinting here instead would
	// compare the current file with itself and always agree.
	baseline := b.loaded
	if !b.loadedExist {
		baseline = sha256.Sum256(nil)
	}

	staged, err := applyEdits(b.path, original, edits)
	if err != nil {
		return nil, err
	}

	// Decode what was written rather than what was intended. If the two
	// differ, the difference is a bug in this module, and it should surface
	// here rather than as a snapshot that disagrees with the file on disk.
	layers, err := decodeDocuments(b.path, staged)
	if err != nil {
		return nil, fmt.Errorf("%w: staged content for %s does not decode: %w",
			ErrBackendParse, b.path, err)
	}

	return &filePending{
		backend:     b,
		original:    original,
		existed:     existed,
		fingerprint: baseline,
		staged:      staged,
		layers:      layers,
		tempPath:    b.path + ".yamldoc-tmp",
	}, nil
}

func (b *fileBackend) read() (content []byte, existed bool, err error) {
	src, err := afero.ReadFile(b.fs, b.path)
	if err == nil {
		return src, true, nil
	}

	if errors.Is(err, fs.ErrNotExist) {
		// A file that does not exist yet is a legitimate target: creating it
		// is how a new overlay comes into being.
		return nil, false, nil
	}

	return nil, false, fmt.Errorf("config: reading %s: %w", b.path, err)
}

// applyEdits edits the document tree and re-emits it.
//
// Editing goes through yamldoc so comments, key order, quoting and block
// styles survive. Nothing here decodes values from that tree: the
// documents-versus-values boundary is what keeps the two YAML parsers from
// disagreeing about types.
func applyEdits(path string, original []byte, edits []Edit) ([]byte, error) {
	source := original
	if len(source) == 0 {
		// An absent or empty file still needs a document to edit into.
		source = []byte("{}\n")
	}

	doc, err := yamldoc.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrBackendParse, path, err)
	}

	if unsupported := doc.Unsupported(); len(unsupported) > 0 {
		return nil, fmt.Errorf("%w: %s: %s", ErrBackendUnsafe, path, unsupported[0])
	}

	docs := doc.Documents()

	for _, edit := range edits {
		if err := applyOne(path, docs, edit); err != nil {
			return nil, err
		}
	}

	out, err := doc.Bytes()
	if err != nil {
		return nil, fmt.Errorf("config: rendering %s: %w", path, err)
	}

	return out, nil
}

// applyOne applies a single edit to the document it addresses.
func applyOne(path string, docs []*yamldoc.Document, edit Edit) error {
	if edit.Document >= len(docs) {
		return fmt.Errorf("%w: %s has %d document(s), edit addressed document %d",
			ErrInternal, path, len(docs), edit.Document)
	}

	target := docs[edit.Document]

	if !edit.Remove {
		if err := target.Set(edit.Path, edit.Value); err != nil {
			return fmt.Errorf("config: setting %s in %s: %w", edit.Path, path, err)
		}

		return nil
	}

	if err := target.Remove(edit.Path); err != nil {
		// Removing something already absent reaches the desired end state, so
		// it is not worth failing a batch over.
		if errors.Is(err, yamldoc.ErrNotFound) {
			return nil
		}

		return fmt.Errorf("config: removing %s from %s: %w", edit.Path, path, err)
	}

	return nil
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

	if err := p.backend.fs.Rename(p.tempPath, p.backend.path); err != nil {
		_ = p.backend.fs.Remove(p.tempPath)

		return fmt.Errorf("config: committing %s: %w", p.backend.path, err)
	}

	return nil
}

func (p *filePending) writeTemp() error {
	if dir := filepath.Dir(p.backend.path); dir != "" && dir != "." {
		if err := p.backend.fs.MkdirAll(dir, configDirMode); err != nil {
			return fmt.Errorf("config: preparing %s: %w", dir, err)
		}
	}

	if err := afero.WriteFile(p.backend.fs, p.tempPath, p.staged, configFileMode); err != nil {
		return fmt.Errorf("config: staging %s: %w", p.backend.path, err)
	}

	return nil
}

// Rollback restores what was there before the commit.
func (p *filePending) Rollback(_ context.Context) error {
	if !p.existed {
		// The file is ours; removing it returns the world to how it was.
		if err := p.backend.fs.Remove(p.backend.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("config: rolling back %s: %w", p.backend.path, err)
		}

		return nil
	}

	if err := afero.WriteFile(p.backend.fs, p.backend.path, p.original, configFileMode); err != nil {
		return fmt.Errorf("config: rolling back %s: %w", p.backend.path, err)
	}

	return nil
}

func (p *filePending) Discard(_ context.Context) error {
	if err := p.backend.fs.Remove(p.tempPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("config: discarding staged %s: %w", p.backend.path, err)
	}

	return nil
}

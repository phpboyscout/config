package config

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"time"
)

// Codec turns a source's bytes into layer values.
//
// It is the format-specific half of a file-backed backend: everything else —
// reading the file, translating fs.ErrNotExist, fingerprinting for conflict
// detection, staging, atomic rename, mode preservation, symlink resolution,
// rollback and watching — is the same whatever the format, and lives in the
// backend. A codec knows only how to decode bytes to values, and (if it can) how
// to edit those bytes in place.
type Codec interface {
	// Decode returns one map per document in the source. A format with no
	// multi-document concept returns exactly one. A document that is empty is a
	// nil entry rather than an omission, so a later document keeps its index and
	// its provenance.
	Decode(path string, src []byte) ([]map[string]any, error)
}

// EditingCodec is a Codec that can also edit a document in place.
//
// A format that cannot be safely round-tripped simply does not implement this,
// and the backend built from it is not a write target: routing skips it and a
// write lands in the next writable layer down, reported as shadowed rather than
// failing. The type system checks the capability once, the same way
// [WritableBackend] splits from [Backend], so a codec cannot claim an ability it
// does not have.
type EditingCodec interface {
	Codec

	// Check reports whether this content can be round-tripped safely. It is
	// called at load, so a source that cannot be edited is refused before the
	// user has made any edits to lose.
	Check(path string, src []byte) error

	// Apply edits the source, preserving whatever the format can preserve.
	Apply(path string, src []byte, edits []Edit) ([]byte, error)

	// Empty returns the content of a new, empty document, for creating a file
	// that does not exist yet.
	Empty() []byte
}

// NewCodecBackend returns a backend reading a single file through a codec.
//
// It returns a write target only when the codec can edit: the concrete type
// implements [WritableBackend] exactly when the codec implements [EditingCodec],
// so routing and Plan agree with the type system instead of a backend
// advertising a capability at the type level and failing at call time. A file on
// a filesystem is always watchable, so both shapes implement [WatchableBackend].
func NewCodecBackend(filesystem FS, path string, codec Codec) Backend {
	base := &codecBackend{fs: filesystem, path: path, codec: codec}

	if edit, ok := codec.(EditingCodec); ok {
		return &editingBackend{codecBackend: base, edit: edit}
	}

	return base
}

// codecBackend reads configuration from a single file, decoding it through a
// codec.
//
// The file machinery here has nothing to do with any particular format: the
// codec owns decoding and editing, and this owns everything a file needs whoever
// wrote it. It is the read-only shape; [editingBackend] adds the write path when
// the codec can edit.
type codecBackend struct {
	// onWatchError, when set, is told when watching this file degrades and the
	// fallback cannot be established either.
	onWatchError func(error)

	fs    FS
	path  string
	codec Codec

	// loaded is a hash of the content this backend last read, and whether it
	// read anything at all.
	//
	// Conflict detection compares against what was read at load, not at the
	// start of the write. Routing decisions were made against the loaded
	// content, so a change that landed since then invalidates them — taking
	// the fingerprint at write time would compare the intruder's file with
	// itself and find nothing wrong.
	//
	// Access is serialised by the Store, which is the only caller.
	loaded      [32]byte
	loadedExist bool
}

func (b *codecBackend) ID() string { return b.path }

func (b *codecBackend) Capabilities() Capabilities {
	return Capabilities{
		// A file backend preserves comments only if its codec does; the format
		// decides, not the backend.
		PreservesComments: preservesComments(b.codec),
		AtomicMultiKey:    true, // a file is replaced in one rename
		NativeWatch:       false,
		Sensitive:         false,
	}
}

func (b *codecBackend) Load(ctx context.Context, _ []Layer) ([]Layer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	src, err := b.fs.ReadFile(b.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// The record of what this file held has to go with the file. A
			// source deleted while the process runs is a legitimate state — the
			// Store tolerates it for an optional source — but leaving the write
			// fingerprint describing content that is no longer there makes
			// every later write to that path fail as a conflict with a change
			// nobody made, permanently, including the write that would recreate
			// it.
			b.loaded = [32]byte{}
			b.loadedExist = false

			return nil, fmt.Errorf("%s: %w", b.path, fs.ErrNotExist)
		}

		return nil, fmt.Errorf("config: reading %s: %w", b.path, err)
	}

	// Refuse a document that cannot be safely round-tripped, at load rather
	// than at write. Discovering it at write means the user has already made
	// their edits, and the failure then looks arbitrary. Only a codec that can
	// edit has anything to refuse — a read-only source is always readable.
	if edit, ok := b.codec.(EditingCodec); ok {
		if err := edit.Check(b.path, src); err != nil {
			return nil, err
		}
	}

	docs, err := b.codec.Decode(b.path, src)
	if err != nil {
		return nil, err
	}

	b.loaded = sha256.Sum256(src)
	b.loadedExist = true

	return b.layers(docs), nil
}

// layers turns decoded documents into layers, one per document.
//
// A document is a layer. That is what lets routing, precedence and provenance
// treat documents and files uniformly rather than needing a second dimension.
// An empty document contributes no layer but still spends its index, so the
// documents that follow it keep the identity provenance reports.
//
// A layer is writable exactly when the codec can edit, so a read-only format is
// never offered as a write target.
func (b *codecBackend) layers(docs []map[string]any) []Layer {
	_, writable := b.codec.(EditingCodec)

	var layers []Layer

	for index, values := range docs {
		if values == nil {
			continue
		}

		layers = append(layers, Layer{
			Source: Source{
				Kind:     SourceFile,
				Name:     b.path,
				Document: index,
				Writable: writable,
			},
			Values: values,
		})
	}

	return layers
}

func (b *codecBackend) read() (content []byte, existed bool, err error) {
	src, err := b.fs.ReadFile(b.path)
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

// Watch reports changes to this backend's file.
//
// The backend owns the knowledge that its source is a path on a filesystem,
// which is what keeps that knowledge out of the Store.
func (b *codecBackend) Watch(
	ctx context.Context,
	interval time.Duration,
	onChange func(),
) (func(), error) {
	w := NewWatcher(b.fs, interval)
	if fs, ok := w.(*fsnotifyWatcher); ok {
		fs.onError = b.onWatchError
	}

	return w.Watch(ctx, []string{b.path}, onChange)
}

// watchErrorReporter is a backend that can be told where to send the errors
// raised when watching one of its sources degrades. The Store asks by interface
// rather than concrete type, so a consumer's own backend can opt in the same
// way a file backend does.
type watchErrorReporter interface {
	setWatchErrorHandler(func(error))
}

// watchPathReporter is a backend reading a filesystem path, which an injected
// watcher needs named up front. A backend with no filesystem path — an
// in-memory source — does not implement it and contributes no path.
type watchPathReporter interface {
	watchPath() (string, bool)
}

// setWatchErrorHandler routes this backend's watch-degradation errors, so the
// Store can wire them to its notifier without reaching into a concrete type.
func (b *codecBackend) setWatchErrorHandler(fn func(error)) { b.onWatchError = fn }

// watchPath reports the filesystem path this backend reads, for an injected
// watcher that expects the set of paths up front.
func (b *codecBackend) watchPath() (string, bool) { return b.path, true }

// editingBackend is a codecBackend whose codec can edit, so it can persist a
// write. It is a distinct type rather than a flag, so [WritableBackend] is
// satisfied at the type level exactly when the codec supports it.
type editingBackend struct {
	*codecBackend

	// edit is the same value as codecBackend.codec, typed for editing so the
	// write path does not repeat an assertion that construction already
	// guarantees.
	edit EditingCodec
}

// Prepare applies edits to the file's documents and stages the result.
func (b *editingBackend) Prepare(ctx context.Context, edits []Edit) (Pending, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	original, existed, err := b.read()
	if err != nil {
		return nil, err
	}

	target := resolveTarget(b.fs, b.path)

	// The comparison point is what was read at load, so a change that landed
	// between then and now is caught. Fingerprinting here instead would
	// compare the current file with itself and always agree.
	baseline := b.loaded
	if !b.loadedExist {
		baseline = sha256.Sum256(nil)
	}

	staged, err := b.edit.Apply(b.path, original, edits)
	if err != nil {
		return nil, err
	}

	// Decode what was written rather than what was intended. If the two
	// differ, the difference is a bug in this module, and it should surface
	// here rather than as a snapshot that disagrees with the file on disk.
	docs, err := b.edit.Decode(b.path, staged)
	if err != nil {
		return nil, fmt.Errorf("%w: staged content for %s does not decode: %w",
			ErrBackendParse, b.path, err)
	}

	return &filePending{
		backend:     b.codecBackend,
		original:    original,
		existed:     existed,
		fingerprint: baseline,
		loadedExist: b.loadedExist,
		staged:      staged,
		layers:      b.layers(docs),
		target:      target,
		tempPath:    stagingPath(target),
	}, nil
}

// preservesComments reports whether a codec retains comments and formatting
// through an edit.
//
// It is an optional capability: a codec that preserves comments says so, and one
// that does not — or cannot, having no comments to lose — simply does not, and
// reads as false. Kept unexported until a format with comments meets a codec
// that cannot preserve them, which is the point the distinction starts to matter
// (see D8 of the format-adapters spec).
func preservesComments(c Codec) bool {
	cp, ok := c.(interface{ preservesComments() bool })

	return ok && cp.preservesComments()
}

package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/spf13/afero"
	"gitlab.com/phpboyscout/go/yamldoc"
	yaml "go.yaml.in/yaml/v3"
)

// Backend errors. Callers should branch on these with errors.Is.
var (
	// ErrBackendUnsafe is returned when a source contains a construct that
	// cannot be safely round-tripped, so editing it would risk corruption.
	ErrBackendUnsafe = errors.New("config: source cannot be safely edited")

	// ErrBackendParse is returned when a source is not valid for its format.
	ErrBackendParse = errors.New("config: source could not be parsed")

	// ErrInternal is returned when an invariant of this module does not hold.
	// It is never the caller's fault and is always worth reporting.
	ErrInternal = errors.New("config: internal invariant violated")
)

// Backend is a source of configuration layers.
//
// Read and write are deliberately separate interfaces. Reading is fetch, parse,
// normalise; writing carries ownership, atomicity, conflict detection, secret
// handling and failure modes that differ enormously between a local file and a
// remote parameter store. One interface pretending they are the same would
// either lie about those differences or degrade to the weakest member.
type Backend interface {
	// ID identifies the backend for diagnostics and provenance.
	ID() string

	// Load returns the layers this backend contributes, in precedence order.
	// A backend may contribute more than one — a multi-document file
	// contributes one layer per document.
	//
	// A source that does not exist returns fs.ErrNotExist, so the caller can
	// decide whether that is fatal: a base file usually is, an optional
	// overlay usually is not.
	Load(ctx context.Context) ([]Layer, error)

	// Capabilities describes what this backend can do, so callers can adapt
	// rather than discover limitations by hitting them.
	Capabilities() Capabilities
}

// Capabilities describes what a backend supports.
//
// Declaring them is what lets a heterogeneous set of backends coexist without
// the weakest one setting the contract for all of them.
type Capabilities struct {
	// Writable reports whether this backend can persist changes at all.
	Writable bool
	// PreservesComments reports whether an edit retains comments and
	// formatting. True for document-like sources; meaningless for key-value
	// stores, which have nowhere to put a comment.
	PreservesComments bool
	// AtomicMultiKey reports whether several keys can be written as one
	// indivisible operation.
	AtomicMultiKey bool
	// NativeWatch reports whether the backend can notify of foreign changes,
	// as opposed to needing to be polled.
	NativeWatch bool
	// Sensitive marks a backend as holding secret material. A value sourced
	// from one must never be written into a layer that is not also sensitive.
	Sensitive bool
}

// fileBackend reads YAML configuration from a single file.
//
// It reads the file twice, deliberately. Values are decoded by the YAML value
// parser, while document structure — comments, positions, and whether the file
// can be edited safely at all — comes from yamldoc. The two disagree about
// scalar types (`8080` decodes as int in one and uint64 in the other, and large
// integers survive in one and are destroyed in the other), so the boundary
// between documents and values must not be crossed. Values never come from
// yamldoc; documents never come from the value parser.
type fileBackend struct {
	fs   afero.Fs
	path string

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

// NewFileBackend returns a backend reading YAML from a path on the given
// filesystem.
func NewFileBackend(filesystem afero.Fs, path string) Backend {
	return &fileBackend{fs: filesystem, path: path}
}

func (b *fileBackend) ID() string { return b.path }

func (b *fileBackend) Capabilities() Capabilities {
	return Capabilities{
		Writable:          true,
		PreservesComments: true,
		AtomicMultiKey:    true, // a file is replaced in one rename
		NativeWatch:       false,
		Sensitive:         false,
	}
}

func (b *fileBackend) Load(ctx context.Context) ([]Layer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	src, err := afero.ReadFile(b.fs, b.path)
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
	// their edits, and the failure then looks arbitrary.
	if err := checkEditable(b.path, src); err != nil {
		return nil, err
	}

	layers, err := decodeDocuments(b.path, src)
	if err != nil {
		return nil, err
	}

	b.loaded = sha256.Sum256(src)
	b.loadedExist = true

	return layers, nil
}

// checkEditable reports whether a source can be edited without risking
// corruption.
//
// The judgement is this module's; the detection is yamldoc's. It reports what
// it cannot round-trip safely, and refusing is the policy applied to that
// report.
func checkEditable(path string, src []byte) error {
	doc, err := yamldoc.Parse(src)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrBackendParse, path, err)
	}

	if unsupported := doc.Unsupported(); len(unsupported) > 0 {
		return fmt.Errorf("%w: %s: %s", ErrBackendUnsafe, path, unsupported[0])
	}

	return nil
}

// decodeDocuments decodes every YAML document in a file into its own layer.
//
// A document is a layer. That is what lets routing, precedence and provenance
// treat documents and files uniformly rather than needing a second dimension —
// and it fixes a defect in the incumbent, which reads the first document of a
// multi-document file and silently discards the rest.
func decodeDocuments(path string, src []byte) ([]Layer, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))

	var layers []Layer

	for index := 0; ; index++ {
		var values map[string]any

		err := dec.Decode(&values)
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("%w: %s document %d: %w", ErrBackendParse, path, index, err)
		}

		if values == nil {
			// An empty document contributes nothing, but still occupies an
			// index so later documents keep their identity.
			continue
		}

		layers = append(layers, Layer{
			Source: Source{
				Kind:     SourceFile,
				Name:     path,
				Document: index,
				Writable: true,
			},
			Values: values,
		})
	}

	return layers, nil
}

// readerBackend contributes configuration from an in-memory source.
//
// This is how compiled-in defaults reach the configuration: an embedded asset
// is read once at startup and contributes a layer like any other, so it takes
// part in precedence and provenance rather than being a special case.
type readerBackend struct {
	name    string
	content []byte
}

// NewReaderBackend returns a backend contributing YAML from bytes.
//
// The name appears in provenance, so give it something a user would recognise
// — "embedded:defaults.yaml" rather than "reader1".
func NewReaderBackend(name string, content []byte) Backend {
	return &readerBackend{name: name, content: content}
}

func (b *readerBackend) ID() string { return b.name }

// Capabilities reports an in-memory source as readable but not writable:
// there is nowhere for a write to persist to.
func (b *readerBackend) Capabilities() Capabilities {
	return Capabilities{Writable: false}
}

func (b *readerBackend) Load(ctx context.Context) ([]Layer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	layers, err := decodeDocuments(b.name, b.content)
	if err != nil {
		return nil, err
	}

	// An in-memory source cannot be written back to, so its layers must say so
	// or routing would offer it as a target.
	for i := range layers {
		layers[i].Source.Writable = false
	}

	return layers, nil
}

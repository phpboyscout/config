package config

import (
	"context"
	"time"

	"gitlab.com/phpboyscout/go/errors"
)

// Backend errors. Callers should branch on these with errors.Is.
var (
	// ErrBackendUnsafe is returned when a source contains a construct that
	// cannot be safely round-tripped, so editing it would risk corruption.
	ErrBackendUnsafe = errors.NewSentinel("config.backend_unsafe", "config: source cannot be safely edited")

	// ErrBackendParse is returned when a source is not valid for its format.
	ErrBackendParse = errors.NewSentinel("config.backend_parse", "config: source could not be parsed")

	// ErrInternal is returned when an invariant of this module does not hold.
	// It is never the caller's fault and is always worth reporting.
	ErrInternal = errors.NewSentinel("config.internal", "config: internal invariant violated")
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
	//
	// For a [WritableBackend] it is also how the Store finds this backend
	// again when routing a write, by matching a layer's Source.Name against
	// it. Those two must therefore agree: a backend whose Load reports layers
	// named something other than what ID returns cannot receive writes.
	ID() string

	// Load returns the layers this backend contributes, in precedence order.
	// A backend may contribute more than one — a multi-document file
	// contributes one layer per document.
	//
	// A source that does not exist returns fs.ErrNotExist, so the caller can
	// decide whether that is fatal: a base file usually is, an optional
	// overlay usually is not.
	// Load reads this backend's sources into layers.
	//
	// below is everything the lower-precedence backends contributed, in order.
	// Most backends ignore it; a backend whose reading of its own input depends
	// on what is already defined needs it, and receiving it as an argument is
	// what stops that being a separate call someone can forget to make. The
	// environment backend is the case: mapping APP_SERVER_PORT back to a dotted
	// key is ambiguous without knowing whether server.port already exists.
	Load(ctx context.Context, below []Layer) ([]Layer, error)

	// Capabilities describes what this backend can do, so callers can adapt
	// rather than discover limitations by hitting them.
	Capabilities() Capabilities
}

// WatchableBackend is a backend that can report when its own sources change.
//
// Separate from Backend for the same reason writing is: being readable does not
// make a source watchable, and only the backend knows how to notice a change to
// whatever it reads — a file has a path on a filesystem, a remote store has a
// subscription — and the Store should not have to know which.
//
// The Store's job is coordination: it asks each backend that can watch to say
// when something moved, then decides for itself whether the resolved
// configuration actually changed.
type WatchableBackend interface {
	Backend

	// Watch calls onChange when this backend's sources may have changed. The
	// returned function stops watching and releases whatever it holds.
	//
	// It reports *possible* change rather than actual change: a backend cannot
	// know whether a write altered anything that resolves, and deciding that is
	// the Store's job.
	Watch(ctx context.Context, interval time.Duration, onChange func()) (stop func(), err error)
}

// SourceKindDeclarer is an optional interface a [Backend] may satisfy to name
// the [SourceKind] its layers carry.
//
// It matters only for a writable backend that has not contributed a layer yet:
// the Store must synthesise a source entry for it so a write can be routed at a
// target that does not exist yet, and without this declaration that entry
// defaults to [SourceFile] — so an empty Consul prefix or parameter path would
// present with file semantics in [Plan] output and [Operation] targets. A
// backend that reports a layer names its kind in the layer's [Source] and need
// not implement this; it is the empty-backend case the interface exists for.
//
// A backend that does not implement it keeps the [SourceFile] default, which is
// right for the built-in file backend.
type SourceKindDeclarer interface {
	SourceKind() SourceKind
}

// Capabilities describes what a backend supports.
//
// Declaring them is what lets a heterogeneous set of backends coexist without
// the weakest one setting the contract for all of them.
//
// Nothing here says whether a backend can be written to or watched. Those are
// answered by implementing [WritableBackend] and [WatchableBackend], so the
// type system checks them once instead of every caller checking a flag at
// runtime — and so a backend cannot claim one thing and do another.
//
// Each field carries a consequence worth recording: a value from a Sensitive
// source must never be written into a layer that is not (the environment-secret
// leak in a new costume), the comment guarantee is document-backend-only and
// must be scoped rather than implied, cross-backend atomicity is impossible and
// must be refused or declared, and foreign-change latency differs per backend
// and must be stated.
//
// Sensitive is enforced: the write path refuses a change that would land a key a
// Sensitive source defines into a layer that is not sensitive, returning
// [ErrSensitiveLeak]. The remaining fields are still forward-declared — stated
// where the reasoning is fresh so the consumer that eventually reads them
// inherits the intent rather than re-deriving it.
type Capabilities struct {
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
	// from one must never be written into a layer that is not also sensitive;
	// the write path enforces this, refusing such a write with [ErrSensitiveLeak].
	Sensitive bool
}

// readerBackend contributes configuration from an in-memory source.
//
// This is how compiled-in defaults reach the configuration: an embedded asset
// is read once at startup and contributes a layer like any other, so it takes
// part in precedence and provenance rather than being a special case. Its
// content is YAML, decoded through the same codec as a YAML file.
type readerBackend struct {
	name    string
	content []byte
	// kind is what this source reports as, so provenance distinguishes a
	// compiled-in default from a layer added at runtime. Both are in-memory,
	// and calling either a file invites a user to open something that does not
	// exist.
	kind SourceKind
}

// NewReaderBackend returns a backend contributing YAML from bytes.
//
// The name appears in provenance, so give it something a user would recognise
// — "embedded:defaults.yaml" rather than "reader1".
func NewReaderBackend(name string, content []byte) Backend {
	return &readerBackend{name: name, content: content, kind: SourceDefault}
}

// newOverrideBackend returns an in-memory source that reports as a runtime
// override rather than a compiled-in default.
func newOverrideBackend(name string, content []byte) Backend {
	return &readerBackend{name: name, content: content, kind: SourceOverride}
}

func (b *readerBackend) ID() string { return b.name }

// Capabilities reports an in-memory source as readable but not writable:
// there is nowhere for a write to persist to.
func (b *readerBackend) Capabilities() Capabilities {
	return Capabilities{}
}

func (b *readerBackend) Load(ctx context.Context, _ []Layer) ([]Layer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	docs, err := YAMLCodec{}.Decode(b.name, b.content)
	if err != nil {
		return nil, err
	}

	// An in-memory source cannot be written back to, so its layers say so or
	// routing would offer it as a target. An empty document contributes no
	// layer but still spends its index.
	var layers []Layer

	for index, values := range docs {
		if values == nil {
			continue
		}

		layers = append(layers, Layer{
			Source: Source{
				Kind:     b.kind,
				Name:     b.name,
				Document: index,
				Writable: false,
			},
			Values: values,
		})
	}

	return layers, nil
}

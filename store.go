package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"sync/atomic"

	"github.com/spf13/afero"
	"github.com/spf13/pflag"
)

// ErrNoSources is returned when a Store is built with nothing to read.
var ErrNoSources = errors.New("config: no sources configured")

// Store is the sole owner of configuration I/O.
//
// Nothing else reads, writes or watches a configuration source. That single
// rule is what removes the need for coordination between components: there is
// no protocol between a reader and a writer because there is only one of each,
// and it is this.
//
// Access is serialised internally. Concurrent loads and writes cannot
// interleave, so a reader never observes a partially applied change, and two
// writers cannot both decide to write and then overwrite one another.
type Store struct {
	// mu serialises everything that mutates state: loading, and later,
	// applying changes. It is deliberately a plain mutex rather than a
	// read-write lock — reads do not take it at all, because they go through
	// the snapshot pointer instead.
	mu sync.Mutex

	backends []Backend
	// requireFirst makes the first backend mandatory. A base file that has
	// gone missing is a broken installation; a missing overlay is normal.
	requireFirst bool

	// loaded records which backend produced which layers, in precedence
	// order. The snapshot alone cannot answer that: a backend's layers may be
	// named after something other than the backend itself, and a backend that
	// contributed nothing has no layer to be found by. Both matter — the first
	// for rebuilding after a write, the second for routing a new key at a file
	// that does not exist yet.
	loaded []backendLayers

	// current is read without the lock. Readers load the pointer and work
	// against an immutable snapshot, so a reload swapping it cannot disturb a
	// read already in progress.
	current atomic.Pointer[Snapshot]
	version atomic.Uint64
}

// keyAware is an optional interface for a backend whose interpretation of its
// own input depends on what the layers beneath it define.
//
// The environment backend is the case that needs it: mapping APP_SERVER_PORT
// back to a dotted key is ambiguous without knowing whether server.port or
// server_port exists.
type keyAware interface {
	observeKnownKeys(keys []string)
}

// backendLayers pairs a backend with what it contributed.
type backendLayers struct {
	backend Backend
	layers  []Layer
}

// StoreOption configures a Store.
type StoreOption func(*Store)

// WithBackend appends a backend. Backends are read in the order they are
// added, and later ones take precedence.
func WithBackend(b Backend) StoreOption {
	return func(s *Store) {
		s.backends = append(s.backends, b)
	}
}

// WithFiles appends a file backend per path, in precedence order — the first
// is the base and the last wins.
func WithFiles(filesystem afero.Fs, paths ...string) StoreOption {
	return func(s *Store) {
		for _, p := range paths {
			s.backends = append(s.backends, NewFileBackend(filesystem, p))
		}
	}
}

// WithEnv appends an environment backend reading variables under a prefix.
//
// Add it after the file sources: environment variables are expected to
// override what is on disk, and precedence follows the order backends are
// added.
func WithEnv(prefix string, opts ...EnvOption) StoreOption {
	return func(s *Store) {
		s.backends = append(s.backends, NewEnvBackend(prefix, opts...))
	}
}

// WithFlags appends a backend contributing the flags the user actually
// changed. Add it last: an explicit flag is the most deliberate input there
// is.
func WithFlags(flags *pflag.FlagSet, opts ...FlagOption) StoreOption {
	return func(s *Store) {
		s.backends = append(s.backends, NewFlagBackend(flags, opts...))
	}
}

// RequireFirstSource makes the first backend mandatory: if it is missing, the
// Store fails to load rather than starting up with a hole in its
// configuration.
func RequireFirstSource() StoreOption {
	return func(s *Store) {
		s.requireFirst = true
	}
}

// NewStore builds a Store and performs its first load.
//
// Construction and loading are deliberately not separable. A Store that exists
// but has not loaded is a state every caller would have to handle and most
// would forget to.
func NewStore(ctx context.Context, opts ...StoreOption) (*Store, error) {
	s := &Store{}

	for _, opt := range opts {
		opt(s)
	}

	if len(s.backends) == 0 {
		return nil, ErrNoSources
	}

	if err := s.Reload(ctx); err != nil {
		return nil, err
	}

	return s, nil
}

// Snapshot returns the current configuration.
//
// The returned snapshot is immutable and will not change underneath the
// caller, so a sequence of reads against one snapshot is coherent even if a
// reload lands midway.
func (s *Store) Snapshot() *Snapshot {
	return s.current.Load()
}

// Reload re-reads every backend and, on success, publishes a new snapshot.
//
// It is fail-closed: if any source fails to load or parse, the error is
// returned and the previous snapshot is retained. A configuration that is
// partly the old values and partly the new is worse than either, and worse
// than refusing.
func (s *Store) Reload(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	loaded, err := s.loadAll(ctx)
	if err != nil {
		return err
	}

	s.publish(loaded)

	return nil
}

// loadAll reads every backend in order, building the layer list.
//
// The caller must hold the lock.
func (s *Store) loadAll(ctx context.Context) ([]backendLayers, error) {
	loaded := make([]backendLayers, 0, len(s.backends))

	for i, backend := range s.backends {
		if aware, ok := backend.(keyAware); ok {
			aware.observeKnownKeys(keysOf(loaded))
		}

		got, err := backend.Load(ctx)
		if err == nil {
			loaded = append(loaded, backendLayers{backend: backend, layers: got})

			continue
		}

		// A missing source is only fatal for the first backend, and only when
		// the caller asked for it to be. Everything else — a parse failure, an
		// unsafe document, a permissions problem — is fatal wherever it
		// happens, because silently dropping a layer changes the effective
		// configuration without telling anyone.
		if errors.Is(err, fs.ErrNotExist) {
			if i == 0 && s.requireFirst {
				return nil, fmt.Errorf("config: required source %s: %w", backend.ID(), err)
			}

			// A source that is not there contributes nothing but is still a
			// candidate for a write: creating it is how a new overlay comes
			// into being.
			loaded = append(loaded, backendLayers{backend: backend})

			continue
		}

		return nil, err
	}

	return loaded, nil
}

// publish installs a new snapshot. The caller must hold the lock.
func (s *Store) publish(loaded []backendLayers) *Snapshot {
	s.loaded = loaded

	var flat []Layer
	for _, bl := range loaded {
		flat = append(flat, bl.layers...)
	}

	next := newSnapshot(s.version.Add(1), flat)
	s.current.Store(next)

	return next
}

// Sources returns the identity of every backend, in precedence order.
func (s *Store) Sources() []string {
	out := make([]string, 0, len(s.backends))
	for _, b := range s.backends {
		out = append(out, b.ID())
	}

	return out
}

// Plan routes changes without writing anything.
//
// The result is exactly what Apply would execute, so a dry run cannot drift
// from the real thing the way a separate preview implementation would.
func (s *Store) Plan(changes ...Change) (*Plan, error) {
	s.mu.Lock()
	targets := s.writableTargets()
	s.mu.Unlock()

	return route(s.Snapshot(), targets, changes)
}

// Apply routes changes, writes them, and publishes the resulting snapshot.
//
// The snapshot is constructed from the content just written rather than by
// re-reading afterwards. That is what makes a write self-contained: there is
// no round trip through the filesystem, no waiting for a watcher, and no
// window in which the configuration in memory disagrees with the file on disk.
//
// Execution is prepare → verify → commit. Everything expensive or likely to
// fail happens while nothing is visible; commit is a sequence of renames.
func (s *Store) Apply(ctx context.Context, changes ...Change) (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current := s.current.Load()

	plan, err := route(current, s.writableTargets(), changes)
	if err != nil {
		return nil, err
	}

	pending, err := s.prepare(ctx, plan)
	if err != nil {
		return nil, err
	}

	if err := verifyAll(ctx, pending); err != nil {
		discardAll(ctx, pending)

		return nil, err
	}

	if err := commitAll(ctx, pending); err != nil {
		return nil, err
	}

	return s.publish(s.rebuild(pending)), nil
}

// prepare groups a plan by backend and stages the edits for each.
//
// Grouping matters: a file is read, edited and written once per apply however
// many changes target it. Editing per change would reparse the same document
// repeatedly, and a settings screen saving fifty fields would pay for it.
func (s *Store) prepare(ctx context.Context, plan *Plan) (map[string]Pending, error) {
	grouped := map[string][]Edit{}
	order := []string{}

	for _, op := range plan.Operations {
		id := op.Target.Name
		if _, seen := grouped[id]; !seen {
			order = append(order, id)
		}

		grouped[id] = append(grouped[id], Edit{
			Document: op.Target.Document,
			Path:     op.Change.Path,
			Value:    op.Change.Value,
			Remove:   op.Change.Remove,
		})
	}

	pending := make(map[string]Pending, len(grouped))

	for _, id := range order {
		backend, ok := s.backendByID(id)
		if !ok {
			discardAll(ctx, pending)

			return nil, fmt.Errorf("%w: no backend for %s", ErrInternal, id)
		}

		writable, ok := backend.(WritableBackend)
		if !ok {
			discardAll(ctx, pending)

			return nil, fmt.Errorf("%w: %s", ErrNotWritable, id)
		}

		staged, err := writable.Prepare(ctx, grouped[id])
		if err != nil {
			discardAll(ctx, pending)

			return nil, err
		}

		pending[id] = staged
	}

	return pending, nil
}

// verifyAll checks every source is unchanged before anything is committed.
func verifyAll(ctx context.Context, pending map[string]Pending) error {
	for _, p := range pending {
		if err := p.Verify(ctx); err != nil {
			return err
		}
	}

	return nil
}

// commitAll commits every staged write, undoing those already committed if one
// fails.
//
// This is not cross-source transactionality — that needs a journal — but it
// narrows the window in which a partially applied set can be observed to the
// duration of the renames, and it fails in the safe direction: everything
// likely to go wrong has already happened during prepare.
func commitAll(ctx context.Context, pending map[string]Pending) error {
	committed := make([]Pending, 0, len(pending))

	for id, p := range pending {
		if err := p.Commit(ctx); err != nil {
			rollbackErr := rollbackAll(ctx, committed)
			discardAll(ctx, pending)

			if rollbackErr != nil {
				// The caller must never be left guessing what is in which
				// state, so say so explicitly rather than returning the
				// original error alone.
				return fmt.Errorf("%w: %s failed (%w) and rolling back the rest failed: %w",
					ErrPartialCommit, id, err, rollbackErr)
			}

			return err
		}

		committed = append(committed, p)
	}

	return nil
}

func rollbackAll(ctx context.Context, committed []Pending) error {
	var firstErr error

	for _, p := range committed {
		if err := p.Rollback(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

func discardAll(ctx context.Context, pending map[string]Pending) {
	for _, p := range pending {
		_ = p.Discard(ctx)
	}
}

// rebuild assembles the next layer set: written sources contribute what was
// just staged, and everything else is carried over untouched.
//
// Carrying layers over rather than re-reading them is deliberate. Re-reading
// would be slower, and it would open the possibility of picking up a foreign
// change halfway through an apply — turning one caller's write into a snapshot
// containing someone else's edits too.
//
// The pairing recorded at load is what makes this correct. Matching layers to
// backends by name would work for files and silently drop any backend whose
// layers are named after something else, such as an environment variable.
func (s *Store) rebuild(pending map[string]Pending) []backendLayers {
	next := make([]backendLayers, 0, len(s.loaded))

	for _, bl := range s.loaded {
		if staged, ok := pending[bl.backend.ID()]; ok {
			next = append(next, backendLayers{backend: bl.backend, layers: staged.Layers()})

			continue
		}

		next = append(next, bl)
	}

	return next
}

// keysOf lists every leaf key the layers loaded so far define.
func keysOf(loaded []backendLayers) []string {
	seen := map[string]bool{}

	var keys []string

	for _, bl := range loaded {
		for _, layer := range bl.layers {
			collectKeys(layer.Values, "", seen, &keys)
		}
	}

	return keys
}

func collectKeys(values map[string]any, prefix string, seen map[string]bool, out *[]string) {
	for k, v := range values {
		path := normaliseKey(k)
		if prefix != "" {
			path = prefix + "." + path
		}

		nested, isMap := asStringMap(v)
		if isMap && len(nested) > 0 {
			collectKeys(nested, path, seen, out)

			continue
		}

		if !seen[path] {
			seen[path] = true

			*out = append(*out, path)
		}
	}
}

// writableTargets lists the layers a change could be written to, in precedence
// order, lowest first.
//
// A backend that contributed no layers is still a candidate when it can be
// written: a configured file that does not exist yet is exactly where a new
// key should go, and leaving it out would make the highest-precedence writable
// layer depend on whether a file happened to exist.
func (s *Store) writableTargets() []Source {
	var out []Source

	for _, bl := range s.loaded {
		if len(bl.layers) > 0 {
			for _, l := range bl.layers {
				if l.Source.Writable {
					out = append(out, l.Source)
				}
			}

			continue
		}

		if _, ok := bl.backend.(WritableBackend); ok && bl.backend.Capabilities().Writable {
			out = append(out, Source{
				Kind:     SourceFile,
				Name:     bl.backend.ID(),
				Writable: true,
			})
		}
	}

	return out
}

func (s *Store) backendByID(id string) (Backend, bool) {
	for _, b := range s.backends {
		if b.ID() == id {
			return b, true
		}
	}

	return nil, false
}

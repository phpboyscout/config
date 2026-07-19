package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"sync/atomic"

	"github.com/spf13/afero"
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

	// current is read without the lock. Readers load the pointer and work
	// against an immutable snapshot, so a reload swapping it cannot disturb a
	// read already in progress.
	current atomic.Pointer[Snapshot]
	version atomic.Uint64
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

	layers, err := s.loadAll(ctx)
	if err != nil {
		return err
	}

	s.publish(layers)

	return nil
}

// loadAll reads every backend in order, building the layer list.
//
// The caller must hold the lock.
func (s *Store) loadAll(ctx context.Context) ([]Layer, error) {
	var layers []Layer

	for i, backend := range s.backends {
		got, err := backend.Load(ctx)
		if err == nil {
			layers = append(layers, got...)

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

			continue
		}

		return nil, err
	}

	return layers, nil
}

// publish installs a new snapshot. The caller must hold the lock.
func (s *Store) publish(layers []Layer) *Snapshot {
	next := newSnapshot(s.version.Add(1), layers)
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

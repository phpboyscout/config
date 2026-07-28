package config

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Composition errors.
var (
	// ErrCyclicStore is returned when composing stores would put one store into
	// the graph twice — whether or not that closes a cycle. See [Nested].
	ErrCyclicStore = errors.New("config: store is already present in this graph")

	// ErrDuplicateLayer is returned when two layers in a composed store are
	// indistinguishable: equal in kind, name and document, so nothing can tell
	// them apart. See [Nested].
	ErrDuplicateLayer = errors.New("config: two layers in the composed store are indistinguishable")
)

// NestedOption configures a nested store. See [NestedPromotable].
type NestedOption func(*nestedConfig)

type nestedConfig struct{ promotable bool }

// NestedPromotable makes a nested store's writable layers valid targets for an
// explicitly named write, without making them candidates for routing.
//
// Without it a nested store is read-only: its layers are read and merged, and a
// write never lands in one. That is the safe default and the usual case — a
// shared organisational base, a team default.
//
// With it, a write that names an inner layer with [To] reaches it, while an
// unpinned write still never does. The asymmetry is the point. Routing prefers
// a writable layer that already defines the key, so a routable nested store
// would mean an ordinary project-scoped edit walking past the project's own file
// and rewriting the shared config every other consumer inherits. Promotion has
// to be deliberate, and naming the target is what makes it so.
func NestedPromotable(c *nestedConfig) { c.promotable = true }

// Nested makes a [Store] a backend of another Store, so one configuration can
// carry another's layers as its own.
//
// The inner store's layers pass through as a contiguous block at the position
// this backend is declared, each keeping its own [Source]. Provenance therefore
// survives the join — a value from the inner store is reported as coming from
// the file it came from, not from the aggregate — and precedence stays exactly
// what the two stores' declaration orders say it is, with nothing interleaved.
//
// The id names this aggregate in errors. It is not a layer name, because an
// aggregate contributes no layer of its own.
//
// A nested store is read-only unless [NestedPromotable] is passed.
//
// Composition forms a tree: a store may appear at most once in a graph, and
// nesting one already reachable is [ErrCyclicStore] at construction. Reads
// recurse to any depth.
//
// A store composed over another does not see writes made directly to the inner
// store until it next reloads. When the outer store is watching, that happens on
// the next tick; when it is not, call [Store.Reload].
func Nested(s *Store, id string, opts ...NestedOption) Backend {
	cfg := nestedConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}

	return &nested{inner: s, id: id, promotable: cfg.promotable}
}

// nested is a Store presented as a Backend.
type nested struct {
	inner      *Store
	id         string
	promotable bool

	// version is the inner store's snapshot version at the last Load, so the
	// watch bridge can notice the inner store publishing without registering a
	// callback across the store boundary.
	mu      sync.RWMutex
	version uint64
}

func (n *nested) ID() string { return n.id }

// Capabilities are the inner store's, reduced to what composition can promise.
//
// Sensitive is true if ANY inner backend is, which is conservative on purpose:
// the guard is keyed per layer, so this only decides whether the aggregate as a
// whole is treated as holding secret material. A false negative is a secret
// written into a plain layer; a false positive is a write refused that could
// have been allowed. The asymmetry is not close.
//
// AtomicMultiKey is always false. A write spanning the outer store and an inner
// one spans two stores; nothing can make that indivisible, so it is declared
// rather than pretended.
func (n *nested) Capabilities() Capabilities {
	var caps Capabilities

	for _, bl := range n.inner.loadedBackends() {
		inner := bl.Capabilities()
		caps.Sensitive = caps.Sensitive || inner.Sensitive
		caps.NativeWatch = caps.NativeWatch || inner.NativeWatch
		caps.PreservesComments = caps.PreservesComments || inner.PreservesComments
	}

	caps.AtomicMultiKey = false

	return caps
}

// Load contributes the inner store's layers.
//
// It reads the inner store's current snapshot rather than reloading it. The
// inner store refreshes itself — on its own Reload, its own Apply, or its own
// watch — and reloading it from here would notify its observers on the outer
// store's schedule and could fail outright when called from inside one.
func (n *nested) Load(_ context.Context, _ []Layer) ([]Layer, error) {
	snap := n.inner.Snapshot()
	if snap == nil {
		return nil, nil
	}

	n.mu.Lock()
	n.version = snap.Version()
	n.mu.Unlock()

	out := make([]Layer, 0, len(snap.layers))

	for _, l := range snap.layers {
		source := l.Source

		// A nested layer is never a routing candidate. Writable says whether a
		// layer CAN be written to, and routing walks exactly the writable ones —
		// so clearing it for a read-only nested store is what keeps an ordinary
		// write forking into the outer store's own layers instead of rewriting
		// the shared configuration beneath it.
		if !n.promotable {
			source.Writable = false
		}

		out = append(out, Layer{Source: source, Values: cloneMap(l.Values)})
	}

	return out, nil
}

// innerVersion reports the inner store's snapshot version as of the last Load.
func (n *nested) innerVersion() uint64 {
	n.mu.RLock()
	defer n.mu.RUnlock()

	return n.version
}

// Watch reports when the inner store's configuration may have moved.
//
// Two things can move it, and only one of them travels the inner store's own
// watch: a foreign change to one of its sources, and the inner store's own
// Apply — which deliberately never reaches a watcher, because a store's writes
// must not come back round through one.
//
// So this polls the inner store's snapshot version alongside. That costs an
// atomic load per tick and, crucially, registers no callback across the store
// boundary: the notification travels the OUTER store's own watch path, on its
// own goroutine, exactly as a file change would. A cross-store observer chain
// would sit outside what ErrWriteFromObserver is built to catch.
//
// The version increments on every publish, changed or not, so a tick can cost a
// reload that resolves to "nothing moved" and notifies nobody. Wasteful, never
// wrong.
func (n *nested) Watch(ctx context.Context, interval time.Duration, onChange func()) (func(), error) {
	if interval <= 0 {
		interval = DefaultPollInterval
	}

	ctx, cancel := context.WithCancel(ctx)
	ticker := time.NewTicker(interval)

	go func() {
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				snap := n.inner.Snapshot()
				if snap == nil {
					continue
				}

				if snap.Version() != n.innerVersion() {
					onChange()
				}
			}
		}
	}()

	var once sync.Once

	return func() { once.Do(cancel) }, nil
}

// nestedStores reports the stores reachable through a backend, including the
// one it wraps, so composition can be checked for a store appearing twice.
func nestedStores(b Backend) []*Store {
	n, ok := b.(*nested)
	if !ok {
		return nil
	}

	out := []*Store{n.inner}
	for _, inner := range n.inner.backends {
		out = append(out, nestedStores(inner)...)
	}

	return out
}

// ensureTree rejects a set of backends that would put one store into the graph
// more than once.
//
// Stricter than refusing cycles: a store appearing twice is not a cycle and
// resolves perfectly well for reads, but it would contribute its layers twice at
// two different precedences, so every value in it would be shadowed by a copy of
// itself and provenance would name a layer that occurs more than once in the
// order. Enforcement is identity alone, which leaves no room for a reachability
// analysis to be subtly wrong.
func ensureTree(self *Store, backends []Backend) error {
	seen := map[*Store]bool{self: true}

	for _, b := range backends {
		for _, s := range nestedStores(b) {
			if seen[s] {
				return fmt.Errorf("%w: %q", ErrCyclicStore, b.ID())
			}

			seen[s] = true
		}
	}

	return nil
}

// ensureDistinctLayers rejects a layer set containing two indistinguishable
// sources.
//
// Equal in kind, name and document means nothing downstream can tell them
// apart: the routing index is keyed by Source, so one layer's values silently
// overwrite the other's, and shadow detection compares sources for equality. The
// observable result is a wrong plan — a key defined only in the hidden copy is
// invisible to shadow detection, so a write is reported as effective when it is
// not.
//
// Two stores pointed at one file is a composition mistake, and the same file
// resolved twice at two precedences is not something to resolve silently.
func ensureDistinctLayers(loaded []backendLayers) error {
	seen := map[Source]bool{}

	for _, bl := range loaded {
		for _, l := range bl.layers {
			if seen[l.Source] {
				return fmt.Errorf("%w: %q appears twice", ErrDuplicateLayer, l.Source)
			}

			seen[l.Source] = true
		}
	}

	return nil
}

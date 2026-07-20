package config_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"testing"
	"time"

	"gitlab.com/phpboyscout/go/config"
)

// The backend below is the worked example in docs/how-to/custom-backend.md.
// It lives here so the guide's code is compiled and exercised rather than
// merely plausible.

// remoteStore stands in for whatever a real backend talks to — Consul, a
// parameter store, an HTTP endpoint. Keeping it an interface is what lets the
// backend be tested without the real thing.
type remoteStore interface {
	Fetch(ctx context.Context) (map[string]any, uint64, error)
	Put(ctx context.Context, values map[string]any, ifVersion uint64) error
}

// remoteBackend contributes configuration from a remote key-value store.
type remoteBackend struct {
	store  remoteStore
	prefix string

	mu      sync.Mutex
	version uint64
}

func newRemoteBackend(store remoteStore, prefix string) *remoteBackend {
	return &remoteBackend{store: store, prefix: prefix}
}

// ID identifies the backend in diagnostics — and, for a writable backend, is
// how the Store finds it again when routing a write. It must equal the
// Source.Name of the layers Load returns.
func (b *remoteBackend) ID() string { return b.prefix }

// Capabilities describes what this source can and cannot do.
func (b *remoteBackend) Capabilities() config.Capabilities {
	return config.Capabilities{
		PreservesComments: false, // a key-value store has nowhere to put a comment
		AtomicMultiKey:    true,  // the compare-and-swap covers the whole set
		NativeWatch:       true,  // a real subscription, not polling
		Sensitive:         false, // set true for a secrets store
	}
}

// Load fetches the current values and returns them as one layer.
func (b *remoteBackend) Load(ctx context.Context, _ []config.Layer) ([]config.Layer, error) {
	values, version, err := b.store.Fetch(ctx)
	if err != nil {
		return nil, err
	}

	if values == nil {
		// Absent is not an error here — the Store decides whether a missing
		// source is fatal, and says so with RequireFirstSource.
		return nil, fs.ErrNotExist
	}

	b.mu.Lock()
	b.version = version
	b.mu.Unlock()

	return []config.Layer{{
		Source: config.Source{
			Kind:     config.SourceKind("remote"),
			Name:     b.prefix,
			Writable: true,
		},
		Values: values,
	}}, nil
}

// Watch reports that the remote data may have changed.
func (b *remoteBackend) Watch(
	ctx context.Context,
	_ time.Duration,
	onChange func(),
) (func(), error) {
	done := make(chan struct{})

	sub, ok := b.store.(interface{ Subscribe() <-chan struct{} })
	if !ok {
		return func() {}, nil
	}

	go func() {
		events := sub.Subscribe()

		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-events:
				onChange()
			}
		}
	}()

	var once sync.Once

	return func() { once.Do(func() { close(done) }) }, nil
}

// --- the fake the tests drive -------------------------------------------

type fakeRemote struct {
	mu      sync.Mutex
	values  map[string]any
	version uint64
	events  chan struct{}
}

func newFakeRemote(values map[string]any) *fakeRemote {
	return &fakeRemote{values: values, version: 1, events: make(chan struct{}, 8)}
}

func (f *fakeRemote) Fetch(context.Context) (map[string]any, uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.values == nil {
		return nil, 0, nil
	}

	out := make(map[string]any, len(f.values))
	for k, v := range f.values {
		out[k] = v
	}

	return out, f.version, nil
}

func (f *fakeRemote) Put(_ context.Context, values map[string]any, ifVersion uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.version != ifVersion {
		return errors.New("version conflict")
	}

	f.values = values
	f.version++

	return nil
}

func (f *fakeRemote) Subscribe() <-chan struct{} { return f.events }

func (f *fakeRemote) set(key string, value any) {
	f.mu.Lock()
	f.values[key] = value
	f.version++
	f.mu.Unlock()

	f.events <- struct{}{}
}

// --- what the guide claims ----------------------------------------------

func TestCustomBackend_ParticipatesAsAnOrdinaryLayer(t *testing.T) {
	t.Parallel()

	remote := newFakeRemote(map[string]any{
		"server": map[string]any{"port": 9090},
	})

	store, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{
			Name: "embedded:defaults.yaml", Content: []byte("server:\n  port: 8080\n  host: localhost\n"),
		}),
		config.WithBackend(newRemoteBackend(remote, "app/")),
	)
	if err != nil {
		t.Fatal(err)
	}

	view := store.View()

	// Precedence: added after the defaults, so it wins.
	if got := view.GetInt("server.port"); got != 9090 {
		t.Errorf("port = %d, want 9090 from the remote layer", got)
	}

	// Merging is per-key, so the default still supplies what the remote omits.
	if got := view.GetString("server.host"); got != "localhost" {
		t.Errorf("host = %q, want the default to survive", got)
	}

	// Provenance names it, including the backend's own name.
	src, ok := view.Origin("server.port")
	if !ok || src.String() != "remote:app/" {
		t.Errorf("origin = %q (ok=%v), want \"remote:app/\"", src.String(), ok)
	}

	// And shadowing reports the full chain.
	if got := len(view.Shadowed("server.port")); got != 2 {
		t.Errorf("shadowed layers = %d, want 2", got)
	}
}

func TestCustomBackend_TakesPartInHotReload(t *testing.T) {
	t.Parallel()

	remote := newFakeRemote(map[string]any{"level": "info"})

	store, err := config.NewStore(context.Background(),
		config.WithBackend(newRemoteBackend(remote, "app/")))
	if err != nil {
		t.Fatal(err)
	}

	seen := make(chan string, 4)

	store.AddObserverFunc(func(cfg config.Observed) error {
		seen <- cfg.GetString("level")

		return nil
	})

	// Settling disabled: the subscription reports a real change, so there is
	// no burst to coalesce and the test should not wait on a timer.
	stop, err := store.Watch(context.Background(), config.WithSettleInterval(0))
	if err != nil {
		t.Fatal(err)
	}

	defer stop()

	remote.set("level", "debug")

	select {
	case got := <-seen:
		if got != "debug" {
			t.Errorf("observed %q, want \"debug\"", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a change to the remote never reached observers")
	}
}

func TestCustomBackend_AbsentSourceIsNotFatal(t *testing.T) {
	t.Parallel()

	// An empty remote reports fs.ErrNotExist, which is tolerated for an
	// optional source exactly as a missing overlay file is.
	store, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{
			Name: "embedded:defaults.yaml", Content: []byte("port: 8080\n"),
		}),
		config.WithBackend(newRemoteBackend(newFakeRemote(nil), "app/")),
	)
	if err != nil {
		t.Fatal(err)
	}

	if got := store.View().GetInt("port"); got != 8080 {
		t.Errorf("port = %d, want the defaults to stand", got)
	}
}

// --- writing -------------------------------------------------------------
//
// Everything below implements WritableBackend, so the write half of the guide
// has compiled code behind it rather than only prose.

// Prepare stages edits without touching the remote.
//
// Nothing here may modify the source: the Store may abandon this batch because
// some *other* backend's Verify failed, and that must cost nothing.
func (b *remoteBackend) Prepare(ctx context.Context, edits []config.Edit) (config.Pending, error) {
	current, _, err := b.store.Fetch(ctx)
	if err != nil {
		return nil, err
	}

	if current == nil {
		current = map[string]any{}
	}

	// The version recorded at Load, NOT one fetched here. Routing decided
	// where this write goes based on what Load saw, so a change that landed
	// since then invalidates that decision. Comparing a prepare-time version
	// against a commit-time one compares the intruder's data with itself and
	// finds nothing wrong.
	b.mu.Lock()
	version := b.version
	b.mu.Unlock()

	// Work on a copy. The staged result must not be visible to a concurrent
	// Load, and must be discardable without having changed anything.
	next := make(map[string]any, len(current))
	for k, v := range current {
		next[k] = v
	}

	for _, edit := range edits {
		if edit.Remove {
			delete(next, edit.Path)

			continue
		}

		next[edit.Path] = edit.Value
	}

	return &remotePending{
		backend: b,
		next:    next,
		// The version read here is what Verify compares against, so a change
		// landing between now and commit is detected rather than overwritten.
		version: version,
		previous: func() map[string]any {
			copied := make(map[string]any, len(current))
			for k, v := range current {
				copied[k] = v
			}

			return copied
		}(),
	}, nil
}

// remotePending is one backend's staged write.
type remotePending struct {
	backend   *remoteBackend
	next      map[string]any
	previous  map[string]any
	version   uint64
	committed bool
}

// Layers is what this backend will contribute once committed.
//
// It describes the post-commit state, which is what lets the Store build the
// next snapshot from what was just written instead of re-reading and hoping.
func (p *remotePending) Layers() []config.Layer {
	return []config.Layer{{
		Source: config.Source{
			Kind:     config.SourceKind("remote"),
			Name:     p.backend.prefix,
			Writable: true,
		},
		Values: p.next,
	}}
}

// Verify reports whether the remote is still as it was when Prepare ran.
func (p *remotePending) Verify(ctx context.Context) error {
	_, version, err := p.backend.store.Fetch(ctx)
	if err != nil {
		return err
	}

	if version != p.version {
		return fmt.Errorf("%w: remote moved from version %d to %d",
			config.ErrConflict, p.version, version)
	}

	return nil
}

// Commit makes the staged values visible, conditional on the version still
// being the one Prepare saw.
func (p *remotePending) Commit(ctx context.Context) error {
	if err := p.backend.store.Put(ctx, p.next, p.version); err != nil {
		return err
	}

	p.committed = true

	return nil
}

// Rollback restores what was there before, best effort, for when a later
// commit in the same batch failed.
func (p *remotePending) Rollback(ctx context.Context) error {
	if !p.committed {
		return nil
	}

	_, version, err := p.backend.store.Fetch(ctx)
	if err != nil {
		return err
	}

	return p.backend.store.Put(ctx, p.previous, version)
}

// Discard abandons staged work. Nothing was written, so there is nothing to
// undo — which is the point of staging.
func (p *remotePending) Discard(context.Context) error { return nil }

func TestCustomBackend_ReceivesWrites(t *testing.T) {
	t.Parallel()

	remote := newFakeRemote(map[string]any{"level": "info"})

	store, err := config.NewStore(context.Background(),
		config.WithBackend(newRemoteBackend(remote, "app/")))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.Apply(context.Background(), config.Set("level", "debug")); err != nil {
		t.Fatal(err)
	}

	// The value the Store now serves...
	if got := store.View().GetString("level"); got != "debug" {
		t.Errorf("view = %q, want \"debug\"", got)
	}

	// ...and the value that actually reached the remote.
	values, _, _ := remote.Fetch(context.Background())
	if got := values["level"]; got != "debug" {
		t.Errorf("remote = %v, want \"debug\"", got)
	}
}

func TestCustomBackend_VerifyDetectsAConcurrentChange(t *testing.T) {
	t.Parallel()

	remote := newFakeRemote(map[string]any{"level": "info"})
	backend := newRemoteBackend(remote, "app/")

	store, err := config.NewStore(context.Background(), config.WithBackend(backend))
	if err != nil {
		t.Fatal(err)
	}

	// Something else changes the remote after the Store loaded it.
	remote.set("other", "value")

	_, err = store.Apply(context.Background(), config.Set("level", "debug"))
	if !errors.Is(err, config.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict — a stale write was accepted", err)
	}

	// And nothing was written.
	values, _, _ := remote.Fetch(context.Background())
	if got := values["level"]; got != "info" {
		t.Errorf("remote level = %v, want the original \"info\"", got)
	}
}

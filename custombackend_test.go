package config_test

import (
	"context"
	"errors"
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

// ID identifies the backend in diagnostics.
func (b *remoteBackend) ID() string { return "remote:" + b.prefix }

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

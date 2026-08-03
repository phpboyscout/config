package config_test

import (
	"context"
	"io/fs"
	"sync"
	"testing"
	"time"

	"gitlab.com/phpboyscout/go/errors"

	"gitlab.com/phpboyscout/go/config"
)

// These regression tests live in package config_test on purpose: the seams they
// exercise exist so a codec or backend defined OUTSIDE package config can satisfy
// them. Defining the types here — where only exported methods are reachable — is
// what makes the test honest about the boundary. On the pre-fix code the probes
// carried unexported methods, so no external type could ever satisfy them.

// --- F1: exported optional capability interfaces -------------------------

// commentCodec is an external codec that preserves comments, declared through the
// exported [config.CommentPreservingCodec] method — the only kind a package
// outside config can define.
type commentCodec struct{}

func (commentCodec) Decode(string, []byte) ([]map[string]any, error) {
	return []map[string]any{{"k": "v"}}, nil
}

func (commentCodec) PreservesComments() bool { return true }

// A codec defined outside package config that preserves comments must have that
// surfaced in Capabilities. Before F1 the probe asserted on an unexported method,
// so an external comment-preserving codec always read back as false.
func TestF1_ExternalCommentCodecIsObserved(t *testing.T) {
	t.Parallel()

	backend := config.NewCodecBackend(config.OS(), "x.yaml", commentCodec{})
	if !backend.Capabilities().PreservesComments {
		t.Error("an external comment-preserving codec reads back as PreservesComments=false")
	}
}

// errReporterBackend is an external watchable backend that routes its watch
// errors through the exported [config.WatchErrorReporter].
type errReporterBackend struct {
	mu      sync.Mutex
	handler func(error)
}

func (b *errReporterBackend) ID() string { return "errrep" }

func (b *errReporterBackend) Capabilities() config.Capabilities { return config.Capabilities{} }

func (b *errReporterBackend) Load(context.Context, []config.Layer) ([]config.Layer, error) {
	return []config.Layer{{
		Source: config.Source{Kind: config.SourceKind("remote"), Name: "errrep"},
		Values: map[string]any{"k": "v"},
	}}, nil
}

func (b *errReporterBackend) Watch(context.Context, time.Duration, func()) (func(), error) {
	return func() {}, nil
}

func (b *errReporterBackend) SetWatchErrorHandler(fn func(error)) {
	b.mu.Lock()
	b.handler = fn
	b.mu.Unlock()
}

func (b *errReporterBackend) fireError(err error) {
	b.mu.Lock()
	fn := b.handler
	b.mu.Unlock()

	if fn != nil {
		fn(err)
	}
}

// A backend defined outside package config that implements WatchErrorReporter
// must have its watch-degradation errors routed to OnReloadError. Before F1 the
// interface carried an unexported method, so store.go's claim that a consumer's
// backend could opt in was unreachable.
func TestF1_ExternalWatchErrorReporterRoutesToOnReloadError(t *testing.T) {
	t.Parallel()

	backend := &errReporterBackend{}

	s, err := config.NewStore(context.Background(), config.WithBackend(backend))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	got := make(chan error, 1)
	s.OnReloadError(func(e error) {
		select {
		case got <- e:
		default:
		}
	})

	stop, err := s.Watch(context.Background())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	defer stop()

	sentinel := errors.New("watch degraded")
	backend.fireError(sentinel)

	select {
	case e := <-got:
		if !errors.Is(e, sentinel) {
			t.Errorf("OnReloadError got %v, want the backend's watch error", e)
		}
	case <-time.After(time.Second):
		t.Error("an external backend's watch error never reached OnReloadError")
	}
}

// pathBackend is an external watchable backend reading a filesystem path,
// declared through the exported [config.WatchPathReporter].
type pathBackend struct{ path string }

func (b *pathBackend) ID() string { return "pathrep" }

func (b *pathBackend) Capabilities() config.Capabilities { return config.Capabilities{} }

func (b *pathBackend) Load(context.Context, []config.Layer) ([]config.Layer, error) {
	return []config.Layer{{
		Source: config.Source{Kind: config.SourceKind("remote"), Name: "pathrep"},
		Values: map[string]any{"k": "v"},
	}}, nil
}

func (b *pathBackend) Watch(context.Context, time.Duration, func()) (func(), error) {
	return func() {}, nil
}

func (b *pathBackend) WatchPath() (string, bool) { return b.path, true }

// recordingWatcher captures the paths an injected watcher is handed.
type recordingWatcher struct {
	mu    sync.Mutex
	paths []string
}

func (w *recordingWatcher) Watch(_ context.Context, paths []string, _ func()) (func(), error) {
	w.mu.Lock()
	w.paths = append([]string(nil), paths...)
	w.mu.Unlock()

	return func() {}, nil
}

// A backend defined outside package config that implements WatchPathReporter must
// contribute its path to an injected watcher. Before F1 the interface carried an
// unexported method, so an externally defined file-like backend was silently
// omitted from the injected watcher's path set.
func TestF1_ExternalWatchPathReporterContributesToInjectedWatcher(t *testing.T) {
	t.Parallel()

	backend := &pathBackend{path: "/etc/app/config.yaml"}
	watcher := &recordingWatcher{}

	s, err := config.NewStore(context.Background(), config.WithBackend(backend))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	stop, err := s.Watch(context.Background(), config.WithWatcher(watcher))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	defer stop()

	watcher.mu.Lock()
	defer watcher.mu.Unlock()

	found := false
	for _, p := range watcher.paths {
		if p == backend.path {
			found = true
		}
	}

	if !found {
		t.Errorf("injected watcher paths = %v, want the external backend's %q", watcher.paths, backend.path)
	}
}

// --- F3: backends declare their SourceKind -------------------------------

// declaringBackend is an empty writable backend that declares its own kind and
// sensitivity — the shape a Consul-prefix or parameter-store backend has before
// it has been written to.
type declaringBackend struct {
	*remoteBackend

	kind      config.SourceKind
	sensitive bool
}

func (b declaringBackend) Capabilities() config.Capabilities {
	return config.Capabilities{Sensitive: b.sensitive}
}

func (b declaringBackend) SourceKind() config.SourceKind { return b.kind }

// An empty writable non-file backend must present in Plan with its declared kind,
// not the hard-coded SourceFile the synthesised entry used before F3.
func TestF3_SynthesisedSourceUsesDeclaredKind(t *testing.T) {
	t.Parallel()

	empty := declaringBackend{
		remoteBackend: newRemoteBackend(newFakeRemote(nil), "app/"),
		kind:          config.SourceKind("remote"),
	}

	s, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "base", Content: []byte("x: 1\n")}),
		config.WithBackend(empty),
	)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	plan, err := s.Plan(config.Set("newkey", "v"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if got := plan.Operations[0].Target.Kind; got != config.SourceKind("remote") {
		t.Errorf("synthesised target kind = %q, want the backend's declared %q", got, "remote")
	}
}

// An empty writable sensitive backend must be marked sensitive so its synthesised
// target carries the leak guard: a write routed into it is writing into a secrets
// store, which is allowed, and must not be mistaken for a plain layer the guard
// then refuses a secret-defined key into.
func TestF3_EmptyWritableSensitiveBackendIsGuarded(t *testing.T) {
	t.Parallel()

	empty := declaringBackend{
		remoteBackend: newRemoteBackend(newFakeRemote(nil), "secrets/"),
		kind:          config.SourceKind("remote"),
		sensitive:     true,
	}
	defining := newSensitiveReadOnlyBackend(newFakeRemote(map[string]any{"apikey": "old"}), "vault/")

	s, err := config.NewStore(context.Background(),
		config.WithBackend(defining), // sensitive, read-only, defines apikey
		config.WithBackend(empty),    // sensitive, writable, empty — the write target
	)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// The guard must not fire: the target is the empty sensitive backend itself,
	// so the write is landing in a secrets store, which is allowed. Before F3 the
	// synthesised target was unmarked, the guard treated it as a plain layer, and
	// the sensitive-defined key was refused with ErrSensitiveLeak.
	plan, err := s.Plan(config.Set("apikey", "new"))
	if errors.Is(err, config.ErrSensitiveLeak) {
		t.Fatalf("writing into the empty sensitive backend was refused as a leak: %v", err)
	}

	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if got := plan.Operations[0].Target.Name; got != "secrets/" {
		t.Errorf("routed to %q, want the empty sensitive backend %q", got, "secrets/")
	}
}

// --- F4: a backend may hint its own poll cadence -------------------------

// hintBackend is a watchable backend that declares a poll cadence through
// [config.PollIntervalHinter].
type hintBackend struct{ got chan time.Duration }

func (h *hintBackend) ID() string { return "hint" }

func (h *hintBackend) Capabilities() config.Capabilities { return config.Capabilities{} }

func (h *hintBackend) Load(context.Context, []config.Layer) ([]config.Layer, error) {
	return []config.Layer{{
		Source: config.Source{Kind: config.SourceKind("remote"), Name: "hint"},
		Values: map[string]any{"k": "v"},
	}}, nil
}

func (h *hintBackend) Watch(_ context.Context, interval time.Duration, _ func()) (func(), error) {
	select {
	case h.got <- interval:
	default:
	}

	return func() {}, nil
}

func (h *hintBackend) PollInterval() time.Duration { return 30 * time.Second }

var _ fs.FS // keep io/fs imported for the interface-satisfaction intent

// A default Store.Watch adopts a backend's PollIntervalHinter cadence rather than
// billing the 2-second default; an explicit WithPollInterval still wins.
func TestF4_BackendPollIntervalHintIsHonoured(t *testing.T) {
	t.Parallel()

	t.Run("default adopts the hint", func(t *testing.T) {
		t.Parallel()

		h := &hintBackend{got: make(chan time.Duration, 1)}

		s, err := config.NewStore(context.Background(), config.WithBackend(h))
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}

		stop, err := s.Watch(context.Background())
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}

		defer stop()

		if got := <-h.got; got != 30*time.Second {
			t.Errorf("backend received %v, want its 30s hint", got)
		}
	})

	t.Run("explicit interval overrides the hint", func(t *testing.T) {
		t.Parallel()

		h := &hintBackend{got: make(chan time.Duration, 1)}

		s, err := config.NewStore(context.Background(), config.WithBackend(h))
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}

		stop, err := s.Watch(context.Background(), config.WithPollInterval(5*time.Second))
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}

		defer stop()

		if got := <-h.got; got != 5*time.Second {
			t.Errorf("backend received %v, want the explicit 5s override", got)
		}
	})
}

package config_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/phpboyscout/go/errors"

	. "gitlab.com/phpboyscout/go/config"
)

// Spec 0013 (Store.Close). Each test names the decision it holds the Store to.

// events is an ordered record shared by everything a test closes or stops, so
// the order Close does things in is observable rather than inferred.
type events struct {
	mu  sync.Mutex
	log []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	e.log = append(e.log, s)
	e.mu.Unlock()
}

func (e *events) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return slices.Clone(e.log)
}

func (e *events) indexOf(s string) int { return slices.Index(e.all(), s) }

// heldClient is a backend over a connection it must not use once closed: a Load
// after Close fails the way a closed gRPC client does.
type heldClient struct {
	id     string
	events *events
	value  atomic.Value

	closed   atomic.Bool
	closeErr error

	// loadGate, when set, holds every Load until it is closed, after
	// signalling loadEntered, so a test can land Close while a load is in flight.
	loadGate    chan struct{}
	loadEntered chan struct{}

	// respectCtx makes a held Load fail if its context ended while it waited.
	respectCtx bool

	// leakyStop is a stop function that forgets to stop anything.
	leakyStop bool

	watchCtx atomic.Pointer[context.Context]
	onChange atomic.Pointer[func()]

	// watchEndedBeforeClose records, at the moment Close ran, whether the
	// context its watch was given had already ended.
	watchEndedBeforeClose atomic.Bool
}

func newHeldClient(id string, ev *events) *heldClient {
	h := &heldClient{id: id, events: ev}
	h.value.Store("v1")

	return h
}

func (h *heldClient) ID() string { return h.id }

func (h *heldClient) Capabilities() Capabilities { return Capabilities{} }

func (h *heldClient) Load(ctx context.Context, _ []Layer) ([]Layer, error) {
	if h.loadGate != nil {
		h.loadEntered <- struct{}{}
		<-h.loadGate

		if h.respectCtx && ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}

	if h.closed.Load() {
		return nil, errors.New("the client connection is closing")
	}

	return []Layer{{
		Source: Source{Kind: SourceKind("held"), Name: h.id},
		Values: map[string]any{h.id: h.value.Load()},
	}}, nil
}

func (h *heldClient) Watch(ctx context.Context, _ time.Duration, onChange func()) (func(), error) {
	h.watchCtx.Store(&ctx)
	h.onChange.Store(&onChange)
	h.events.add(h.id + ":watch")

	go func() {
		<-ctx.Done()
		h.events.add(h.id + ":watch-ctx-done")
	}()

	if h.leakyStop {
		return func() {}, nil
	}

	return func() { h.events.add(h.id + ":stop") }, nil
}

func (h *heldClient) Close() error {
	if wc := h.watchCtx.Load(); wc != nil && (*wc).Err() != nil {
		h.watchEndedBeforeClose.Store(true)
	}

	h.events.add(h.id + ":close")
	h.closed.Store(true)

	return h.closeErr
}

func (h *heldClient) gate() {
	h.loadGate = make(chan struct{})
	h.loadEntered = make(chan struct{}, 4)
}

// closer is an io.Closer that records itself.
type closer struct {
	name   string
	events *events
	err    error
	block  chan struct{}
	inside chan struct{}
}

func (c *closer) Close() error {
	if c.block != nil {
		c.inside <- struct{}{}
		<-c.block
	}

	c.events.add(c.name + ":close")

	return c.err
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(time.Millisecond)
	}
}

func receive(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// --- D1: ownership is handed over, never inferred --------------------------

func TestClose_ClosesOnlyWhatItWasHanded(t *testing.T) {
	t.Parallel()

	ev := &events{}
	handed := newHeldClient("handed", ev)
	notHanded := newHeldClient("not-handed", ev)

	s, err := NewStore(context.Background(),
		WithBackend(handed), WithBackend(notHanded), WithCloser(handed))
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if !handed.closed.Load() {
		t.Error("the handed-over backend was not closed")
	}

	if notHanded.closed.Load() {
		t.Error("a closeable backend that was never handed over was closed")
	}
}

// --- D2 / D14: watches stop before anything closes, and the backstop -------

func TestClose_StopsWatchesBeforeClosing(t *testing.T) {
	t.Parallel()

	ev := &events{}
	b := newHeldClient("b", ev)

	s, err := NewStore(context.Background(), WithBackend(b), WithCloser(b))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Watch(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	stop, closed := ev.indexOf("b:stop"), ev.indexOf("b:close")
	if stop < 0 || closed < 0 || stop > closed {
		t.Fatalf("want the watch stopped before the client closed, got %v", ev.all())
	}
}

func TestClose_EndsAWatchWhoseStopForgetsTo(t *testing.T) {
	t.Parallel()

	ev := &events{}
	b := newHeldClient("b", ev)
	b.leakyStop = true

	s, err := NewStore(context.Background(), WithBackend(b), WithCloser(b))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Watch(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if !b.watchEndedBeforeClose.Load() {
		t.Fatal("want the backstop to have ended the watch's context before the closers ran")
	}

	waitFor(t, func() bool { return ev.indexOf("b:watch-ctx-done") >= 0 }, "the leaked watch goroutine to end")
}

func TestWatch_CallerContextStillEndsTheWatch(t *testing.T) {
	t.Parallel()

	ev := &events{}
	b := newHeldClient("b", ev)

	s, err := NewStore(context.Background(), WithBackend(b))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if _, err := s.Watch(ctx); err != nil {
		t.Fatal(err)
	}

	cancel()

	waitFor(t, func() bool { return ev.indexOf("b:watch-ctx-done") >= 0 }, "the caller's ctx to end the watch")
}

func TestClose_AWatchTriggeredReloadInFlightCompletes(t *testing.T) {
	t.Parallel()

	ev := &events{}
	b := newHeldClient("b", ev)

	s, err := NewStore(context.Background(), WithBackend(b), WithCloser(b))
	if err != nil {
		t.Fatal(err)
	}

	var reloadErrs atomic.Int64

	s.OnReloadError(func(error) { reloadErrs.Add(1) })

	notified := make(chan struct{}, 1)

	s.AddObserverFunc(func(Observed) error {
		notified <- struct{}{}

		return nil
	})

	if _, err := s.Watch(context.Background(), WithSettleInterval(0)); err != nil {
		t.Fatal(err)
	}

	b.respectCtx = true
	b.gate()
	b.value.Store("v2")

	onChange := *b.onChange.Load()
	go onChange()

	receive(t, b.loadEntered, "the watch-triggered reload to start loading")

	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()

	waitFor(t, func() bool { return ev.indexOf("b:stop") >= 0 }, "Close to stop the watch")
	close(b.loadGate)

	if err := <-closed; err != nil {
		t.Fatal(err)
	}

	receive(t, notified, "the in-flight reload to notify (D11)")

	if got := s.View().Get("b"); got != "v2" {
		t.Errorf("the in-flight reload did not publish: got %v", got)
	}

	if n := reloadErrs.Load(); n != 0 {
		t.Errorf("a watch-triggered reload in flight at Close reported %d errors", n)
	}
}

// --- D3: wait out work in flight, refuse work queued behind it -------------

func TestClose_WaitsForAReloadInFlightAndRefusesTheOneQueued(t *testing.T) {
	t.Parallel()

	ev := &events{}
	b := newHeldClient("b", ev)

	s, err := NewStore(context.Background(), WithBackend(b), WithCloser(b))
	if err != nil {
		t.Fatal(err)
	}

	var reloadErrs atomic.Int64

	s.OnReloadError(func(error) { reloadErrs.Add(1) })

	if _, err := s.Watch(context.Background()); err != nil {
		t.Fatal(err)
	}

	b.gate()

	first := make(chan error, 1)
	go func() { first <- s.Reload(context.Background()) }()

	receive(t, b.loadEntered, "the first reload to start loading")

	queued := make(chan error, 1)
	go func() { queued <- s.Reload(context.Background()) }()

	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()

	waitFor(t, func() bool { return ev.indexOf("b:stop") >= 0 }, "Close to stop the watch")

	if b.closed.Load() {
		t.Fatal("the client closed while a reload was still loading through it")
	}

	close(b.loadGate)

	if err := <-first; err != nil {
		t.Errorf("the reload in flight failed: %v", err)
	}

	if err := <-queued; !errors.Is(err, ErrStoreClosed) {
		t.Errorf("the queued reload: want ErrStoreClosed, got %v", err)
	}

	if err := <-closed; err != nil {
		t.Fatal(err)
	}

	if n := reloadErrs.Load(); n != 0 {
		t.Errorf("want nothing published to OnReloadError, got %d", n)
	}
}

func TestClose_ABlockedCloserDoesNotBlockLockingReads(t *testing.T) {
	t.Parallel()

	ev := &events{}
	c := &closer{name: "slow", events: ev, block: make(chan struct{}), inside: make(chan struct{}, 1)}

	s, err := NewStore(context.Background(),
		WithReaders(NamedSource{Name: "defaults", Content: []byte("a: 1\n")}), WithCloser(c))
	if err != nil {
		t.Fatal(err)
	}

	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()

	receive(t, c.inside, "the closer to start")

	read := make(chan struct{})

	go func() {
		_, _ = s.Plan(Set("a", 2))
		_ = s.Sources()
		_ = s.WritableTargets()

		close(read)
	}()

	receive(t, read, "Plan, Sources and WritableTargets while a closer is blocked")

	close(c.block)

	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

// --- D4: a closed Store still answers reads --------------------------------

func closedHeldStore(t *testing.T) (*Store, *events, *atomic.Int64) {
	t.Helper()

	ev := &events{}
	b := newHeldClient("b", ev)

	s, err := NewStore(context.Background(),
		WithBackend(b), WithReaders(NamedSource{Name: "defaults", Content: []byte("a: 1\n")}), WithCloser(b))
	if err != nil {
		t.Fatal(err)
	}

	reloadErrs := &atomic.Int64{}

	s.OnReloadError(func(error) { reloadErrs.Add(1) })

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	return s, ev, reloadErrs
}

func TestClose_ReadsStillWork(t *testing.T) {
	t.Parallel()

	s, _, _ := closedHeldStore(t)

	if got := s.View().Get("b"); got != "v1" {
		t.Errorf("View after Close: got %v", got)
	}

	if s.Snapshot() == nil || len(s.Sources()) == 0 {
		t.Error("Snapshot or Sources stopped answering after Close")
	}

	if _, err := s.Plan(Set("a", 2)); errors.Is(err, ErrStoreClosed) {
		t.Error("Plan refused after Close")
	}
}

func TestClose_LoadingWritingAndWatchingRefuse(t *testing.T) {
	t.Parallel()

	s, ev, reloadErrs := closedHeldStore(t)

	refusals := map[string]error{
		"Reload":   s.Reload(context.Background()),
		"AddLayer": s.AddLayer(context.Background(), "late", strings.NewReader("late: 1\n")),
	}

	_, refusals["Apply"] = s.Apply(context.Background(), Set("a", 2))
	_, refusals["Watch"] = s.Watch(context.Background())

	for name, err := range refusals {
		if !errors.Is(err, ErrStoreClosed) {
			t.Errorf("%s after Close: want ErrStoreClosed, got %v", name, err)
		}
	}

	if n := reloadErrs.Load(); n != 0 {
		t.Errorf("a refusal after Close was published to OnReloadError %d times", n)
	}

	if slices.Contains(ev.all(), "b:watch") {
		t.Error("Watch after Close started a backend watch on a closed client")
	}
}

// --- D5: idempotent, reverse order, every closer runs ----------------------

func TestClose_ReverseOrderJoinedErrorsAndIdempotent(t *testing.T) {
	t.Parallel()

	ev := &events{}
	failure := errors.New("b failed")
	a := &closer{name: "a", events: ev}
	b := &closer{name: "b", events: ev, err: failure}
	c := &closer{name: "c", events: ev}

	s, err := NewStore(context.Background(),
		WithReaders(NamedSource{Name: "defaults", Content: []byte("a: 1\n")}),
		WithCloser(a), WithCloser(b), WithCloser(c))
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Close(); !errors.Is(err, failure) {
		t.Errorf("want the closer's error joined in, got %v", err)
	}

	if got, want := ev.all(), []string{"c:close", "b:close", "a:close"}; !slices.Equal(got, want) {
		t.Errorf("close order: got %v, want %v", got, want)
	}

	if err := s.Close(); err != nil {
		t.Errorf("second Close: want nil, got %v", err)
	}

	if n := len(ev.all()); n != 3 {
		t.Errorf("second Close closed something again: %v", ev.all())
	}
}

// --- D6: the Store owns a closer from the moment NewStore is called --------

func TestNewStore_FailingWithNilClosesWhatItWasHanded(t *testing.T) {
	t.Parallel()

	ev := &events{}
	c := &closer{name: "c", events: ev}

	s, err := NewStore(context.Background(),
		WithReaders(NamedSource{Name: "broken", Content: []byte("a: [unclosed\n")}), WithCloser(c))
	if err == nil || s != nil {
		t.Fatalf("want a nil Store and an error, got %v, %v", s, err)
	}

	if ev.indexOf("c:close") < 0 {
		t.Error("NewStore returned nil without closing what it was handed")
	}

	ev2 := &events{}
	c2 := &closer{name: "c2", events: ev2}

	if _, err := NewStore(context.Background(), WithCloser(c2)); !errors.Is(err, ErrNoSources) {
		t.Fatalf("want ErrNoSources, got %v", err)
	}

	if ev2.indexOf("c2:close") < 0 {
		t.Error("NewStore failing with ErrNoSources did not close what it was handed")
	}
}

func TestNewStore_InvalidConfigTransfersOwnership(t *testing.T) {
	t.Parallel()

	schema, err := NewSchema(WithStructSchema(struct {
		Name string `config:"name" validate:"required"`
	}{}))
	if err != nil {
		t.Fatal(err)
	}

	ev := &events{}
	c := &closer{name: "c", events: ev}

	s, err := NewStore(context.Background(),
		WithReaders(NamedSource{Name: "defaults", Content: []byte("other: 1\n")}),
		WithSchema(schema), WithCloser(c))
	if !errors.Is(err, ErrInvalidConfig) || s == nil {
		t.Fatalf("want a Store alongside ErrInvalidConfig, got %v, %v", s, err)
	}

	if ev.indexOf("c:close") >= 0 {
		t.Fatal("NewStore closed a closer it handed back a Store for")
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if ev.indexOf("c:close") < 0 {
		t.Error("the returned Store did not own the closer")
	}
}

// --- D7 / D12: nested stores -----------------------------------------------

func TestClose_OuterLeavesANestedInnerOpen(t *testing.T) {
	t.Parallel()

	ev := &events{}
	b := newHeldClient("b", ev)

	inner, err := NewStore(context.Background(), WithBackend(b), WithCloser(b))
	if err != nil {
		t.Fatal(err)
	}

	outer, err := NewStore(context.Background(), WithBackend(Nested(inner, "base")))
	if err != nil {
		t.Fatal(err)
	}

	if err := outer.Close(); err != nil {
		t.Fatal(err)
	}

	if b.closed.Load() {
		t.Fatal("closing the outer Store closed the inner one's client")
	}

	if err := inner.Reload(context.Background()); err != nil {
		t.Errorf("the inner Store stopped reloading: %v", err)
	}

	stop, err := inner.Watch(context.Background())
	if err != nil {
		t.Errorf("the inner Store stopped watching: %v", err)
	} else {
		stop()
	}
}

func TestClose_AClosedInnerFailsTheOuterLoadAndAWatchingOuterNotices(t *testing.T) {
	t.Parallel()

	ev := &events{}
	b := newHeldClient("b", ev)

	inner, err := NewStore(context.Background(), WithBackend(b), WithCloser(b))
	if err != nil {
		t.Fatal(err)
	}

	outer, err := NewStore(context.Background(), WithBackend(Nested(inner, "base")))
	if err != nil {
		t.Fatal(err)
	}

	reported := make(chan error, 4)

	outer.OnReloadError(func(err error) { reported <- err })

	stop, err := outer.Watch(context.Background(), WithPollInterval(5*time.Millisecond), WithSettleInterval(0))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	if err := inner.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-reported:
		if !errors.Is(err, ErrStoreClosed) {
			t.Errorf("want ErrStoreClosed reported, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a watching outer Store never noticed its inner was closed")
	}

	if got := outer.View().Get("b"); got != "v1" {
		t.Errorf("the outer did not keep its last good configuration: got %v", got)
	}

	if err := outer.Reload(context.Background()); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("an explicit outer reload: want ErrStoreClosed, got %v", err)
	}
}

func TestClose_APinnedWriteToAClosedInnerIsRefused(t *testing.T) {
	t.Parallel()

	global := innerStore(t, map[string]string{"global.yaml": "theme: dark\n"}, "global.yaml")

	outer, err := NewStore(context.Background(), WithBackend(Nested(global, "global", NestedPromotable)))
	if err != nil {
		t.Fatal(err)
	}

	if err := global.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := outer.Apply(context.Background(), Set("theme", "light", To("global.yaml"))); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("a write pinned into a closed inner store: want ErrStoreClosed, got %v", err)
	}
}

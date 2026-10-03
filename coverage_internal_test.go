package config

import (
	"context"
	"sync/atomic"
	"testing"
)

// countingWatcher records how many watches it was asked to start.
type countingWatcher struct{ started atomic.Int64 }

func (w *countingWatcher) Watch(context.Context, []string, func()) (func(), error) {
	w.started.Add(1)

	return func() {}, nil
}

// A native watch's event loop can degrade to polling at the same moment its
// stop runs. Calling the two in that order is the interleaving that leaked a
// poller nobody would stop. go/config#13.
func TestCoverage_DegradingAfterStopStartsNothing(t *testing.T) {
	t.Parallel()

	poll := &countingWatcher{}
	cover := &coverage{poll: poll, ctx: context.Background(), onChange: func() {}}

	cover.stop()

	if err := cover.ensurePolled([]string{"/app.yaml"}); err != nil {
		t.Fatalf("ensurePolled after stop: %v", err)
	}

	if n := poll.started.Load(); n != 0 {
		t.Fatalf("ensurePolled after stop started %d poll watch(es) that nothing will stop", n)
	}
}

// stoppingWatcher runs the coverage's stop while its own Watch is starting,
// which is the narrower window: the check before starting has already passed.
type stoppingWatcher struct {
	cover   *coverage
	stopped atomic.Bool
}

func (w *stoppingWatcher) Watch(context.Context, []string, func()) (func(), error) {
	w.cover.stop()

	return func() { w.stopped.Store(true) }, nil
}

func TestCoverage_StoppingWhilePollStartsStopsIt(t *testing.T) {
	t.Parallel()

	poll := &stoppingWatcher{}
	cover := &coverage{poll: poll, ctx: context.Background(), onChange: func() {}}
	poll.cover = cover

	if err := cover.ensurePolled([]string{"/app.yaml"}); err != nil {
		t.Fatalf("ensurePolled: %v", err)
	}

	if !poll.stopped.Load() {
		t.Fatal("a poll started as the coverage stopped was left running")
	}
}

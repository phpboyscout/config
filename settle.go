package config

import (
	"sync"
	"time"
)

// DefaultSettleInterval is how long a burst of foreign change reports is
// allowed to settle before the Store reloads.
//
// It exists because a logical configuration change is not always one
// filesystem event. A deploy replacing two overlays, a config-management run
// rewriting a directory, an operator saving two files — each produces separate
// events, and nothing in the filesystem says they were meant as one change.
// Without a settle window observers run once per file, and the first run sees
// a combination nobody intended: the first file updated, the second not yet.
//
// 250ms is inherited from the pre-Store implementation, where it was chosen to
// tolerate slow and networked filesystems. It is long enough to absorb the
// writes of a single deploy step and short enough to be imperceptible in the
// reload latency of a long-running service.
const DefaultSettleInterval = 250 * time.Millisecond

// settleBurstFactor bounds how long settling may defer a reload, as a multiple
// of the settle interval.
//
// A pure "wait for quiet" window never fires while changes keep arriving
// faster than the window. For a file rewritten continuously that means
// observers are told nothing at all, which is the silent-failure mode this
// module refuses everywhere else. The bound guarantees a reload within
// settleBurstFactor × interval of the first change in a burst, however long
// the burst runs.
const settleBurstFactor = 4

// settler coalesces a burst of change reports into one action.
//
// It is deliberately only used for *foreign* changes. A write this Store
// performs is exactly-once by construction — Apply knows the extent of its own
// batch and notifies directly — so it never comes near this timer. Timing is
// the last resort, used only where there is genuinely no other information:
// the filesystem reports that files changed, and not that they changed
// together.
//
// This reduces the problem rather than removing it. A change spanning several
// files is only atomic if whoever makes it makes it atomically; writes spaced
// further apart than the window will still be seen as separate changes. The
// guarantee lives with the writer, not here.
type settler struct {
	action func()

	mu      sync.Mutex
	timer   *time.Timer
	first   time.Time
	window  time.Duration
	stopped bool
}

// newSettler returns a settler invoking action once a burst has settled. A
// window of zero or less runs the action immediately, which is what an
// injected watcher in a test wants.
func newSettler(window time.Duration, action func()) *settler {
	return &settler{window: window, action: action}
}

// trigger reports that something may have changed.
//
// Safe to call from several watcher goroutines at once: each call extends the
// window, and exactly one action runs when it expires.
func (s *settler) trigger() {
	if s.window <= 0 {
		s.action()

		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return
	}

	now := time.Now()

	if s.timer == nil {
		s.first = now
		s.timer = time.AfterFunc(s.window, s.fire)

		return
	}

	// Extend the window, but never past the bound measured from the first
	// change in this burst.
	wait := s.window
	if deadline := s.first.Add(s.window * settleBurstFactor); now.Add(wait).After(deadline) {
		wait = max(0, deadline.Sub(now))
	}

	s.timer.Reset(wait)
}

// fire runs the action once the burst has settled.
func (s *settler) fire() {
	s.mu.Lock()

	if s.stopped {
		s.mu.Unlock()

		return
	}

	// Cleared before the action runs, so a change arriving *during* the reload
	// starts a fresh burst rather than being folded into one already in
	// progress and lost.
	s.timer = nil
	s.first = time.Time{}

	s.mu.Unlock()

	// Outside the lock: the action reloads and notifies observers, which may
	// take arbitrarily long and must not block a concurrent trigger.
	s.action()
}

// stop abandons any pending action. Safe to call more than once.
func (s *settler) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stopped = true

	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

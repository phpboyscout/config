package config

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ErrWatchUnavailable is returned when watching cannot work for the given
// sources. It is deliberately loud: a watcher that silently does nothing is
// worse than none, because the application believes it will hear about changes
// and never will.
var ErrWatchUnavailable = errors.New("config: cannot watch sources")

// DefaultPollInterval is how often the polling watcher checks for changes when
// the filesystem cannot notify.
const DefaultPollInterval = 2 * time.Second

// Watcher reports that watched paths may have changed.
//
// It reports *possible* change rather than actual change: filesystem
// notification is noisy, delivering events for permission changes, atomic
// renames and editors writing in several passes. Deciding whether anything
// really changed belongs to the Store, which can compare configurations.
type Watcher interface {
	// Watch begins watching, calling onChange when the paths may have changed.
	// The returned function stops watching and releases resources.
	Watch(ctx context.Context, paths []string, onChange func()) (stop func(), err error)
}

// NewWatcher returns the watcher appropriate to a filesystem.
//
// Native notification is used where it works and polling everywhere else. The
// distinction is not cosmetic: fsnotify operates on real paths, so it silently
// fails on an in-memory filesystem — which is exactly what a test, or a tool
// working over a virtual worktree, will be using.
func NewWatcher(filesystem FS, interval time.Duration) Watcher {
	if interval <= 0 {
		interval = DefaultPollInterval
	}

	poll := &pollWatcher{fs: filesystem, interval: interval}

	resolve, ok := realPathResolver(filesystem)
	if !ok {
		return poll
	}

	return &fsnotifyWatcher{fs: filesystem, resolve: resolve, fallback: poll}
}

// realPathResolver returns a function translating a filesystem's paths into
// the ones the operating system would know them by, and whether such a
// translation exists at all.
//
// The filesystem answers this about itself through [RealPather]. It used to be
// a switch on concrete afero types, which could not see through a wrapper it
// did not recognise — so a consumer decorating the real filesystem with their
// own type silently lost native notification and was polled instead, with
// nothing to say why. See D2 of the filesystem abstraction spec.
//
// What it reports is a claim rather than proof. isReallyOnDisk settles it per
// path at watch time, which is the only point where the question can be
// answered honestly.
func realPathResolver(filesystem FS) (func(string) (string, bool), bool) {
	pather, ok := filesystem.(RealPather)
	if !ok {
		// No operating-system path to offer, so there is nothing for native
		// notification to watch. Polling always works; guessing does not.
		return nil, false
	}

	// Passed through rather than wrapped. An earlier version collapsed this to
	// func(string) string, mapping "no real path" onto "use the path as given"
	// — which then cost two Stat calls in isReallyOnDisk to rediscover what the
	// filesystem had already said. A filesystem can have real paths for some
	// names and not others, and it needs to be able to say so.
	return pather.RealPath, true
}

// fsnotifyWatcher uses operating-system notification, falling back to polling
// when it cannot be established.
type fsnotifyWatcher struct {
	// onError, when set, is told when watching degrades and the fallback cannot
	// be established either. Without it the failure would pass silently, which
	// is the one outcome this design refuses.
	onError func(error)

	// fs is the filesystem the caller's paths are expressed in, used to confirm
	// that a resolved path really is the same file.
	fs FS
	// resolve translates a path into the one the operating system knows,
	// reporting false when this filesystem has none for that name.
	resolve  func(string) (string, bool)
	fallback Watcher
}

// register points the watcher at every path it can, returning how many it took
// and which it could not.
//
// Paths the operating system cannot watch are collected rather than dropped. A
// configured file that does not exist yet is the ordinary case — the overlay a
// user gets the first time they change a setting — and reporting success while
// silently never watching it is the failure this design exists to prevent.
func (w *fsnotifyWatcher) register(watcher *fsnotify.Watcher, paths []string) (int, []string) {
	watched := 0

	var unwatchable []string

	for _, p := range paths {
		real, hasReal := w.resolve(p)

		// A filesystem saying it has no real path for this name is answered
		// without touching the disk; isReallyOnDisk would spend two Stat calls
		// reaching the same conclusion.
		if !hasReal || !w.isReallyOnDisk(p, real) {
			unwatchable = append(unwatchable, p)

			continue
		}

		if err := watcher.Add(real); err != nil {
			unwatchable = append(unwatchable, p)

			continue
		}

		watched++
	}

	return watched, unwatchable
}

// isReallyOnDisk reports whether a path seen through the filesystem is the same
// file the operating system has at the resolved location.
//
// Path translation alone is not proof. A base-path wrapper over an in-memory
// filesystem produces real-looking paths for files that exist only in memory,
// and if some unrelated file happens to sit at that location, watching it would
// report changes to entirely the wrong file. Comparing what both sides see
// settles it.
func (w *fsnotifyWatcher) isReallyOnDisk(virtual, real string) bool {
	seen, err := w.fs.Stat(virtual)
	if err != nil {
		return false
	}

	actual, err := os.Stat(real)
	if err != nil {
		return false
	}

	return os.SameFile(seen, actual) ||
		(seen.Size() == actual.Size() && seen.ModTime().Equal(actual.ModTime()))
}

func (w *fsnotifyWatcher) Watch(ctx context.Context, paths []string, onChange func()) (func(), error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		// Descriptor exhaustion is the common cause and is a property of the
		// host rather than the code, so degrade to polling instead of failing.
		return w.fallback.Watch(ctx, paths, onChange)
	}

	watched, unwatchable := w.register(watcher, paths)

	if watched == 0 {
		// Nothing could be watched — the paths may not exist yet. Polling
		// handles that case, so use it rather than pretending to watch.
		_ = watcher.Close()

		return w.fallback.Watch(ctx, paths, onChange)
	}

	// Coverage is one idea with two moments: paths that cannot be watched
	// natively now, and paths whose native watch stops being trustworthy later.
	// Both are answered by polling them, and writing that twice let the two
	// disagree — the construction-time gap failed loudly while the runtime one
	// returned quietly, leaving the caller holding a stop function and nothing
	// watching.
	cover := &coverage{poll: w.fallback, ctx: ctx, onChange: onChange}

	if len(unwatchable) > 0 {
		if err := cover.ensurePolled(unwatchable); err != nil {
			// Reporting success would tell the caller the whole set is covered
			// while a path is silently uncovered, which is the failure this
			// design exists to prevent.
			_ = watcher.Close()

			return nil, err
		}
	}

	done := make(chan struct{})

	var once sync.Once

	release := func() {
		once.Do(func() {
			close(done)

			cover.stop()

			_ = watcher.Close()
		})
	}

	// degrade takes over when native notification stops being trustworthy:
	// polling is slower but it cannot silently stop working, which is the
	// property that matters here. A failure to establish it is reported the
	// same way as at construction, rather than passing quietly.
	degrade := func() {
		if err := cover.ensurePolled(paths); err != nil && w.onError != nil {
			w.onError(err)
		}
	}

	// The consumer releases too. Cancelling the context is the natural way to
	// shut down — it is why consumeEvents watches ctx.Done at all — but only
	// the returned stop function used to close the watcher, so a cancelled
	// Watch leaked its inotify handle and left fsnotify's sender goroutine
	// blocked on a channel nothing was draining.
	go func() {
		defer release()

		consumeEvents(ctx, watcher, done, onChange, degrade)
	}()

	return release, nil
}

// coverage tracks which paths are being polled, so the same paths are never
// polled twice and every stop is released exactly once.
type coverage struct {
	poll     Watcher
	ctx      context.Context
	onChange func()

	mu     sync.Mutex
	polled map[string]bool
	stops  []func()
}

// ensurePolled starts polling any of the given paths not already covered.
//
// Merging rather than adding: a path polled at construction because it could
// not be watched natively must not be polled a second time when the rest of the
// set degrades, or every change to it would be reported twice.
func (c *coverage) ensurePolled(paths []string) error {
	c.mu.Lock()

	if c.polled == nil {
		c.polled = map[string]bool{}
	}

	var fresh []string

	for _, p := range paths {
		if !c.polled[p] {
			c.polled[p] = true

			fresh = append(fresh, p)
		}
	}

	c.mu.Unlock()

	if len(fresh) == 0 {
		return nil
	}

	stop, err := c.poll.Watch(c.ctx, fresh, c.onChange)
	if err != nil {
		c.mu.Lock()

		for _, p := range fresh {
			delete(c.polled, p)
		}

		c.mu.Unlock()

		return err
	}

	c.mu.Lock()
	c.stops = append(c.stops, stop)
	c.mu.Unlock()

	return nil
}

func (c *coverage) stop() {
	c.mu.Lock()
	stops := c.stops
	c.stops = nil
	c.mu.Unlock()

	for _, s := range stops {
		s()
	}
}

// consumeEvents forwards filesystem events until the context or the watcher
// ends.
func consumeEvents(
	ctx context.Context,
	watcher *fsnotify.Watcher,
	done <-chan struct{},
	onChange func(),
	degrade func(),
) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}

			if !rewatch(watcher, event) {
				// The watch could not be re-established — a re-add can lose the
				// race with the rename that triggered it — so the path is
				// watched by nothing from here. Degrade rather than go quiet
				// for the life of the process.
				onChange()
				degrade()

				return
			}

			onChange()
		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}

			// The kernel drops events when its queue overflows, and a watch
			// descriptor can be invalidated outright. Discarding that left the
			// application believing it would hear about changes when it would
			// not — so the watch is abandoned here and the caller's degrade
			// callback takes over.
			degrade()

			return
		}
	}
}

// rewatch re-establishes a watch after an atomic save.
//
// Saving by writing a temporary file and renaming it replaces the inode, so
// the original watch is left pointing at a file nothing will write to again
// and every later save goes unnoticed.
func rewatch(watcher *fsnotify.Watcher, event fsnotify.Event) bool {
	if event.Op&(fsnotify.Rename|fsnotify.Remove) == 0 {
		return true
	}

	_ = watcher.Remove(event.Name)

	// A re-add can lose a race with the rename that triggered it, and the path
	// is then watched by nothing. Reporting the failure lets the caller fall
	// back rather than believing it is still being told about saves.
	return watcher.Add(event.Name) == nil
}

// pollWatcher checks for changes on an interval.
//
// It works over any filesystem, including in-memory ones, and over network
// mounts where native notification is unreliable or absent.
type pollWatcher struct {
	fs       FS
	interval time.Duration
}

func (w *pollWatcher) Watch(ctx context.Context, paths []string, onChange func()) (func(), error) {
	if len(paths) == 0 {
		return func() {}, fmt.Errorf("%w: nothing to watch", ErrWatchUnavailable)
	}

	done := make(chan struct{})

	var once sync.Once

	// The baseline is taken before Watch returns. Sampling it inside the
	// goroutine would leave a window between the caller being told watching
	// has started and the first sample being taken — a change landing in that
	// window would become the baseline and never be reported.
	state := w.sample(paths)

	go func() {
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				next := w.sample(paths)
				if !maps.Equal(state, next) {
					state = next

					onChange()
				}
			}
		}
	}()

	return func() { once.Do(func() { close(done) }) }, nil
}

// sample records a content hash per path. A file that does not exist is
// recorded as absent, so it appearing later counts as a change.
//
// Hashing rather than comparing size and modification time, for the reason the
// write path already gives for the same choice: timestamp granularity varies
// and is coarse on some filesystems, so an edit that preserves a file's length
// within one tick — changing "info" to "warn", say — is invisible to a
// stat-based comparison. Configuration files are small, and a missed change is
// the failure this watcher exists to prevent.
func (w *pollWatcher) sample(paths []string) map[string]string {
	state := make(map[string]string, len(paths))

	for _, p := range paths {
		content, err := w.fs.ReadFile(p)
		if err != nil {
			state[p] = "absent"

			continue
		}

		sum := sha256.Sum256(content)
		state[p] = string(sum[:])
	}

	return state
}

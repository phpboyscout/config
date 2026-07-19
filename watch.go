package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/afero"
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
func NewWatcher(filesystem afero.Fs, interval time.Duration) Watcher {
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
// A base-path wrapper qualifies even though afero will not say what it wraps.
// Rooting a tool at a project directory that way is common, and treating it as
// unwatchable would downgrade those consumers to polling for no reason. What it
// wraps is settled per path at watch time by isReallyOnDisk, which is the only
// point where the question can be answered honestly.
func realPathResolver(filesystem afero.Fs) (func(string) string, bool) {
	switch f := filesystem.(type) {
	case *afero.OsFs:
		return func(p string) string { return p }, true
	case *afero.BasePathFs:
		return func(p string) string {
			if real, err := f.RealPath(p); err == nil {
				return real
			}

			return p
		}, true
	default:
		// Anything else — an in-memory filesystem, a read-only or caching
		// wrapper that hides what it holds — cannot be assumed to have contents
		// the operating system can see. Polling always works; guessing does not.
		return nil, false
	}
}

// fsnotifyWatcher uses operating-system notification, falling back to polling
// when it cannot be established.
type fsnotifyWatcher struct {
	// fs is the filesystem the caller's paths are expressed in, used to confirm
	// that a resolved path really is the same file.
	fs afero.Fs
	// resolve translates a path into the one the operating system knows.
	resolve  func(string) string
	fallback Watcher
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

	watched := 0

	// Paths the operating system cannot watch are collected rather than
	// dropped. A configured file that does not exist yet is the ordinary case —
	// the overlay a user gets the first time they change a setting — and
	// reporting success while silently never watching it is the failure this
	// design exists to prevent.
	var unwatchable []string

	for _, p := range paths {
		real := w.resolve(p)

		if !w.isReallyOnDisk(p, real) {
			unwatchable = append(unwatchable, p)

			continue
		}

		if err := watcher.Add(real); err != nil {
			unwatchable = append(unwatchable, p)

			continue
		}

		watched++
	}

	if watched == 0 {
		// Nothing could be watched — the paths may not exist yet. Polling
		// handles that case, so use it rather than pretending to watch.
		_ = watcher.Close()

		return w.fallback.Watch(ctx, paths, onChange)
	}

	// Some watchable, some not: watch what can be watched and poll the rest, so
	// the set is covered without downgrading the whole of it.
	stopPolling := func() {}

	if len(unwatchable) > 0 {
		stop, err := w.fallback.Watch(ctx, unwatchable, onChange)
		if err == nil {
			stopPolling = stop
		}
	}

	done := make(chan struct{})

	var once sync.Once

	go consumeEvents(ctx, watcher, done, onChange)

	return func() {
		once.Do(func() {
			close(done)

			stopPolling()

			_ = watcher.Close()
		})
	}, nil
}

// consumeEvents forwards filesystem events until the context or the watcher
// ends.
func consumeEvents(ctx context.Context, watcher *fsnotify.Watcher, done <-chan struct{}, onChange func()) {
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

			rewatch(watcher, event)
			onChange()
		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

// rewatch re-establishes a watch after an atomic save.
//
// Saving by writing a temporary file and renaming it replaces the inode, so
// the original watch is left pointing at a file nothing will write to again
// and every later save goes unnoticed.
func rewatch(watcher *fsnotify.Watcher, event fsnotify.Event) {
	if event.Op&(fsnotify.Rename|fsnotify.Remove) == 0 {
		return
	}

	_ = watcher.Remove(event.Name)
	_ = watcher.Add(event.Name)
}

// pollWatcher checks for changes on an interval.
//
// It works over any filesystem, including in-memory ones, and over network
// mounts where native notification is unreliable or absent.
type pollWatcher struct {
	fs       afero.Fs
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
				if !sameState(state, next) {
					state = next

					onChange()
				}
			}
		}
	}()

	return func() { once.Do(func() { close(done) }) }, nil
}

// sample records size and modification time per path. A file that does not
// exist is recorded as absent, so it appearing later counts as a change.
func (w *pollWatcher) sample(paths []string) map[string]string {
	state := make(map[string]string, len(paths))

	for _, p := range paths {
		info, err := w.fs.Stat(p)
		if err != nil {
			state[p] = "absent"

			continue
		}

		state[p] = fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
	}

	return state
}

func sameState(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if b[k] != v {
			return false
		}
	}

	return true
}

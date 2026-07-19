package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// manualWatcher lets a test decide exactly when a change is reported, so
// watching behaviour can be asserted without waiting on a real filesystem or
// tolerating a flaky sleep.
type manualWatcher struct {
	mu      sync.Mutex
	fire    func()
	stopped bool
}

func (w *manualWatcher) Watch(_ context.Context, _ []string, onChange func()) (func(), error) {
	w.mu.Lock()
	w.fire = onChange
	w.mu.Unlock()

	return func() {
		w.mu.Lock()
		w.stopped = true
		w.mu.Unlock()
	}, nil
}

func (w *manualWatcher) trigger() {
	w.mu.Lock()
	fire := w.fire
	w.mu.Unlock()

	if fire != nil {
		fire()
	}
}

func (w *manualWatcher) wasStopped() bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.stopped
}

type countingObserver struct {
	mu     sync.Mutex
	calls  int
	values []string
}

func (o *countingObserver) Run(cfg Observed) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.calls++
	o.values = append(o.values, cfg.GetString("value"))

	return nil
}

func (o *countingObserver) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.calls
}

func TestWatch_NotifiesOnAForeignChange(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "value: first\n"})
	s := storeOn(t, filesystem, "/app.yaml")

	obs := &countingObserver{}
	s.AddObserver(obs)

	w := &manualWatcher{}

	stop, err := s.Watch(context.Background(), WithWatcher(w))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	defer stop()

	if err := afero.WriteFile(filesystem, "/app.yaml", []byte("value: second\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	w.trigger()

	if got := obs.count(); got != 1 {
		t.Fatalf("observer called %d times, want 1", got)
	}

	if got := s.View().GetString("value"); got != "second" {
		t.Errorf("value = %q, want second", got)
	}
}

// Filesystem notification is noisy: permission changes, atomic renames and
// editors writing in several passes all produce events, and none of them
// alters configuration.
func TestWatch_IgnoresEventsThatChangeNothing(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "value: same\n"})
	s := storeOn(t, filesystem, "/app.yaml")

	obs := &countingObserver{}
	s.AddObserver(obs)

	w := &manualWatcher{}

	stop, err := s.Watch(context.Background(), WithWatcher(w))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	defer stop()

	// Several events, no change behind them.
	for range 5 {
		w.trigger()
	}

	if got := obs.count(); got != 0 {
		t.Errorf("observer called %d times for events that changed nothing", got)
	}

	// Rewriting identical content is still not a change.
	if err := afero.WriteFile(filesystem, "/app.yaml", []byte("value: same\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	w.trigger()

	if got := obs.count(); got != 0 {
		t.Errorf("observer called %d times for identical content", got)
	}
}

// The Store's own writes build the next snapshot directly, so they never come
// back round through the watcher. A cascade is therefore unrepresentable
// rather than something to be detected and broken.
func TestWatch_OwnWritesDoNotCascade(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "value: first\ncount: 0\n"})
	s := storeOn(t, filesystem, "/app.yaml")

	w := &manualWatcher{}

	stop, err := s.Watch(context.Background(), WithWatcher(w))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	defer stop()

	// An observer that reacts to a change by writing more configuration is the
	// shape that would loop.
	var writes int

	var mu sync.Mutex

	s.AddObserverFunc(func(cfg Observed) error {
		mu.Lock()
		defer mu.Unlock()

		writes++
		if writes > 5 {
			return errors.New("cascade")
		}

		_, err := s.Apply(context.Background(), Set("count", cfg.GetInt("count")+1))

		return err
	})

	if _, err := s.Apply(context.Background(), Set("value", "second")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Every event the watcher could deliver now finds the file already
	// matching the published configuration.
	for range 3 {
		w.trigger()
	}

	mu.Lock()
	got := writes
	mu.Unlock()

	if got > 2 {
		t.Errorf("observer ran %d times — a write is cascading", got)
	}
}

// Notification is exactly once per logical change, however many sources it
// touched. A watcher-driven design cannot promise that: it sees N filesystem
// events and has to guess whether they were one edit or several.
func TestWatch_MultiFileApplyNotifiesOnce(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{
		"/base.yaml": "onlybase: 1\n",
		"/over.yaml": "shared: over\n",
	})
	s := storeOn(t, filesystem, "/base.yaml", "/over.yaml")

	obs := &countingObserver{}
	s.AddObserver(obs)

	if _, err := s.Apply(context.Background(),
		Set("onlybase", 2),
		Set("shared", "changed"),
	); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := obs.count(); got != 1 {
		t.Errorf("observer called %d times for one apply touching two files, want 1", got)
	}
}

// A watcher that cannot function must say so. An application that believes it
// will hear about changes and never does is worse off than one that knows it
// must restart.
func TestWatch_FailsLoudlyWithNothingToWatch(t *testing.T) {
	t.Parallel()

	s, err := NewStore(context.Background(), WithReaders(NamedSource{
		Name: "embedded", Content: []byte("a: 1\n"),
	}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := s.Watch(context.Background()); !errors.Is(err, ErrWatchUnavailable) {
		t.Errorf("err = %v, want ErrWatchUnavailable", err)
	}
}

func TestWatch_StopReleasesTheWatcher(t *testing.T) {
	t.Parallel()

	s := storeOn(t, memFS(t, map[string]string{"/app.yaml": "a: 1\n"}), "/app.yaml")

	w := &manualWatcher{}

	stop, err := s.Watch(context.Background(), WithWatcher(w))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	stop()

	if !w.wasStopped() {
		t.Error("stopping the Store did not stop the watcher")
	}
}

// An in-memory filesystem cannot be watched natively, so it must be polled.
// Choosing native notification there is how hot-reload ends up silently dead.
func TestNewWatcher_PicksPollingForNonOSFilesystems(t *testing.T) {
	t.Parallel()

	if _, ok := NewWatcher(afero.NewMemMapFs(), time.Second).(*pollWatcher); !ok {
		t.Error("an in-memory filesystem must be polled, not watched natively")
	}

	if _, ok := NewWatcher(afero.NewOsFs(), time.Second).(*fsnotifyWatcher); !ok {
		t.Error("a real filesystem should use native notification")
	}
}

// Polling has to work: it is the fallback that keeps hot-reload functioning on
// in-memory filesystems, network mounts and hosts out of watch descriptors.
func TestPollWatcher_DetectsAChange(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "a: 1\n"})
	w := &pollWatcher{fs: filesystem, interval: 5 * time.Millisecond}

	changed := make(chan struct{}, 1)

	stop, err := w.Watch(context.Background(), []string{"/app.yaml"}, func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	defer stop()

	if err := afero.WriteFile(filesystem, "/app.yaml", []byte("a: 2\nb: 3\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("polling did not detect a change")
	}
}

func TestPollWatcher_NothingToWatch(t *testing.T) {
	t.Parallel()

	w := &pollWatcher{fs: afero.NewMemMapFs(), interval: time.Millisecond}

	if _, err := w.Watch(context.Background(), nil, func() {}); !errors.Is(err, ErrWatchUnavailable) {
		t.Errorf("err = %v, want ErrWatchUnavailable", err)
	}
}

// Native notification must actually fire, not merely be selected. The poll
// interval here is an hour, so anything that arrives came from the operating
// system.
func TestWatch_NativeNotificationFires(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")

	if err := os.WriteFile(path, []byte("a: 1\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := NewWatcher(afero.NewOsFs(), time.Hour)

	if _, ok := w.(*fsnotifyWatcher); !ok {
		t.Fatalf("watcher for a real filesystem is %T, want native notification", w)
	}

	fired := make(chan struct{}, 4)

	stop, err := w.Watch(context.Background(), []string{path}, func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	defer stop()

	time.Sleep(50 * time.Millisecond)

	if err := os.WriteFile(path, []byte("a: 2\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("native notification did not fire within five seconds")
	}
}

// Rooting a tool at a project directory is common, and it must not silently
// downgrade to polling: the paths are virtual but the files are real.
func TestWatch_BasePathFilesystemUsesNativeNotification(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte("a: 1\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rooted := afero.NewBasePathFs(afero.NewOsFs(), dir)

	w := NewWatcher(rooted, time.Hour)
	if _, ok := w.(*fsnotifyWatcher); !ok {
		t.Fatalf("watcher for a rooted real filesystem is %T, want native notification", w)
	}

	fired := make(chan struct{}, 4)

	stop, err := w.Watch(context.Background(), []string{"/app.yaml"}, func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	defer stop()

	time.Sleep(50 * time.Millisecond)

	// Written through the real path, seen through the virtual one.
	if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte("a: 2\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("a rooted filesystem fell back to polling instead of watching natively")
	}
}

// A base-path wrapper produces real-looking paths for files that exist only in
// memory. Watching the resolved location would report changes to whatever
// unrelated file happens to sit there, so it must be recognised as unwatchable.
func TestWatch_VirtualFilesBehindRealLookingPathsFallBackToPolling(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// The path resolves under a directory that genuinely exists, but the file
	// itself lives only in memory.
	mem := afero.NewMemMapFs()
	if err := afero.WriteFile(mem, "/app.yaml", []byte("a: 1\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte("unrelated: file\n"), 0o600); err != nil {
		t.Fatalf("decoy: %v", err)
	}

	w, ok := NewWatcher(afero.NewBasePathFs(mem, dir), 10*time.Millisecond).(*fsnotifyWatcher)
	if !ok {
		t.Fatal("expected the native watcher to be selected before the per-path check")
	}

	if w.isReallyOnDisk("/app.yaml", filepath.Join(dir, "app.yaml")) {
		t.Error("an in-memory file was mistaken for the unrelated file on disk")
	}
}

package config_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/phpboyscout/go/config"
)

// The four guarantees below are what the documentation claims for observers.
// They are cheap to state and easy to lose one at a time, so each is asserted
// separately — a failure should name which promise broke.

func TestObserver_ABatchNotifiesExactlyOnce(t *testing.T) {
	t.Parallel()

	fsys := config.NewMemFS()
	_ = fsys.WriteFile("/base.yaml", []byte("a: 1\nb: 1\n"), 0o600)
	_ = fsys.WriteFile("/over.yaml", []byte("b: 2\n"), 0o600)

	store, err := config.NewStore(context.Background(),
		config.WithFiles(fsys, "/base.yaml", "/over.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var fires atomic.Int64

	store.AddObserverFunc(func(config.Observed) error {
		fires.Add(1)

		return nil
	})

	// Two changes that route to two different files, in one Apply.
	if _, err := store.Apply(context.Background(),
		config.Set("a", 99), config.Set("b", 99)); err != nil {
		t.Fatal(err)
	}

	if got := fires.Load(); got != 1 {
		t.Errorf("notifications = %d, want exactly 1 for one logical change", got)
	}
}

func TestObserver_ARejectedReloadNotifiesNobody(t *testing.T) {
	t.Parallel()

	fsys := config.NewMemFS()
	_ = fsys.WriteFile("/app.yaml", []byte("port: 8080\n"), 0o600)

	store, err := config.NewStore(context.Background(), config.WithFiles(fsys, "/app.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var (
		fires  atomic.Int64
		errors atomic.Int64
	)

	store.AddObserverFunc(func(config.Observed) error {
		fires.Add(1)

		return nil
	})
	store.OnReloadError(func(error) { errors.Add(1) })

	// Break the file behind the store's back, then reload.
	_ = fsys.WriteFile("/app.yaml", []byte("port: [oops\n"), 0o600)

	if err := store.Reload(context.Background()); err == nil {
		t.Fatal("a broken file was accepted")
	}

	if got := fires.Load(); got != 0 {
		t.Errorf("observers fired %d times for a reload that was rejected", got)
	}

	if got := errors.Load(); got != 1 {
		t.Errorf("reload errors = %d, want 1", got)
	}

	// And the last-known-good value is still being served.
	if got := store.View().GetInt("port"); got != 8080 {
		t.Errorf("port = %d, want the retained 8080", got)
	}
}

func TestObserver_AWriteThatChangesNothingNotifiesNobody(t *testing.T) {
	t.Parallel()

	fsys := config.NewMemFS()
	_ = fsys.WriteFile("/app.yaml", []byte("port: 8080\n"), 0o600)

	store, err := config.NewStore(context.Background(), config.WithFiles(fsys, "/app.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var fires atomic.Int64

	store.AddObserverFunc(func(config.Observed) error {
		fires.Add(1)

		return nil
	})

	// Setting a key to the value it already has.
	if _, err := store.Apply(context.Background(), config.Set("port", 8080)); err != nil {
		t.Fatal(err)
	}

	if got := fires.Load(); got != 0 {
		t.Errorf("observers fired %d times for a write that changed nothing", got)
	}
}

func TestObserver_DeliveryIsNeverOutOfOrder(t *testing.T) {
	t.Parallel()

	fsys := config.NewMemFS()
	_ = fsys.WriteFile("/app.yaml", []byte("n: 0\n"), 0o600)

	store, err := config.NewStore(context.Background(), config.WithFiles(fsys, "/app.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var (
		mu         sync.Mutex
		last       uint64
		inversions int
	)

	store.AddObserverFunc(func(cfg config.Observed) error {
		mu.Lock()
		defer mu.Unlock()

		if v := cfg.Snapshot().Version(); v < last {
			inversions++
		} else {
			last = v
		}

		return nil
	})

	// Hammer concurrently: every writer changes the same key, so each
	// Apply produces a new snapshot and they race to deliver.
	var wg sync.WaitGroup

	for i := range 64 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			_, _ = store.Apply(context.Background(), config.Set("n", i+1))
		}()
	}

	wg.Wait()

	if inversions != 0 {
		t.Errorf("%d observers were handed an older snapshot after a newer one", inversions)
	}
}

// TestObserver_WritesSpacedWiderThanTheWindowStillNotifyTwice documents what
// settling does not fix, so the limit is known rather than discovered.
//
// A settle window coalesces a burst of foreign changes into one reload, which
// covers the ordinary case of a deploy writing several files in quick
// succession. It cannot make a multi-file change atomic: writes spaced further
// apart than the window are indistinguishable from two separate changes,
// because that is exactly what they look like.
//
// The guarantee therefore belongs to whoever writes the files — by writing them
// atomically, or by keeping settings that change together in one file, which is
// always read atomically. This is the residual case the documentation warns
// about.
func TestObserver_WritesSpacedWiderThanTheWindowStillNotifyTwice(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	over := filepath.Join(dir, "over.yaml")

	atomicWriteFile(t, base, "a: 1\n")
	atomicWriteFile(t, over, "b: 1\n")

	store, err := config.NewStore(context.Background(),
		config.WithFiles(config.OS(), base, over))
	if err != nil {
		t.Fatal(err)
	}

	states := make(chan string, 16)

	store.AddObserverFunc(func(cfg config.Observed) error {
		states <- fmt.Sprintf("a=%d b=%d", cfg.GetInt("a"), cfg.GetInt("b"))

		return nil
	})

	// A deliberately short window, so the test does not have to sleep for long
	// to exceed it.
	stop, err := store.Watch(context.Background(),
		config.WithSettleInterval(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	defer stop()

	time.Sleep(300 * time.Millisecond)

	// Two writes further apart than the window: two logical changes as far as
	// anything here can tell.
	atomicWriteFile(t, base, "a: 2\n")
	time.Sleep(600 * time.Millisecond)
	atomicWriteFile(t, over, "b: 2\n")

	var seen []string

	deadline := time.After(5 * time.Second)

	for len(seen) < 2 {
		select {
		case got := <-states:
			seen = append(seen, got)
		case <-deadline:
			t.Fatalf("expected 2 notifications for writes spaced beyond the window, got %d: %v",
				len(seen), seen)
		}
	}

	if seen[0] != "a=2 b=1" {
		t.Errorf("first notification = %q, want the intermediate \"a=2 b=1\"", seen[0])
	}

	if seen[1] != "a=2 b=2" {
		t.Errorf("second notification = %q, want the settled \"a=2 b=2\"", seen[1])
	}
}

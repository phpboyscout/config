package config_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"

	"gitlab.com/phpboyscout/go/config"
)

// The four guarantees below are what the documentation claims for observers.
// They are cheap to state and easy to lose one at a time, so each is asserted
// separately — a failure should name which promise broke.

func TestObserver_ABatchNotifiesExactlyOnce(t *testing.T) {
	t.Parallel()

	fsys := afero.NewMemMapFs()
	_ = afero.WriteFile(fsys, "/base.yaml", []byte("a: 1\nb: 1\n"), 0o600)
	_ = afero.WriteFile(fsys, "/over.yaml", []byte("b: 2\n"), 0o600)

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

	fsys := afero.NewMemMapFs()
	_ = afero.WriteFile(fsys, "/app.yaml", []byte("port: 8080\n"), 0o600)

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
	_ = afero.WriteFile(fsys, "/app.yaml", []byte("port: [oops\n"), 0o600)

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

	fsys := afero.NewMemMapFs()
	_ = afero.WriteFile(fsys, "/app.yaml", []byte("port: 8080\n"), 0o600)

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

	fsys := afero.NewMemMapFs()
	_ = afero.WriteFile(fsys, "/app.yaml", []byte("n: 0\n"), 0o600)

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

// TestObserver_ForeignMultiFileChangeNotifiesPerFile documents a real limit of
// the exactly-once guarantee, so that it is a known property rather than a
// surprise.
//
// Exactly-once is per *logical change as the Store understands it*. An Apply is
// one logical change however many files it touches, because the Store performs
// it and knows the batch. Two files edited by something else are two events,
// and nothing in the filesystem says they were meant as one change — so
// observers are told twice, and the first telling can carry a combination that
// existed on disk but that nobody intended: the first file updated, the second
// not yet.
//
// Each snapshot is still internally coherent — it is a real read of the files
// at a moment in time, never a mixture of two reads. The limit is that a
// logical change spanning several files is not atomic unless whoever makes it
// makes it atomically.
//
// Mitigations, in order of preference: keep settings that change together in
// one file, which is always read atomically; or swap the whole directory
// atomically, as a Kubernetes ConfigMap update does.
func TestObserver_ForeignMultiFileChangeNotifiesPerFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	over := filepath.Join(dir, "over.yaml")

	write := func(path, body string) {
		t.Helper()

		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
	}

	write(base, "a: 1\n")
	write(over, "b: 1\n")

	store, err := config.NewStore(context.Background(),
		config.WithFiles(afero.NewOsFs(), base, over))
	if err != nil {
		t.Fatal(err)
	}

	states := make(chan string, 16)

	store.AddObserverFunc(func(cfg config.Observed) error {
		states <- fmt.Sprintf("a=%d b=%d", cfg.GetInt("a"), cfg.GetInt("b"))

		return nil
	})

	stop, err := store.Watch(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	defer stop()

	// Let the watcher settle before changing anything underneath it.
	time.Sleep(300 * time.Millisecond)

	// Something outside this process updates both files as one intended change,
	// with an ordinary gap between the two writes.
	write(base, "a: 2\n")
	time.Sleep(50 * time.Millisecond)
	write(over, "b: 2\n")

	var seen []string

	deadline := time.After(3 * time.Second)

	for len(seen) < 2 {
		select {
		case s := <-states:
			seen = append(seen, s)
		case <-deadline:
			t.Fatalf("expected 2 notifications for 2 file changes, got %d: %v", len(seen), seen)
		}
	}

	// The documented behaviour: one notification per file, and the first shows
	// the half-applied combination.
	if seen[0] != "a=2 b=1" {
		t.Errorf("first notification = %q, want the intermediate \"a=2 b=1\"", seen[0])
	}

	if seen[1] != "a=2 b=2" {
		t.Errorf("second notification = %q, want the settled \"a=2 b=2\"", seen[1])
	}
}

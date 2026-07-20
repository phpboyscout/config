package config_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"

	"gitlab.com/phpboyscout/go/config"
)

// atomicWriteFile replaces a file in one operation, so a reader can never
// observe it half-written. Tests that measure watching need this: without it
// they measure the writer instead.
func atomicWriteFile(t *testing.T, path, body string) {
	t.Helper()

	tmp := path + ".tmp"

	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// TestSettle_ForeignMultiFileChangeCoalesces is the D8 requirement that a
// logical change spanning several files reaches observers once.
//
// The Store cannot know two file writes were one intended change — nothing in
// the filesystem says so — so this is the one place timing is used. It reduces
// the problem rather than removing it, which is why the window is documented
// and configurable rather than hidden.
func TestSettle_ForeignMultiFileChangeCoalesces(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	over := filepath.Join(dir, "over.yaml")

	atomicWriteFile(t, base, "a: 1\n")
	atomicWriteFile(t, over, "b: 1\n")

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

	stop, err := store.Watch(context.Background(),
		config.WithSettleInterval(300*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	defer stop()

	time.Sleep(300 * time.Millisecond)

	// A deploy updates both files, with an ordinary gap between the writes.
	// Before settling, this produced two notifications and the first showed
	// "a=2 b=1" — a combination nobody intended.
	atomicWriteFile(t, base, "a: 2\n")
	time.Sleep(50 * time.Millisecond)
	atomicWriteFile(t, over, "b: 2\n")

	select {
	case got := <-states:
		if got != "a=2 b=2" {
			t.Errorf("first notification = %q, want the settled \"a=2 b=2\"", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notification arrived")
	}

	// And nothing further: the two writes were one notification, not two.
	select {
	case extra := <-states:
		t.Errorf("a second notification arrived for one logical change: %q", extra)
	case <-time.After(time.Second):
	}
}

// TestSettle_DisabledReloadsPerChange keeps the escape hatch honest: a zero
// window reloads on each report, which is what a test driving an injected
// watcher needs so the trigger and the reload stay in step.
func TestSettle_DisabledReloadsPerChange(t *testing.T) {
	t.Parallel()

	fsys := afero.NewMemMapFs()
	if err := afero.WriteFile(fsys, "/app.yaml", []byte("n: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := config.NewStore(context.Background(), config.WithFiles(fsys, "/app.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var fires atomic.Int64

	store.AddObserverFunc(func(config.Observed) error {
		fires.Add(1)

		return nil
	})

	watcher := &manualWatcher{}

	stop, err := store.Watch(context.Background(),
		config.WithWatcher(watcher), config.WithSettleInterval(0))
	if err != nil {
		t.Fatal(err)
	}

	defer stop()

	// Each trigger reloads synchronously, so the count is exact rather than
	// something to wait for.
	if err := afero.WriteFile(fsys, "/app.yaml", []byte("n: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	watcher.fire()

	if got := fires.Load(); got != 1 {
		t.Fatalf("notifications = %d, want 1", got)
	}

	if err := afero.WriteFile(fsys, "/app.yaml", []byte("n: 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	watcher.fire()

	if got := fires.Load(); got != 2 {
		t.Errorf("notifications = %d, want 2 with settling disabled", got)
	}
}

// manualWatcher hands the test the trigger, so change detection is driven
// deterministically instead of by waiting on a filesystem.
type manualWatcher struct{ onChange func() }

func (w *manualWatcher) Watch(_ context.Context, _ []string, onChange func()) (func(), error) {
	w.onChange = onChange

	return func() {}, nil
}

func (w *manualWatcher) fire() {
	if w.onChange != nil {
		w.onChange()
	}
}

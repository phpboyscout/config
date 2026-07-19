package config_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"
	"github.com/spf13/afero"

	. "gitlab.com/phpboyscout/go/config"
)

// world is the state one scenario operates on.
//
// Each scenario gets a fresh one. Sharing would let an earlier scenario's
// configuration leak into a later one, which is the failure mode that makes a
// suite pass in order and fail in isolation.
type world struct {
	fs    afero.Fs
	store *Store

	// err holds the outcome of the last operation a step performed, so a Then
	// step can assert on it rather than failing at the point of the call.
	err error

	mu sync.Mutex
	// notifications records the configuration each observer run saw, which is
	// what "notified exactly once" is asserted against.
	notifications []map[string]any
	// observerErr captures what a write attempted from inside an observer
	// returned.
	observerErr error
	// deferred receives the result of a write an observer handed to another
	// goroutine.
	deferred chan error

	// reloadErrors collects what OnReloadError was told, which is the separate
	// channel a rejected reload travels on.
	reloadErrors []error
	// schema, when set, is attached to the next store built.
	schema *Schema
	// watcher lets a scenario decide exactly when a change is reported.
	watcher *scriptedWatcher
	// stopWatching releases the watcher at the end of a scenario.
	stopWatching func()
	// section is a typed section bound to the store, for the D10 contract.
	section *ObservedSection[serverSection]
	// applyErrs collects the outcome of concurrent writes.
	applyErrs []error
}

// serverSection is the typed shape the typed-section scenarios bind to. It is
// deliberately small: the contract under test is the binding, not the struct.
type serverSection struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

// scriptedWatcher reports a change exactly when a scenario says so, rather than
// waiting on a real filesystem or tolerating a sleep.
type scriptedWatcher struct {
	mu      sync.Mutex
	fire    func()
	stopped bool
}

func (w *scriptedWatcher) Watch(_ context.Context, _ []string, onChange func()) (func(), error) {
	w.mu.Lock()
	w.fire = onChange
	w.mu.Unlock()

	return func() {
		w.mu.Lock()
		w.stopped = true
		w.mu.Unlock()
	}, nil
}

func (w *scriptedWatcher) trigger() {
	w.mu.Lock()
	fire := w.fire
	w.mu.Unlock()

	if fire != nil {
		fire()
	}
}

func (w *scriptedWatcher) wasStopped() bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.stopped
}

func (w *world) reset() {
	w.fs = afero.NewMemMapFs()
	w.store = nil
	w.err = nil
	w.notifications = nil
	w.observerErr = nil
	w.deferred = make(chan error, 1)
	w.reloadErrors = nil
	w.schema = nil
	w.watcher = nil
	w.stopWatching = nil
	w.section = nil
	w.applyErrs = nil
}

func (w *world) recordReloadError(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.reloadErrors = append(w.reloadErrors, err)
}

func (w *world) record(cfg Observed) {
	w.mu.Lock()
	defer w.mu.Unlock()

	seen := map[string]any{}
	for _, k := range cfg.Keys() {
		seen[k] = cfg.Get(k)
	}

	w.notifications = append(w.notifications, seen)
}

func (w *world) notifyCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return len(w.notifications)
}

// initStoreSteps registers the step definitions for the store features.
func initStoreSteps(ctx *godog.ScenarioContext) {
	w := &world{}

	ctx.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
		w.reset()

		return c, nil
	})

	// --- Given -------------------------------------------------------------

	ctx.Step(`^a config file "([^"]*)" containing:$`, w.aConfigFileContaining)
	ctx.Step(`^a store reading "([^"]*)"$`, w.aStoreReading)
	ctx.Step(`^a store reading "([^"]*)" then "([^"]*)"$`, w.aStoreReadingTwoFiles)
	ctx.Step(`^an observer that records what it is told$`, w.anObserverThatRecords)
	ctx.Step(`^an observer that writes "([^"]*)" while reacting$`, w.anObserverThatWritesWhileReacting)
	ctx.Step(`^an observer that defers its write to another goroutine$`, w.anObserverThatDefersItsWrite)

	// --- When --------------------------------------------------------------

	ctx.Step(`^I set "([^"]*)" to "([^"]*)"$`, w.iSetTo)
	ctx.Step(`^I set "([^"]*)" to "([^"]*)" and "([^"]*)" to "([^"]*)"$`, w.iSetTwoValues)
	ctx.Step(`^I remove "([^"]*)"$`, w.iRemove)
	ctx.Step(`^"([^"]*)" is changed on disk to:$`, w.isChangedOnDiskTo)
	ctx.Step(`^the store reloads$`, w.theStoreReloads)

	// --- Then --------------------------------------------------------------

	ctx.Step(`^the write succeeds$`, w.theWriteSucceeds)
	ctx.Step(`^the write is refused$`, w.theWriteIsRefused)
	ctx.Step(`^"([^"]*)" reads as "([^"]*)"$`, w.readsAs)
	ctx.Step(`^"([^"]*)" comes from "([^"]*)"$`, w.comesFrom)
	ctx.Step(`^"([^"]*)" on disk contains:$`, w.onDiskContains)
	ctx.Step(`^"([^"]*)" on disk does not contain "([^"]*)"$`, w.onDiskDoesNotContain)
	ctx.Step(`^observers were notified (\d+) times?$`, w.observersWereNotified)
	ctx.Step(`^the observer's write was refused$`, w.theObserversWriteWasRefused)
	ctx.Step(`^the deferred write succeeds$`, w.theDeferredWriteSucceeds)

	initLifecycleSteps(ctx, w)
}

// --- Given implementations -------------------------------------------------

func (w *world) aConfigFileContaining(path string, body *godog.DocString) error {
	return afero.WriteFile(w.fs, path, []byte(body.Content+"\n"), 0o644)
}

func (w *world) aStoreReading(path string) error {
	return w.openStore(path)
}

func (w *world) aStoreReadingTwoFiles(base, overlay string) error {
	return w.openStore(base, overlay)
}

func (w *world) openStore(paths ...string) error {
	s, err := NewStore(context.Background(), WithFiles(w.fs, paths...))
	if err != nil {
		return fmt.Errorf("opening a store over %v: %w", paths, err)
	}

	w.store = s

	return nil
}

func (w *world) anObserverThatRecords() error {
	w.store.AddObserverFunc(func(cfg Observed) error {
		w.record(cfg)

		return nil
	})

	return nil
}

func (w *world) anObserverThatWritesWhileReacting(path string) error {
	w.store.AddObserverFunc(func(cfg Observed) error {
		w.record(cfg)

		w.mu.Lock()
		defer w.mu.Unlock()

		_, w.observerErr = w.store.Apply(context.Background(), Set(path, "written-by-observer"))

		return nil
	})

	return nil
}

func (w *world) anObserverThatDefersItsWrite() error {
	var once sync.Once

	w.store.AddObserverFunc(func(cfg Observed) error {
		w.record(cfg)

		value := "derived-from-" + fmt.Sprint(cfg.Get("value"))

		once.Do(func() {
			go func() {
				_, err := w.store.Apply(context.Background(), Set("derived", value))
				w.deferred <- err
			}()
		})

		return nil
	})

	return nil
}

// --- When implementations --------------------------------------------------

func (w *world) iSetTo(path, value string) error {
	_, w.err = w.store.Apply(context.Background(), Set(path, value))

	return nil
}

func (w *world) iSetTwoValues(p1, v1, p2, v2 string) error {
	_, w.err = w.store.Apply(context.Background(),
		Set(p1, v1),
		Set(p2, v2))

	return nil
}

func (w *world) iRemove(path string) error {
	_, w.err = w.store.Apply(context.Background(), Remove(path))

	return nil
}

func (w *world) isChangedOnDiskTo(path string, body *godog.DocString) error {
	return afero.WriteFile(w.fs, path, []byte(body.Content+"\n"), 0o644)
}

func (w *world) theStoreReloads() error {
	w.err = w.store.Reload(context.Background())

	return nil
}

// --- Then implementations --------------------------------------------------

func (w *world) theWriteSucceeds() error {
	if w.err != nil {
		return fmt.Errorf("expected the write to succeed, got: %w", w.err)
	}

	return nil
}

func (w *world) theWriteIsRefused() error {
	if w.err == nil {
		return errors.New("expected the write to be refused, but it succeeded")
	}

	return nil
}

func (w *world) readsAs(path, want string) error {
	if got := w.store.View().GetString(path); got != want {
		return fmt.Errorf("%s reads as %q, want %q", path, got, want)
	}

	return nil
}

func (w *world) comesFrom(path, want string) error {
	src, ok := w.store.View().Origin(path)
	if !ok {
		return fmt.Errorf("%s has no recorded origin", path)
	}

	if !strings.Contains(src.String(), want) {
		return fmt.Errorf("%s comes from %q, want it to name %q", path, src.String(), want)
	}

	return nil
}

func (w *world) onDiskContains(path string, body *godog.DocString) error {
	raw, err := afero.ReadFile(w.fs, path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	for _, line := range strings.Split(strings.TrimSpace(body.Content), "\n") {
		if !strings.Contains(string(raw), strings.TrimSpace(line)) {
			return fmt.Errorf("%s does not contain %q. Full content:\n%s", path, strings.TrimSpace(line), raw)
		}
	}

	return nil
}

func (w *world) onDiskDoesNotContain(path, unwanted string) error {
	raw, err := afero.ReadFile(w.fs, path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	if strings.Contains(string(raw), unwanted) {
		return fmt.Errorf("%s still contains %q:\n%s", path, unwanted, raw)
	}

	return nil
}

func (w *world) observersWereNotified(want int) error {
	if got := w.notifyCount(); got != want {
		return fmt.Errorf("observers were notified %d times, want %d", got, want)
	}

	return nil
}

func (w *world) theObserversWriteWasRefused() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !errors.Is(w.observerErr, ErrWriteFromObserver) {
		return fmt.Errorf("the write attempted from inside the observer returned %w, want ErrWriteFromObserver", w.observerErr)
	}

	return nil
}

func (w *world) theDeferredWriteSucceeds() error {
	// Bounded so a scenario fails with a message rather than hanging the suite.
	select {
	case err := <-w.deferred:
		if err != nil {
			return fmt.Errorf("the deferred write was refused: %w", err)
		}

		return nil
	case <-time.After(5 * time.Second):
		return errors.New("the deferred write never completed")
	}
}

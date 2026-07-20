package config_test

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/cucumber/godog"

	. "gitlab.com/phpboyscout/go/config"
)

// initLifecycleSteps registers steps for reload, concurrency, watching and the
// typed-section contract — the behaviour that spans components and time, where
// a scenario earns its keep over a unit test.
func initLifecycleSteps(ctx *godog.ScenarioContext, w *world) {
	// --- Given -------------------------------------------------------------

	ctx.Step(`^a schema requiring "([^"]*)" to be an? (\w+)$`, w.aSchemaRequiring)
	ctx.Step(`^a store reading "([^"]*)" with that schema$`, w.aStoreWithSchema)
	ctx.Step(`^the reload errors are being collected$`, w.reloadErrorsAreCollected)
	ctx.Step(`^the store is watching$`, w.theStoreIsWatching)
	ctx.Step(`^a typed section bound to "([^"]*)"$`, w.aTypedSectionBoundTo)

	// --- When --------------------------------------------------------------

	ctx.Step(`^a change is reported$`, w.aChangeIsReported)
	ctx.Step(`^watching stops$`, w.watchingStops)
	ctx.Step(`^(\d+) writers set their own key at once$`, w.concurrentWritersSetTheirOwnKey)
	ctx.Step(`^"([^"]*)" is changed behind the store's back to:$`, w.isChangedBehindTheStoresBack)

	// --- Then --------------------------------------------------------------

	ctx.Step(`^the reload is rejected$`, w.theReloadIsRejected)
	ctx.Step(`^the reload succeeds$`, w.theReloadSucceeds)
	ctx.Step(`^the rejection reached the error channel$`, w.theRejectionReachedTheErrorChannel)
	ctx.Step(`^the write is refused as a conflict$`, w.theWriteIsRefusedAsAConflict)
	ctx.Step(`^every write succeeded$`, w.everyWriteSucceeded)
	ctx.Step(`^the watcher was released$`, w.theWatcherWasReleased)
	ctx.Step(`^the section reads host "([^"]*)" and port (\d+)$`, w.theSectionReads)
	ctx.Step(`^the section version is (\d+)$`, w.theSectionVersionIs)
	ctx.Step(`^the section exists$`, w.theSectionExists)
}

// --- Given -----------------------------------------------------------------

// requiredField is the schema shape the reload scenarios validate against.
type requiredField struct {
	Server struct {
		Host string `config:"server.host" validate:"required"`
		Port int    `config:"server.port"`
	}
}

func (w *world) aSchemaRequiring(_, _ string) error {
	schema, err := NewSchema(WithStructSchema(requiredField{}))
	if err != nil {
		return fmt.Errorf("building the schema: %w", err)
	}

	w.schema = schema

	return nil
}

func (w *world) aStoreWithSchema(path string) error {
	s, err := NewStore(context.Background(),
		WithFiles(w.fs, path),
		WithSchema(w.schema))
	if err != nil && !errors.Is(err, ErrInvalidConfig) {
		return fmt.Errorf("opening a store over %s: %w", path, err)
	}

	if s == nil {
		return errors.New("no store was returned, leaving an invalid config unrepairable")
	}

	w.store = s

	return nil
}

func (w *world) reloadErrorsAreCollected() error {
	w.store.OnReloadError(w.recordReloadError)

	return nil
}

func (w *world) theStoreIsWatching() error {
	w.watcher = &scriptedWatcher{}

	stop, err := w.store.Watch(context.Background(), WithWatcher(w.watcher))
	if err != nil {
		return fmt.Errorf("starting to watch: %w", err)
	}

	w.stopWatching = stop

	return nil
}

func (w *world) aTypedSectionBoundTo(key string) error {
	section, err := ObserveSection[serverSection](w.store, key)
	if err != nil {
		return fmt.Errorf("binding the section: %w", err)
	}

	w.section = section

	return nil
}

// --- When ------------------------------------------------------------------

func (w *world) aChangeIsReported() error {
	if w.watcher == nil {
		return errors.New("the store is not watching")
	}

	w.watcher.trigger()

	return nil
}

func (w *world) watchingStops() error {
	if w.stopWatching == nil {
		return errors.New("the store is not watching")
	}

	w.stopWatching()

	return nil
}

// concurrentWritersSetTheirOwnKey runs N writes at once, each on its own key.
// Distinct keys are the point: a lost update shows up as a missing key rather
// than as a value that could plausibly have been overwritten last.
func (w *world) concurrentWritersSetTheirOwnKey(n int) error {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)

	for i := range n {
		wg.Add(1)

		go func() {
			defer wg.Done()

			_, err := w.store.Apply(context.Background(),
				Set(fmt.Sprintf("writer%d", i), i))

			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}()
	}

	wg.Wait()

	w.mu.Lock()
	w.applyErrs = errs
	w.mu.Unlock()

	return nil
}

func (w *world) isChangedBehindTheStoresBack(path string, body *godog.DocString) error {
	return w.fs.WriteFile(path, []byte(body.Content+"\n"), 0o644)
}

// --- Then ------------------------------------------------------------------

func (w *world) theReloadIsRejected() error {
	if w.err == nil {
		return errors.New("the reload was accepted")
	}

	return nil
}

func (w *world) theReloadSucceeds() error {
	if w.err != nil {
		return fmt.Errorf("the reload was rejected: %w", w.err)
	}

	return nil
}

func (w *world) theRejectionReachedTheErrorChannel() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.reloadErrors) == 0 {
		return errors.New("nothing reached OnReloadError")
	}

	return nil
}

func (w *world) theWriteIsRefusedAsAConflict() error {
	if !errors.Is(w.err, ErrConflict) {
		return fmt.Errorf("want ErrConflict, got: %w", w.err)
	}

	return nil
}

func (w *world) everyWriteSucceeded() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	for i, err := range w.applyErrs {
		if err != nil {
			return fmt.Errorf("writer %d failed: %w", i, err)
		}
	}

	return nil
}

func (w *world) theWatcherWasReleased() error {
	if w.watcher == nil {
		return errors.New("the store was never watching")
	}

	if !w.watcher.wasStopped() {
		return errors.New("stopping the store did not release the watcher")
	}

	return nil
}

func (w *world) theSectionReads(host string, port int) error {
	got := w.section.Value()

	if got.Host != host {
		return fmt.Errorf("host = %q, want %q", got.Host, host)
	}

	if got.Port != port {
		return fmt.Errorf("port = %d, want %d", got.Port, port)
	}

	return nil
}

func (w *world) theSectionVersionIs(want int) error {
	if want < 0 {
		return fmt.Errorf("a version cannot be negative: %d", want)
	}

	if got := w.section.Version(); got != uint64(want) {
		return fmt.Errorf("version = %d, want %d", got, want)
	}

	return nil
}

func (w *world) theSectionExists() error {
	if !w.section.Exists() {
		return errors.New("the section does not exist")
	}

	return nil
}

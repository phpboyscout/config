package config_test

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/cucumber/godog"
	"gitlab.com/phpboyscout/go/errors"

	. "gitlab.com/phpboyscout/go/config"
)

// initClosingSteps registers the steps for spec 0013's Store.Close.
func initClosingSteps(ctx *godog.ScenarioContext, w *world) {
	ctx.Step(`^a store reading "([^"]*)" that holds a connection$`, w.aStoreHoldingAConnection)
	ctx.Step(`^the store is closed$`, w.theStoreIsClosed)
	ctx.Step(`^the connection was released$`, w.theConnectionWasReleased)
	ctx.Step(`^the store refuses because it is closed$`, w.theStoreRefusesBecauseItIsClosed)
}

// heldConnection stands in for a client a backend built for itself.
type heldConnection struct{ closed atomic.Bool }

func (c *heldConnection) Close() error {
	c.closed.Store(true)

	return nil
}

func (w *world) aStoreHoldingAConnection(path string) error {
	w.connection = &heldConnection{}

	s, err := NewStore(context.Background(), WithFiles(w.fs, path), WithCloser(w.connection))
	if err != nil {
		return fmt.Errorf("opening a store over %s: %w", path, err)
	}

	w.store = s

	return nil
}

func (w *world) theStoreIsClosed() error {
	if err := w.store.Close(); err != nil {
		return fmt.Errorf("closing the store: %w", err)
	}

	return nil
}

func (w *world) theConnectionWasReleased() error {
	if w.connection == nil || !w.connection.closed.Load() {
		return errors.New("closing the store did not release the connection it was handed")
	}

	return nil
}

func (w *world) theStoreRefusesBecauseItIsClosed() error {
	if !errors.Is(w.err, ErrStoreClosed) {
		return fmt.Errorf("want ErrStoreClosed, got: %w", w.err)
	}

	return nil
}

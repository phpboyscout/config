package config_test

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"
	"gitlab.com/phpboyscout/go/errors"

	. "gitlab.com/phpboyscout/go/config"
)

func initKeyCaseSteps(ctx *godog.ScenarioContext, w *world) {
	ctx.Step(`^a case-sensitive store reading "([^"]*)"$`, w.aCaseSensitiveStoreReading)
	ctx.Step(`^opening a store reading "([^"]*)" fails because two keys collide$`, w.openingFailsOnACollision)
	ctx.Step(`^"([^"]*)" is not set$`, w.isNotSet)
}

func (w *world) aCaseSensitiveStoreReading(path string) error {
	s, err := NewStore(context.Background(), WithCaseSensitiveKeys(), WithFiles(w.fs, path))
	if err != nil {
		return fmt.Errorf("opening a case-sensitive store over %s: %w", path, err)
	}

	w.store = s

	return nil
}

func (w *world) openingFailsOnACollision(path string) error {
	_, err := NewStore(context.Background(), WithFiles(w.fs, path))
	if !errors.Is(err, ErrKeyCollision) {
		return fmt.Errorf("opening a store over %s: err = %w, want ErrKeyCollision", path, err)
	}

	return nil
}

func (w *world) isNotSet(path string) error {
	if w.store.View().IsSet(path) {
		return fmt.Errorf("%s is set to %v, want it unset", path, w.store.View().Get(path))
	}

	return nil
}

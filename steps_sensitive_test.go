package config_test

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cucumber/godog"

	. "gitlab.com/phpboyscout/go/config"
)

// scenarioBackend is a read-only backend standing in for a remote source, with
// its sensitivity a scenario's choice.
//
// It is deliberately the shape a secrets manager has rather than a mock of one:
// read-only, so a write to a key it provides cannot land in it and must route
// somewhere else — which is the whole situation the leak guard exists for.
type scenarioBackend struct {
	name      string
	values    map[string]any
	sensitive bool
}

func (b scenarioBackend) ID() string { return b.name }

func (b scenarioBackend) Capabilities() Capabilities {
	return Capabilities{Sensitive: b.sensitive}
}

func (b scenarioBackend) Load(_ context.Context, _ []Layer) ([]Layer, error) {
	return []Layer{{
		Source: Source{Kind: SourceKind("remote"), Name: b.name, Writable: false},
		Values: b.values,
	}}, nil
}

// initSensitiveSteps registers the steps the sensitive-leak feature needs. They
// live apart from the file-oriented steps because they are the only ones that
// build a store over a backend rather than over files alone.
func initSensitiveSteps(ctx *godog.ScenarioContext, w *world) {
	ctx.Step(`^a sensitive read-only layer providing "([^"]*)" as "([^"]*)"$`,
		w.aSensitiveLayerProviding)
	ctx.Step(`^a read-only layer providing "([^"]*)" as "([^"]*)"$`,
		w.aReadOnlyLayerProviding)
	ctx.Step(`^a store reading "([^"]*)" beneath that layer$`, w.aStoreReadingBeneathThatLayer)
	ctx.Step(`^the write is refused as a sensitive leak$`, w.theWriteIsRefusedAsASensitiveLeak)
}

func (w *world) aSensitiveLayerProviding(path, value string) error {
	return w.declareLayer(path, value, true)
}

func (w *world) aReadOnlyLayerProviding(path, value string) error {
	return w.declareLayer(path, value, false)
}

// declareLayer records the backend a later step builds a store over. It is not
// built here because the store needs both the file and the backend at once, and
// the file comes from a separate Given.
func (w *world) declareLayer(path, value string, sensitive bool) error {
	name := "plain-remote"
	if sensitive {
		name = "vault"
	}

	w.backend = scenarioBackend{
		name:      name,
		values:    nest(strings.Split(path, "."), value),
		sensitive: sensitive,
	}

	return nil
}

// nest turns a dotted path and a value into the tree a layer contributes.
func nest(segs []string, value string) map[string]any {
	if len(segs) == 1 {
		return map[string]any{segs[0]: value}
	}

	return map[string]any{segs[0]: nest(segs[1:], value)}
}

// aStoreReadingBeneathThatLayer builds a store with the file beneath the
// declared backend, so the backend wins the read and a write to a key it
// provides has to route down to the file — the arrangement the guard governs.
func (w *world) aStoreReadingBeneathThatLayer(path string) error {
	store, err := NewStore(context.Background(),
		WithFiles(w.fs, path),  // writable, lower precedence
		WithBackend(w.backend), // read-only, higher precedence
	)
	if err != nil {
		return fmt.Errorf("opening a store over %s beneath %s: %w", path, w.backend.ID(), err)
	}

	w.store = store

	return nil
}

func (w *world) theWriteIsRefusedAsASensitiveLeak() error {
	if w.err == nil {
		return errors.New("expected the write to be refused as a sensitive leak, but it succeeded")
	}

	if !errors.Is(w.err, ErrSensitiveLeak) {
		return fmt.Errorf("expected ErrSensitiveLeak, got: %w", w.err)
	}

	return nil
}

package config_test

import (
	"context"
	"errors"
	"testing"

	"gitlab.com/phpboyscout/go/config"
)

// Attribution is what makes an aggregate report actionable, and every case here
// came from running the thing by hand rather than from a unit test — which is
// the argument for doing that before a release.

// namedSchema knows what it is called, as config-schema's implementation does.
type namedSchema struct{ n string }

func (s namedSchema) Name() string                                                { return s.n }
func (s namedSchema) Validate(*config.Snapshot, string, *config.ValidationResult) {}

func TestRequiredMissing_IsAttributedWhenTheSchemaKnowsItsName(t *testing.T) {
	t.Parallel()

	// This is the case where attribution matters MOST — a component declared
	// configuration it needs and got none — and it was the one failure the
	// report could not attribute, printing "(unattributed)".
	store, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "app", Content: []byte("other: x\n")}),
		config.WithSchemaAt("plugins.cache", namedSchema{n: "cache-plugin"}, config.Required),
	)
	if err != nil && !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("NewStore: %v", err)
	}

	res := store.Validate()
	if res.Valid() {
		t.Fatal("want the missing required section reported")
	}

	if got := res.Errors[0].Contributor; got != "cache-plugin" {
		t.Errorf("Contributor = %q, want cache-plugin — a component that needs "+
			"configuration and got none is exactly who a reader needs named", got)
	}
}

func TestRequiredMissing_FallsBackToThePathWhenTheSchemaIsAnonymous(t *testing.T) {
	t.Parallel()

	// Naming is optional: a Schema need not implement Name. The report must
	// still be usable, so it falls back to the mount point rather than printing
	// nothing.
	anon := anonSchema{}

	store, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "app", Content: []byte("other: x\n")}),
		config.WithSchemaAt("plugins.cache", anon, config.Required),
	)
	if err != nil && !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("NewStore: %v", err)
	}

	if got := store.Validate().Errors[0].Contributor; got != "plugins.cache" {
		t.Errorf("Contributor = %q, want the mount point as a fallback", got)
	}
}

type anonSchema struct{}

func (anonSchema) Validate(*config.Snapshot, string, *config.ValidationResult) {}

func TestMountedSchema_ThatDoesNotAttributeIsNamedFromItsMount(t *testing.T) {
	t.Parallel()

	// The same gap, one level up: the tag-derived schema this package builds
	// reports no Contributor at all, so every failure from one reached the
	// reader anonymous. Attribution belongs to the mount, not to each
	// implementation remembering.
	schema, err := config.NewSchema(config.WithStructSchema(struct {
		Mode string `config:"mode" enum:"lru,lfu"`
	}{}))
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "app", Content: []byte(
			"plugins:\n  cache:\n    mode: nonsense\n")}),
		config.WithSchemaAt("plugins.cache", schema),
	)
	if err != nil && !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("NewStore: %v", err)
	}

	res := store.Validate()
	if res.Valid() {
		t.Fatal("want the enum violation reported")
	}

	if got := res.Errors[0].Contributor; got != "plugins.cache" {
		t.Errorf("Contributor = %q, want the mount point — an aggregate report "+
			"cannot be acted on when it will not say who objected", got)
	}
}

func TestMountedSchema_KeepsAnAttributionTheSchemaSetItself(t *testing.T) {
	t.Parallel()

	// Backfilling must not overwrite: a schema composing several documents
	// knows which one objected, and the mount does not.
	store, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "app", Content: []byte("a: 1\n")}),
		config.WithSchemaAt("a", attributingSchema{}),
	)
	if err != nil && !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("NewStore: %v", err)
	}

	if got := store.Validate().Errors[0].Contributor; got != "inner-document" {
		t.Errorf("Contributor = %q, want the schema's own attribution preserved", got)
	}
}

// attributingSchema names a contributor other than itself, as a schema composing
// several documents would.
type attributingSchema struct{}

func (attributingSchema) Name() string { return "outer" }

func (attributingSchema) Validate(_ *config.Snapshot, at string, r *config.ValidationResult) {
	r.AddError(config.ValidationError{Key: at, Message: "nope", Contributor: "inner-document"})
}

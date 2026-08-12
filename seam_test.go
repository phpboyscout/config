package config_test

import (
	"context"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/go/config"
)

// The seam spec 0010 D7 describes: config defines what a validator IS, and an
// implementation lives outside this module. These tests are written against the
// interface, not against the tag-derived schema, because the whole point is that
// config-schema can supply a different one.
//
// The seam is named Validator rather than made out of Schema deliberately: the
// concrete *Schema keeps its identity, so View.Validate(*Schema) and every
// existing consumer compile unchanged, and *Schema is simply one Validator.

// stubSchema is an out-of-module implementation, standing in for config-schema.
// It fails any key whose value is the string "bad".
type stubSchema struct {
	name string
	// at is where this schema was mounted, recorded so a test can prove the
	// mount prefix reaches the implementation rather than being applied for it.
	sawAt string
}

func (s *stubSchema) Validate(snap *config.Snapshot, at string, r *config.ValidationResult) {
	s.sawAt = at

	for _, key := range snap.Keys() {
		if at != "" && !strings.HasPrefix(key, at+".") {
			continue
		}

		if v, ok := snap.Get(key); ok && v == "bad" {
			r.AddError(config.ValidationError{
				Key:         key,
				Message:     "value is bad",
				Contributor: s.name,
			})
		}
	}
}

func storeWith(t *testing.T, yaml string, opts ...config.StoreOption) *config.Store {
	t.Helper()

	all := append([]config.StoreOption{
		config.WithReaders(config.NamedSource{Name: "test", Content: []byte(yaml)}),
	}, opts...)

	store, err := config.NewStore(context.Background(), all...)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	return store
}

func TestValidatorSeam_AnOutOfModuleImplementationSatisfiesIt(t *testing.T) {
	t.Parallel()

	// The compile-time assertion is the test: if config.Validator's method set
	// changes, this stops building — which is the failure a consumer outside
	// this module would hit.
	var _ config.Validator = (*stubSchema)(nil)
}

func TestValidationError_CarriesTheContributor(t *testing.T) {
	t.Parallel()

	// D3: aggregation is useless without attribution.
	store := storeWith(t, "server:\n  host: bad\n",
		config.WithSchemaAt("server", &stubSchema{name: "server-component"}))

	res := store.Validate()

	if res.Valid() {
		t.Fatal("want a failure, got a clean result")
	}

	if got := res.Errors[0].Contributor; got != "server-component" {
		t.Errorf("Contributor = %q, want %q — a failure nobody owns cannot be acted on",
			got, "server-component")
	}
}

func TestWithSchemaAt_PassesTheMountPrefixToTheImplementation(t *testing.T) {
	t.Parallel()

	// D2: a contribution is written in its own terms and mounted by the
	// assembling code. The implementation has to be TOLD where it sits;
	// applying the prefix for it would stop a schema knowing its own scope.
	stub := &stubSchema{name: "cache"}

	store := storeWith(t, "plugins:\n  cache:\n    ttl: bad\n",
		config.WithSchemaAt("plugins.cache", stub))

	_ = store.Validate()

	if stub.sawAt != "plugins.cache" {
		t.Errorf("implementation saw at=%q, want %q", stub.sawAt, "plugins.cache")
	}
}

func TestStoreValidate_AggregatesEveryContributor(t *testing.T) {
	t.Parallel()

	store := storeWith(t,
		"server:\n  host: bad\nplugins:\n  cache:\n    ttl: bad\n",
		config.WithSchemaAt("server", &stubSchema{name: "server-component"}),
		config.WithSchemaAt("plugins.cache", &stubSchema{name: "cache-plugin"}),
	)

	res := store.Validate()

	if len(res.Errors) != 2 {
		t.Fatalf("want 2 errors from 2 contributors, got %d", len(res.Errors))
	}

	seen := map[string]bool{}
	for _, e := range res.Errors {
		seen[e.Contributor] = true
	}

	for _, want := range []string{"server-component", "cache-plugin"} {
		if !seen[want] {
			t.Errorf("no failure attributed to %q — the point of composing is that "+
				"every contributor gets to speak", want)
		}
	}
}

func TestStoreValidate_ScopesToANamedBranch(t *testing.T) {
	t.Parallel()

	// D14: Store.Validate("branch") checks one subtree.
	store := storeWith(t,
		"server:\n  host: bad\nplugins:\n  cache:\n    ttl: bad\n",
		config.WithSchemaAt("server", &stubSchema{name: "server-component"}),
		config.WithSchemaAt("plugins.cache", &stubSchema{name: "cache-plugin"}),
	)

	res := store.Validate("plugins.cache")

	if len(res.Errors) != 1 {
		t.Fatalf("want only the cache-plugin failure, got %d errors", len(res.Errors))
	}

	if got := res.Errors[0].Contributor; got != "cache-plugin" {
		t.Errorf("Contributor = %q, want cache-plugin — a scoped validate must not "+
			"report contributors mounted elsewhere", got)
	}
}

func TestStoreValidate_CleanConfigurationIsValid(t *testing.T) {
	t.Parallel()

	store := storeWith(t, "server:\n  host: fine\n",
		config.WithSchemaAt("server", &stubSchema{name: "server-component"}))

	if res := store.Validate(); !res.Valid() {
		t.Errorf("want valid, got %v", res.Error())
	}
}

func TestStoreValidate_NoRegisteredSchemasIsValid(t *testing.T) {
	t.Parallel()

	// Nothing registered means nothing to violate. Reporting a failure here
	// would make the call unusable on a store that simply does not use schemas.
	if res := storeWith(t, "a: b").Validate(); !res.Valid() {
		t.Errorf("want valid with no schemas registered, got %v", res.Error())
	}
}

func TestRequired_AnAbsentMountFailsOnlyWhenDeclaredRequired(t *testing.T) {
	t.Parallel()

	// D9, and the finding the spike turned up: a contribution whose subtree is
	// entirely absent says nothing, because a schema constrains a key only when
	// the key is present. That is right for an optional plugin and wrong for a
	// mandatory one, so the mount decides.
	yaml := "server:\n  host: fine\n" // nothing at plugins.cache at all

	optional := storeWith(t, yaml,
		config.WithSchemaAt("plugins.cache", &stubSchema{name: "cache-plugin"}))

	if res := optional.Validate(); !res.Valid() {
		t.Errorf("an absent OPTIONAL mount must be silent, got %v", res.Error())
	}

	mandatory := storeWith(t, yaml,
		config.WithSchemaAt("plugins.cache", &stubSchema{name: "cache-plugin"}, config.Required))

	res := mandatory.Validate()
	if res.Valid() {
		t.Fatal("an absent REQUIRED mount must fail — otherwise a component that " +
			"needs configuration and got none is reported as healthy")
	}

	if got := res.Errors[0].Key; got != "plugins.cache" {
		t.Errorf("Key = %q, want the mount point %q", got, "plugins.cache")
	}
}

func TestSchema_IsItselfAValidator_AndReportsFullPaths(t *testing.T) {
	t.Parallel()

	// D6: the tag-derived schema a consumer already writes is one contribution
	// among several. Its keys are relative to the mount; its REPORT is absolute,
	// because a user needs the key they would edit.
	type cacheConfig struct {
		Mode string `config:"mode" validate:"required" enum:"lru,lfu"`
	}

	schema, err := config.NewSchema(config.WithStructSchema(cacheConfig{}))
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}

	store := storeWith(t, "plugins:\n  cache:\n    mode: nonsense\n",
		config.WithSchemaAt("plugins.cache", schema))

	res := store.Validate()
	if res.Valid() {
		t.Fatal("want an enum failure")
	}

	if got := res.Errors[0].Key; got != "plugins.cache.mode" {
		t.Errorf("Key = %q, want the full path %q — a relative key is not one a "+
			"user could act on", got, "plugins.cache.mode")
	}
}

func TestSchema_TheSameSchemaMountsTwice(t *testing.T) {
	t.Parallel()

	// D2's payoff: a contribution written in its own terms is relocatable, and
	// can describe two sections at once.
	type pluginConfig struct {
		Mode string `config:"mode" enum:"lru,lfu"`
	}

	schema, err := config.NewSchema(config.WithStructSchema(pluginConfig{}))
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}

	store := storeWith(t,
		"plugins:\n  cache:\n    mode: bogus\n  session:\n    mode: alsobogus\n",
		config.WithSchemaAt("plugins.cache", schema),
		config.WithSchemaAt("plugins.session", schema),
	)

	res := store.Validate()

	keys := map[string]bool{}
	for _, e := range res.Errors {
		keys[e.Key] = true
	}

	for _, want := range []string{"plugins.cache.mode", "plugins.session.mode"} {
		if !keys[want] {
			t.Errorf("no failure at %q — one schema must be able to describe two "+
				"sections, or a component cannot be mounted twice", want)
		}
	}
}

func TestValidationError_StringNamesTheContributor(t *testing.T) {
	t.Parallel()

	e := config.ValidationError{Key: "a.b", Message: "boom", Contributor: "widget"}
	if got := e.String(); !strings.Contains(got, "widget") {
		t.Errorf("String() = %q, want it to name the contributor", got)
	}
}

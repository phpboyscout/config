package config_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/go/config"
)

// D10's second surface and D11: a constraint on a SOURCE. It judges only what
// that source actually supplies — a layer is never required to be complete —
// and it can forbid keys outright.
//
// The contrast with config.Filtered is the whole reason it exists and is
// asserted here rather than left to prose: filtering DROPS a key silently and
// reads fall through to a lower layer; forbidding REPORTS it. Applied to a
// credential leaked into a file, filtering hides the very thing you wanted told.

func constrainedStore(t *testing.T, yaml string, opts ...config.ConstraintOption) (*config.Store, error) {
	t.Helper()

	backend := config.NewReaderBackend("suspect", []byte(yaml))

	return config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{
			Name: "base", Content: []byte("keep: yes\n"),
		}),
		config.WithBackend(config.Constrained(backend, opts...)),
	)
}

func TestConstrained_ForbiddenKeyFromASourceIsReported(t *testing.T) {
	t.Parallel()

	store, err := constrainedStore(t,
		"credentials:\n  token: literal-secret\n",
		config.Forbid("credentials.*"))

	if !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("want ErrInvalidConfig for a forbidden key, got %v", err)
	}

	if store == nil {
		t.Fatal("want the store back alongside the error, so a repair tool has something to repair")
	}

	res := store.Validate()
	if res.Valid() {
		t.Fatal("want the violation reported on demand too")
	}

	if got := res.Errors[0].Key; got != "credentials.token" {
		t.Errorf("Key = %q, want the offending key %q", got, "credentials.token")
	}
}

func TestConstrained_ForbidReportsRatherThanHiding(t *testing.T) {
	t.Parallel()

	// The distinction from Filtered, asserted directly. A forbidden key stays
	// VISIBLE — it has to, or a report about it would be describing something
	// the caller cannot see or fix.
	store, _ := constrainedStore(t,
		"credentials:\n  token: literal-secret\n",
		config.Forbid("credentials.*"))

	if got := store.View().GetString("credentials.token"); got != "literal-secret" {
		t.Errorf("credentials.token reads %q — a forbidden key must remain visible. "+
			"Dropping it is what Filtered does, and it would hide the leak this "+
			"exists to report", got)
	}
}

func TestConstrained_KeysItDoesNotForbidPassThrough(t *testing.T) {
	t.Parallel()

	store, err := constrainedStore(t, "safe: value\n", config.Forbid("credentials.*"))
	if err != nil {
		t.Fatalf("a source supplying nothing forbidden must load: %v", err)
	}

	if got := store.View().GetString("safe"); got != "value" {
		t.Errorf("safe = %q, want the value through untouched", got)
	}
}

func TestConstrained_AbsenceIsNeverAFailure(t *testing.T) {
	t.Parallel()

	// D10: a contribution constraint judges what the source SUPPLIES. A layer
	// is never required to be complete, so a key the schema describes and this
	// source does not provide is somebody else's to supply.
	type shape struct {
		Port int `config:"port" validate:"required"`
	}

	schema, err := config.NewSchema(config.WithStructSchema(shape{}))
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}

	// The source supplies nothing at all beyond an unrelated key.
	if _, err := constrainedStore(t, "unrelated: x\n", config.MustMatch(schema)); err != nil {
		t.Errorf("a source that simply does not supply a described key must not fail: %v", err)
	}
}

func TestConstrained_ShapeOfWhatItSuppliesIsChecked(t *testing.T) {
	t.Parallel()

	// ...but what it DOES supply must be right. This is the "Consul handed back
	// port=banana" case, caught at the source rather than after the merge has
	// hidden which layer it came from.
	type shape struct {
		Mode string `config:"mode" enum:"lru,lfu"`
	}

	schema, err := config.NewSchema(config.WithStructSchema(shape{}))
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}

	_, err = constrainedStore(t, "mode: nonsense\n", config.MustMatch(schema))
	if !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("want the bad value refused at the source, got %v", err)
	}
}

func TestConstrained_AWriteToAForbiddenKeyFails(t *testing.T) {
	t.Parallel()

	// OQ4, and the opposite of filtering. config.Filtered routes a write to a
	// denied key PAST the backend (0008 D7) — correct for a visibility bound.
	// A forbidden write that quietly rerouted would be the same leak in a
	// different file, with nothing reported.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte("safe: yes\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	fsys, err := config.Dir(dir)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}

	// A file backend, so the source is genuinely writable and the refusal comes
	// from the constraint rather than from there being nowhere to write.
	plain := config.NewFileBackend(fsys, "app.yaml")

	if _, ok := plain.(config.WritableBackend); !ok {
		t.Fatal("precondition: the file backend should be writable")
	}

	store, err := config.NewStore(context.Background(),
		config.WithBackend(config.Constrained(plain, config.Forbid("credentials.*"))),
	)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	_, err = store.Apply(context.Background(), config.Set("credentials.token", "sneaky"))

	if err == nil {
		t.Fatal("a write to a forbidden key must fail, not reroute")
	}

	if !strings.Contains(strings.ToLower(err.Error()), "forbid") {
		t.Errorf("error should say the key is forbidden, got: %v", err)
	}
}

func TestConstrained_PreservesOptionalBackendCapabilities(t *testing.T) {
	t.Parallel()

	// The capability-honesty rule the filesystem adapters established and
	// Filtered already follows: a decorator must implement exactly the optional
	// interfaces the thing it wraps does, or the Store's behaviour changes
	// merely by constraining a backend.
	// A reader backend is not writable, so testing with one cannot tell a
	// preserved capability from a dropped one — both read false. The pair has to
	// span both cases to mean anything.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte("a: b\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	fsys, err := config.Dir(dir)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}

	for _, tc := range []struct {
		name  string
		inner config.Backend
	}{
		{"writable file backend", config.NewFileBackend(fsys, "app.yaml")},
		{"read-only reader backend", config.NewReaderBackend("plain", []byte("a: b\n"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertSameCapabilities(t, tc.inner, config.Constrained(tc.inner, config.Forbid("x")))
		})
	}
}

func assertSameCapabilities(t *testing.T, plain, wrapped config.Backend) {
	t.Helper()

	_, plainWritable := plain.(config.WritableBackend)
	_, wrappedWritable := wrapped.(config.WritableBackend)

	if plainWritable != wrappedWritable {
		t.Errorf("writable: inner=%v wrapped=%v — capability must not change",
			plainWritable, wrappedWritable)
	}

	_, plainWatchable := plain.(config.WatchableBackend)
	_, wrappedWatchable := wrapped.(config.WatchableBackend)

	if plainWatchable != wrappedWatchable {
		t.Errorf("watchable: inner=%v wrapped=%v — capability must not change",
			plainWatchable, wrappedWatchable)
	}
}

func TestConstrained_AForbiddenWriteHasItsOwnSentinel(t *testing.T) {
	t.Parallel()

	// Found by manual testing. The refusal used to wrap ErrInvalidConfig and
	// read "source supplied a forbidden key" — but the configuration is fine
	// and nothing was supplied. A policy refusal needs its own sentinel so a
	// caller can tell it from a value that failed its shape check.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte("safe: yes\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	fsys, err := config.Dir(dir)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithBackend(config.Constrained(
			config.NewFileBackend(fsys, "app.yaml"), config.Forbid("credentials.*"))),
	)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	_, err = store.Apply(context.Background(), config.Set("credentials.token", "x"))
	if err == nil {
		t.Fatal("want the write refused")
	}

	if !errors.Is(err, config.ErrForbiddenKey) {
		t.Errorf("want ErrForbiddenKey, so a policy refusal is distinguishable "+
			"from an invalid value, got %v", err)
	}

	if strings.Contains(err.Error(), "supplied") {
		t.Errorf("a refused WRITE must not say the key was supplied: %v", err)
	}
}

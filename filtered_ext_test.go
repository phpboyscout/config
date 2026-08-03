package config_test

import (
	"context"
	"testing"

	"gitlab.com/phpboyscout/go/errors"

	"gitlab.com/phpboyscout/go/config"
)

// filteredStore is sensitiveStore's shape with a filter over the secrets
// backend: a writable plain file beneath a sensitive read-only backend that
// owns db.password and db.user, with db.password denied.
func filteredStore(t *testing.T, opts ...config.FilterOption) *config.Store {
	t.Helper()

	fsys, err := config.Dir(t.TempDir())
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}

	if err := fsys.WriteFile("base.yaml", []byte("app:\n  name: demo\n"), 0o600); err != nil {
		t.Fatalf("writing base.yaml: %v", err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithBackend(config.NewFileBackend(fsys, "base.yaml")),
		config.WithBackend(config.Filtered(sensitiveBackend{
			name: "vault",
			values: map[string]any{"db": map[string]any{
				"password": "s3cret",
				"user":     "app",
			}},
		}, opts...)),
	)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	return store
}

// The decision this whole spec turns on. A denied key is invisible to reads, so
// nothing in the snapshot records that a sensitive backend holds it — and the
// leak guard works by asking which source DEFINES a key. Left there, denying
// db.password would make a write of it route into the plain file beneath and
// NOT be refused: the filter would silently re-open the hole ErrSensitiveLeak
// exists to close.
func TestFiltered_DeniedSensitiveKeyIsStillGuarded(t *testing.T) {
	t.Parallel()

	store := filteredStore(t, config.Deny("db.password"))

	// Hidden from reads.
	if got := store.View().GetString("db.password"); got != "" {
		t.Errorf("db.password = %q, want it hidden by the filter", got)
	}

	// But still refused as a write, because the backend genuinely holds it.
	_, err := store.Apply(context.Background(), config.Set("db.password", "new"))
	if !errors.Is(err, config.ErrSensitiveLeak) {
		t.Fatalf("Apply(Set(db.password)) err = %v, want ErrSensitiveLeak — a filtered "+
			"secret must not become writable to the plain layer beneath", err)
	}

	if _, ok := store.View().Origin("db.password"); ok {
		t.Error("db.password reached a layer despite the refusal")
	}
}

// The permitted half of the same backend keeps working, which is what proves
// the guard is keyed on the withheld path rather than smeared over the backend.
func TestFiltered_PermittedKeysAreUnaffected(t *testing.T) {
	t.Parallel()

	store := filteredStore(t, config.Deny("db.password"))

	if got := store.View().GetString("db.user"); got != "app" {
		t.Errorf("db.user = %q, want app — only db.password was denied", got)
	}
}

// An ordinary key must still route into the plain file. If the withheld set
// leaked into unrelated paths, this is where it shows.
func TestFiltered_UnrelatedWriteStillRoutes(t *testing.T) {
	t.Parallel()

	store := filteredStore(t, config.Deny("db.password"))

	if _, err := store.Apply(context.Background(), config.Set("app.name", "other")); err != nil {
		t.Fatalf("Apply(Set(app.name)) err = %v, want it to route into base.yaml", err)
	}
}

// A filter that hides nothing the backend holds must not invent a guard: the
// withheld set is recorded from real data, not derived from the rules, so
// denying a key the backend does not have refuses nothing.
func TestFiltered_DenyingAnAbsentKeyGuardsNothing(t *testing.T) {
	t.Parallel()

	store := filteredStore(t, config.Deny("db.nonexistent"))

	if _, err := store.Apply(context.Background(), config.Set("db.nonexistent", "x")); err != nil {
		t.Errorf("Apply err = %v — nothing owns this key, so nothing should refuse it", err)
	}
}

// Capability honesty. ID must forward or the write path cannot find the backend
// again; Capabilities must forward or a filtered Vault stops being sensitive at
// all, which disarms the guard for every key it owns.
func TestFiltered_ForwardsIdentityAndCapabilities(t *testing.T) {
	t.Parallel()

	inner := sensitiveBackend{name: "vault", values: map[string]any{"a": 1}}
	wrapped := config.Filtered(inner, config.Allow("a"))

	if got := wrapped.ID(); got != inner.ID() {
		t.Errorf("ID() = %q, want %q — the write path finds a backend by this", got, inner.ID())
	}

	if !wrapped.Capabilities().Sensitive {
		t.Error("Capabilities().Sensitive was dropped by the filter, which disarms the leak guard")
	}
}

// With no options there is nothing to wrap, so the backend is returned as-is
// rather than paying for a pass-through.
func TestFiltered_NoRulesReturnsTheBackendUnchanged(t *testing.T) {
	t.Parallel()

	// A pointer, because sensitiveBackend holds a map and comparing two
	// interfaces over an uncomparable dynamic type panics at run time.
	inner := &sensitiveBackend{name: "vault", values: map[string]any{"a": 1}}

	if config.Filtered(inner) != config.Backend(inner) {
		t.Error("Filtered with no options wrapped the backend anyway")
	}
}

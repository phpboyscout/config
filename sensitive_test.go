package config_test

import (
	"context"
	"testing"

	"gitlab.com/phpboyscout/go/errors"

	"gitlab.com/phpboyscout/go/config"
)

// sensitiveBackend is a read-only backend that declares itself sensitive — the
// shape a secrets manager has (Vault, AWS Secrets Manager): it provides values
// but cannot be written to, so a write to a key it owns routes down to whatever
// writable layer sits beneath it.
type sensitiveBackend struct {
	name   string
	values map[string]any
}

func (b sensitiveBackend) ID() string { return b.name }

func (b sensitiveBackend) Capabilities() config.Capabilities {
	return config.Capabilities{Sensitive: true}
}

func (b sensitiveBackend) Load(_ context.Context, _ []config.Layer) ([]config.Layer, error) {
	return []config.Layer{{
		Source: config.Source{Kind: config.SourceKind("vault"), Name: b.name, Writable: false},
		Values: b.values,
	}}, nil
}

// sensitiveStore builds a store with a writable, non-sensitive YAML file beneath
// a read-only, sensitive backend that owns db.password.
func sensitiveStore(t *testing.T) *config.Store {
	t.Helper()

	fsys, err := config.Dir(t.TempDir())
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}

	if err := fsys.WriteFile("base.yaml", []byte("app:\n  name: demo\n"), 0o600); err != nil {
		t.Fatalf("writing base.yaml: %v", err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithBackend(config.NewFileBackend(fsys, "base.yaml")), // writable, non-sensitive
		config.WithBackend(sensitiveBackend{
			name:   "vault",
			values: map[string]any{"db": map[string]any{"password": "s3cret"}},
		}), // sensitive, read-only, higher precedence
	)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// The sensitive layer wins the read.
	if got := store.View().GetString("db.password"); got != "s3cret" {
		t.Fatalf("db.password = %q, want the sensitive value to be served", got)
	}

	return store
}

func TestSensitive_ApplyRefusesLeakIntoNonSensitiveLayer(t *testing.T) {
	t.Parallel()

	store := sensitiveStore(t)

	_, err := store.Apply(context.Background(), config.Set("db.password", "new"))
	if !errors.Is(err, config.ErrSensitiveLeak) {
		t.Errorf("Apply(Set(db.password)) err = %v, want ErrSensitiveLeak — the secret must not be "+
			"written into the plain file beneath it", err)
	}

	// The plain file must not have gained the key.
	if _, ok := store.View().Origin("db.password"); ok {
		if src, _ := store.View().Origin("db.password"); src.Name == "base.yaml" {
			t.Error("db.password leaked into base.yaml despite the refusal")
		}
	}
}

func TestSensitive_PlanRefusesLeak(t *testing.T) {
	t.Parallel()

	store := sensitiveStore(t)

	if _, err := store.Plan(config.Set("db.password", "new")); !errors.Is(err, config.ErrSensitiveLeak) {
		t.Errorf("Plan(Set(db.password)) err = %v, want ErrSensitiveLeak — a dry run must refuse the "+
			"same write Apply would", err)
	}
}

func TestSensitive_PinnedTargetStillRefused(t *testing.T) {
	t.Parallel()

	store := sensitiveStore(t)

	// Pinning routing does not opt out of the leak guard: it is a safety
	// invariant, not a routing preference.
	pinned := config.Change{
		Path:   "db.password",
		Value:  "new",
		Target: &config.Source{Name: "base.yaml"},
	}

	if _, err := store.Plan(pinned); !errors.Is(err, config.ErrSensitiveLeak) {
		t.Errorf("Plan(pinned Set) err = %v, want ErrSensitiveLeak even when the non-sensitive target "+
			"is pinned", err)
	}
}

func TestSensitive_RemoveIsAllowed(t *testing.T) {
	t.Parallel()

	store := sensitiveStore(t)

	// Removing writes no value, so it cannot leak one. It must not be refused.
	if _, err := store.Plan(config.Remove("db.password")); errors.Is(err, config.ErrSensitiveLeak) {
		t.Error("Plan(Remove(db.password)) was refused as a leak; removing a key writes no secret")
	}
}

func TestSensitive_UnrelatedKeyIsAllowed(t *testing.T) {
	t.Parallel()

	store := sensitiveStore(t)

	// A key no sensitive layer defines routes and writes normally.
	if _, err := store.Plan(config.Set("app.name", "renamed")); err != nil {
		t.Errorf("Plan(Set(app.name)) err = %v, want a clean plan for a non-sensitive key", err)
	}
}

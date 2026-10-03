package config_test

import (
	"context"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/go/errors"

	"gitlab.com/phpboyscout/go/config"
)

// Filtered and Constrained compose with each other and with Nested. Whatever a
// wrapper sits over, the Store must treat the source exactly as it would
// unwrapped. go/config#14.

func secretsBackend() sensitiveBackend {
	return sensitiveBackend{
		name: "vault",
		values: map[string]any{"db": map[string]any{
			"password": "s3cret",
			"user":     "app",
		}},
	}
}

func storeOver(t *testing.T, secrets config.Backend) *config.Store {
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
		config.WithBackend(secrets))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	return store
}

// A filtered-away secret stays guarded however the filter is wrapped. Losing
// the withheld set would route the write into the plain file beneath.
func TestWrapped_FilteredSecretIsStillGuarded(t *testing.T) {
	t.Parallel()

	cases := map[string]config.Backend{
		"Constrained over Filtered": config.Constrained(
			config.Filtered(secretsBackend(), config.Deny("db.password")),
			config.Forbid("unrelated.*")),
		"Filtered over Filtered": config.Filtered(
			config.Filtered(secretsBackend(), config.Deny("db.password")),
			config.Deny("unrelated.*")),
	}

	for name, backend := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store := storeOver(t, backend)

			_, err := store.Apply(context.Background(), config.Set("db.password", "new"))
			if !errors.Is(err, config.ErrSensitiveLeak) {
				t.Fatalf("Apply(Set(db.password)) err = %v, want ErrSensitiveLeak", err)
			}
		})
	}
}

// A constraint still judges what its source supplies when a filter wraps it.
func TestWrapped_ConstraintIsStillChecked(t *testing.T) {
	t.Parallel()

	_, err := config.NewStore(context.Background(), config.WithBackend(config.Filtered(
		config.Constrained(secretsBackend(), config.Forbid("db.user")),
		config.Deny("unrelated.*"))))
	if !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("a forbidden key supplied through a filtered constrained source: want ErrInvalidConfig, got %v", err)
	}
}

// A promotable nested store stays pin-only behind a filter: an ordinary write
// must fork into the outer file, not rewrite the shared configuration.
func TestWrapped_PromotableNestedStaysPinOnly(t *testing.T) {
	t.Parallel()

	for name, wrap := range map[string]func(config.Backend) config.Backend{
		"Filtered":    func(b config.Backend) config.Backend { return config.Filtered(b, config.Deny("unrelated.*")) },
		"Constrained": func(b config.Backend) config.Backend { return config.Constrained(b, config.Forbid("unrelated.*")) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			global := innerStore(t, map[string]string{"global.yaml": "theme: dark\n"}, "global.yaml")

			fsys, err := config.Dir(t.TempDir())
			if err != nil {
				t.Fatalf("config.Dir: %v", err)
			}

			if err := fsys.WriteFile("project.yaml", []byte("name: demo\n"), 0o600); err != nil {
				t.Fatalf("writing project.yaml: %v", err)
			}

			store, err := config.NewStore(context.Background(),
				config.WithBackend(wrap(config.Nested(global, "global", config.NestedPromotable))),
				config.WithFiles(fsys, "project.yaml"))
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}

			plan, err := store.Plan(config.Set("theme", "light"))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}

			if got := plan.Operations[0].Target.Name; got != "project.yaml" {
				t.Errorf("target = %q, want project.yaml: an unpinned write reached the shared store", got)
			}
		})
	}
}

// Constraints stack: each judges what the source supplied.
func TestWrapped_StackedConstraintsAreBothChecked(t *testing.T) {
	t.Parallel()

	_, err := config.NewStore(context.Background(), config.WithBackend(config.Constrained(
		config.Constrained(secretsBackend(), config.Forbid("db.user")),
		config.Forbid("db.password"))))
	if !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("want ErrInvalidConfig, got %v", err)
	}

	for _, key := range []string{"db.user", "db.password"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("%s was not reported: %v", key, err)
		}
	}
}

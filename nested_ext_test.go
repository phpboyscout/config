package config_test

import (
	"context"
	"errors"
	"testing"

	"gitlab.com/phpboyscout/go/config"
)

func innerStore(t *testing.T, files map[string]string, paths ...string) *config.Store {
	t.Helper()

	fsys, err := config.Dir(t.TempDir())
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}

	for name, body := range files {
		if err := fsys.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	store, err := config.NewStore(context.Background(), config.WithFiles(fsys, paths...))
	if err != nil {
		t.Fatalf("NewStore(inner): %v", err)
	}

	return store
}

// Provenance surviving the join is the whole reason the inner layers pass
// through rather than being flattened into one. If Explain named the aggregate,
// a composed store would answer "where did this come from" less usefully than
// the two stores it replaced.
func TestNested_ProvenanceNamesTheInnerLayer(t *testing.T) {
	t.Parallel()

	global := innerStore(t, map[string]string{
		"global.yaml": "theme: dark\neditor: vim\n",
	}, "global.yaml")

	fsys, err := config.Dir(t.TempDir())
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}

	if err := fsys.WriteFile("project.yaml", []byte("editor: nano\n"), 0o600); err != nil {
		t.Fatalf("writing project.yaml: %v", err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithBackend(config.Nested(global, "global")),
		config.WithFiles(fsys, "project.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// The inherited value resolves, and says where it came from.
	if got := store.View().GetString("theme"); got != "dark" {
		t.Errorf("theme = %q, want dark from the nested store", got)
	}

	src, ok := store.View().Origin("theme")
	if !ok {
		t.Fatal("theme has no provenance")
	}

	if src.Name != "global.yaml" {
		t.Errorf("Origin(theme).Name = %q, want global.yaml — the inner layer, not the aggregate", src.Name)
	}

	// The outer layer, declared after the nested block, outranks it.
	if got := store.View().GetString("editor"); got != "nano" {
		t.Errorf("editor = %q, want the outer project.yaml to win", got)
	}
}

// The block sits where it was declared. Declared first it is outranked by
// everything after it; declared last it outranks everything before.
func TestNested_BlockSitsWhereItIsDeclared(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		nestedLast bool
		want       string
	}{
		{"nested declared first is outranked", false, "outer"},
		{"nested declared last wins", true, "inner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			inner := innerStore(t, map[string]string{"in.yaml": "who: inner\n"}, "in.yaml")

			fsys, err := config.Dir(t.TempDir())
			if err != nil {
				t.Fatalf("config.Dir: %v", err)
			}

			if err := fsys.WriteFile("out.yaml", []byte("who: outer\n"), 0o600); err != nil {
				t.Fatalf("writing out.yaml: %v", err)
			}

			opts := []config.StoreOption{
				config.WithBackend(config.Nested(inner, "inner")),
				config.WithFiles(fsys, "out.yaml"),
			}
			if tc.nestedLast {
				opts[0], opts[1] = opts[1], opts[0]
			}

			store, err := config.NewStore(context.Background(), opts...)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}

			if got := store.View().GetString("who"); got != tc.want {
				t.Errorf("who = %q, want %q", got, tc.want)
			}
		})
	}
}

// Reads recurse to any depth, and nothing in the read path needs to know how
// deep it went.
func TestNested_ReadsRecurseToAnyDepth(t *testing.T) {
	t.Parallel()

	deep := innerStore(t, map[string]string{"deep.yaml": "found: yes\n"}, "deep.yaml")

	mid, err := config.NewStore(context.Background(), config.WithBackend(config.Nested(deep, "deep")))
	if err != nil {
		t.Fatalf("NewStore(mid): %v", err)
	}

	top, err := config.NewStore(context.Background(), config.WithBackend(config.Nested(mid, "mid")))
	if err != nil {
		t.Fatalf("NewStore(top): %v", err)
	}

	if got := top.View().GetString("found"); got != "yes" {
		t.Errorf("found = %q, want yes through two levels of nesting", got)
	}

	src, _ := top.View().Origin("found")
	if src.Name != "deep.yaml" {
		t.Errorf("Origin.Name = %q, want deep.yaml — provenance survives both joins", src.Name)
	}
}

// A store may appear at most once. The diamond is not a cycle and resolves
// perfectly well for reads, but it would contribute its layers twice at two
// precedences, so every value in it would be shadowed by a copy of itself.
func TestNested_TreeRuleRefusesAStoreAppearingTwice(t *testing.T) {
	t.Parallel()

	t.Run("directly, twice in one store", func(t *testing.T) {
		t.Parallel()

		shared := innerStore(t, map[string]string{"s.yaml": "a: 1\n"}, "s.yaml")

		_, err := config.NewStore(context.Background(),
			config.WithBackend(config.Nested(shared, "first")),
			config.WithBackend(config.Nested(shared, "second")))

		if !errors.Is(err, config.ErrCyclicStore) {
			t.Errorf("err = %v, want ErrCyclicStore", err)
		}
	})

	t.Run("as a diamond, through two intermediates", func(t *testing.T) {
		t.Parallel()

		shared := innerStore(t, map[string]string{"s.yaml": "a: 1\n"}, "s.yaml")

		left, err := config.NewStore(context.Background(), config.WithBackend(config.Nested(shared, "s")))
		if err != nil {
			t.Fatalf("NewStore(left): %v", err)
		}

		right, err := config.NewStore(context.Background(), config.WithBackend(config.Nested(shared, "s")))
		if err != nil {
			t.Fatalf("NewStore(right): %v", err)
		}

		_, err = config.NewStore(context.Background(),
			config.WithBackend(config.Nested(left, "left")),
			config.WithBackend(config.Nested(right, "right")))

		if !errors.Is(err, config.ErrCyclicStore) {
			t.Errorf("err = %v, want ErrCyclicStore for the diamond", err)
		}
	})
}

// Two stores over the same file, both able to write to it, produce layers equal
// in every field. Nothing downstream can tell them apart — the routing index is
// keyed by Source and shadow detection compares sources for equality — so the
// composition is refused rather than silently resolved into a wrong plan.
func TestNested_RefusesIndistinguishableLayers(t *testing.T) {
	t.Parallel()

	fsys, inner := sameFileStore(t)

	_, err := config.NewStore(context.Background(),
		config.WithBackend(config.Nested(inner, "inner", config.NestedPromotable)),
		config.WithFiles(fsys, "same.yaml"))

	if !errors.Is(err, config.ErrDuplicateLayer) {
		t.Errorf("err = %v, want ErrDuplicateLayer — the same file resolved twice, writable both times", err)
	}
}

// The same composition read-only is NOT refused, and the distinction is worth
// pinning down rather than leaving to chance.
//
// A read-only nested store clears Writable on the layers it contributes, so the
// two sources differ in that field and stay distinguishable: the index holds
// both, shadow detection separates them, and there is no wrong plan to prevent.
// Refusing it anyway would reject a composition that works.
func TestNested_ReadOnlyOverTheSameFileIsAllowed(t *testing.T) {
	t.Parallel()

	fsys, inner := sameFileStore(t)

	store, err := config.NewStore(context.Background(),
		config.WithBackend(config.Nested(inner, "inner")),
		config.WithFiles(fsys, "same.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v — read-only nesting leaves the layers distinguishable", err)
	}

	if got := store.View().GetString("a"); got != "1" {
		t.Errorf("a = %q, want 1", got)
	}
}

func sameFileStore(t *testing.T) (config.FS, *config.Store) {
	t.Helper()

	fsys, err := config.Dir(t.TempDir())
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}

	if err := fsys.WriteFile("same.yaml", []byte("a: 1\n"), 0o600); err != nil {
		t.Fatalf("writing same.yaml: %v", err)
	}

	inner, err := config.NewStore(context.Background(), config.WithFiles(fsys, "same.yaml"))
	if err != nil {
		t.Fatalf("NewStore(inner): %v", err)
	}

	return fsys, inner
}

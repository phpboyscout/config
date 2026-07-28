package config_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// D3a, and the reason a writable nested layer cannot simply be routable.
//
// Routing prefers a writable layer that ALREADY DEFINES the key. So if a nested
// global config defines theme and its layers routed like any other, an ordinary
// project-scoped Set would walk past the project file, find theme in the global
// config, and write there — silently changing what every other project
// inherits. Demotion by default.
func TestNested_PromotableIsPinnableButNotRoutable(t *testing.T) {
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
		config.WithBackend(config.Nested(global, "global", config.NestedPromotable)),
		config.WithFiles(fsys, "project.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	t.Run("an unpinned write forks into the outer layer", func(t *testing.T) {
		plan, err := store.Plan(config.Set("theme", "light"))
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}

		if got := plan.Operations[0].Target.Name; got != "project.yaml" {
			t.Errorf("target = %q, want project.yaml — an ordinary edit must not "+
				"rewrite the shared config it inherited the value from", got)
		}
	})

	t.Run("a named write reaches the inner layer", func(t *testing.T) {
		plan, err := store.Plan(config.Set("theme", "light", config.To("global.yaml")))
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}

		if got := plan.Operations[0].Target.Name; got != "global.yaml" {
			t.Errorf("target = %q, want the named global.yaml", got)
		}
	})
}

// Without NestedPromotable the inner layers are not targets at all, so naming
// one is a caller error rather than a silent fall back to routing.
func TestNested_ReadOnlyInnerLayerCannotBeNamed(t *testing.T) {
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
		config.WithBackend(config.Nested(global, "global")),
		config.WithFiles(fsys, "project.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := store.Plan(config.Set("theme", "light", config.To("global.yaml"))); !errors.Is(err, config.ErrInvalidTarget) {
		t.Errorf("err = %v, want ErrInvalidTarget for a read-only nested layer", err)
	}
}

// Promotion end to end: the value lands in the inner store's own file, verified
// by reading that store rather than the composed view, and the project's copy
// goes in the same batch so the promoted value is not immediately shadowed.
func TestNested_PromotionWritesThroughToTheInnerStore(t *testing.T) {
	t.Parallel()

	global := innerStore(t, map[string]string{"global.yaml": "theme: dark\n"}, "global.yaml")

	fsys, err := config.Dir(t.TempDir())
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}

	if err := fsys.WriteFile("project.yaml", []byte("theme: light\n"), 0o600); err != nil {
		t.Fatalf("writing project.yaml: %v", err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithBackend(config.Nested(global, "global", config.NestedPromotable)),
		config.WithFiles(fsys, "project.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := store.Apply(context.Background(),
		config.Set("theme", "solarized", config.To("global.yaml")),
		config.Remove("theme"),
	); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Read the INNER store, not the composed view: this is what proves the
	// write reached the file rather than merely appearing to.
	if err := global.Reload(context.Background()); err != nil {
		t.Fatalf("inner Reload: %v", err)
	}

	if got := global.View().GetString("theme"); got != "solarized" {
		t.Errorf("inner theme = %q, want solarized — the promotion did not reach the inner store", got)
	}
}

// D11a. A store's own Apply deliberately never reaches a watcher — "Apply
// builds the next snapshot from what it just wrote and notifies directly, so a
// write cannot come back round through the watcher" — so the inner store's own
// watch cannot report its own write. The aggregate polls the inner snapshot
// version instead, which registers no callback across the store boundary.
func TestNested_WatchNoticesADirectInnerApply(t *testing.T) {
	t.Parallel()

	global := innerStore(t, map[string]string{"global.yaml": "theme: dark\n"}, "global.yaml")

	store, err := config.NewStore(context.Background(),
		config.WithBackend(config.Nested(global, "global")))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	changed := make(chan struct{}, 1)

	store.AddObserverFunc(func(config.Observed) error {
		select {
		case changed <- struct{}{}:
		default:
		}

		return nil
	})

	stop, err := store.Watch(context.Background(), config.WithPollInterval(20*time.Millisecond))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer stop()

	// Written directly on the inner store, which is the case the watch bridge
	// alone cannot see.
	if _, err := global.Apply(context.Background(), config.Set("theme", "light")); err != nil {
		t.Fatalf("inner Apply: %v", err)
	}

	select {
	case <-changed:
	case <-time.After(3 * time.Second):
		t.Fatal("the outer store never heard about a write made directly to the inner store")
	}

	if got := store.View().GetString("theme"); got != "light" {
		t.Errorf("theme = %q, want light after the reload", got)
	}
}

// A tick that finds nothing moved must not notify. The version increments on
// every publish, changed or not, so without the Store's own sameConfiguration
// check a quiet reload would look like a change.
func TestNested_WatchDoesNotNotifyWhenNothingMoved(t *testing.T) {
	t.Parallel()

	global := innerStore(t, map[string]string{"global.yaml": "theme: dark\n"}, "global.yaml")

	store, err := config.NewStore(context.Background(),
		config.WithBackend(config.Nested(global, "global")))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	var notified atomic.Int32

	store.AddObserverFunc(func(config.Observed) error {
		notified.Add(1)

		return nil
	})

	stop, err := store.Watch(context.Background(), config.WithPollInterval(20*time.Millisecond))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer stop()

	// A reload that changes nothing still publishes, so the version moves.
	for range 3 {
		if err := global.Reload(context.Background()); err != nil {
			t.Fatalf("inner Reload: %v", err)
		}
	}

	time.Sleep(200 * time.Millisecond)

	if got := notified.Load(); got != 0 {
		t.Errorf("observers notified %d times for a configuration that did not move", got)
	}
}

// Prepare must route each edit to the inner backend that owns the layer it
// NAMES. With a single-layer inner store that is indistinguishable from picking
// whatever comes first, so this composes over an inner store with two writable
// files and promotes into the lower-precedence one — the copy a "first match"
// or "highest precedence" shortcut would miss.
func TestNested_PromotionReachesTheNamedInnerLayerNotJustAnyOfThem(t *testing.T) {
	t.Parallel()

	inner := innerStore(t, map[string]string{
		"base.yaml": "theme: dark\n",
		"over.yaml": "editor: vim\n",
	}, "base.yaml", "over.yaml")

	fsys, err := config.Dir(t.TempDir())
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}

	if err := fsys.WriteFile("project.yaml", []byte("name: demo\n"), 0o600); err != nil {
		t.Fatalf("writing project.yaml: %v", err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithBackend(config.Nested(inner, "inner", config.NestedPromotable)),
		config.WithFiles(fsys, "project.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// base.yaml is the LOWER-precedence of the two inner layers.
	if _, err := store.Apply(context.Background(),
		config.Set("promoted", "yes", config.To("base.yaml"))); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if err := inner.Reload(context.Background()); err != nil {
		t.Fatalf("inner Reload: %v", err)
	}

	src, ok := inner.View().Origin("promoted")
	if !ok {
		t.Fatal("the promoted key reached neither inner layer")
	}

	if src.Name != "base.yaml" {
		t.Errorf("promoted landed in %q, want base.yaml — the layer the write named", src.Name)
	}
}

// failingCommit is a writable backend whose commit fails, so a batch spanning
// two inner backends leaves the first committed and the second not.
type failingCommit struct{ name string }

func (b failingCommit) ID() string { return b.name }

func (b failingCommit) Capabilities() config.Capabilities { return config.Capabilities{} }

func (b failingCommit) Load(context.Context, []config.Layer) ([]config.Layer, error) {
	return []config.Layer{{
		Source: config.Source{Kind: config.SourceKind("fake"), Name: b.name, Writable: true},
		Values: map[string]any{"boom": "before"},
	}}, nil
}

func (b failingCommit) Prepare(context.Context, []config.Edit) (config.Pending, error) {
	return failingPending{}, nil
}

type failingPending struct{}

func (failingPending) Layers() []config.Layer       { return nil }
func (failingPending) Verify(context.Context) error { return nil }
func (failingPending) Commit(context.Context) error {
	return errors.New("the remote refused the write")
}
func (failingPending) Rollback(context.Context) error { return nil }
func (failingPending) Discard(context.Context) error  { return nil }

// A failure inside a composed store must name the path to it, not only the
// leaf. "the remote refused the write" is not actionable when the caller aimed
// at a layer two levels away and has never heard of the backend that refused.
func TestNested_CommitFailureNamesTheChain(t *testing.T) {
	t.Parallel()

	inner, err := config.NewStore(context.Background(),
		config.WithBackend(failingCommit{name: "inner-backend"}))
	if err != nil {
		t.Fatalf("NewStore(inner): %v", err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithBackend(config.Nested(inner, "global", config.NestedPromotable)))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	_, err = store.Apply(context.Background(),
		config.Set("boom", "after", config.To("inner-backend")))
	if err == nil {
		t.Fatal("Apply succeeded, want the inner commit failure to surface")
	}

	for _, want := range []string{"global", "inner-backend", "the remote refused the write"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q — the chain must name the path, not just the leaf", err, want)
		}
	}
}

// recordingPending commits successfully and remembers whether it was rolled
// back, so a partial commit inside an aggregate can be observed.
type recordingBackend struct {
	name       string
	rolledBack *atomic.Bool
}

func (b recordingBackend) ID() string { return b.name }

func (b recordingBackend) Capabilities() config.Capabilities { return config.Capabilities{} }

func (b recordingBackend) Load(context.Context, []config.Layer) ([]config.Layer, error) {
	return []config.Layer{{
		Source: config.Source{Kind: config.SourceKind("fake"), Name: b.name, Writable: true},
		Values: map[string]any{"first": "before"},
	}}, nil
}

func (b recordingBackend) Prepare(context.Context, []config.Edit) (config.Pending, error) {
	return recordingPending{rolledBack: b.rolledBack}, nil
}

type recordingPending struct{ rolledBack *atomic.Bool }

func (recordingPending) Layers() []config.Layer       { return nil }
func (recordingPending) Verify(context.Context) error { return nil }
func (recordingPending) Commit(context.Context) error { return nil }
func (p recordingPending) Rollback(context.Context) error {
	p.rolledBack.Store(true)

	return nil
}
func (recordingPending) Discard(context.Context) error { return nil }

// A batch spanning two inner backends, where the second fails to commit, must
// not leave the first one's write in place.
//
// The Store cannot do this for us: commitAll rolls back the pendings that
// already succeeded and discards the rest, so a pending that fails midway is
// never asked to roll back. That is right for a backend whose Commit is
// all-or-nothing and wrong for an aggregate, whose Commit is several.
func TestNested_PartialInnerCommitIsRolledBack(t *testing.T) {
	t.Parallel()

	var rolledBack atomic.Bool

	inner, err := config.NewStore(context.Background(),
		config.WithBackend(recordingBackend{name: "first-backend", rolledBack: &rolledBack}),
		config.WithBackend(failingCommit{name: "second-backend"}))
	if err != nil {
		t.Fatalf("NewStore(inner): %v", err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithBackend(config.Nested(inner, "global", config.NestedPromotable)))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	_, err = store.Apply(context.Background(),
		config.Set("first", "after", config.To("first-backend")),
		config.Set("boom", "after", config.To("second-backend")),
	)
	if err == nil {
		t.Fatal("Apply succeeded, want the second backend's commit failure")
	}

	if !rolledBack.Load() {
		t.Error("the first inner backend's committed write was left in place after the second failed")
	}
}

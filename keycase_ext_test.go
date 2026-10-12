package config_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/phpboyscout/go/errors"

	"gitlab.com/phpboyscout/go/config"
)

// A Store folds keys, so two keys in one layer that differ only in case are one
// key. Loading such a layer used to keep whichever Go's map iteration reached
// last; now the layer is refused. Spec 0015 D8.

func TestKeyCollision_ALayerWhoseKeysFoldTogetherIsRefused(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"top level":           "port: 1\nPort: 2\n",
		"equal values":        "port: 1\nPort: 1\n",
		"nested":              "server:\n  Host: a\n  host: b\n",
		"inside a list":       "items:\n  - Name: a\n    name: b\n",
		"parents that differ": "Server:\n  port: 1\nserver:\n  host: b\n",
	}

	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, err := config.NewStore(context.Background(),
				config.WithReaders(config.NamedSource{Name: "app.yaml", Content: []byte(doc)}))
			if !errors.Is(err, config.ErrKeyCollision) {
				t.Fatalf("NewStore err = %v, want ErrKeyCollision", err)
			}

			if store != nil {
				t.Error("NewStore returned a Store alongside a collision; it must return none")
			}

			if !strings.Contains(err.Error(), "app.yaml") {
				t.Errorf("err = %v, want it to name the layer", err)
			}
		})
	}
}

func TestKeyCollision_TheErrorNamesBothSpellings(t *testing.T) {
	t.Parallel()

	_, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "app.yaml", Content: []byte("db:\n  Password: a\n  password: b\n")}))

	for _, want := range []string{`"db.Password"`, `"db.password"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %s", err, want)
		}
	}
}

// Non-string keys are stringified before folding, so 1 and 1.0 are one key
// whichever mode a Store is in.
func TestKeyCollision_NonStringKeysThatStringifyAlikeCollide(t *testing.T) {
	t.Parallel()

	backend := staticBackend{name: "mem", values: map[string]any{
		"codes": map[any]any{1: "int", 1.0: "float"},
	}}

	_, err := config.NewStore(context.Background(), config.WithBackend(backend))
	if !errors.Is(err, config.ErrKeyCollision) {
		t.Fatalf("NewStore err = %v, want ErrKeyCollision", err)
	}
}

// A filter sees the backend's keys before the Store does, and stringifies
// non-string keys while walking them, so the check has to run there too or
// the filter would pick a winner silently.
func TestKeyCollision_IsCaughtBeneathAFilter(t *testing.T) {
	t.Parallel()

	backend := staticBackend{name: "mem", values: map[string]any{
		"codes": map[any]any{1: "int", 1.0: "float"},
	}}

	_, err := config.NewStore(context.Background(),
		config.WithBackend(config.Filtered(backend, config.Deny("other"))))
	if !errors.Is(err, config.ErrKeyCollision) {
		t.Fatalf("NewStore err = %v, want ErrKeyCollision", err)
	}
}

// Across layers, different spellings are precedence, as they always were.
func TestKeyCollision_SpellingsInDifferentLayersStillMerge(t *testing.T) {
	t.Parallel()

	store, err := config.NewStore(context.Background(),
		config.WithReaders(
			config.NamedSource{Name: "base.yaml", Content: []byte("Port: 1\n")},
			config.NamedSource{Name: "over.yaml", Content: []byte("port: 2\n")},
		))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if got := store.View().GetInt("port"); got != 2 {
		t.Errorf("port = %d, want the later layer's 2", got)
	}
}

func TestKeyCollision_AReloadKeepsTheLastGoodSnapshot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	writeFile(t, path, "port: 1\n")

	fsys, err := config.Dir(dir)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}

	store, err := config.NewStore(context.Background(), config.WithBackend(config.NewFileBackend(fsys, "app.yaml")))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	notified := 0

	store.AddObserverFunc(func(config.Observed) error {
		notified++

		return nil
	})

	writeFile(t, path, "port: 1\nPort: 2\n")

	if err := store.Reload(context.Background()); !errors.Is(err, config.ErrKeyCollision) {
		t.Fatalf("Reload err = %v, want ErrKeyCollision", err)
	}

	if got := store.View().GetInt("port"); got != 1 {
		t.Errorf("port = %d after a refused reload, want the last good 1", got)
	}

	if notified != 0 {
		t.Errorf("observers notified %d times by a refused reload, want 0", notified)
	}
}

func TestKeyCollision_AWatchReloadIsRefusedAndReported(t *testing.T) {
	t.Parallel()

	remote := newFakeRemote(map[string]any{"level": "info"})

	store, err := config.NewStore(context.Background(), config.WithBackend(newRemoteBackend(remote, "app/")))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	reported := make(chan error, 4)
	store.OnReloadError(func(err error) { reported <- err })

	stop, err := store.Watch(context.Background(), config.WithSettleInterval(0))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	defer stop()

	remote.set("Level", "debug")

	select {
	case err := <-reported:
		if !errors.Is(err, config.ErrKeyCollision) {
			t.Errorf("reported %v, want ErrKeyCollision", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the colliding reload was never reported")
	}

	if got := store.View().GetString("level"); got != "info" {
		t.Errorf("level = %q, want the last good info", got)
	}
}

func TestKeyCollision_AddLayerWithdrawsOnlyItself(t *testing.T) {
	t.Parallel()

	store, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "base.yaml", Content: []byte("port: 1\n")}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	err = store.AddLayer(context.Background(), "extra.yaml", strings.NewReader("host: a\nHost: b\n"))
	if !errors.Is(err, config.ErrKeyCollision) {
		t.Fatalf("AddLayer err = %v, want ErrKeyCollision", err)
	}

	if got := store.View().GetInt("port"); got != 1 {
		t.Errorf("port = %d, want the existing layer untouched", got)
	}

	if store.View().IsSet("host") {
		t.Error("host is set; the refused layer was not withdrawn")
	}
}

// A backend whose staged result collides must not commit.
func TestKeyCollision_AnApplyThatWouldCollideCommitsNothing(t *testing.T) {
	t.Parallel()

	backend := newSpellingBackend(map[string]any{"port": 1})
	backend.extra = map[string]any{"Port": 9}

	store, err := config.NewStore(context.Background(), config.WithBackend(backend))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	_, err = store.Apply(context.Background(), config.Set("host", "a"))
	if !errors.Is(err, config.ErrKeyCollision) {
		t.Fatalf("Apply err = %v, want ErrKeyCollision", err)
	}

	if backend.committed() {
		t.Error("the backend committed a write whose result collides")
	}
}

// A write addresses a key the target layer already defines in that layer's
// own spelling, so a mis-cased write edits the key instead of adding a second
// spelling beside it. Spec 0015 D9.
func TestWriteSpelling_TheBackendReceivesTheLayersSpelling(t *testing.T) {
	t.Parallel()

	seed := map[string]any{
		"Server": map[string]any{"Port": 1, "TLS": map[string]any{"Cert": "a"}},
		"Mode":   "x",
		"Flat":   "scalar",
	}

	cases := []struct {
		name   string
		change config.Change
		want   string
	}{
		{"an existing leaf", config.Set("server.port", 2), "Server.Port"},
		{"an existing nested leaf", config.Set("SERVER.tls.CERT", "b"), "Server.TLS.Cert"},
		{"a removal", config.Remove("mode"), "Mode"},
		{"a new leaf under an existing parent", config.Set("server.Host", "h"), "Server.Host"},
		{"a new subtree keeps the caller's spelling below it", config.Set("server.New.Key", 1), "Server.New.Key"},
		{"below a scalar the caller's spelling is kept", config.Set("flat.Child", 1), "Flat.Child"},
		{"a path no layer defines", config.Set("Other.Key", 1), "Other.Key"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			backend := newSpellingBackend(seed)

			store, err := config.NewStore(context.Background(), config.WithBackend(backend))
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}

			if _, err := store.Apply(context.Background(), tc.change); err != nil {
				t.Fatalf("Apply: %v", err)
			}

			if got := backend.paths(); !slices.Equal(got, []string{tc.want}) {
				t.Errorf("Edit.Path = %v, want [%s]", got, tc.want)
			}
		})
	}
}

// Only the target layer's spelling counts. A key spelled differently in some
// other layer tells the target nothing about how its own source spells it.
func TestWriteSpelling_AnotherLayersSpellingIsNotBorrowed(t *testing.T) {
	t.Parallel()

	backend := newSpellingBackend(map[string]any{"other": 1})

	store, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "defaults.yaml", Content: []byte("Port: 1\n")}),
		config.WithBackend(backend))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := store.Apply(context.Background(), config.Set("port", 2, config.To("kv"))); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := backend.paths(); !slices.Equal(got, []string{"port"}) {
		t.Errorf("Edit.Path = %v, want [port]", got)
	}
}

// The writes D9 exists for: before it, each of these left two spellings in the
// source, and D8 would then refuse the rebuild.
func TestWriteSpelling_AMisCasedWriteLeavesOneKey(t *testing.T) {
	t.Parallel()

	backend := newSpellingBackend(map[string]any{"Level": "info"})

	store, err := config.NewStore(context.Background(), config.WithBackend(backend))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := store.Apply(context.Background(), config.Set("level", "debug")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := backend.snapshot(); len(got) != 1 || got["Level"] != "debug" {
		t.Errorf("backend holds %v, want only Level=debug", got)
	}

	if got := store.View().GetString("level"); got != "debug" {
		t.Errorf("level = %q, want debug", got)
	}
}

// A nested Store's own layers keep the spelling their sources gave them, which
// the outer Store never sees, so the inner one respells too.
func TestWriteSpelling_ReachesThroughANestedStore(t *testing.T) {
	t.Parallel()

	backend := newSpellingBackend(map[string]any{"Server": map[string]any{"Port": 1}})

	inner, err := config.NewStore(context.Background(), config.WithBackend(backend))
	if err != nil {
		t.Fatalf("inner NewStore: %v", err)
	}

	outer, err := config.NewStore(context.Background(), config.WithBackend(config.Nested(inner, "team", config.NestedPromotable)))
	if err != nil {
		t.Fatalf("outer NewStore: %v", err)
	}

	if _, err := outer.Apply(context.Background(), config.Set("server.port", 2, config.To("kv"))); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := backend.paths(); !slices.Equal(got, []string{"Server.Port"}) {
		t.Errorf("Edit.Path = %v, want [Server.Port]", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// staticBackend contributes fixed values, including shapes a codec would not
// produce, such as map[any]any keyed by numbers.
type staticBackend struct {
	name   string
	values map[string]any
}

func (b staticBackend) ID() string                        { return b.name }
func (b staticBackend) Capabilities() config.Capabilities { return config.Capabilities{} }

func (b staticBackend) Load(context.Context, []config.Layer) ([]config.Layer, error) {
	return []config.Layer{{Source: config.Source{Kind: "mem", Name: b.name}, Values: b.values}}, nil
}

// spellingBackend is a writable backend that addresses paths exactly, as most
// remote stores do, and records each Edit.Path it is handed.
type spellingBackend struct {
	mu     sync.Mutex
	values map[string]any
	got    []string
	done   bool

	// extra is merged into every staged result, to stage one that collides.
	extra map[string]any
}

func newSpellingBackend(values map[string]any) *spellingBackend {
	return &spellingBackend{values: deepCopy(values)}
}

func (b *spellingBackend) ID() string                        { return "kv" }
func (b *spellingBackend) Capabilities() config.Capabilities { return config.Capabilities{} }

func (b *spellingBackend) Load(context.Context, []config.Layer) ([]config.Layer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return []config.Layer{b.layer(deepCopy(b.values))}, nil
}

func (b *spellingBackend) layer(values map[string]any) config.Layer {
	return config.Layer{Source: config.Source{Kind: "kv", Name: "kv", Writable: true}, Values: values}
}

func (b *spellingBackend) Prepare(_ context.Context, edits []config.Edit) (config.Pending, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	next := deepCopy(b.values)

	for _, e := range edits {
		b.got = append(b.got, e.Path)
		setPath(next, strings.Split(e.Path, "."), e.Value, e.Remove)
	}

	for k, v := range b.extra {
		next[k] = v
	}

	return &spellingPending{backend: b, next: next}, nil
}

func (b *spellingBackend) paths() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return slices.Clone(b.got)
}

func (b *spellingBackend) snapshot() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()

	return deepCopy(b.values)
}

func (b *spellingBackend) committed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.done
}

type spellingPending struct {
	backend *spellingBackend
	next    map[string]any
}

func (p *spellingPending) Layers() []config.Layer { return []config.Layer{p.backend.layer(p.next)} }

func (p *spellingPending) Verify(context.Context) error { return nil }

func (p *spellingPending) Commit(context.Context) error {
	p.backend.mu.Lock()
	defer p.backend.mu.Unlock()

	p.backend.values = p.next
	p.backend.done = true

	return nil
}

func (p *spellingPending) Rollback(context.Context) error { return nil }
func (p *spellingPending) Discard(context.Context) error  { return nil }

func setPath(m map[string]any, segs []string, value any, remove bool) {
	if len(segs) == 1 {
		if remove {
			delete(m, segs[0])
		} else {
			m[segs[0]] = value
		}

		return
	}

	child, ok := m[segs[0]].(map[string]any)
	if !ok {
		child = map[string]any{}
		m[segs[0]] = child
	}

	setPath(child, segs[1:], value, remove)
}

func deepCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))

	for k, v := range m {
		if nested, ok := v.(map[string]any); ok {
			v = deepCopy(nested)
		}

		out[k] = v
	}

	return out
}

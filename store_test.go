package config

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/afero"
)

func memFS(t *testing.T, files map[string]string) FS {
	t.Helper()

	filesystem := wrapAfero(afero.NewMemMapFs())

	for path, content := range files {
		if err := filesystem.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
	}

	return filesystem
}

func newTestStore(t *testing.T, files map[string]string, paths ...string) *Store {
	t.Helper()

	s, err := NewStore(context.Background(), WithFiles(memFS(t, files), paths...))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	return s
}

func TestStore_LoadsAndMergesInPrecedenceOrder(t *testing.T) {
	t.Parallel()

	s := newTestStore(t, map[string]string{
		"/base.yaml": "server:\n  host: base-host\n  port: 8080\nlog:\n  level: info\n",
		"/over.yaml": "server:\n  port: 9090\n",
	}, "/base.yaml", "/over.yaml")

	snap := s.Snapshot()

	cases := []struct {
		path       string
		want       any
		wantSource string
	}{
		{"server.port", 9090, "/over.yaml"},
		{"server.host", "base-host", "/base.yaml"},
		{"log.level", "info", "/base.yaml"},
	}

	for _, c := range cases {
		got, ok := snap.Get(c.path)
		if !ok {
			t.Errorf("Get(%q) not found", c.path)

			continue
		}

		if got != c.want {
			t.Errorf("Get(%q) = %v, want %v", c.path, got, c.want)
		}

		src, _ := snap.Origin(c.path)
		if src.Name != c.wantSource {
			t.Errorf("Origin(%q) = %q, want %q", c.path, src.Name, c.wantSource)
		}
	}
}

// A base file that has gone missing is a broken installation; a missing
// overlay is normal. The Store must distinguish them.
func TestStore_MissingSources(t *testing.T) {
	t.Parallel()

	t.Run("missing overlay is skipped", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t, map[string]string{
			"/base.yaml": "a: 1\n",
		}, "/base.yaml", "/absent.yaml")

		if got, _ := s.Snapshot().Get("a"); got != 1 {
			t.Errorf("a = %v, want the base to load despite the missing overlay", got)
		}
	})

	t.Run("missing base is tolerated by default", func(t *testing.T) {
		t.Parallel()

		s, err := NewStore(context.Background(),
			WithFiles(memFS(t, map[string]string{"/over.yaml": "a: 1\n"}), "/absent.yaml", "/over.yaml"))
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}

		if got, _ := s.Snapshot().Get("a"); got != 1 {
			t.Errorf("a = %v, want the overlay to load", got)
		}
	})

	t.Run("missing base is fatal when required", func(t *testing.T) {
		t.Parallel()

		_, err := NewStore(context.Background(),
			WithFiles(memFS(t, map[string]string{}), "/absent.yaml"),
			RequireFirstSource())

		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("err = %v, want fs.ErrNotExist", err)
		}
	})
}

// Silently dropping a layer changes the effective configuration without
// telling anyone, so a parse failure is fatal wherever it happens.
func TestStore_ParseFailureIsFatal(t *testing.T) {
	t.Parallel()

	_, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{
			"/base.yaml": "a: 1\n",
			"/bad.yaml":  "a: b: c\n", // mapping value in an invalid context
		}), "/base.yaml", "/bad.yaml"))

	if !errors.Is(err, ErrBackendParse) {
		t.Errorf("err = %v, want ErrBackendParse", err)
	}
}

// A document that cannot be round-tripped safely is refused at load, not at
// write: discovering it at write means the user has already made their edits.
func TestStore_UnsafeDocumentIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	_, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{
			"/bad.yaml": "bounds: {\n  min: 1,  # lower\n  max: 10  # upper\n}\n",
		}), "/bad.yaml"))

	if !errors.Is(err, ErrBackendUnsafe) {
		t.Fatalf("err = %v, want ErrBackendUnsafe", err)
	}

	if !strings.Contains(err.Error(), "/bad.yaml") {
		t.Errorf("error does not name the file: %v", err)
	}
}

// A document is a layer, so a multi-document file contributes several — and
// nothing after the first is silently discarded, which is what the incumbent
// does.
func TestStore_MultiDocumentFileContributesEveryDocument(t *testing.T) {
	t.Parallel()

	s := newTestStore(t, map[string]string{
		"/app.yaml": "log:\n  level: info\nshared: from-doc-0\n---\nshared: from-doc-1\nextra: present\n",
	}, "/app.yaml")

	snap := s.Snapshot()

	if got, _ := snap.Get("extra"); got != "present" {
		t.Errorf("extra = %v, want the second document to contribute", got)
	}

	if got, _ := snap.Get("shared"); got != "from-doc-1" {
		t.Errorf("shared = %v, want the later document to win", got)
	}

	src, _ := snap.Origin("shared")
	if src.Document != 1 {
		t.Errorf("provenance document = %d, want 1", src.Document)
	}

	if got := src.String(); got != "/app.yaml#1" {
		t.Errorf("provenance = %q, want /app.yaml#1", got)
	}

	if got, _ := snap.Get("log.level"); got != "info" {
		t.Errorf("log.level = %v, want the first document to survive", got)
	}
}

func TestStore_ReloadPublishesANewSnapshot(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "a: 1\n"})

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/app.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	first := s.Snapshot()

	if err := filesystem.WriteFile("/app.yaml", []byte("a: 2\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	if err := s.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	second := s.Snapshot()

	if second.Version() <= first.Version() {
		t.Errorf("version did not advance: %d then %d", first.Version(), second.Version())
	}

	if got, _ := second.Get("a"); got != 2 {
		t.Errorf("new snapshot a = %v, want 2", got)
	}

	// The snapshot a caller was already holding is unaffected — that is what
	// makes a read sequence coherent across a reload.
	if got, _ := first.Get("a"); got != 1 {
		t.Errorf("previously held snapshot changed: a = %v, want 1", got)
	}
}

// Fail-closed: a reload that cannot complete leaves the previous configuration
// in place rather than publishing something partial.
func TestStore_FailedReloadRetainsLastKnownGood(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "a: 1\n"})

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/app.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	before := s.Snapshot()

	if err := filesystem.WriteFile("/app.yaml", []byte("a: b: c\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	if err := s.Reload(context.Background()); err == nil {
		t.Fatal("Reload should have failed on a malformed source")
	}

	after := s.Snapshot()

	if after != before {
		t.Error("a failed reload replaced the snapshot")
	}

	if got, _ := after.Get("a"); got != 1 {
		t.Errorf("a = %v, want the last-known-good value 1", got)
	}
}

func TestStore_NoSources(t *testing.T) {
	t.Parallel()

	if _, err := NewStore(context.Background()); !errors.Is(err, ErrNoSources) {
		t.Errorf("err = %v, want ErrNoSources", err)
	}
}

func TestStore_Sources(t *testing.T) {
	t.Parallel()

	s := newTestStore(t, map[string]string{
		"/base.yaml": "a: 1\n", "/over.yaml": "b: 2\n",
	}, "/base.yaml", "/over.yaml")

	got := s.Sources()
	if len(got) != 2 || got[0] != "/base.yaml" || got[1] != "/over.yaml" {
		t.Errorf("Sources() = %v, want the paths in precedence order", got)
	}
}

// Reads go through an immutable snapshot rather than taking the lock, so a
// reload cannot disturb a read already in progress.
func TestStore_ConcurrentReadsAndReloads(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "server:\n  port: 1\n"})

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/app.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	var wg sync.WaitGroup

	for range 8 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range 50 {
				snap := s.Snapshot()

				// Every read against one snapshot must agree with itself, even
				// while reloads are landing.
				got, ok := snap.Get("server.port")
				if !ok {
					t.Errorf("server.port missing from snapshot v%d", snap.Version())

					return
				}

				if _, isInt := got.(int); !isInt {
					t.Errorf("server.port = %T, want a stable type", got)

					return
				}
			}
		}()
	}

	for range 4 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range 25 {
				_ = s.Reload(context.Background())
			}
		}()
	}

	wg.Wait()
}

func TestFileBackend_Capabilities(t *testing.T) {
	t.Parallel()

	backend := NewFileBackend(wrapAfero(afero.NewMemMapFs()), "/a.yaml")
	c := backend.Capabilities()

	if !c.PreservesComments {
		t.Errorf("a file backend preserves comments: %+v", c)
	}

	if c.Sensitive {
		t.Error("a plain file backend should not be marked sensitive")
	}

	// Whether a backend can be written to or watched is answered by the type
	// system, not by a flag a backend could contradict.
	if _, ok := backend.(WritableBackend); !ok {
		t.Error("a file backend should implement WritableBackend")
	}

	if _, ok := backend.(WatchableBackend); !ok {
		t.Error("a file backend should implement WatchableBackend")
	}

	// An in-memory source implements neither, which is how routing and watching
	// know to skip it.
	reader := NewReaderBackend("embedded", []byte("a: 1\n"))

	if _, ok := reader.(WritableBackend); ok {
		t.Error("an in-memory source must not be writable")
	}

	if _, ok := reader.(WatchableBackend); ok {
		t.Error("an in-memory source must not be watchable")
	}
}

// Pinning by name requires knowing the names, and the only route to one used to
// be planning a write and inspecting it — backwards, and it allocates a plan the
// caller throws away.
//
// The contract is "everything To accepts", which is why this feeds every
// returned name back through To rather than comparing against a literal list. A
// literal list drifts; this cannot, because the matcher itself decides whether
// the test passes.
func TestWritableTargets_EveryEntryIsAcceptedByTo(t *testing.T) {
	t.Parallel()

	store, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{
			"/base.yaml": "server:\n  port: 8080\n",
			"/over.yaml": "server:\n  port: 9090\n",
		}), "/base.yaml", "/over.yaml"),
		WithEnv("APP"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	targets := store.WritableTargets()
	if len(targets) == 0 {
		t.Fatal("WritableTargets returned nothing, so nothing can be pinned")
	}

	for _, target := range targets {
		if !target.Writable {
			t.Errorf("%s is offered as a target but reports Writable false", target)
		}

		if _, err := store.Plan(Set("server.port", 1, ToDocument(target.Name, target.Document))); err != nil {
			t.Errorf("To(%q) rejected a target WritableTargets offered: %v", target.Name, err)
		}
	}
}

// The environment is readable and never a write target, so offering it would
// invite a pin that can only fail.
func TestWritableTargets_ExcludesReadOnlyLayers(t *testing.T) {
	// Not parallel: t.Setenv and t.Parallel cannot be combined.
	t.Setenv("APP_SERVER_PORT", "7070")

	store, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{"/base.yaml": "server:\n  port: 8080\n"}), "/base.yaml"),
		WithEnv("APP"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	for _, target := range store.WritableTargets() {
		if target.Kind == SourceEnv {
			t.Errorf("WritableTargets offered the environment layer %s", target)
		}
	}
}

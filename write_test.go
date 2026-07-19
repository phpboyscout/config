package config

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

func readFile(t *testing.T, filesystem afero.Fs, path string) string {
	t.Helper()

	b, err := afero.ReadFile(filesystem, path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(b)
}

func storeOn(t *testing.T, filesystem afero.Fs, paths ...string) *Store {
	t.Helper()

	s, err := NewStore(context.Background(), WithFiles(filesystem, paths...))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	return s
}

// The whole point of the exercise: change a value and leave the author's work
// alone.
func TestApply_PreservesComments(t *testing.T) {
	t.Parallel()

	const src = `# Server configuration.
server:
  host: localhost   # where we bind
  port: 8080

# Logging is deliberately verbose in development.
log:
  level: info
`

	filesystem := memFS(t, map[string]string{"/app.yaml": src})
	s := storeOn(t, filesystem, "/app.yaml")

	if _, err := s.Apply(context.Background(), Set("server.port", 9090)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	out := readFile(t, filesystem, "/app.yaml")

	for _, want := range []string{
		"# Server configuration.",
		"where we bind",
		"# Logging is deliberately verbose in development.",
		"port: 9090",
		"host: localhost",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	if strings.Contains(out, "8080") {
		t.Errorf("old value still present:\n%s", out)
	}
}

// A write must never materialise values from other layers into the file it
// touches. This is the defect the whole design exists to remove, and the
// security-relevant case is an environment-supplied secret.
func TestApply_DoesNotFlattenOtherLayers(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{
		"/base.yaml": "server:\n  host: base-host\n  port: 8080\n",
		"/over.yaml": "server:\n  port: 9090\n",
	})

	s, err := NewStore(context.Background(), WithBackend(NewFileBackend(filesystem, "/base.yaml")),
		WithBackend(NewFileBackend(filesystem, "/over.yaml")),
		WithBackend(staticBackend{
			id: "env",
			layers: []Layer{{
				Source: Source{Kind: SourceEnv, Name: "APP_DB_TOKEN", Writable: false},
				Values: map[string]any{"db": map[string]any{"token": "s3cr3t"}},
			}},
		}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := s.Apply(context.Background(), Set("server.port", 7070)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	over := readFile(t, filesystem, "/over.yaml")

	if strings.Contains(over, "s3cr3t") {
		t.Errorf("environment secret was written into the file:\n%s", over)
	}

	if strings.Contains(over, "base-host") {
		t.Errorf("a value from another layer was materialised:\n%s", over)
	}

	if !strings.Contains(over, "7070") {
		t.Errorf("the intended change is missing:\n%s", over)
	}

	// The base is untouched: it was not a target.
	if base := readFile(t, filesystem, "/base.yaml"); !strings.Contains(base, "8080") {
		t.Errorf("an untargeted file was modified:\n%s", base)
	}
}

// The snapshot is built from what was written, so it is correct the instant
// Apply returns — no watcher, no re-read, no window of disagreement.
func TestApply_PublishesTheNewSnapshotImmediately(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "a: 1\nb: 2\n"})
	s := storeOn(t, filesystem, "/app.yaml")

	before := s.Snapshot()

	next, err := s.Apply(context.Background(), Set("a", 42))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got, _ := next.Get("a"); got != 42 {
		t.Errorf("returned snapshot a = %v, want 42", got)
	}

	if got, _ := s.Snapshot().Get("a"); got != 42 {
		t.Errorf("published snapshot a = %v, want 42", got)
	}

	if next.Version() <= before.Version() {
		t.Error("version did not advance")
	}

	// Untouched keys survive, and the snapshot the caller already held is
	// unaffected.
	if got, _ := next.Get("b"); got != 2 {
		t.Errorf("b = %v, want the untouched value 2", got)
	}

	if got, _ := before.Get("a"); got != 1 {
		t.Errorf("previously held snapshot changed: a = %v", got)
	}
}

func TestApply_RemoveAndCreate(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{
		"/app.yaml": "keep: 1\ndrop: 2\nnested:\n  only: 3\n",
	})
	s := storeOn(t, filesystem, "/app.yaml")

	snap, err := s.Apply(context.Background(),
		Remove("drop"),
		Set("brand.new", "value"),
		Remove("nested.only"),
	)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if snap.Has("drop") {
		t.Error("removed key still present")
	}

	if got, _ := snap.Get("brand.new"); got != "value" {
		t.Errorf("created key = %v, want value", got)
	}

	// Emptiness is a value: removing the last entry leaves the parent, it does
	// not delete it.
	if !snap.Has("nested") {
		t.Error("parent was removed when its last entry went — emptiness is a value")
	}

	out := readFile(t, filesystem, "/app.yaml")
	if !strings.Contains(out, "nested: {}") {
		t.Errorf("emptied mapping not written as an explicit empty map:\n%s", out)
	}
}

// Removing something already absent reaches the desired end state, so it is
// not worth failing a batch over.
func TestApply_RemovingAnAbsentKeyIsNotAnError(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "a: 1\n"})
	s := storeOn(t, filesystem, "/app.yaml")

	if _, err := s.Apply(context.Background(), Remove("never.existed")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

// A change landing between routing and commit must be detected rather than
// silently overwritten.
func TestApply_DetectsAForeignChange(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "a: 1\n"})
	s := storeOn(t, filesystem, "/app.yaml")

	// Someone else edits the file after the Store read it.
	if err := afero.WriteFile(filesystem, "/app.yaml", []byte("a: 99\nadded: elsewhere\n"), 0o644); err != nil {
		t.Fatalf("foreign write: %v", err)
	}

	_, err := s.Apply(context.Background(), Set("a", 2))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}

	// The foreign change survives: a conflict must not become a silent
	// overwrite.
	out := readFile(t, filesystem, "/app.yaml")
	if !strings.Contains(out, "added: elsewhere") {
		t.Errorf("the other writer's change was lost:\n%s", out)
	}

	if strings.Contains(out, "a: 2") {
		t.Errorf("the conflicting write was applied anyway:\n%s", out)
	}
}

// A multi-file apply commits every target or none that can be undone.
func TestApply_AcrossSeveralFiles(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{
		"/base.yaml": "# base\nonlybase: 1\nshared: base\n",
		"/over.yaml": "# overlay\nshared: over\n",
	})
	s := storeOn(t, filesystem, "/base.yaml", "/over.yaml")

	if _, err := s.Apply(context.Background(),
		Set("onlybase", 2),       // routes to base.yaml
		Set("shared", "changed"), // routes to over.yaml
	); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	base := readFile(t, filesystem, "/base.yaml")
	over := readFile(t, filesystem, "/over.yaml")

	if !strings.Contains(base, "onlybase: 2") || !strings.Contains(base, "# base") {
		t.Errorf("base not updated correctly:\n%s", base)
	}

	if !strings.Contains(over, "shared: changed") || !strings.Contains(over, "# overlay") {
		t.Errorf("overlay not updated correctly:\n%s", over)
	}

	// Each file keeps its own content: the base's shared value is untouched.
	if !strings.Contains(base, "shared: base") {
		t.Errorf("base's own copy of shared was modified:\n%s", base)
	}
}

// A write that will not be visible is still performed — that is what was asked
// — but the caller must be able to tell the user why nothing changed.
func TestApply_ReportsAShadowedWrite(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/base.yaml": "host: file-host\n"})

	s, err := NewStore(context.Background(),
		WithBackend(NewFileBackend(filesystem, "/base.yaml")),
		WithBackend(staticBackend{
			id: "env",
			layers: []Layer{{
				Source: Source{Kind: SourceEnv, Name: "APP_HOST", Writable: false},
				Values: map[string]any{"host": "env-host"},
			}},
		}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	plan, err := s.Plan(Set("host", "new-host"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if plan.Effective() {
		t.Error("plan reported as effective while the environment still wins")
	}

	if _, err := s.Apply(context.Background(), Set("host", "new-host")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The file changed...
	if out := readFile(t, filesystem, "/base.yaml"); !strings.Contains(out, "new-host") {
		t.Errorf("file was not written:\n%s", out)
	}

	// ...and the effective value did not, because the environment outranks it.
	if got, _ := s.Snapshot().Get("host"); got != "env-host" {
		t.Errorf("effective host = %v, want env-host to still win", got)
	}
}

func TestApply_CreatesAMissingFile(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/base.yaml": "a: 1\n"})
	s := storeOn(t, filesystem, "/base.yaml", "/local.yaml")

	if _, err := s.Apply(context.Background(), Set("b", 2)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The new key routes to the highest-precedence writable layer, which is
	// the overlay even though it did not exist yet.
	out := readFile(t, filesystem, "/local.yaml")
	if !strings.Contains(out, "b: 2") {
		t.Errorf("missing file was not created with the change:\n%s", out)
	}
}

func TestApply_MultiDocumentTargetsTheRightDocument(t *testing.T) {
	t.Parallel()

	const src = `# document zero
a: 1
onlyzero: true
---
# document one
a: 2
`

	filesystem := memFS(t, map[string]string{"/app.yaml": src})
	s := storeOn(t, filesystem, "/app.yaml")

	if _, err := s.Apply(context.Background(), Set("a", 3), Set("onlyzero", false)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	out := readFile(t, filesystem, "/app.yaml")

	if !strings.Contains(out, "# document zero") || !strings.Contains(out, "# document one") {
		t.Errorf("document comments lost:\n%s", out)
	}

	if strings.Count(out, "---") != 1 {
		t.Errorf("document separator lost:\n%s", out)
	}

	// a existed in both documents; the later one wins, so that is where it is
	// written. The earlier document keeps its own value.
	docs := strings.SplitN(out, "---", 2)
	if !strings.Contains(docs[0], "a: 1") {
		t.Errorf("document zero's value was changed:\n%s", out)
	}

	if !strings.Contains(docs[1], "a: 3") {
		t.Errorf("document one was not updated:\n%s", out)
	}

	if !strings.Contains(docs[0], "onlyzero: false") {
		t.Errorf("a key only in document zero was not updated there:\n%s", out)
	}
}

func TestApply_Rejects(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "a: 1\n"})
	s := storeOn(t, filesystem, "/app.yaml")

	t.Run("no changes", func(t *testing.T) {
		t.Parallel()

		if _, err := s.Apply(context.Background()); !errors.Is(err, ErrNoChanges) {
			t.Errorf("err = %v, want ErrNoChanges", err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if _, err := s.Apply(ctx, Set("a", 2)); err == nil {
			t.Error("Apply with a cancelled context should fail")
		}
	})
}

// A change routed at a backend that cannot persist must fail loudly rather
// than silently doing nothing.
func TestApply_NonWritableBackend(t *testing.T) {
	t.Parallel()

	s, err := NewStore(context.Background(), WithBackend(staticBackend{
		id: "static",
		layers: []Layer{{
			Source: Source{Kind: SourceFile, Name: "static", Writable: true},
			Values: map[string]any{"a": 1},
		}},
	}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := s.Apply(context.Background(), Set("a", 2)); !errors.Is(err, ErrNotWritable) {
		t.Errorf("err = %v, want ErrNotWritable", err)
	}
}

// staticBackend serves fixed layers and cannot be written to. It stands in for
// the env and flag layers, and for any read-only source.
type staticBackend struct {
	id     string
	layers []Layer
}

func (b staticBackend) ID() string { return b.id }

func (b staticBackend) Load(_ context.Context) ([]Layer, error) { return b.layers, nil }

func (b staticBackend) Capabilities() Capabilities { return Capabilities{} }

// Writing twice to the same file must work. The Store never re-reads after a
// commit — it builds the next snapshot from the staged layers — so unless the
// commit advances what the backend knows the file to hold, the second write
// compares against the content from load time and is reported as a conflict
// with the first.
func TestApply_SequentialWritesToTheSameFile(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "count: 0\nname: start\n"})
	s := storeOn(t, filesystem, "/app.yaml")

	for i := 1; i <= 5; i++ {
		if _, err := s.Apply(context.Background(), Set("count", i)); err != nil {
			t.Fatalf("write %d of 5: %v", i, err)
		}
	}

	if got := s.View().GetInt("count"); got != 5 {
		t.Errorf("count = %d, want 5", got)
	}

	content, err := afero.ReadFile(filesystem, "/app.yaml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if !strings.Contains(string(content), "count: 5") {
		t.Errorf("the file does not hold the last write:\n%s", content)
	}

	// Untouched keys survive every one of those rewrites.
	if !strings.Contains(string(content), "name: start") {
		t.Errorf("an unrelated key was lost across repeated writes:\n%s", content)
	}
}

// Advancing the fingerprint on commit must not blind the Store to genuine
// foreign edits — that check is the whole reason the fingerprint exists.
func TestApply_ForeignEditBetweenWritesStillConflicts(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "count: 0\n"})
	s := storeOn(t, filesystem, "/app.yaml")

	if _, err := s.Apply(context.Background(), Set("count", 1)); err != nil {
		t.Fatalf("first write: %v", err)
	}

	// Someone else edits the file behind the Store's back.
	if err := afero.WriteFile(filesystem, "/app.yaml", []byte("count: 99\nintruder: yes\n"), 0o644); err != nil {
		t.Fatalf("foreign write: %v", err)
	}

	_, err := s.Apply(context.Background(), Set("count", 2))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict — a foreign edit was silently overwritten", err)
	}

	// And their work is still there.
	content, readErr := afero.ReadFile(filesystem, "/app.yaml")
	if readErr != nil {
		t.Fatalf("read: %v", readErr)
	}

	if !strings.Contains(string(content), "intruder: yes") {
		t.Errorf("the foreign edit was discarded:\n%s", content)
	}
}

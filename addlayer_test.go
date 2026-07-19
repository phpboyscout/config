package config

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// A programmatic layer is how a tool contributes configuration it computed
// rather than read: a resolved path, a value negotiated with a server, a
// session-only toggle. It is an ordinary layer, so it takes part in precedence
// and provenance like any other and needs no separate concept.
func TestAddLayer_OverridesLowerSourcesAndReportsProvenance(t *testing.T) {
	t.Parallel()

	s := storeOn(t, memFS(t, map[string]string{
		"/app.yaml": "server:\n  host: file-host\n  port: 8080\n",
	}), "/app.yaml")

	if err := s.AddLayer(context.Background(), "computed",
		strings.NewReader("server:\n  host: computed-host\n")); err != nil {
		t.Fatalf("AddLayer: %v", err)
	}

	v := s.View()

	if got := v.GetString("server.host"); got != "computed-host" {
		t.Errorf("server.host = %q, want the added layer to win", got)
	}

	// Untouched keys still resolve from the file.
	if got := v.GetInt("server.port"); got != 8080 {
		t.Errorf("server.port = %d, want the file value to survive", got)
	}

	src, ok := v.Origin("server.host")
	if !ok {
		t.Fatal("no provenance for a value from an added layer")
	}

	if !strings.Contains(src.String(), "computed") {
		t.Errorf("provenance = %q, want it to name the added layer", src.String())
	}
}

// The point of adding a layer rather than editing a file: it is never written
// back to. Routing must not offer it as a target, so a later write lands in the
// file underneath and the added layer keeps winning.
func TestAddLayer_IsNeverAWriteTarget(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "host: file-host\n"})
	s := storeOn(t, filesystem, "/app.yaml")

	if err := s.AddLayer(context.Background(), "session",
		strings.NewReader("host: session-host\n")); err != nil {
		t.Fatalf("AddLayer: %v", err)
	}

	plan, err := s.Plan(Set("host", "written"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	op := plan.Operations[0]

	if op.Target.Name == "session" {
		t.Fatal("routed a write at an in-memory layer")
	}

	// And the caller is told the write will not be visible.
	if op.Effective() {
		t.Error("write reported as effective while the added layer still wins")
	}

	if _, err := s.Apply(context.Background(), Set("host", "written")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := readFile(t, filesystem, "/app.yaml"); !strings.Contains(got, "written") {
		t.Errorf("the write did not reach the file:\n%s", got)
	}

	if got := s.View().GetString("host"); got != "session-host" {
		t.Errorf("host = %q, want the added layer to still win", got)
	}
}

// It has to survive a reload, or it is not configuration — it is a value that
// silently disappears the first time a file changes on disk.
func TestAddLayer_SurvivesAReload(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "a: 1\n"})
	s := storeOn(t, filesystem, "/app.yaml")

	if err := s.AddLayer(context.Background(), "computed",
		strings.NewReader("b: 2\n")); err != nil {
		t.Fatalf("AddLayer: %v", err)
	}

	if err := afero.WriteFile(filesystem, "/app.yaml", []byte("a: 99\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := s.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if got := s.View().GetInt("b"); got != 2 {
		t.Errorf("b = %d, want the added layer to survive the reload", got)
	}

	if got := s.View().GetInt("a"); got != 99 {
		t.Errorf("a = %d, want the reloaded file value", got)
	}
}

// A layer that cannot be parsed must not be adopted. Leaving it in place would
// make every later reload fail on content the caller has no way to withdraw.
func TestAddLayer_RejectsUnusableContentWithoutAdoptingIt(t *testing.T) {
	t.Parallel()

	s := storeOn(t, memFS(t, map[string]string{"/app.yaml": "a: 1\n"}), "/app.yaml")

	err := s.AddLayer(context.Background(), "bad", strings.NewReader("a:\n  - broken\n :\n"))
	if err == nil {
		t.Fatal("unparseable content was accepted")
	}

	// The Store is untouched and still works.
	if got := s.View().GetInt("a"); got != 1 {
		t.Errorf("a = %d, want the configuration unchanged", got)
	}

	if err := s.Reload(context.Background()); err != nil {
		t.Fatalf("the rejected layer was adopted anyway: %v", err)
	}
}

// Two layers with the same name would be indistinguishable in provenance and
// ambiguous when rebuilding after a write.
func TestAddLayer_RefusesADuplicateName(t *testing.T) {
	t.Parallel()

	s := storeOn(t, memFS(t, map[string]string{"/app.yaml": "a: 1\n"}), "/app.yaml")

	if err := s.AddLayer(context.Background(), "same", strings.NewReader("b: 1\n")); err != nil {
		t.Fatalf("AddLayer: %v", err)
	}

	if err := s.AddLayer(context.Background(), "same", strings.NewReader("c: 1\n")); err == nil {
		t.Error("a duplicate layer name was accepted")
	}
}

// Adding a layer changes configuration and notifies, so it cascades exactly as
// a write would if attempted from inside an observer.
func TestAddLayer_IsRefusedFromInsideAnObserver(t *testing.T) {
	t.Parallel()

	s := storeOn(t, memFS(t, map[string]string{"/app.yaml": "a: 1\n"}), "/app.yaml")

	var got error

	s.AddObserverFunc(func(_ Observed) error {
		got = s.AddLayer(context.Background(), "from-observer", strings.NewReader("b: 1\n"))

		return nil
	})

	if _, err := s.Apply(context.Background(), Set("a", 2)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !errors.Is(got, ErrWriteFromObserver) {
		t.Errorf("err = %v, want ErrWriteFromObserver", got)
	}
}

// Observers hear about it, because the configuration genuinely changed.
func TestAddLayer_NotifiesObservers(t *testing.T) {
	t.Parallel()

	s := storeOn(t, memFS(t, map[string]string{"/app.yaml": "value: first\n"}), "/app.yaml")

	obs := &countingObserver{}
	s.AddObserver(obs)

	if err := s.AddLayer(context.Background(), "computed",
		strings.NewReader("value: second\n")); err != nil {
		t.Fatalf("AddLayer: %v", err)
	}

	if got := obs.count(); got != 1 {
		t.Errorf("observers notified %d times, want 1", got)
	}
}

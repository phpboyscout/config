package config

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/go/errors"
)

// Declaring an optional, not-yet-created source is a supported pattern at load
// time — creating it is how a new overlay comes into being. A write routed at a
// source that exists must therefore not fail because a *different* declared
// source is absent: rebuild re-reads every non-written backend, and treating
// the absence as fatal refused a perfectly writable edit with an error naming a
// file the caller never mentioned.
func TestApply_ToleratesAnAbsentSiblingDeclaredSource(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/base.yaml": "foo: bar\n"})

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/base.yaml", "/overlay.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// foo is defined in base.yaml, so the write routes there — not at the
	// absent overlay.
	if _, err := s.Apply(context.Background(), Set("foo", "baz")); err != nil {
		t.Fatalf("Apply with an absent sibling source: %v", err)
	}

	if got := s.View().GetString("foo"); got != "baz" {
		t.Errorf("foo = %q, want %q", got, "baz")
	}

	// The write landed in the routed target.
	if got := readFile(t, filesystem, "/base.yaml"); !strings.Contains(got, "foo: baz") {
		t.Errorf("base.yaml missing the routed write:\n%s", got)
	}

	// The absent source was carried forward, not created: only a write routed
	// *at* it brings it into being.
	if _, err := filesystem.ReadFile("/overlay.yaml"); err == nil {
		t.Error("the absent overlay was created by a write routed elsewhere")
	}
}

// A source required at load that vanishes before an Apply is tolerated like
// any other absent source: the snapshot becomes what the surviving sources
// resolve to plus the write. RequireFirstSource guards startup — a service must
// not come up with a hole in its configuration — but once running, refusing
// every write because a file disappeared would leave the one surface that can
// recreate configuration unable to write anything at all.
func TestApply_ToleratesAVanishedRequiredFirstSource(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{
		"/base.yaml":    "foo: bar\n",
		"/overlay.yaml": "over: lay\n",
	})

	s, err := NewStore(context.Background(),
		WithFiles(filesystem, "/base.yaml", "/overlay.yaml"),
		RequireFirstSource())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if err := filesystem.Remove("/base.yaml"); err != nil {
		t.Fatalf("removing the required base: %v", err)
	}

	// over is defined in overlay.yaml, so the write routes there — rebuild
	// re-reads the vanished base on the way.
	if _, err := s.Apply(context.Background(), Set("over", "written")); err != nil {
		t.Fatalf("Apply after the required base vanished: %v", err)
	}

	if got := s.View().GetString("over"); got != "written" {
		t.Errorf("over = %q, want %q", got, "written")
	}
}

// Only absence is tolerated. A sibling source that is present but unreadable —
// here, corrupted out of band after load — still fails the Apply, exactly as it
// fails a reload: silently dropping a layer would change the effective
// configuration without telling anyone. The staged write is discarded, so the
// routed target keeps its previous content.
func TestApply_AParseErrorInASiblingSourceStillFails(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{
		"/base.yaml":    "foo: bar\n",
		"/overlay.yaml": "over: lay\n",
	})

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/base.yaml", "/overlay.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if err := filesystem.WriteFile("/overlay.yaml", []byte(":\n\t- not yaml"), 0o600); err != nil {
		t.Fatalf("corrupting the overlay: %v", err)
	}

	if _, err := s.Apply(context.Background(), Set("foo", "baz")); err == nil {
		t.Fatal("Apply succeeded despite a parse error in a sibling source")
	}

	// The failed apply changed nothing: the staged write was discarded and the
	// last-known-good snapshot stands.
	if got := s.View().GetString("foo"); got != "bar" {
		t.Errorf("foo = %q after a failed apply, want the last-known-good %q", got, "bar")
	}

	if got := readFile(t, filesystem, "/base.yaml"); !strings.Contains(got, "foo: bar") {
		t.Errorf("base.yaml changed despite the failed apply:\n%s", got)
	}
}

// The tolerance is for fs.ErrNotExist alone, and load-time strictness is
// untouched: a required first source that is missing at construction still
// refuses to load.
func TestNewStore_RequiredFirstSourceStillFatalAtLoad(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/overlay.yaml": "over: lay\n"})

	_, err := NewStore(context.Background(),
		WithFiles(filesystem, "/base.yaml", "/overlay.yaml"),
		RequireFirstSource())
	if err == nil {
		t.Fatal("NewStore loaded despite the required first source being absent")
	}

	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "required source") {
		t.Errorf("err = %v, want the required-source refusal wrapping fs.ErrNotExist", err)
	}
}

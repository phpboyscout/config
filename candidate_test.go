package config

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// D15's central claim is that validation and the reload a write causes cannot
// disagree, because both build the candidate the same way. That only holds if
// the candidate is built the same way — a backend whose reading of its own
// input depends on the keys beneath it must be re-derived against the keys the
// write produces, not carried over from before it.
//
// The environment backend is that case. It maps APP_SERVER_PORT onto an
// existing key, so a write that introduces a second key spelled the same way
// makes the variable ambiguous. Carried over, the candidate cannot see it and
// the write lands — then the next reload fails on a file the process just
// wrote, leaving it running on last-known-good with no way back.
func TestApply_RefusesAWriteThatWouldBreakTheNextReload(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{
		"/app.yaml": "server:\n  port: 2\n",
	})

	s, err := NewStore(context.Background(),
		WithFiles(filesystem, "/app.yaml"),
		WithEnv("APP", envOf("APP_SERVER_PORT=9999")))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// server_port would be a second key the variable could designate.
	_, err = s.Apply(context.Background(), Set("server_port", 1))

	if err == nil {
		t.Fatal("the write was accepted; it makes the next reload fail")
	}

	if !errors.Is(err, ErrAmbiguousEnvKey) {
		t.Errorf("err = %v, want it to name the ambiguity the write would create", err)
	}

	// Nothing reached the file.
	content, readErr := filesystem.ReadFile("/app.yaml")
	if readErr != nil {
		t.Fatalf("read: %v", readErr)
	}

	if strings.Contains(string(content), "server_port") {
		t.Errorf("the refused write was applied anyway:\n%s", content)
	}

	// And the Store is still usable, which is the point of refusing early.
	if err := s.Reload(context.Background()); err != nil {
		t.Fatalf("the Store was left unable to reload: %v", err)
	}
}

// The converse: a write that changes the key space harmlessly must still go
// through. Re-deriving must not turn into refusing anything that touches keys.
func TestApply_AWriteThatChangesTheKeySpaceHarmlesslyIsAccepted(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "server:\n  port: 2\n"})

	s, err := NewStore(context.Background(),
		WithFiles(filesystem, "/app.yaml"),
		WithEnv("APP", envOf("APP_SERVER_PORT=9999")))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := s.Apply(context.Background(), Set("server.host", "h")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := s.View().GetString("server.host"); got != "h" {
		t.Errorf("server.host = %q, want h", got)
	}

	// The environment still wins where it applies, and still reloads cleanly.
	if got := s.View().GetInt("server.port"); got != 9999 {
		t.Errorf("server.port = %d, want the environment to still win", got)
	}

	if err := s.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
}

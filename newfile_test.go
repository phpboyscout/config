package config

import (
	"context"
	"strings"
	"testing"
)

// D13 routes a key that no file yet defines at the highest-precedence writable
// layer, which is routinely a configured overlay that does not exist. Creating
// that file must work for nested keys, not only flat ones — a user setting
// server.port on a fresh install is the ordinary case, not an edge one.
func TestApply_CreatesAMissingFileWithNestedKeys(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		change Change
		want   []string
	}{
		{
			name:   "flat scalar",
			change: Set("flat", "value"),
			want:   []string{"flat: value"},
		},
		{
			name:   "nested scalar",
			change: Set("server.port", 9090),
			want:   []string{"server:", "port: 9090"},
		},
		{
			name:   "deeply nested scalar",
			change: Set("a.b.c.d", "deep"),
			want:   []string{"a:", "b:", "c:", "d: deep"},
		},
		{
			name:   "map value",
			change: Set("server", map[string]any{"host": "h", "port": 1}),
			want:   []string{"server:", "host: h", "port: 1"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			filesystem := memFS(t, map[string]string{"/base.yaml": "existing: yes\n"})

			s, err := NewStore(context.Background(),
				WithFiles(filesystem, "/base.yaml", "/overlay.yaml"))
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}

			if _, err := s.Apply(context.Background(), c.change); err != nil {
				t.Fatalf("Apply: %v", err)
			}

			got, err := filesystem.ReadFile("/overlay.yaml")
			if err != nil {
				t.Fatalf("the overlay was not created: %v", err)
			}

			for _, want := range c.want {
				if !strings.Contains(string(got), want) {
					t.Errorf("missing %q in the created file:\n%s", want, got)
				}
			}

			// It must re-read as what was written, and survive a reload.
			if err := s.Reload(context.Background()); err != nil {
				t.Fatalf("the created file does not reload: %v", err)
			}
		})
	}
}

// A created file should be an ordinary block document, not the flow mapping the
// empty seed produced. Flow style is valid YAML but nothing authors config that
// way, and it is the shape a user has to edit by hand afterwards.
func TestApply_ACreatedFileIsBlockStyle(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/base.yaml": "existing: yes\n"})

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/base.yaml", "/overlay.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := s.Apply(context.Background(), Set("key", "value")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := readFile(t, filesystem, "/overlay.yaml")

	if strings.Contains(got, "{") || strings.Contains(got, "}") {
		t.Errorf("a created file should be block style, got flow:\n%s", got)
	}
}

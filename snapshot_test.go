package config

import (
	"testing"
)

func testSnapshot() *Snapshot {
	return newSnapshot(1, []Layer{
		{Source: fileSource("base.yaml", 0), Values: map[string]any{
			"server": map[string]any{
				"host": "base-host",
				"port": 8080,
				"tls":  map[string]any{"enabled": false},
			},
			"log":       map[string]any{"level": "info"},
			"emptymap":  map[string]any{},
			"emptylist": []any{},
		}},
		{Source: fileSource("overlay.yaml", 0), Values: map[string]any{
			"server": map[string]any{"port": 9090},
		}},
		{Source: Source{Kind: SourceEnv, Name: "APP_SERVER_HOST"}, Values: map[string]any{
			"server": map[string]any{"host": "env-host"},
		}},
	})
}

func TestSnapshot_Get(t *testing.T) {
	t.Parallel()

	s := testSnapshot()

	cases := []struct {
		path string
		want any
		ok   bool
	}{
		{"server.host", "env-host", true},
		{"server.port", 9090, true},
		{"server.tls.enabled", false, true},
		{"log.level", "info", true},
		{"SERVER.PORT", 9090, true}, // paths are case-insensitive
		{"missing", nil, false},
		{"server.missing", nil, false},
		{"server.port.deeper", nil, false}, // through a scalar
		{"", nil, false},
		{"a..b", nil, false},
	}

	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			t.Parallel()

			got, ok := s.Get(c.path)
			if ok != c.ok {
				t.Fatalf("Get(%q) ok = %v, want %v", c.path, ok, c.ok)
			}

			if ok && got != c.want {
				t.Errorf("Get(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

// Emptiness is a value: a key holding an empty container is present, and
// enumeration must agree with the getters about that.
func TestSnapshot_EmptyContainersArePresent(t *testing.T) {
	t.Parallel()

	s := testSnapshot()

	for _, path := range []string{"emptymap", "emptylist"} {
		if !s.Has(path) {
			t.Errorf("Has(%q) = false, want true — emptiness is a value", path)
		}

		if _, ok := s.Origin(path); !ok {
			t.Errorf("Origin(%q) missing — an empty container still came from somewhere", path)
		}
	}

	keys := s.Keys()
	seen := map[string]bool{}

	for _, k := range keys {
		seen[k] = true
	}

	for _, path := range []string{"emptymap", "emptylist"} {
		if !seen[path] {
			t.Errorf("Keys() omits %q while Has() reports it present", path)
		}
	}
}

func TestSnapshot_Origin(t *testing.T) {
	t.Parallel()

	s := testSnapshot()

	cases := []struct {
		path string
		want string
	}{
		{"server.host", "env:APP_SERVER_HOST"},
		{"server.port", "overlay.yaml"},
		{"server.tls.enabled", "base.yaml"},
		{"log.level", "base.yaml"},
	}

	for _, c := range cases {
		src, ok := s.Origin(c.path)
		if !ok {
			t.Errorf("Origin(%q) not found", c.path)

			continue
		}

		if got := src.String(); got != c.want {
			t.Errorf("Origin(%q) = %q, want %q", c.path, got, c.want)
		}
	}

	if _, ok := s.Origin("missing"); ok {
		t.Error("Origin reported a source for a missing key")
	}
}

// "Which file do I edit?" and "why is my edit not taking effect?" are the same
// question from two directions, and both need the full list.
func TestSnapshot_Shadowed(t *testing.T) {
	t.Parallel()

	s := testSnapshot()

	got := s.Shadowed("server.host")
	if len(got) != 2 {
		t.Fatalf("Shadowed(server.host) = %v, want 2 layers", got)
	}

	if got[0].String() != "base.yaml" {
		t.Errorf("lowest-precedence layer = %q, want base.yaml", got[0])
	}

	if got[len(got)-1].String() != "env:APP_SERVER_HOST" {
		t.Errorf("winning layer = %q, want env:APP_SERVER_HOST", got[len(got)-1])
	}

	if single := s.Shadowed("log.level"); len(single) != 1 {
		t.Errorf("Shadowed(log.level) = %v, want exactly one layer", single)
	}

	if none := s.Shadowed("missing"); len(none) != 0 {
		t.Errorf("Shadowed(missing) = %v, want none", none)
	}
}

// A snapshot that can be changed through a reference the caller kept is not
// immutable, so every exit point copies.
func TestSnapshot_IsImmutable(t *testing.T) {
	t.Parallel()

	original := map[string]any{"server": map[string]any{"port": 8080}}
	s := newSnapshot(1, []Layer{{Source: fileSource("a.yaml", 0), Values: original}})

	t.Run("mutating the source map after construction", func(t *testing.T) {
		t.Parallel()

		srv, _ := asStringMap(original["server"])
		srv["port"] = 1

		if got, _ := s.Get("server.port"); got != 8080 {
			t.Errorf("snapshot changed with its input: got %v, want 8080", got)
		}
	})

	t.Run("mutating the returned Values", func(t *testing.T) {
		t.Parallel()

		vals := s.Values()
		srv, _ := asStringMap(vals["server"])
		srv["port"] = 2

		if got, _ := s.Get("server.port"); got != 8080 {
			t.Errorf("snapshot changed through Values(): got %v, want 8080", got)
		}
	})

	t.Run("mutating the returned Layers", func(t *testing.T) {
		t.Parallel()

		layers := s.Layers()
		srv, _ := asStringMap(layers[0].Values["server"])
		srv["port"] = 3

		if got, _ := s.Get("server.port"); got != 8080 {
			t.Errorf("snapshot changed through Layers(): got %v, want 8080", got)
		}
	})
}

func TestSnapshot_VersionAndNilSafety(t *testing.T) {
	t.Parallel()

	s := newSnapshot(7, nil)
	if s.Version() != 7 {
		t.Errorf("Version() = %d, want 7", s.Version())
	}

	// A nil snapshot is what a caller holds before the first load; it should
	// answer honestly rather than panic.
	var nilSnap *Snapshot

	if nilSnap.Version() != 0 {
		t.Error("nil snapshot Version() should be 0")
	}

	if _, ok := nilSnap.Get("a"); ok {
		t.Error("nil snapshot Get() should report not found")
	}

	if nilSnap.Has("a") {
		t.Error("nil snapshot Has() should be false")
	}

	if _, ok := nilSnap.Origin("a"); ok {
		t.Error("nil snapshot Origin() should report not found")
	}

	if len(nilSnap.Shadowed("a")) != 0 || len(nilSnap.Layers()) != 0 || len(nilSnap.Keys()) != 0 {
		t.Error("nil snapshot accessors should return empty results")
	}

	if len(nilSnap.Values()) != 0 {
		t.Error("nil snapshot Values() should be empty")
	}
}

func TestSnapshot_Keys(t *testing.T) {
	t.Parallel()

	s := testSnapshot()
	keys := s.Keys()

	want := []string{"emptylist", "emptymap", "log.level", "server.host", "server.port", "server.tls.enabled"}
	if len(keys) != len(want) {
		t.Fatalf("Keys() = %v, want %v", keys, want)
	}

	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("Keys()[%d] = %q, want %q (sorted)", i, keys[i], want[i])
		}
	}
}

// Provenance is defined for leaves. A populated subtree is assembled from
// however many layers contributed, so naming one source for it would be
// dishonest.
func TestSnapshot_OriginIsLeafOnly(t *testing.T) {
	t.Parallel()

	s := testSnapshot()

	// server is assembled from base.yaml, overlay.yaml and the environment.
	if src, ok := s.Origin("server"); ok {
		t.Errorf("Origin(server) = %q, want not-found for a composite subtree", src)
	}

	// Shadowed is the honest answer for a composite.
	if got := s.Shadowed("server"); len(got) != 3 {
		t.Errorf("Shadowed(server) = %v, want all three contributing layers", got)
	}

	// An empty container is a leaf: it holds a value, just not entries.
	if _, ok := s.Origin("emptymap"); !ok {
		t.Error("Origin(emptymap) not found — an empty container is a leaf")
	}
}

// A subtree that starts empty and is later populated stops being a leaf, so
// its provenance must be withdrawn rather than left stale.
func TestSnapshot_EmptyThenPopulatedDropsLeafProvenance(t *testing.T) {
	t.Parallel()

	s := newSnapshot(1, []Layer{
		{Source: fileSource("base.yaml", 0), Values: map[string]any{"x": map[string]any{}}},
		{Source: fileSource("overlay.yaml", 0), Values: map[string]any{"x": map[string]any{"a": 1}}},
	})

	if src, ok := s.Origin("x"); ok {
		t.Errorf("Origin(x) = %q, want not-found once the subtree is populated", src)
	}

	if src, ok := s.Origin("x.a"); !ok || src.Name != "overlay.yaml" {
		t.Errorf("Origin(x.a) = %q/%v, want overlay.yaml", src, ok)
	}
}

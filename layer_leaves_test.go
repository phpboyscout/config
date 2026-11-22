package config

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"testing"
)

// Spec 0016. A layer's leaves follow the same rule as Snapshot.Keys, so the
// oracle throughout is the module's own fold over that one layer.

func leavesOf(l Layer) ([]string, map[string]any) {
	var paths []string

	values := map[string]any{}

	for path, v := range l.Leaves() {
		paths = append(paths, path)
		values[path] = v
	}

	return paths, values
}

func TestLayer_LeavesYieldsScalarsSequencesAndEmptyMaps(t *testing.T) {
	t.Parallel()

	l := layer("app.yaml", map[string]any{
		"server": map[string]any{"host": "h", "port": 8080},
		"hosts":  []any{"a", "b"},
		"extra":  map[string]any{},
	})

	paths, values := leavesOf(l)

	want := []string{"extra", "hosts", "server.host", "server.port"}
	if !slices.Equal(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}

	if !reflect.DeepEqual(values["hosts"], []any{"a", "b"}) {
		t.Errorf("hosts = %#v, want the sequence whole", values["hosts"])
	}

	if !reflect.DeepEqual(values["extra"], map[string]any{}) {
		t.Errorf("extra = %#v, want the empty map", values["extra"])
	}

	if values["server.port"] != 8080 {
		t.Errorf("server.port = %#v, want 8080", values["server.port"])
	}
}

func TestLayer_LeavesAgreesWithKeysForEachLayerOfAStore(t *testing.T) {
	t.Parallel()

	s := newTestStore(t, map[string]string{
		"/base.yaml": "server:\n  host: base\n  tls: {}\nhosts: [a, b]\nonly: {in: base}\nlog-level: info\nlog:\n  file: app.log\n",
		"/over.yaml": "server:\n  port: 9090\n  tls:\n    cert: c.pem\nhosts: []\nflags: {}\n",
	}, "/base.yaml", "/over.yaml")

	snap := s.Snapshot()
	union := map[string]bool{}

	for _, l := range snap.Layers() {
		paths, _ := leavesOf(l)

		want := newSnapshot(1, []Layer{l}, keysFolded).Keys()
		if !slices.Equal(paths, want) {
			t.Errorf("%s: Leaves = %v, want Keys of that layer alone %v", l.Source, paths, want)
		}

		for _, p := range paths {
			union[p] = true
		}
	}

	for _, k := range snap.Keys() {
		if !union[k] {
			t.Errorf("merged key %q is in no layer's Leaves", k)
		}
	}

	if !union["server.tls"] || !union["server.tls.cert"] {
		t.Errorf("union = %v, want both the lower empty map and the higher leaf under it", slices.Sorted(maps.Keys(union)))
	}
}

func TestLayer_LeavesOfAZeroLayerIsEmpty(t *testing.T) {
	t.Parallel()

	for path := range (Layer{}).Leaves() {
		t.Errorf("zero Layer yielded %q", path)
	}
}

func TestLayer_LeavesYieldsADuplicatedPathOnceWithTheValueGetReturns(t *testing.T) {
	t.Parallel()

	l := layer("hand", map[string]any{
		"a.b": 1,
		"a":   map[string]any{"b": 2},
	})

	paths, values := leavesOf(l)

	if want := []string{"a.b"}; !slices.Equal(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}

	got, _ := newSnapshot(1, []Layer{l}, keysFolded).Get("a.b")
	if values["a.b"] != got {
		t.Errorf("a.b = %v, want %v, the value Get returns", values["a.b"], got)
	}
}

func TestLayer_LeavesWalksMapAnyAnyAndYieldsValuesAsHeld(t *testing.T) {
	t.Parallel()

	held := []string{"x", "y"}
	l := layer("hand", map[string]any{
		"outer": map[any]any{"inner": held, 7: "seven"},
	})

	paths, values := leavesOf(l)

	if want := []string{"outer.7", "outer.inner"}; !slices.Equal(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}

	got, ok := values["outer.inner"].([]string)
	if !ok || &got[0] != &held[0] {
		t.Errorf("outer.inner = %#v, want the held slice itself", values["outer.inner"])
	}
}

func TestLayer_LeavesStopsWhenTheCallerBreaks(t *testing.T) {
	t.Parallel()

	l := layer("app.yaml", map[string]any{"a": 1, "b": 2, "c": 3})

	var seen []string

	for path := range l.Leaves() {
		seen = append(seen, path)

		break
	}

	if !slices.Equal(seen, []string{"a"}) {
		t.Errorf("seen = %v, want only the first path", seen)
	}
}

func TestLayer_LeavesFollowsTheStoresKeyCase(t *testing.T) {
	t.Parallel()

	files := map[string]string{"/app.yaml": "Database:\n  URL: postgres://x\n"}

	folded := newTestStore(t, files, "/app.yaml")

	exact, err := NewStore(context.Background(), WithFiles(memFS(t, files), "/app.yaml"), WithCaseSensitiveKeys())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	for _, tc := range []struct {
		name  string
		store *Store
		want  string
	}{
		{"folded", folded, "database.url"},
		{"exact", exact, "Database.URL"},
	} {
		paths, _ := leavesOf(tc.store.Snapshot().Layers()[0])
		if !slices.Equal(paths, []string{tc.want}) {
			t.Errorf("%s: paths = %v, want [%s]", tc.name, paths, tc.want)
		}
	}
}

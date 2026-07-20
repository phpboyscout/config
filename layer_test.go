package config

import (
	"testing"
)

// Merge and provenance are the foundation the Store rests on: routing decides
// where to write by asking which layer supplied a key, and diagnostics answer
// "why is this value what it is" from the same record. Both are wrong if the
// merge is wrong.

func fileSource(name string, doc int) Source {
	return Source{Kind: SourceFile, Name: name, Document: doc, Writable: true}
}

func layer(name string, values map[string]any) Layer {
	return Layer{Source: fileSource(name, 0), Values: values}
}

func TestMerge_LaterLayerWinsPerLeaf(t *testing.T) {
	t.Parallel()

	merged, origin := mergeLayers([]Layer{
		layer("base.yaml", map[string]any{
			"server": map[string]any{
				"host": "base-host",
				"port": 8080,
				"tls":  map[string]any{"enabled": false},
			},
			"log": map[string]any{"level": "info"},
		}),
		layer("overlay.yaml", map[string]any{
			"server": map[string]any{
				"port": 9090,
				"tls":  map[string]any{"cert": "/etc/cert.pem"},
			},
		}),
	})

	server, _ := asStringMap(merged["server"])
	tls, _ := asStringMap(server["tls"])
	log, _ := asStringMap(merged["log"])

	// The overlay changed one key; everything it did not mention survives.
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"overridden leaf", server["port"], 9090},
		{"untouched sibling", server["host"], "base-host"},
		{"untouched nested leaf", tls["enabled"], false},
		{"leaf added by the overlay", tls["cert"], "/etc/cert.pem"},
		{"untouched top-level subtree", log["level"], "info"},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}

	// Provenance follows the same per-leaf rule.
	wantOrigin := map[string]string{
		"server.port":        "overlay.yaml",
		"server.tls.cert":    "overlay.yaml",
		"server.host":        "base.yaml",
		"server.tls.enabled": "base.yaml",
		"log.level":          "base.yaml",
	}

	for path, want := range wantOrigin {
		if got := origin[path].Name; got != want {
			t.Errorf("provenance of %s: got %q, want %q", path, got, want)
		}
	}
}

// Keys carry the module's casing rule from the moment a layer enters the Store,
// so merging receives them already normalised and does not apply the rule a
// second time. The guarantee is asserted end to end by
// TestApply_RoutesAMixedCaseKeyToTheFileThatOwnsIt, which is the behaviour a
// user sees; this covers the rule itself.
func TestMerge_KeysAreLowercased(t *testing.T) {
	t.Parallel()

	merged, origin := mergeLayers(normalised([]Layer{
		layer("a.yaml", map[string]any{"Server": map[string]any{"Port": 1}}),
		layer("b.yaml", map[string]any{"SERVER": map[string]any{"PORT": 2}}),
	}))

	server, ok := asStringMap(merged["server"])
	if !ok {
		t.Fatalf("expected a lowercase 'server' key, got %v", merged)
	}

	// Differently-cased spellings are the same setting, so the later one wins
	// rather than creating a second key.
	if got := server["port"]; got != 2 {
		t.Errorf("server.port = %v, want 2 (later layer wins across casings)", got)
	}

	if len(merged) != 1 {
		t.Errorf("casing produced duplicate keys: %v", merged)
	}

	if got := origin["server.port"].Name; got != "b.yaml" {
		t.Errorf("provenance = %q, want b.yaml", got)
	}
}

func TestMerge_SequencesReplaceRatherThanAppend(t *testing.T) {
	t.Parallel()

	merged, _ := mergeLayers([]Layer{
		layer("base.yaml", map[string]any{"tags": []any{"alpha", "beta"}}),
		layer("overlay.yaml", map[string]any{"tags": []any{"gamma"}}),
	})

	tags, ok := merged["tags"].([]any)
	if !ok {
		t.Fatalf("tags is %T, want a slice", merged["tags"])
	}

	// Appending would make it impossible for an overlay to shorten a list, and
	// there would be no way to express "exactly these".
	if len(tags) != 1 || tags[0] != "gamma" {
		t.Errorf("tags = %v, want [gamma] — sequences replace, they do not append", tags)
	}
}

func TestMerge_ScalarOverSubtreeTakesOwnership(t *testing.T) {
	t.Parallel()

	merged, origin := mergeLayers([]Layer{
		layer("base.yaml", map[string]any{
			"thing": map[string]any{"a": 1, "b": 2},
		}),
		layer("overlay.yaml", map[string]any{"thing": "scalar"}),
	})

	if got := merged["thing"]; got != "scalar" {
		t.Errorf("thing = %v, want the scalar to win", got)
	}

	if got := origin["thing"].Name; got != "overlay.yaml" {
		t.Errorf("provenance of thing = %q, want overlay.yaml", got)
	}

	// The keys beneath are no longer reachable, so claiming provenance for
	// them would be a lie.
	for _, orphan := range []string{"thing.a", "thing.b"} {
		if src, ok := origin[orphan]; ok {
			t.Errorf("provenance retained for unreachable key %s (%s)", orphan, src)
		}
	}
}

func TestMerge_SubtreeOverScalarReplacesCleanly(t *testing.T) {
	t.Parallel()

	merged, origin := mergeLayers([]Layer{
		layer("base.yaml", map[string]any{"thing": "scalar"}),
		layer("overlay.yaml", map[string]any{
			"thing": map[string]any{"a": 1},
		}),
	})

	thing, ok := asStringMap(merged["thing"])
	if !ok {
		t.Fatalf("thing is %T, want a mapping", merged["thing"])
	}

	if thing["a"] != 1 {
		t.Errorf("thing.a = %v, want 1", thing["a"])
	}

	if got := origin["thing.a"].Name; got != "overlay.yaml" {
		t.Errorf("provenance of thing.a = %q, want overlay.yaml", got)
	}
}

// A document is a layer, so a multi-document file contributes several — and
// provenance must distinguish them.
func TestMerge_DocumentsAreLayers(t *testing.T) {
	t.Parallel()

	merged, origin := mergeLayers([]Layer{
		{Source: fileSource("app.yaml", 0), Values: map[string]any{
			"log": map[string]any{"level": "info"}, "shared": "from-doc-0",
		}},
		{Source: fileSource("app.yaml", 1), Values: map[string]any{
			"shared": "from-doc-1",
		}},
	})

	if got := merged["shared"]; got != "from-doc-1" {
		t.Errorf("shared = %v, want the later document to win", got)
	}

	if got := origin["shared"]; got.Document != 1 {
		t.Errorf("provenance document index = %d, want 1", got.Document)
	}

	if got := origin["shared"].String(); got != "app.yaml#1" {
		t.Errorf("provenance string = %q, want app.yaml#1", got)
	}

	// The first document still owns what the second did not mention.
	if got := origin["log.level"]; got.Document != 0 {
		t.Errorf("log.level document = %d, want 0", got.Document)
	}
}

func TestMerge_EmptyContainersArePreserved(t *testing.T) {
	t.Parallel()

	merged, origin := mergeLayers([]Layer{
		layer("a.yaml", map[string]any{
			"emptymap":  map[string]any{},
			"emptylist": []any{},
			"present":   map[string]any{"a": 1},
		}),
	})

	// Emptiness is a value: code may require the parent to exist while it
	// holds nothing, so an empty container must not vanish in the merge.
	if _, ok := merged["emptymap"]; !ok {
		t.Error("empty map was dropped by the merge")
	}

	if _, ok := merged["emptylist"]; !ok {
		t.Error("empty list was dropped by the merge")
	}

	if _, ok := origin["emptymap"]; !ok {
		t.Error("empty map has no provenance")
	}
}

func TestMerge_HandlesMapAnyAny(t *testing.T) {
	t.Parallel()

	// A YAML decoder targeting `any` can yield map[any]any. Treating it as a
	// scalar would replace a subtree instead of merging into it.
	merged, origin := mergeLayers([]Layer{
		layer("base.yaml", map[string]any{
			"server": map[any]any{"host": "base", "port": 8080},
		}),
		layer("overlay.yaml", map[string]any{
			"server": map[any]any{"port": 9090},
		}),
	})

	server, ok := asStringMap(merged["server"])
	if !ok {
		t.Fatalf("server is %T, want a mapping", merged["server"])
	}

	if server["host"] != "base" {
		t.Errorf("host = %v, want the untouched sibling to survive", server["host"])
	}

	if server["port"] != 9090 {
		t.Errorf("port = %v, want 9090", server["port"])
	}

	if got := origin["server.host"].Name; got != "base.yaml" {
		t.Errorf("provenance of server.host = %q, want base.yaml", got)
	}
}

func TestMerge_NonWritableLayersAreIdentifiable(t *testing.T) {
	t.Parallel()

	_, origin := mergeLayers([]Layer{
		layer("app.yaml", map[string]any{"db": map[string]any{"host": "file-host"}}),
		{
			Source: Source{Kind: SourceEnv, Name: "APP_DB_HOST", Writable: false},
			Values: map[string]any{"db": map[string]any{"host": "env-host"}},
		},
	})

	src := origin["db.host"]

	// Routing walks layers in reverse precedence looking for somewhere to
	// write; an env layer must be visibly unwritable so it is skipped rather
	// than attempted.
	if src.Writable {
		t.Error("env layer reported as writable")
	}

	if src.Kind != SourceEnv {
		t.Errorf("kind = %q, want %q", src.Kind, SourceEnv)
	}

	if got, want := src.String(), "env:APP_DB_HOST"; got != want {
		t.Errorf("provenance string = %q, want %q", got, want)
	}
}

func TestSource_String(t *testing.T) {
	t.Parallel()

	cases := []struct {
		src  Source
		want string
	}{
		{Source{Kind: SourceFile, Name: "app.yaml"}, "app.yaml"},
		{Source{Kind: SourceFile, Name: "app.yaml", Document: 2}, "app.yaml#2"},
		{Source{Kind: SourceEnv, Name: "APP_PORT"}, "env:APP_PORT"},
		{Source{Kind: SourceFlag, Name: "--port"}, "flag:--port"},
		{Source{Kind: SourceDefault}, "default"},
		{Source{Kind: SourceOverride}, "override"},
	}

	for _, c := range cases {
		if got := c.src.String(); got != c.want {
			t.Errorf("Source%+v.String() = %q, want %q", c.src, got, c.want)
		}
	}
}

// Snapshots are immutable, and immutability that depends on callers behaving
// is not immutability.
func TestCloneMap_IsDeep(t *testing.T) {
	t.Parallel()

	original := map[string]any{
		"server": map[string]any{"tls": map[string]any{"enabled": true}},
		"tags":   []any{"a", "b"},
	}

	clone := cloneMap(original)

	server, _ := asStringMap(clone["server"])
	tls, _ := asStringMap(server["tls"])
	tls["enabled"] = false
	clone["tags"].([]any)[0] = "mutated"

	origServer, _ := asStringMap(original["server"])
	origTLS, _ := asStringMap(origServer["tls"])

	if origTLS["enabled"] != true {
		t.Error("mutating the clone changed the original's nested map")
	}

	if original["tags"].([]any)[0] != "a" {
		t.Error("mutating the clone changed the original's slice")
	}
}

// TestSource_StringDistinguishesDocumentsForAnyKind covers a multi-document
// source that is not a YAML file.
//
// The document index used to be rendered only for SourceFile, so a backend
// contributing one layer per document — a JSONL file is the obvious case —
// had every layer render identically and provenance could not say which one
// supplied a value.
func TestSource_StringDistinguishesDocumentsForAnyKind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  Source
		want string
	}{
		{"file, first document", Source{Kind: SourceFile, Name: "/app.yaml"}, "/app.yaml"},
		{"file, later document", Source{Kind: SourceFile, Name: "/app.yaml", Document: 2}, "/app.yaml#2"},
		{"custom kind", Source{Kind: SourceKind("jsonl"), Name: "/s.jsonl"}, "jsonl:/s.jsonl"},
		{
			"custom kind, later document",
			Source{Kind: SourceKind("jsonl"), Name: "/s.jsonl", Document: 1},
			"jsonl:/s.jsonl#1",
		},
		{"named env", Source{Kind: SourceEnv, Name: "APP_PORT"}, "env:APP_PORT"},
		{"kind with no name", Source{Kind: SourceKind("consul")}, "consul"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.src.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

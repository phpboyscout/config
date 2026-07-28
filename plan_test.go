package config

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Routing decides where an edit lands. Getting it wrong is not a crash — it is
// a change written somewhere the user did not expect, or written somewhere it
// will never be read. Both are silent, so the rules are tested directly.

func routingSnapshot() *Snapshot {
	return newSnapshot(1, []Layer{
		{Source: fileSource("base.yaml", 0), Values: map[string]any{
			"server": map[string]any{"host": "base-host", "port": 8080},
			"only":   map[string]any{"in": "base"},
		}},
		{Source: fileSource("over.yaml", 0), Values: map[string]any{
			"server": map[string]any{"port": 9090},
		}},
		{Source: Source{Kind: SourceEnv, Name: "APP_SERVER_HOST", Writable: false},
			Values: map[string]any{"server": map[string]any{"host": "env-host"}}},
	})
}

// sourcesOf lists a snapshot's sources in precedence order, which is what
// routing walks to decide what outranks a target.
func sourcesOf(snap *Snapshot) []Source {
	out := make([]Source, 0, len(snap.layers))
	for _, l := range snap.layers {
		out = append(out, l.Source)
	}

	return out
}

func planFor(t *testing.T, snap *Snapshot, changes ...Change) *Plan {
	t.Helper()

	p, err := route(snap, writableOf(snap), writableOf(snap), sourcesOf(snap), nil, nil, changes)
	if err != nil {
		t.Fatalf("route: %v", err)
	}

	return p
}

// An edit should land where it will be read back. Writing to the base when an
// overlay already wins looks to the user like the write silently failed.
func TestRoute_ExistingKeyGoesToTheHighestWritableLayerThatDefinesIt(t *testing.T) {
	t.Parallel()

	p := planFor(t, routingSnapshot(), Set("server.port", 7070))
	op := p.Operations[0]

	if op.Target.Name != "over.yaml" {
		t.Errorf("target = %q, want over.yaml (the layer already winning)", op.Target.Name)
	}

	if op.Creates {
		t.Error("Creates = true for a key that already exists there")
	}

	if !op.Effective() {
		t.Errorf("operation reported as shadowed by %v, want effective", op.ShadowedBy)
	}
}

func TestRoute_KeyOnlyInTheBaseGoesToTheBase(t *testing.T) {
	t.Parallel()

	p := planFor(t, routingSnapshot(), Set("only.in", "changed"))

	if got := p.Operations[0].Target.Name; got != "base.yaml" {
		t.Errorf("target = %q, want base.yaml — the only layer defining it", got)
	}
}

func TestRoute_NewKeyGoesToTheHighestWritableLayer(t *testing.T) {
	t.Parallel()

	p := planFor(t, routingSnapshot(), Set("brand.new", "value"))
	op := p.Operations[0]

	if op.Target.Name != "over.yaml" {
		t.Errorf("target = %q, want over.yaml — where a new key will be visible", op.Target.Name)
	}

	if !op.Creates {
		t.Error("Creates = false for a key that exists nowhere")
	}
}

// Environment variables, flags and defaults are readable but cannot be
// persisted to, so routing must skip them rather than attempt a write that
// cannot work.
func TestRoute_SkipsNonWritableLayers(t *testing.T) {
	t.Parallel()

	p := planFor(t, routingSnapshot(), Set("server.host", "new-host"))
	op := p.Operations[0]

	if op.Target.Kind == SourceEnv {
		t.Fatal("routed a write to the environment layer")
	}

	if op.Target.Name != "base.yaml" {
		t.Errorf("target = %q, want base.yaml — the highest writable layer defining it", op.Target.Name)
	}
}

// Writing the file is what was asked, so this is not an error — but a caller
// that cannot tell the user "written, but the environment still wins" leaves
// them wondering why nothing happened.
func TestRoute_ReportsWhenAWriteWillBeShadowed(t *testing.T) {
	t.Parallel()

	p := planFor(t, routingSnapshot(), Set("server.host", "new-host"))
	op := p.Operations[0]

	if op.Effective() {
		t.Fatal("write to a shadowed key reported as effective")
	}

	if len(op.ShadowedBy) != 1 {
		t.Fatalf("ShadowedBy = %v, want exactly the env layer", op.ShadowedBy)
	}

	if got := op.ShadowedBy[0].String(); got != "env:APP_SERVER_HOST" {
		t.Errorf("shadowing source = %q, want env:APP_SERVER_HOST", got)
	}

	if p.Effective() {
		t.Error("Plan.Effective() = true when one operation is shadowed")
	}

	if !strings.Contains(p.String(), "shadowed by env:APP_SERVER_HOST") {
		t.Errorf("dry run does not mention the shadowing:\n%s", p)
	}
}

func TestRoute_ExplicitTargetOverridesRouting(t *testing.T) {
	t.Parallel()

	base := fileSource("base.yaml", 0)
	p := planFor(t, routingSnapshot(), Change{Path: "server.port", Value: 1, Target: &base})
	op := p.Operations[0]

	if op.Target.Name != "base.yaml" {
		t.Errorf("target = %q, want the pinned base.yaml", op.Target.Name)
	}

	// Pinning below the winning layer means the edit will not be read back,
	// and the caller must be told.
	if op.Effective() {
		t.Error("pinned write below a defining layer reported as effective")
	}
}

func TestRoute_NoWritableLayer(t *testing.T) {
	t.Parallel()

	snap := newSnapshot(1, []Layer{
		{Source: Source{Kind: SourceEnv, Name: "APP_A", Writable: false},
			Values: map[string]any{"a": 1}},
	})

	if _, err := route(snap, writableOf(snap), writableOf(snap), sourcesOf(snap), nil, nil, []Change{Set("a", 2)}); !errors.Is(err, ErrNoWritableLayer) {
		t.Errorf("err = %v, want ErrNoWritableLayer", err)
	}
}

func TestRoute_Rejects(t *testing.T) {
	t.Parallel()

	snap := routingSnapshot()

	t.Run("no changes", func(t *testing.T) {
		t.Parallel()

		if _, err := route(snap, writableOf(snap), writableOf(snap), sourcesOf(snap), nil, nil, nil); !errors.Is(err, ErrNoChanges) {
			t.Errorf("err = %v, want ErrNoChanges", err)
		}
	})

	t.Run("malformed paths", func(t *testing.T) {
		t.Parallel()

		for _, path := range []string{"", "a..b", ".", "a."} {
			if _, err := route(snap, writableOf(snap), writableOf(snap), sourcesOf(snap), nil, nil, []Change{Set(path, 1)}); !errors.Is(err, ErrInvalidPath) {
				t.Errorf("route(%q) err = %v, want ErrInvalidPath", path, err)
			}
		}
	})
}

// A document is a layer, so routing must choose between documents of the same
// file exactly as it chooses between files.
func TestRoute_ChoosesBetweenDocumentsOfOneFile(t *testing.T) {
	t.Parallel()

	snap := newSnapshot(1, []Layer{
		{Source: fileSource("app.yaml", 0), Values: map[string]any{"a": 1, "onlydoc0": true}},
		{Source: fileSource("app.yaml", 1), Values: map[string]any{"a": 2}},
	})

	p := planFor(t, snap, Set("a", 3), Set("onlydoc0", false), Set("brandnew", 1))

	if got := p.Operations[0].Target.Document; got != 1 {
		t.Errorf("existing key routed to document %d, want 1 (the winner)", got)
	}

	if got := p.Operations[1].Target.Document; got != 0 {
		t.Errorf("key only in document 0 routed to document %d, want 0", got)
	}

	if got := p.Operations[2].Target.Document; got != 1 {
		t.Errorf("new key routed to document %d, want 1 (highest writable)", got)
	}
}

func TestPlan_Targets(t *testing.T) {
	t.Parallel()

	p := planFor(t, routingSnapshot(),
		Set("server.port", 1), // over.yaml
		Set("only.in", "x"),   // base.yaml
		Set("server.port", 2), // over.yaml again
	)

	targets := p.Targets()
	if len(targets) != 2 {
		t.Fatalf("Targets() = %v, want 2 distinct layers", targets)
	}
}

func TestPlan_StringIsReadable(t *testing.T) {
	t.Parallel()

	p := planFor(t, routingSnapshot(), Set("brand.new", 1), Remove("server.port"))

	out := p.String()
	for _, want := range []string{"set brand.new", "new key", "remove server.port", "over.yaml"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run missing %q:\n%s", want, out)
		}
	}

	empty := &Plan{}
	if empty.String() != "no changes" {
		t.Errorf("empty plan renders as %q", empty.String())
	}
}

// writableOf lists the writable layers of a snapshot, which is what the Store
// passes to routing when every configured source produced layers.
func writableOf(snap *Snapshot) []Source {
	var out []Source

	for _, l := range snap.layers {
		if l.Source.Writable {
			out = append(out, l.Source)
		}
	}

	return out
}

// A key no file yet defines routes at the highest-precedence writable layer,
// which is routinely a configured file that does not exist. The shadowing
// report has to survive that: "you set this and the environment still wins" is
// exactly the feedback a user needs, and it is needed most when they are
// setting the value for the first time.
func TestRoute_ReportsShadowingForAFileThatDoesNotExistYet(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/base.yaml": "unrelated: 1\n"})

	s, err := NewStore(context.Background(),
		WithFiles(filesystem, "/base.yaml", "/overlay.yaml"),
		WithEnv("APP", envOf("APP_DB_HOST=from-env")))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	plan, err := s.Plan(Set("db.host", "from-file"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	op := plan.Operations[0]

	if op.Effective() {
		t.Error("the write is reported as effective while the environment still wins")
	}

	if len(op.ShadowedBy) != 1 {
		t.Fatalf("ShadowedBy = %v, want the environment variable named", op.ShadowedBy)
	}

	if got := op.ShadowedBy[0].String(); got != "env:APP_DB_HOST" {
		t.Errorf("shadowed by %q, want env:APP_DB_HOST", got)
	}
}

// Two layers can share a Source.Name — two backends configured over the same
// path, and, once stores compose, an inner and an outer layer that happen to
// agree. Pinning by name then has to pick one, and the two halves of the router
// disagreed about which: matchTarget walked the writable targets forward, which
// is lowest precedence first, while findTarget five lines below walked the same
// slice backward because "the highest-precedence writable target is where it
// will be visible".
//
// So a pinned name resolved to the copy whose value nobody reads. They now
// agree, because two matching rules in one router is exactly the drift the
// equivalence assertions elsewhere in this file exist to prevent.
func TestMatchTarget_PrefersTheHighestPrecedenceLayerWithThatName(t *testing.T) {
	t.Parallel()

	// Same Name, different Kind — which is what makes them two distinguishable
	// layers rather than one. matchTarget compares Name and Document only, so
	// both are candidates and it has to choose.
	lower := Source{Kind: SourceKind("remote"), Name: "shared", Writable: true}
	upper := Source{Kind: SourceFile, Name: "shared", Writable: true}

	snap := newSnapshot(1, []Layer{
		{Source: lower, Values: map[string]any{"which": "lower"}},
		{Source: upper, Values: map[string]any{"which": "upper"}},
	})

	p := planFor(t, snap, Change{Path: "which", Value: "x", Target: &Source{Name: "shared"}})

	if got := p.Operations[0].Target; got != upper {
		t.Errorf("pinned target = %+v, want the highest-precedence layer of that "+
			"name (%+v) — the lower copy is the one nobody reads", got, upper)
	}
}

// To is the ergonomic form of pinning: one expression instead of three
// statements and a pointer, and reachable inside the batch Apply is built
// around. It must produce exactly what setting Change.Target by hand produces —
// if the two can diverge, the option has become a second router.
func TestTo_MatchesHandBuiltTarget(t *testing.T) {
	t.Parallel()

	snap := routingSnapshot()
	base := Source{Name: "base.yaml"}

	byHand := planFor(t, snap, Change{Path: "server.port", Value: 1, Target: &base})
	byOption := planFor(t, snap, Set("server.port", 1, To("base.yaml")))

	if !reflect.DeepEqual(byHand.Operations[0], byOption.Operations[0]) {
		t.Errorf("To produced a different operation from the hand-built target:\n"+
			" hand: %+v\n  opt: %+v", byHand.Operations[0], byOption.Operations[0])
	}
}

func TestTo_UnknownNameIsInvalidTarget(t *testing.T) {
	t.Parallel()

	_, err := route(routingSnapshot(), writableOf(routingSnapshot()), writableOf(routingSnapshot()), sourcesOf(routingSnapshot()),
		nil, nil, []Change{Set("server.port", 1, To("nope.yaml"))})

	if !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("err = %v, want ErrInvalidTarget", err)
	}

	if !strings.Contains(err.Error(), "nope.yaml") {
		t.Errorf("error %q does not name the target the caller asked for", err)
	}
}

// A read-only layer is a different fault from having nowhere to write: the
// caller named something specific, and it was the wrong thing.
func TestTo_ReadOnlyLayerIsInvalidTargetNotNoWritableLayer(t *testing.T) {
	t.Parallel()

	snap := routingSnapshot()

	_, err := route(snap, writableOf(snap), writableOf(snap), sourcesOf(snap), nil, nil,
		[]Change{Set("server.host", "x", To("APP_SERVER_HOST"))})

	if !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("err = %v, want ErrInvalidTarget for a read-only layer", err)
	}
}

// Remove takes options too. Setting and removing route identically, so an
// option that reached only one of them would be a trap.
func TestRemove_AcceptsTo(t *testing.T) {
	t.Parallel()

	p := planFor(t, routingSnapshot(), Remove("server.port", To("base.yaml")))

	if got := p.Operations[0].Target.Name; got != "base.yaml" {
		t.Errorf("target = %q, want the pinned base.yaml", got)
	}
}

// The second document of a multi-document file cannot be addressed by name
// alone, because every document in it shares one.
func TestToDocument_AddressesASpecificDocument(t *testing.T) {
	t.Parallel()

	snap := newSnapshot(1, []Layer{
		{Source: fileSource("multi.yaml", 0), Values: map[string]any{"a": 1}},
		{Source: fileSource("multi.yaml", 1), Values: map[string]any{"b": 2}},
	})

	p := planFor(t, snap, Set("a", 9, ToDocument("multi.yaml", 1)))

	if got := p.Operations[0].Target.Document; got != 1 {
		t.Errorf("document = %d, want 1", got)
	}

	// To alone means document 0, rather than "any document of that file".
	q := planFor(t, snap, Set("a", 9, To("multi.yaml")))
	if got := q.Operations[0].Target.Document; got != 0 {
		t.Errorf("To(name) document = %d, want 0", got)
	}
}

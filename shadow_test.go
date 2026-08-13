package config_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/go/config"
)

// Spec 0011. The primitives already composed; what was missing was that every
// consumer wrote the same sweep. These pin the decisions that make one call
// answerable rather than the sweep itself.

// threeLayers is the fixture the spec requires: three layers defining one path,
// because two will pass an implementation that reports only the winner and the
// layer immediately beneath it.
func threeLayers(t *testing.T) *config.Store {
	t.Helper()

	store, err := config.NewStore(context.Background(),
		config.WithReaders(
			config.NamedSource{Name: "defaults", Content: []byte(
				"server:\n  host: a\n  port: 1\ntoken: literal-one\nonly: here\n")},
			config.NamedSource{Name: "project", Content: []byte(
				"server:\n  port: 2\ntoken: literal-two\n")},
		),
		config.WithEnv("APP", config.WithEnviron(func() []string {
			return []string{"APP_TOKEN=from-env"}
		})),
	)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	return store
}

func shadowFor(shadows []config.Shadow, path string) (config.Shadow, bool) {
	for _, s := range shadows {
		if s.Path == path {
			return s, true
		}
	}

	return config.Shadow{}, false
}

func TestShadows_ReportsEveryLayerBeneathTheWinner(t *testing.T) {
	t.Parallel()

	// The motivating case: a credential in effect from the environment with two
	// literals underneath it. Reporting only the nearest one would leave a
	// literal in a file unreported, which is the leak this exists to surface.
	shadows := threeLayers(t).View().Snapshot().Shadows()

	got, ok := shadowFor(shadows, "token")
	if !ok {
		t.Fatalf("token missing from the report: %+v", shadows)
	}

	if got.InEffect.Name != "APP_TOKEN" {
		t.Errorf("InEffect = %q, want the environment layer", got.InEffect.Name)
	}

	var names []string
	for _, s := range got.Shadowed {
		names = append(names, s.Name)
	}

	// Lowest precedence first, matching Shadowed's own ordering.
	if want := "defaults,project"; strings.Join(names, ",") != want {
		t.Errorf("Shadowed = %v, want %s — all of them, lowest first", names, want)
	}
}

func TestShadows_OmitsPathsOnlyOneLayerDefines(t *testing.T) {
	t.Parallel()

	// D2. A path defined once is not shadowed, and including it would make the
	// common case — a large configuration with a handful of duplicates —
	// require filtering before it could be used.
	shadows := threeLayers(t).View().Snapshot().Shadows()

	if _, ok := shadowFor(shadows, "only"); ok {
		t.Error("a singly-defined path must not appear in the report")
	}

	if _, ok := shadowFor(shadows, "server.host"); ok {
		t.Error("server.host is defined once; it must not appear")
	}
}

func TestShadows_IsLeavesOnly(t *testing.T) {
	t.Parallel()

	// D1, and the disagreement this spec exists to settle: Shadowed("server")
	// answers for a populated subtree, but Origin refuses it and Keys omits it.
	// The report follows Origin and Keys, because those are what a caller
	// reaches for next.
	snap := threeLayers(t).View().Snapshot()

	if n := len(snap.Shadowed("server")); n < 2 {
		t.Fatalf("precondition: Shadowed(server) = %d layers, want the subtree "+
			"to be defined by more than one", n)
	}

	if _, ok := shadowFor(snap.Shadows(), "server"); ok {
		t.Error("a populated subtree must not appear — its layer in effect " +
			"cannot be stated honestly, which is why Origin refuses it")
	}
}

func TestShadows_IsOrderedByPath(t *testing.T) {
	t.Parallel()

	// D5. doctor renders the whole report, and one whose line order changes
	// between runs cannot be diffed.
	//
	// Enough entries, asked repeatedly, that an unsorted implementation cannot
	// pass by luck. Go randomises map iteration per range, so a two-entry
	// fixture would let a map-order implementation through half the time — a
	// coin-flip test is worse than none.
	var pairs []string
	for _, k := range []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"} {
		pairs = append(pairs, k+": v\n")
	}

	doc := strings.Join(pairs, "")

	store, err := config.NewStore(context.Background(),
		config.WithReaders(
			config.NamedSource{Name: "lower", Content: []byte(doc)},
			config.NamedSource{Name: "upper", Content: []byte(doc)},
		),
	)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const want = 8

	for attempt := range 5 {
		shadows := store.View().Snapshot().Shadows()

		if len(shadows) != want {
			t.Fatalf("attempt %d: %d entries, want %d — every key is in both layers",
				attempt, len(shadows), want)
		}

		paths := make([]string, 0, len(shadows))
		for _, sh := range shadows {
			paths = append(paths, sh.Path)
		}

		if !slices.IsSorted(paths) {
			t.Fatalf("attempt %d: not sorted by path: %v", attempt, paths)
		}
	}
}

func TestShadows_AgreesWithOriginAndShadowed(t *testing.T) {
	t.Parallel()

	// The report generalises two existing calls, so it must not disagree with
	// them about any path it does report. D1's documented difference is about
	// which paths appear, never about what they say.
	snap := threeLayers(t).View().Snapshot()

	for _, sh := range snap.Shadows() {
		origin, ok := snap.Origin(sh.Path)
		if !ok {
			t.Errorf("%s: reported but Origin says not-found", sh.Path)

			continue
		}

		if origin != sh.InEffect {
			t.Errorf("%s: InEffect = %v, Origin = %v", sh.Path, sh.InEffect, origin)
		}

		if all := snap.Shadowed(sh.Path); len(all) != len(sh.Shadowed)+1 {
			t.Errorf("%s: Shadowed(path) has %d layers, report accounts for %d",
				sh.Path, len(all), len(sh.Shadowed)+1)
		}
	}
}

func TestShadows_CarriesNoValues(t *testing.T) {
	t.Parallel()

	// D7. The motivating consumer is contractually forbidden from printing
	// credential values, so the report must not be one %+v away from leaking
	// them. Rendering the whole report must not reveal what anything is set to.
	rendered := renderAll(threeLayers(t).View().Snapshot().Shadows())

	for _, secret := range []string{"literal-one", "literal-two", "from-env"} {
		if strings.Contains(rendered, secret) {
			t.Errorf("the report exposed the value %q — it must carry paths and "+
				"sources only", secret)
		}
	}
}

func renderAll(shadows []config.Shadow) string {
	var b strings.Builder
	for _, s := range shadows {
		b.WriteString(s.Path)
		b.WriteString(s.InEffect.String())

		for _, src := range s.Shadowed {
			b.WriteString(src.String())
		}
	}

	return b.String()
}

func TestShadows_OnAnEmptySnapshotIsEmpty(t *testing.T) {
	t.Parallel()

	var snap *config.Snapshot

	if got := snap.Shadows(); got != nil {
		t.Errorf("a nil snapshot should report nothing, got %v", got)
	}
}

func TestViewShadows_ScopesToItsOwnSubtree(t *testing.T) {
	t.Parallel()

	// D6. A scoped view reports only its own subtree, so a component can ask
	// about its own configuration without hearing about anybody else's.
	view := threeLayers(t).View().Sub("server")

	shadows := view.Shadows()

	if _, ok := shadowFor(shadows, "token"); ok {
		t.Error("a view scoped to server must not report token")
	}

	got, ok := shadowFor(shadows, "port")
	if !ok {
		t.Fatalf("server.port should appear as %q through a scoped view: %+v", "port", shadows)
	}

	if got.InEffect.Name != "project" {
		t.Errorf("InEffect = %q, want project", got.InEffect.Name)
	}
}

func TestViewShadows_ReportsPathsItsOwnAccessorsAccept(t *testing.T) {
	t.Parallel()

	// The paths must be view-relative, matching Keys, so a caller can feed one
	// straight back into the view it came from. Absolute paths would round-trip
	// to nothing and the mistake would be silent.
	view := threeLayers(t).View().Sub("server")

	for _, sh := range view.Shadows() {
		if _, ok := view.Origin(sh.Path); !ok {
			t.Errorf("%q is not a path this view answers for", sh.Path)
		}
	}
}

func TestViewShadows_UnscopedMatchesTheSnapshot(t *testing.T) {
	t.Parallel()

	store := threeLayers(t)

	if got, want := len(store.View().Shadows()), len(store.View().Snapshot().Shadows()); got != want {
		t.Errorf("unscoped view reported %d entries, snapshot reported %d", got, want)
	}
}

func TestShadowsDocClaims(t *testing.T) {
	t.Parallel()

	// docs/how-to/read-values.md and docs/explanation/provenance.md both quote
	// this output. A page claiming what the module does not print should fail
	// the build rather than the reader.
	store := threeLayers(t)

	var lines []string
	for _, sh := range store.View().Shadows() {
		lines = append(lines, fmt.Sprintf("%s = from %s, shadowing %d",
			sh.Path, sh.InEffect.Name, len(sh.Shadowed)))
	}

	want := []string{
		"server.port = from project, shadowing 1",
		"token = from APP_TOKEN, shadowing 2",
	}

	if !slices.Equal(lines, want) {
		t.Errorf("the pages quote:\n  %s\ngot:\n  %s",
			strings.Join(want, "\n  "), strings.Join(lines, "\n  "))
	}

	// The scoped example, which shows a path being handed straight back.
	srv := store.View().Sub("server")

	shadows := srv.Shadows()
	if len(shadows) != 1 {
		t.Fatalf("scoped example expects one entry, got %d", len(shadows))
	}

	if got := shadows[0].Path; got != "port" {
		t.Errorf("the page shows %q, got %q", "port", got)
	}

	if got := srv.GetInt(shadows[0].Path); got != 2 {
		t.Errorf("the page shows the path resolving to 2 through the same view, got %v", got)
	}
}

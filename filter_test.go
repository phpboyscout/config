package config

import (
	"context"
	"testing"
	"time"
)

// Patterns match the dotted path a key resolves at, not a backend's native
// addressing, so a caller does not have to know whether a store nests by "/",
// by underscore or not at all.
//
// The segment boundary is the whole design: `db.*` meaning "the keys of db" and
// `db.**` meaning "everything under db" is the least surprising reading of a
// dotted path, and a `*` that silently crossed a boundary would widen a filter
// rather than narrow it — the direction that matters when the filter is holding
// back a secret.
func TestMatchPattern(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{"exact", "db.host", "db.host", true},
		{"exact rejects a different key", "db.host", "db.port", false},
		{"exact rejects a prefix of itself", "db.host", "db", false},
		{"exact rejects a child", "db.host", "db.host.name", false},

		{"star matches one segment", "db.*", "db.host", true},
		{"star does not cross a boundary", "db.*", "db.primary.host", false},
		{"star does not match the parent alone", "db.*", "db", false},
		{"star mid-pattern", "db.*.host", "db.primary.host", true},
		{"star mid-pattern is still one segment", "db.*.host", "db.a.b.host", false},
		{"leading star", "*.host", "db.host", true},

		{"doublestar crosses boundaries", "db.**", "db.primary.host", true},
		{"doublestar matches one level too", "db.**", "db.host", true},
		{"doublestar does not match the parent alone", "db.**", "db", false},
		{"doublestar rejects a sibling", "db.**", "cache.host", false},
		{"bare doublestar matches everything", "**", "anything.at.all", true},

		{"a literal segment is not a prefix match", "db", "database.host", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := matchPattern(tc.pattern, splitPath(tc.path)); got != tc.want {
				t.Errorf("matchPattern(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

// Allow and deny compose, and deny wins. A caller who has written both has
// expressed a narrowing intention twice, so resolving the overlap the other way
// would let an Allow re-expose something explicitly denied.
func TestFilterRules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		rules filterRules
		path  string
		want  bool
	}{
		{"no rules permits everything", filterRules{}, "db.host", true},

		{"allow is a whitelist", rulesOf(Allow("db.**")), "db.host", true},
		{"allow excludes what it does not name", rulesOf(Allow("db.**")), "cache.host", false},

		{"deny is a blacklist", rulesOf(Deny("db.password")), "db.host", true},
		{"deny excludes what it names", rulesOf(Deny("db.password")), "db.password", false},

		{"deny beats allow", rulesOf(Allow("db.**"), Deny("db.password")), "db.password", false},
		{"allow still admits the rest", rulesOf(Allow("db.**"), Deny("db.password")), "db.host", true},

		{"several allows union", rulesOf(Allow("db.**", "cache.**")), "cache.ttl", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.rules.permits(splitPath(tc.path)); got != tc.want {
				t.Errorf("permits(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func rulesOf(opts ...FilterOption) filterRules {
	var r filterRules
	for _, opt := range opts {
		opt(&r)
	}

	return r
}

// D9: the returned value implements exactly the optional interfaces the inner
// backend does. Claiming more means the Store takes a path the backend cannot
// honour; claiming less silently drops a capability the caller configured.
//
// Asserted across all four combinations rather than the one that happened to be
// convenient, because the type switch is the only thing keeping them apart and
// a wrong branch is invisible until something tries to write or watch.
func TestFiltered_ImplementsExactlyTheInnerCapabilities(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name              string
		inner             Backend
		writable, watchab bool
	}{
		{"plain", stubBackend{}, false, false},
		{"writable", stubWritable{}, true, false},
		{"watchable", &stubWatchable{}, false, true},
		{"both", stubWritableWatchable{}, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := Filtered(tc.inner, Allow("a"))

			if _, ok := got.(WritableBackend); ok != tc.writable {
				t.Errorf("WritableBackend = %v, want %v", ok, tc.writable)
			}

			if _, ok := got.(WatchableBackend); ok != tc.watchab {
				t.Errorf("WatchableBackend = %v, want %v", ok, tc.watchab)
			}
		})
	}
}

// SourceKind and PollInterval are implemented unconditionally, so their
// delegate-to-nothing behaviour has to match what the Store would have done on
// its own — otherwise implementing them always is not free after all.
func TestFiltered_HintsDelegateOrFallBackToTheStoreDefaults(t *testing.T) {
	t.Parallel()

	t.Run("undeclared kind falls back to SourceFile", func(t *testing.T) {
		t.Parallel()

		got, ok := Filtered(stubBackend{}, Allow("a")).(SourceKindDeclarer)
		if !ok {
			t.Fatal("filtered backend does not declare a source kind")
		}

		if got.SourceKind() != SourceFile {
			t.Errorf("SourceKind() = %q, want the SourceFile default", got.SourceKind())
		}
	})

	t.Run("declared kind is forwarded", func(t *testing.T) {
		t.Parallel()

		got := Filtered(stubKinded{}, Allow("a")).(SourceKindDeclarer)
		if got.SourceKind() != SourceKind("remote") {
			t.Errorf("SourceKind() = %q, want the inner backend's kind", got.SourceKind())
		}
	})

	t.Run("absent hint reads as no hint", func(t *testing.T) {
		t.Parallel()

		got := Filtered(stubBackend{}, Allow("a")).(PollIntervalHinter)
		if got.PollInterval() != 0 {
			t.Errorf("PollInterval() = %v, want 0 so the Store ignores it", got.PollInterval())
		}
	})

	t.Run("hint is forwarded", func(t *testing.T) {
		t.Parallel()

		got := Filtered(stubHinter{}, Allow("a")).(PollIntervalHinter)
		if got.PollInterval() != time.Minute {
			t.Errorf("PollInterval() = %v, want the inner backend's hint", got.PollInterval())
		}
	})
}

// A filtered watchable backend still watches, which the four-way switch is what
// makes true.
func TestFiltered_WatchIsForwarded(t *testing.T) {
	t.Parallel()

	inner := &stubWatchable{}

	watchable, ok := Filtered(inner, Allow("a")).(WatchableBackend)
	if !ok {
		t.Fatal("filtered watchable backend is not watchable")
	}

	stop, err := watchable.Watch(context.Background(), time.Second, func() {})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	stop()

	if !inner.watched {
		t.Error("Watch did not reach the inner backend")
	}
}

// Minimal backends, each implementing one combination of the optional
// interfaces and nothing else.

type stubBackend struct{}

func (stubBackend) ID() string                 { return "stub" }
func (stubBackend) Capabilities() Capabilities { return Capabilities{} }
func (stubBackend) Load(context.Context, []Layer) ([]Layer, error) {
	return []Layer{{Source: Source{Name: "stub"}, Values: map[string]any{"a": 1}}}, nil
}

type stubWritable struct{ stubBackend }

func (stubWritable) Prepare(context.Context, []Edit) (Pending, error) { return nil, nil }

type stubWatchable struct {
	stubBackend

	watched bool
}

func (s *stubWatchable) Watch(context.Context, time.Duration, func()) (func(), error) {
	s.watched = true

	return func() {}, nil
}

type stubWritableWatchable struct{ stubWritable }

func (stubWritableWatchable) Watch(context.Context, time.Duration, func()) (func(), error) {
	return func() {}, nil
}

type stubKinded struct{ stubBackend }

func (stubKinded) SourceKind() SourceKind { return SourceKind("remote") }

type stubHinter struct{ stubBackend }

func (stubHinter) PollInterval() time.Duration { return time.Minute }

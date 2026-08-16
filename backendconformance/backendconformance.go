// Package backendconformance is a reusable test suite that a config backend
// adapter runs against its own backend to prove it behaves like a first-class
// layer.
//
// It is the remote-source counterpart of [config/conformance], which does the
// same job for file codecs. A codec plugs into shared file machinery, so that
// suite can construct everything from a byte sample. A backend owns its whole
// interaction with a remote system — reading, versioning, staging, committing,
// watching — so this suite cannot build one itself. The adapter supplies a
// factory instead (Suite.NewBackend), plus a [Control] that stands in for
// another client of the same system: it changes the backing store out of band
// and re-opens a backend over it.
//
// The trap this exists to make unmissable is the same one [config/conformance]
// guards for files, wearing a remote costume: the conflict fingerprint — here a
// version — must be captured at Load, not at Prepare. A backend that fetches a
// fresh version inside Prepare and compares Verify against that compares the
// intruder's data with itself, so every stale write is accepted and conflict
// detection silently never fires. The conflict_detected subtest catches it.
//
// An adapter writes one test:
//
//	func TestConformance(t *testing.T) {
//		backendconformance.Run(t, backendconformance.Suite{
//			NewBackend: func(t *testing.T, seed map[string]any) (config.Backend, backendconformance.Control) {
//				remote := newFakeRemote(seed) // nil seed => absent source
//				return newConsulBackend(remote, "app/"), &control{remote}
//			},
//			Seed:     map[string]any{"server": map[string]any{"port": 9090}},
//			Defines:  map[string]string{"server.port": "9090"},
//			WriteKey: "server.port", WriteValue: "8080",
//		})
//	}
//
// It uses the standard library testing package and nothing else — deliberately
// no testify — so an adapter that runs it takes on no assertion-library
// dependency it would otherwise avoid.
package backendconformance

import (
	"context"
	"io/fs"
	"sort"
	"strings"
	"testing"
	"time"

	"gitlab.com/phpboyscout/go/errors"

	"gitlab.com/phpboyscout/go/config"
)

// Suite describes a backend and the minimal knowledge the assertions need. The
// capability subtests — write and watch — run only when the backend the factory
// returns satisfies [config.WritableBackend] or [config.WatchableBackend], so a
// read-only backend runs the read contract and no more.
type Suite struct {
	// NewBackend builds a fresh backend over a fresh backing store seeded with
	// the given nested values, plus a [Control] over that same store. A nil seed
	// means an absent source: the backend's Load must report it not there
	// (fs.ErrNotExist) rather than erroring. Each call must be independent — two
	// calls must not share a backing store — except through the [Control], whose
	// Reopen returns a backend over the store its NewBackend call created.
	NewBackend func(t *testing.T, seed map[string]any) (config.Backend, Control)

	// Seed is the nested values the backend is seeded with, and Defines is what
	// they read back through a View. Both are required, with at least one key.
	// Seed carries native types ({"server": {"port": 9090}}); Defines is the
	// string each reads back (GetString), so the suite asserts decode, merge and
	// provenance without knowing the system.
	Seed    map[string]any
	Defines map[string]string

	// WriteKey and WriteValue are a key the backend can set and the value it is
	// set to — required only when NewBackend returns a [config.WritableBackend].
	// The value must read back after the write.
	WriteKey   string
	WriteValue string

	// PinOnlyTargets declares that the backend's layers can receive a write only
	// when a change NAMES them, and are never chosen by routing on their own.
	//
	// Writable normally implies routable, and for every backend that owns its
	// own storage it does. A composed store is the exception: its layers belong
	// to the store it wraps, and letting routing choose one would mean an
	// ordinary edit of an inherited key rewriting the shared configuration it
	// was inherited from. Promotion has to be named to happen.
	//
	// With this set, the write cases pin their target rather than relying on
	// routing to find it, which is the only way such a backend can be written to
	// at all.
	PinOnlyTargets bool

	// BoundedKeySpace declares that the backend accepts writes only to keys it
	// was configured with, rather than to any key routed at it.
	//
	// Almost every backend takes arbitrary keys: a file, a Consul prefix or an
	// object store will hold whatever it is given. Some cannot. A keychain has
	// no way to enumerate itself, so an adapter over one is given an explicit
	// map of config path to keychain account, and a key outside that map has no
	// account to be written to — inventing one would put a secret somewhere no
	// read would ever look for it.
	//
	// Setting this skips the hostile-KEY cases, which invent key names the
	// backend cannot have been configured with. The hostile-VALUE cases still
	// run against WriteKey, because a bounded key space says nothing about what
	// values a backend must survive.
	//
	// Leave it false unless the backend genuinely refuses unconfigured keys. It
	// removes coverage, and a backend that merely finds a key inconvenient
	// should fail closed instead.
	BoundedKeySpace bool

	// NewUnreachable builds a backend whose connection cannot be established,
	// plus a heal func that makes it establishable — the same backend, the same
	// value, now able to connect.
	//
	// It exists because a backend that resolves its own connection has three
	// obligations no other subtest reaches: that it can still say what it is
	// before it has one, that failing to get one is an error rather than a panic,
	// and that the failure is not remembered. The last is the one that bites: a
	// backend memoising its connection with sync.OnceValues caches the first
	// error forever, so a credential chain that was briefly unavailable at
	// startup wedges the process until it restarts.
	//
	// Optional. A backend that owns no connection — a file, an in-memory store,
	// one handed an already-built client — leaves it nil and the connection
	// subtests are skipped. Supplying it is how an adapter with a zero-conf rung
	// proves that rung behaves.
	//
	// The unreachable state must be reached without a network: point the backend
	// at a fake whose dial fails, not at a real endpoint expected to be down.
	NewUnreachable func(t *testing.T) (b config.Backend, heal func())
}

// Control stands in for another client of the same backing store. It is how the
// suite simulates a foreign change — for the conflict and watch assertions — and
// how it re-reads the store to prove a committed write reached it.
type Control interface {
	// Mutate changes the backing store as another client would: it moves the
	// version the backend's conflict check compares against, and — for a
	// watchable backend — emits the backend's change signal. It must change at
	// least one value visible in the merged config, so a reload is not coalesced
	// away as a no-op.
	Mutate(t *testing.T)

	// Reopen returns a fresh backend over the same backing store its NewBackend
	// call created, so the suite can prove a committed write actually landed
	// there rather than only in the Store's in-memory snapshot.
	Reopen(t *testing.T) config.Backend
}

// Run executes the whole suite against s, one named subtest per contract, so a
// failing adapter sees exactly which one it breaks.
//
// Every backend is checked for per-key merge and precedence with a layer of
// another format, tolerance of an absent source, and provenance naming the
// backend. A [config.WritableBackend] additionally has its write round-trip and
// — the trap the suite exists for — its refusal of a change that landed between
// load and commit with [config.ErrConflict] checked; a read-only backend instead
// has its layer confirmed skipped by write routing — or, when it is sensitive
// (a secrets backend), the routed-beneath write confirmed refused with
// [config.ErrSensitiveLeak]. A writable backend also has a set of rendering and
// path-addressing hazards — control bytes, a bidi character, a numeric key, and
// gjson path metacharacters — confirmed to either round-trip exactly or fail
// closed with [config.ErrBackendUnsafe] or [config.ErrBackendParse]. A
// [config.WatchableBackend] has a foreign change confirmed to reach observers.
func Run(t *testing.T, s Suite) {
	t.Helper()

	if s.NewBackend == nil {
		t.Fatal("backendconformance: Suite.NewBackend is required")
	}

	if len(s.Defines) == 0 {
		t.Fatal("backendconformance: Suite.Defines needs at least one key the Seed exposes")
	}

	if len(s.Seed) == 0 {
		t.Fatal("backendconformance: Suite.Seed must be the values the backend is seeded with")
	}

	t.Run("participates_as_layer", s.participatesAsLayer)
	t.Run("absent_source_tolerated", s.absentSourceTolerated)
	t.Run("apply_tolerated_beside_absent_source", s.applyToleratedBesideAbsentSource)
	t.Run("provenance_names_backend", s.provenanceNamesBackend)

	// Discover the capability from the type, never a flag — the same split the
	// backend interfaces enforce (store-architecture D11).
	probe, _ := s.newBackend(t, s.Seed)

	if _, ok := probe.(config.WritableBackend); ok {
		if s.WriteKey == "" {
			t.Fatal("backendconformance: Suite.WriteKey and WriteValue are required for a WritableBackend")
		}

		t.Run("write_round_trips", s.writeRoundTrips)
		t.Run("conflict_detected", s.conflictDetected)
		t.Run("renderable_scalars_and_hostile_keys", s.renderableScalarsAndHostileKeys)
	} else {
		t.Run("read_only_skipped_by_routing", s.readOnlySkippedByRouting)
	}

	if _, ok := probe.(config.WatchableBackend); ok {
		t.Run("foreign_change_reaches_observers", s.foreignChangeReachesObservers)
	}

	if s.NewUnreachable != nil {
		t.Run("identity_without_a_connection", s.identityWithoutAConnection)
		t.Run("connection_failure_is_an_error", s.connectionFailureIsAnError)
		t.Run("connection_failure_is_not_cached", s.connectionFailureIsNotCached)
	}
}

// identityWithoutAConnection confirms a backend can say what it is before it has
// a connection, and without acquiring one to answer.
//
// The Store calls ID and Capabilities while assembling itself — to order layers,
// to route writes, to decide what to watch — long before anything calls Load. A
// backend that reached for its connection to answer either would turn store
// construction into a network operation, and a store built against an
// unreachable backend would fail at NewStore rather than at the Load that
// actually needs the data.
func (s Suite) identityWithoutAConnection(t *testing.T) {
	backend, _ := s.NewUnreachable(t)

	if id := backend.ID(); id == "" {
		t.Error("ID() is empty on a backend that has no connection; it must be " +
			"answerable from the backend's own configuration, not from the remote")
	}

	// Capabilities is a value type with no error return, so the assertion is that
	// the call completes at all: a backend that dialled here would block or panic.
	_ = backend.Capabilities()
}

// connectionFailureIsAnError confirms an unreachable connection surfaces from
// Load as an error.
//
// Not a panic, and not a silent empty layer set. An empty result is the worse
// failure of the two: it looks exactly like a source that is legitimately absent,
// so the Store carries on with the configuration silently missing whatever this
// backend was supposed to contribute.
func (s Suite) connectionFailureIsAnError(t *testing.T) {
	backend, _ := s.NewUnreachable(t)

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Load panicked on an unreachable connection: %v — a connection "+
				"that cannot be established is an ordinary error", r)
		}
	}()

	layers, err := backend.Load(t.Context(), nil)
	if err == nil {
		t.Fatalf("Load returned no error against an unreachable connection, and %d "+
			"layers; an unreachable backend must not read as an absent source",
			len(layers))
	}

	// The error must not be fs.ErrNotExist. That sentinel means "this source is
	// legitimately not there", which the Store is entitled to tolerate — so
	// reporting an unreachable connection with it converts a hard failure into a
	// silently missing layer, and the configuration comes up short with nothing
	// said. Absent and unreachable are different answers and must stay different.
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load reported an unreachable connection as fs.ErrNotExist: %v\n"+
			"That sentinel means the source is legitimately absent, which the Store "+
			"may tolerate — so this turns a connection failure into a silently "+
			"missing layer. Return the connection error instead.", err)
	}
}

// connectionFailureIsNotCached confirms a backend retries after a failed
// connection rather than remembering the failure.
//
// This is the one that catches sync.OnceValues, which caches the first error for
// the life of the process. A configuration store is long-lived and reloads: an
// SSO session that was not yet established when the tool started, an instance
// role that arrives a second later, a network that returns — every one of those
// must be picked up by the next Load rather than requiring a restart.
func (s Suite) connectionFailureIsNotCached(t *testing.T) {
	backend, heal := s.NewUnreachable(t)

	if _, err := backend.Load(t.Context(), nil); err == nil {
		t.Fatal("Load succeeded before heal; the fixture is not actually unreachable")
	}

	heal()

	if _, err := backend.Load(t.Context(), nil); err != nil {
		t.Errorf("Load still fails after the connection became reachable: %v\n"+
			"The failure was cached. Memoise success and clear the in-flight state "+
			"on failure — sync.OnceValues cannot do this, because it caches the "+
			"first error forever.", err)
	}
}

// participatesAsLayer confirms the backend's values merge per-key with a layer
// of another format, that precedence is the order sources were added, and that
// shadowing reports the full chain.
func (s Suite) participatesAsLayer(t *testing.T) {
	backend, _ := s.newBackend(t, s.Seed)
	shared := s.sortedKeys()[0]

	base := nestedYAML(shared, "from-base") + "backendconformance_base_only: present\n"

	store := newStore(t,
		config.WithReaders(config.NamedSource{Name: "base.yaml", Content: []byte(base)}), // lower precedence
		config.WithBackend(backend), // higher precedence
	)
	view := store.View()

	// The backend was added last, so it wins the shared key.
	if got := view.GetString(shared); got != s.Defines[shared] {
		t.Errorf("%s = %q, want %q from the backend layer", shared, got, s.Defines[shared])
	}

	// The base-only key survives the merge — merge is per-key, not layer-replace.
	if got := view.GetString("backendconformance_base_only"); got != "present" {
		t.Errorf("backendconformance_base_only = %q, want the base to survive per-key merge", got)
	}

	// Every declared key resolves to its value.
	for key, want := range s.Defines {
		if got := view.GetString(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	// The shared key is defined by both layers, so shadowing reports both.
	const wantShadow = 2 // the base and the backend both define the shared key

	if got := len(view.Shadowed(shared)); got != wantShadow {
		t.Errorf("Shadowed(%q) = %d layers, want %d (base and backend)", shared, got, wantShadow)
	}
}

// absentSourceTolerated confirms a missing backing store is tolerated for an
// optional layer, exactly as a missing overlay file is: the store builds and the
// layers beneath stand.
func (s Suite) absentSourceTolerated(t *testing.T) {
	key := s.sortedKeys()[0]
	backend, _ := s.newBackend(t, nil) // nil seed: the source is absent

	store := newStore(t,
		config.WithReaders(config.NamedSource{Name: "base.yaml", Content: []byte(nestedYAML(key, "from-base"))}),
		config.WithBackend(backend),
	)

	if got := store.View().GetString(key); got != "from-base" {
		t.Errorf("%s = %q, want the base to stand when the backend source is absent", key, got)
	}
}

// applyToleratedBesideAbsentSource confirms an absent backend does not block a
// write routed at a sibling source. Apply rebuilds the candidate by re-reading
// every backend it did not write to, and a rebuild that treated the absence as
// fatal refused a perfectly writable edit with an error about a source the
// caller never mentioned — the declared-but-not-yet-created overlay setup that
// load-time tolerance exists for.
func (s Suite) applyToleratedBesideAbsentSource(t *testing.T) {
	key := s.sortedKeys()[0]
	backend, _ := s.newBackend(t, nil) // nil seed: the sibling source is absent

	// A writable file layer beneath defines the key, so the write routes there
	// — not at the absent backend.
	fsys := dir(t)
	write(t, fsys, "base.yaml", []byte(nestedYAML(key, "pre-write")))

	store := newStore(t,
		config.WithBackend(config.NewFileBackend(fsys, "base.yaml")), // writable, defines the key
		config.WithBackend(backend),                                  // declared, absent
	)

	if _, err := store.Apply(context.Background(), config.Set(key, "routed-beneath")); err != nil {
		t.Fatalf("Apply routed at the file beneath = %v, want it tolerated while the backend source is absent", err)
	}

	if got := store.View().GetString(key); got != "routed-beneath" {
		t.Errorf("%s = %q after the write, want %q", key, got, "routed-beneath")
	}
}

// provenanceNamesBackend confirms the backend's layers carry an identity, so
// Origin can answer "which source supplied this".
//
// It used to require the name to EQUAL the backend's ID, because write routing
// found a backend by scanning IDs for the target's name. Routing now records
// which backend produced which layer at load, so a backend may name its layers
// anything — and one whose layers are themselves resolved elsewhere, a composed
// store carrying the layers of the store it wraps, necessarily does.
//
// What still has to hold is that the name is one the backend actually
// contributed. A layer named after nothing the backend returned is a source no
// caller can act on and no write can be aimed at.
func (s Suite) provenanceNamesBackend(t *testing.T) {
	key := s.sortedKeys()[0]
	backend, _ := s.newBackend(t, s.Seed)

	store := newStore(t, config.WithBackend(backend))

	src, ok := store.View().Origin(key)
	if !ok {
		t.Fatalf("Origin(%q) reported no source", key)
	}

	layers, err := backend.Load(t.Context(), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, l := range layers {
		if l.Source.Name == src.Name {
			return
		}
	}

	t.Errorf("Origin(%q).Name = %q, which is not the name of any layer %q returned — "+
		"provenance must point at a source the backend actually contributed",
		key, src.Name, backend.ID())
}

// writeChange builds the change a write case applies, naming the target when
// the backend declares [Suite.PinOnlyTargets].
//
// The name comes from the Store's own [config.Store.WritableTargets], which
// reports exactly what a pinned change is allowed to name — including targets
// routing will never choose, which is the whole reason a pin-only backend needs
// it. Asking the BACKEND instead would mean calling Load, and a backend that
// captures its conflict version at Load would have that version refreshed by the
// question, quietly erasing the very conflict a case is trying to provoke.
func (s Suite) writeChange(t *testing.T, store *config.Store, key string, value any) config.Change {
	t.Helper()

	if !s.PinOnlyTargets {
		return config.Set(key, value)
	}

	targets := store.WritableTargets()
	if len(targets) == 0 {
		t.Fatal("PinOnlyTargets is set but the store offers no writable target to name")
	}

	// The highest-precedence one, matching where routing would have put a new
	// key if it were allowed to choose.
	target := targets[len(targets)-1]

	return config.Set(key, value, config.ToDocument(target.Name, target.Document))
}

// readOnlySkippedByRouting confirms a write to a key a read-only backend defines
// lands in the writable layer beneath and is reported shadowed, not applied to
// the read-only source and not failed.
func (s Suite) readOnlySkippedByRouting(t *testing.T) {
	key := s.sortedKeys()[0]
	backend, _ := s.newBackend(t, s.Seed)

	// A writable file layer beneath, so routing has somewhere to land.
	fsys := dir(t)
	write(t, fsys, "base.yaml", []byte(nestedYAML(key, "from-base")))

	store := newStore(t,
		config.WithBackend(config.NewFileBackend(fsys, "base.yaml")), // writable, lower precedence
		config.WithBackend(backend),                                  // read-only, higher precedence
	)

	plan, err := store.Plan(config.Set(key, "new"))

	// A sensitive read-only backend is the stricter case, and the one every
	// read-only secrets backend meets. Its layer defines the key, so routing a
	// write to the plain file beneath would materialise secret-category data
	// there — and the core's leak guard refuses exactly that. So the write must be
	// refused with ErrSensitiveLeak, not silently routed down.
	if backend.Capabilities().Sensitive {
		if !errors.Is(err, config.ErrSensitiveLeak) {
			t.Errorf("Plan of a write to a key the sensitive read-only backend defines = %v, want "+
				"ErrSensitiveLeak — a secret must not route into the plain layer beneath", err)
		}

		return
	}

	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	targets := plan.Targets()
	if len(targets) != 1 || targets[0].Name != "base.yaml" {
		t.Errorf("targets = %v, want the write routed to base.yaml (read-only backend skipped)", targets)
	}

	if plan.Effective() {
		t.Error("a write to a key the read-only backend defines should be shadowed, not effective")
	}
}

// writeRoundTrips confirms a writable backend receives writes end to end: the
// value is served, and a fresh backend over the same backing store reads it back
// — so the write reached the store, not only the Store's snapshot.
func (s Suite) writeRoundTrips(t *testing.T) {
	backend, ctrl := s.newBackend(t, s.Seed)
	store := newStore(t, config.WithBackend(backend))

	if _, err := store.Apply(context.Background(), s.writeChange(t, store, s.WriteKey, s.WriteValue)); err != nil {
		t.Fatalf("Apply(Set(%q, %q)): %v", s.WriteKey, s.WriteValue, err)
	}

	if got := store.View().GetString(s.WriteKey); got != s.WriteValue {
		t.Errorf("%s = %q after write, want %q", s.WriteKey, got, s.WriteValue)
	}

	// A fresh backend over the same backing store must read the written value, or
	// the write did not reach the store through Commit.
	fresh := newStore(t, config.WithBackend(ctrl.Reopen(t)))
	if got := fresh.View().GetString(s.WriteKey); got != s.WriteValue {
		t.Errorf("a fresh read of the backing store has %s = %q, want %q — the write did not reach it",
			s.WriteKey, got, s.WriteValue)
	}
}

// conflictDetected is the trap, asserted: a change landing between load and
// commit is refused with ErrConflict, because the version is captured at Load
// and re-checked at Verify.
func (s Suite) conflictDetected(t *testing.T) {
	backend, ctrl := s.newBackend(t, s.Seed)
	store := newStore(t, config.WithBackend(backend))

	// Something else changes the backing store after the Store loaded it.
	ctrl.Mutate(t)

	_, err := store.Apply(context.Background(), s.writeChange(t, store, s.WriteKey, s.WriteValue))
	if !errors.Is(err, config.ErrConflict) {
		t.Errorf("Apply after a foreign change = %v, want ErrConflict — the version must be "+
			"captured at Load, not at Prepare", err)
	}
}

// renderableScalarsAndHostileKeys confirms a write that carries a rendering or
// path-addressing hazard either round-trips the exact key and value, or fails
// closed with [config.ErrBackendUnsafe] or [config.ErrBackendParse] — never a
// silent success that reads back as something else.
//
// The hazards are the ones an editing codec or a path-addressing store gets
// wrong: control bytes and a bidirectional-override character in a value (which
// an emitter must escape rather than pass through raw), a key literally named
// "0" (which a store using gjson/sjson path syntax would read as an array
// index), and gjson path metacharacters in a key ("*", "?", "#", "\", which
// carry meta-meaning in that syntax). A backend that mis-addresses any of these
// silently corrupts configuration; the core YAML codec escapes them and this
// case is what will catch a sibling adapter that does not.
func (s Suite) renderableScalarsAndHostileKeys(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
		// inventsKey marks a case that writes to a key the backend was never
		// configured with, which a bounded key space cannot accept.
		inventsKey bool
	}{
		{name: "control bytes in value", key: s.WriteKey, value: "a\x01b\x07c\x1bd"},
		{name: "bidi override in value", key: s.WriteKey, value: "before\u202eafter"},
		{name: "numeric key", key: "0", value: "numeric-key-value", inventsKey: true},
		{name: "gjson metacharacters in key", key: `a*b?c#d\e`, value: "meta-key-value", inventsKey: true},
	}

	for _, tc := range cases {
		if tc.inventsKey && s.BoundedKeySpace {
			// The backend was never given an account, path or slot for this
			// key, so refusing it is correct rather than a failure. Asserting
			// otherwise would require it to invent a destination.
			continue
		}

		t.Run(tc.name, func(t *testing.T) {
			backend, _ := s.newBackend(t, s.Seed)
			store := newStore(t, config.WithBackend(backend))

			_, err := store.Apply(context.Background(), s.writeChange(t, store, tc.key, tc.value))
			if err != nil {
				// Fail-closed is an acceptable outcome: refusing a construct it
				// cannot represent is exactly the guarantee. A codec may refuse it
				// up front as unsafe (ErrBackendUnsafe), or a value it cannot render
				// may be caught when the staged content is re-decoded before commit
				// (ErrBackendParse) — a raw control byte no text format can hold
				// surfaces that way. Both mean the write was refused rather than
				// silently mangled. Any other error is a real failure.
				if errors.Is(err, config.ErrBackendUnsafe) || errors.Is(err, config.ErrBackendParse) {
					return
				}

				t.Fatalf("Apply(Set(%q, ...)) = %v, want success with an exact round-trip "+
					"or a fail-closed ErrBackendUnsafe/ErrBackendParse", tc.key, err)
			}

			// The write succeeded, so the value must read back exactly. Anything
			// else is the silent mis-addressing or mangling this case exists to
			// catch.
			if got := store.View().GetString(tc.key); got != tc.value {
				t.Errorf("%q round-tripped as %q, want %q — the write succeeded but "+
					"mis-addressed or mangled it", tc.key, got, tc.value)
			}
		})
	}
}

// foreignChangeReachesObservers confirms a watchable backend feeds hot-reload: a
// change made to the backing store by another client reaches the Store's
// observers.
//
// The watch is opened with an explicit fast poll interval, not the default. A
// backend that must be polled may advertise a slow cadence through
// [config.PollIntervalHinter] — minutes, for a billed remote read — which a
// default Store.Watch would adopt, so a foreign change would not surface inside
// this case's window and the case would flake or time out through no fault of
// the backend. An explicit [config.WithPollInterval] overrides the hint (the
// poll-interval contract), so the case measures that a change propagates, not
// how eagerly the backend chose to poll.
func (s Suite) foreignChangeReachesObservers(t *testing.T) {
	backend, ctrl := s.newBackend(t, s.Seed)
	store := newStore(t, config.WithBackend(backend))

	fired := make(chan struct{}, observerBuffer)

	store.AddObserverFunc(func(config.Observed) error {
		fired <- struct{}{}

		return nil
	})

	// Settling disabled: a real change signal has no burst to coalesce, so the
	// test should not wait on a timer. The explicit poll interval overrides any
	// slow PollIntervalHinter cadence so a polled backend still surfaces the
	// change within watchTimeout.
	stop, err := store.Watch(context.Background(),
		config.WithSettleInterval(0),
		config.WithPollInterval(conformancePoll),
	)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	defer stop()

	ctrl.Mutate(t)

	select {
	case <-fired:
	case <-time.After(watchTimeout):
		t.Fatal("a change to the backing store never reached observers")
	}
}

// --- helpers -------------------------------------------------------------

// newBackend calls the adapter's factory with an independent deep copy of the
// seed. The suite reuses one Seed across every subtest, and a backing store that
// mutates its values in place — most do — would otherwise let one subtest's
// change bleed into the next, so a later "foreign change" is silently a no-op.
// Cloning here makes that impossible without asking every adapter to remember it.
func (s Suite) newBackend(t *testing.T, seed map[string]any) (config.Backend, Control) {
	t.Helper()

	return s.NewBackend(t, cloneTree(seed))
}

// cloneTree deep-copies a nested value tree so callers cannot alias each other
// through a shared map. Scalars and slices are carried by value; only maps, the
// thing a backing store mutates, are copied.
func cloneTree(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}

	out := make(map[string]any, len(m))

	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			out[k] = cloneTree(sub)

			continue
		}

		out[k] = v
	}

	return out
}

func (s Suite) sortedKeys() []string {
	keys := make([]string, 0, len(s.Defines))
	for key := range s.Defines {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

// nestedYAML renders a dotted key and a scalar value as block-style YAML, so a
// suite-controlled base can define the same key path the backend's seed does.
func nestedYAML(key, value string) string {
	var b strings.Builder

	segs := strings.Split(key, ".")
	for i, seg := range segs {
		b.WriteString(strings.Repeat("  ", i))
		b.WriteString(seg)

		if i == len(segs)-1 {
			b.WriteString(": ")
			b.WriteString(value)
			b.WriteString("\n")

			break
		}

		b.WriteString(":\n")
	}

	return b.String()
}

func dir(t *testing.T) config.FS {
	t.Helper()

	fsys, err := config.Dir(t.TempDir())
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}

	return fsys
}

func newStore(t *testing.T, opts ...config.StoreOption) *config.Store {
	t.Helper()

	store, err := config.NewStore(context.Background(), opts...)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	return store
}

// fileMode is what the suite writes the beneath-layer file with — owner-only,
// matching the core's own configuration file mode.
const fileMode = 0o600

// observerBuffer sizes the channel the watch assertion drains, large enough that
// an observer never blocks even if a reload fires more than once.
const observerBuffer = 4

// watchTimeout bounds how long the watch assertion waits for a foreign change to
// reach observers before declaring the signal lost. A native change signal
// arrives in microseconds; this is slack for a loaded CI runner.
const watchTimeout = 3 * time.Second

// conformancePoll is the explicit poll cadence the watch case opens Store.Watch
// with, well inside watchTimeout, so a polled backend advertising a slow
// PollIntervalHinter cadence is still measured for propagation rather than eager
// polling. An explicit WithPollInterval overrides the hint.
const conformancePoll = 5 * time.Millisecond

func write(t *testing.T, fsys config.FS, name string, data []byte) {
	t.Helper()

	if err := fsys.WriteFile(name, data, fileMode); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

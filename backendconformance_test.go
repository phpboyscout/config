package config_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"gitlab.com/phpboyscout/go/config"
	"gitlab.com/phpboyscout/go/config/backendconformance"
)

// The backendconformance suite is proven here against the custom-backend guide's
// remoteBackend — a known-good writable, watchable backend (custombackend_test.go)
// — the way config/conformance is proven against the in-repo trivial codec. A
// second run against a read-only backend over the same fake exercises the suite's
// read-only routing branch, which the writable run does not reach.

// seed is what both runs load, chosen so a top-level scalar (level) exists for
// Control.Mutate to change visibly, alongside a nested key.
func conformanceSeed() map[string]any {
	return map[string]any{
		"server": map[string]any{"port": 9090},
		"level":  "info",
	}
}

func conformanceDefines() map[string]string {
	return map[string]string{"server.port": "9090", "level": "info"}
}

// suiteControl stands in for another client of the fake: it changes the backing
// store out of band and re-opens a backend over it.
type suiteControl struct {
	remote *fakeRemote
	reopen func(*fakeRemote) config.Backend
}

func (c *suiteControl) Mutate(*testing.T) { c.remote.set("level", "externally-changed") }

func (c *suiteControl) Reopen(*testing.T) config.Backend { return c.reopen(c.remote) }

func TestBackendConformance_Writable(t *testing.T) {
	t.Parallel()

	backendconformance.Run(t, backendconformance.Suite{
		NewBackend: func(_ *testing.T, seed map[string]any) (config.Backend, backendconformance.Control) {
			remote := newFakeRemote(seed)

			return newRemoteBackend(remote, "app/"),
				&suiteControl{remote: remote, reopen: func(r *fakeRemote) config.Backend {
					return newRemoteBackend(r, "app/")
				}}
		},
		Seed:     conformanceSeed(),
		Defines:  conformanceDefines(),
		WriteKey: "level", WriteValue: "debug",
	})
}

func TestBackendConformance_ReadOnly(t *testing.T) {
	t.Parallel()

	backendconformance.Run(t, backendconformance.Suite{
		NewBackend: func(_ *testing.T, seed map[string]any) (config.Backend, backendconformance.Control) {
			remote := newFakeRemote(seed)

			return newReadOnlyBackend(remote, "ro/"),
				&suiteControl{remote: remote, reopen: func(r *fakeRemote) config.Backend {
					return newReadOnlyBackend(r, "ro/")
				}}
		},
		Seed:    conformanceSeed(),
		Defines: conformanceDefines(),
	})
}

func TestBackendConformance_SensitiveReadOnly(t *testing.T) {
	t.Parallel()

	backendconformance.Run(t, backendconformance.Suite{
		NewBackend: func(_ *testing.T, seed map[string]any) (config.Backend, backendconformance.Control) {
			remote := newFakeRemote(seed)

			return newSensitiveReadOnlyBackend(remote, "vault/"),
				&suiteControl{remote: remote, reopen: func(r *fakeRemote) config.Backend {
					return newSensitiveReadOnlyBackend(r, "vault/")
				}}
		},
		Seed:    conformanceSeed(),
		Defines: conformanceDefines(),
	})
}

// readOnlyBackend is a backend that reads but neither writes nor watches — it
// implements Backend and nothing more, so the suite discovers it is read-only
// from the type and runs read_only_skipped_by_routing.
type readOnlyBackend struct {
	store     remoteStore
	prefix    string
	sensitive bool
}

func newReadOnlyBackend(store remoteStore, prefix string) *readOnlyBackend {
	return &readOnlyBackend{store: store, prefix: prefix}
}

// newSensitiveReadOnlyBackend is a read-only backend that also declares itself
// sensitive — the shape every read-only secrets backend (Vault, a cloud secrets
// manager) has. The suite must confirm a write to a key it owns is refused, not
// routed into the plain layer beneath.
func newSensitiveReadOnlyBackend(store remoteStore, prefix string) *readOnlyBackend {
	return &readOnlyBackend{store: store, prefix: prefix, sensitive: true}
}

func (b *readOnlyBackend) ID() string { return b.prefix }

func (b *readOnlyBackend) Capabilities() config.Capabilities {
	return config.Capabilities{Sensitive: b.sensitive}
}

func (b *readOnlyBackend) Load(ctx context.Context, _ []config.Layer) ([]config.Layer, error) {
	values, _, err := b.store.Fetch(ctx)
	if err != nil {
		return nil, err
	}

	if values == nil {
		return nil, fs.ErrNotExist
	}

	return []config.Layer{{
		Source: config.Source{
			Kind:     config.SourceKind("remote"),
			Name:     b.prefix,
			Writable: false,
		},
		Values: values,
	}}, nil
}

// boundedKeyBackend is a writable backend that accepts writes only to the keys
// it was configured with — the shape config-keychain has, because a keychain
// cannot be enumerated and an adapter over one is given an explicit map of
// config path to account.
//
// It exists to prove Suite.BoundedKeySpace does something. Without a backend
// that genuinely refuses an unconfigured key, setting the field would skip
// cases that were passing anyway and the flag would be indistinguishable from
// no flag at all.
type boundedKeyBackend struct {
	config.WritableBackend

	// owned is the only key this backend will accept a write for.
	owned string
}

// errNotOwned is deliberately NOT ErrBackendUnsafe or ErrBackendParse: the key
// is neither unsafe nor unparseable, it simply belongs to no slot here. That
// distinction is the whole reason BoundedKeySpace exists — the suite's
// fail-closed allowance does not cover it.
var errNotOwned = errors.New("backendconformance_test: key not owned by this backend")

func (b boundedKeyBackend) Prepare(ctx context.Context, edits []config.Edit) (config.Pending, error) {
	for _, edit := range edits {
		if edit.Path != b.owned {
			return nil, fmt.Errorf("%w: %q", errNotOwned, edit.Path)
		}
	}

	return b.WritableBackend.Prepare(ctx, edits)
}

// TestBackendConformance_BoundedKeySpace runs the suite against a backend that
// refuses unconfigured keys.
//
// It is the regression guard for the field: run the same backend WITHOUT
// BoundedKeySpace and the hostile-key cases fail, because refusing them is
// reported as a contract breach rather than as correct behaviour.
func TestBackendConformance_BoundedKeySpace(t *testing.T) {
	t.Parallel()

	backendconformance.Run(t, backendconformance.Suite{
		NewBackend: func(_ *testing.T, seed map[string]any) (config.Backend, backendconformance.Control) {
			remote := newFakeRemote(seed)

			return boundedKeyBackend{WritableBackend: newRemoteBackend(remote, "app/"), owned: "level"},
				&suiteControl{remote: remote, reopen: func(r *fakeRemote) config.Backend {
					return boundedKeyBackend{WritableBackend: newRemoteBackend(r, "app/"), owned: "level"}
				}}
		},
		Seed:            conformanceSeed(),
		Defines:         conformanceDefines(),
		WriteKey:        "level",
		WriteValue:      "debug",
		BoundedKeySpace: true,
	})
}

// A decorated backend must still satisfy the contract, or the decoration has
// quietly changed what the Store can rely on. This is also the only place the
// writable wrapper's Prepare delegation and its ID forwarding are exercised
// together: the write path finds a backend by matching a layer's Source.Name
// against ID(), so a filter that renamed itself would pass every unit test
// about filtering and fail every write.
func TestBackendConformance_Filtered(t *testing.T) {
	t.Parallel()

	backendconformance.Run(t, backendconformance.Suite{
		NewBackend: func(_ *testing.T, seed map[string]any) (config.Backend, backendconformance.Control) {
			remote := newFakeRemote(seed)

			wrap := func(r *fakeRemote) config.Backend {
				// Allows everything the suite touches, so the filter is present
				// and doing work without hiding what is under test.
				return config.Filtered(newRemoteBackend(r, "app/"),
					config.Allow("server.**", "level"))
			}

			return wrap(remote), &suiteControl{remote: remote, reopen: wrap}
		},
		Seed:     conformanceSeed(),
		Defines:  conformanceDefines(),
		WriteKey: "level", WriteValue: "debug",
	})
}

// multiNameBackend contributes layers named differently from its own ID, which
// no backend has needed to do until now: a file backend names its layers after
// the path, a remote one after its prefix, and both match ID() by construction.
//
// A store aggregate does not. It contributes the layers of the store it wraps,
// each keeping its own name so provenance survives the join, and none of them
// is the aggregate's ID. The write path has to be able to find it again anyway.
type multiNameBackend struct {
	id string
	// One inner backend, not one per call: it captures the version at Load,
	// which is what its Verify compares against. Building a fresh one in
	// Prepare would compare against a version it never read.
	inner *remoteBackend
}

func newMultiNameBackend(id string, remote *fakeRemote) *multiNameBackend {
	return &multiNameBackend{id: id, inner: newRemoteBackend(remote, "app/")}
}

func (b *multiNameBackend) ID() string { return b.id }

func (b *multiNameBackend) Capabilities() config.Capabilities { return b.inner.Capabilities() }

func (b *multiNameBackend) Load(ctx context.Context, below []config.Layer) ([]config.Layer, error) {
	layers, err := b.inner.Load(ctx, below)
	if err != nil {
		return nil, err
	}

	// Renamed, so the layers are named after neither this backend nor anything
	// the Store could have guessed — the shape an aggregate produces.
	for i := range layers {
		layers[i].Source = config.Source{
			Kind: config.SourceKind("inner"), Name: "inner/app.yaml", Writable: true,
		}
	}

	return layers, nil
}

func (b *multiNameBackend) Prepare(ctx context.Context, edits []config.Edit) (config.Pending, error) {
	return b.inner.Prepare(ctx, edits)
}

// A write routed at a layer must reach the backend that produced it, even when
// the layer's name is not the backend's ID.
//
// Before this, prepare() looked a backend up by scanning ID()s for the target's
// NAME, so such a backend could contribute layers, serve reads and be routed
// at — and then fail at apply with ErrInternal, blaming the module for
// something the Backend contract permits.
func TestApply_ReachesABackendWhoseLayersAreNamedDifferently(t *testing.T) {
	t.Parallel()

	remote := newFakeRemote(map[string]any{"level": "info"})

	store, err := config.NewStore(t.Context(),
		config.WithBackend(newMultiNameBackend("aggregate", remote)))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := store.Apply(t.Context(), config.Set("level", "debug")); err != nil {
		t.Fatalf("Apply: %v — the write could not find the backend that owns the layer", err)
	}

	if got := store.View().GetString("level"); got != "debug" {
		t.Errorf("level = %q, want debug", got)
	}
}

// The family's contract suite against a composed store — the Phase 5 gate, and
// the first backend whose layers are themselves resolved by another Store.
//
// Control.Mutate reloads the inner store as well as changing the fake beneath
// it. That is not the test working around the implementation: an aggregate
// reads the inner store's SNAPSHOT rather than reloading it, deliberately, so a
// foreign change reaches the outer store only once the inner store has noticed
// it. Whoever holds the inner store is responsible for that, exactly as they are
// for any other store they own.
func TestBackendConformance_Nested(t *testing.T) {
	t.Parallel()

	backendconformance.Run(t, backendconformance.Suite{
		NewBackend: func(t *testing.T, seed map[string]any) (config.Backend, backendconformance.Control) {
			t.Helper()

			remote := newFakeRemote(seed)

			inner, err := config.NewStore(t.Context(),
				config.WithBackend(newRemoteBackend(remote, "app/")))
			if err != nil {
				t.Fatalf("NewStore(inner): %v", err)
			}

			return config.Nested(inner, "aggregate", config.NestedPromotable),
				&nestedControl{remote: remote, inner: inner}
		},
		Seed:     conformanceSeed(),
		Defines:  conformanceDefines(),
		WriteKey: "level", WriteValue: "debug",

		// A composed store's layers are pin-only by design (D3a): routing must
		// never choose one, or an ordinary edit would rewrite the configuration
		// it inherited the value from.
		PinOnlyTargets: true,
	})
}

// nestedControl stands in for another client of the inner store's backing
// store, and for the inner store noticing.
type nestedControl struct {
	remote *fakeRemote
	inner  *config.Store
}

func (c *nestedControl) Mutate(t *testing.T) {
	t.Helper()

	c.remote.set("level", "externally-changed")

	if err := c.inner.Reload(t.Context()); err != nil {
		t.Fatalf("inner Reload after a foreign change: %v", err)
	}
}

func (c *nestedControl) Reopen(t *testing.T) config.Backend {
	t.Helper()

	inner, err := config.NewStore(t.Context(), config.WithBackend(newRemoteBackend(c.remote, "app/")))
	if err != nil {
		t.Fatalf("NewStore(reopened inner): %v", err)
	}

	return config.Nested(inner, "aggregate", config.NestedPromotable)
}

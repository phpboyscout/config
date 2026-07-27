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

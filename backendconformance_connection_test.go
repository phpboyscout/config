package config_test

import (
	"context"
	"io/fs"
	"sync"
	"sync/atomic"
	"testing"

	"gitlab.com/phpboyscout/go/errors"

	"gitlab.com/phpboyscout/go/config"
	"gitlab.com/phpboyscout/go/config/backendconformance"
)

// The connection subtests are proven here against a backend that resolves its
// own connection lazily — the shape every zero-conf adapter rung takes — so the
// suite's three connection cases are exercised in this repository rather than
// only downstream.

// errUnreachable is what the fake dial returns while the connection is down.
var errUnreachable = errors.NewSentinel("config.test_unreachable", "connection refused")

// lazyBackend contributes from a remote it connects to on first Load.
//
// It is deliberately the correct shape rather than the convenient one: success
// is memoised, failure is not, and neither ID nor Capabilities touches the
// connection. A backend built on sync.OnceValues would pass every other subtest
// in the suite and fail connection_failure_is_not_cached.
type lazyBackend struct {
	name string

	// dial stands in for building a client. It is swapped by heal, under the
	// mutex, so the test's healing and the backend's connecting cannot race.
	mu        sync.Mutex
	dial      func() (*fakeRemote, error)
	connected *fakeRemote
}

func (b *lazyBackend) ID() string { return b.name }

func (b *lazyBackend) Capabilities() config.Capabilities {
	// Static, and answerable with no connection — which is the contract.
	return config.Capabilities{}
}

// connect returns the remote, dialling at most once successfully and never
// remembering a failure.
func (b *lazyBackend) connect() (*fakeRemote, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.connected != nil {
		return b.connected, nil
	}

	remote, err := b.dial()
	if err != nil {
		return nil, err // deliberately not stored
	}

	b.connected = remote

	return remote, nil
}

func (b *lazyBackend) Load(ctx context.Context, _ []config.Layer) ([]config.Layer, error) {
	remote, err := b.connect()
	if err != nil {
		return nil, errors.Wrap(err, "connecting to the remote")
	}

	values, _, err := remote.Fetch(ctx)
	if err != nil {
		return nil, err
	}

	if values == nil {
		// Absent is not an error — the Store decides whether a missing source is
		// fatal. Only an unreachable CONNECTION is an error, which is the
		// distinction the connection subtests exist to hold.
		return nil, fs.ErrNotExist
	}

	return []config.Layer{{
		Source: config.Source{Kind: config.SourceKind("remote"), Name: b.name},
		Values: values,
	}}, nil
}

// newLazyUnreachable returns a backend whose connection is down, and the heal
// that brings it up. It is the fixture Suite.NewUnreachable asks for.
//
// heal changes the WORLD, never the backend: it flips a flag the dial closure
// reads, rather than replacing the closure. That distinction is the whole value
// of the fixture. A heal that reassigned backend.dial would overwrite any
// failure the backend had cached, so a backend caching its failure would still
// recover and connection_failure_is_not_cached would pass against the very
// defect it exists to catch.
func newLazyUnreachable(t *testing.T) (config.Backend, func()) {
	t.Helper()

	var reachable atomic.Bool

	remote := newFakeRemote(conformanceSeed())

	backend := &lazyBackend{
		name: "lazy-remote",
		dial: func() (*fakeRemote, error) {
			if !reachable.Load() {
				return nil, errUnreachable
			}

			return remote, nil
		},
	}

	return backend, func() { reachable.Store(true) }
}

// TestBackendConformance_LazyConnection runs the whole suite against a backend
// that resolves its connection on first Load, including the three connection
// cases a backend handed an already-built client never reaches.
func TestBackendConformance_LazyConnection(t *testing.T) {
	t.Parallel()

	backendconformance.Run(t, backendconformance.Suite{
		NewBackend: func(_ *testing.T, seed map[string]any) (config.Backend, backendconformance.Control) {
			remote := newFakeRemote(seed)
			build := func(r *fakeRemote) config.Backend {
				return &lazyBackend{
					name: "lazy-remote",
					dial: func() (*fakeRemote, error) { return r, nil },
				}
			}

			return build(remote), &suiteControl{remote: remote, reopen: build}
		},
		Seed:           conformanceSeed(),
		Defines:        conformanceDefines(),
		NewUnreachable: newLazyUnreachable,
	})
}

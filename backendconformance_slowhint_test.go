package config_test

import (
	"context"
	"io/fs"
	"sync"
	"testing"
	"time"

	"gitlab.com/phpboyscout/go/config"
	"gitlab.com/phpboyscout/go/config/backendconformance"
)

// pollingSlowHintBackend is a read-only, watchable-by-polling backend that
// advertises a slow poll cadence through [config.PollIntervalHinter].
//
// It exists to pin the watch conformance case's contract: the case must drive
// Store.Watch with an explicit fast poll interval, so a backend that honours the
// interval it is handed and hints a slow cadence still has a foreign change
// reach observers within the case's window. A default Store.Watch would adopt
// this backend's hour-long hint and never fire in time — which is exactly the
// regression an explicit interval defends against (a real one surfaced when
// config-azure-appconfig began advertising its 30s cadence).
type pollingSlowHintBackend struct {
	store  remoteStore
	prefix string
}

func newPollingSlowHintBackend(store remoteStore, prefix string) *pollingSlowHintBackend {
	return &pollingSlowHintBackend{store: store, prefix: prefix}
}

func (b *pollingSlowHintBackend) ID() string { return b.prefix }

func (b *pollingSlowHintBackend) Capabilities() config.Capabilities {
	return config.Capabilities{NativeWatch: false} // it must be polled
}

func (b *pollingSlowHintBackend) Load(ctx context.Context, _ []config.Layer) ([]config.Layer, error) {
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

// PollInterval advertises a cadence far larger than the watch case's window, so
// a default Store.Watch that adopted the hint would never fire in time.
func (b *pollingSlowHintBackend) PollInterval() time.Duration { return time.Hour }

// Watch polls at the interval it is handed, reporting a change when the store's
// version moves. It honours the interval — an explicit fast one from the case
// overrides the slow hint (the poll-interval contract).
func (b *pollingSlowHintBackend) Watch(
	ctx context.Context,
	interval time.Duration,
	onChange func(),
) (func(), error) {
	if interval <= 0 {
		interval = time.Hour
	}

	done := make(chan struct{})

	// Capture the baseline synchronously, before Watch returns, so a foreign
	// change made right after Watch returns is still seen as a change rather
	// than folded into the baseline by a late first poll.
	_, last, _ := b.store.Fetch(ctx)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				if _, version, err := b.store.Fetch(ctx); err == nil && version != last {
					last = version

					onChange()
				}
			}
		}
	}()

	var once sync.Once

	return func() { once.Do(func() { close(done) }) }, nil
}

// TestBackendConformance_SlowPollHintWatch runs the suite against a polling
// backend whose PollIntervalHinter advertises an hour, proving the watch case
// drives Watch with an explicit fast interval rather than the adopted hint.
func TestBackendConformance_SlowPollHintWatch(t *testing.T) {
	t.Parallel()

	backendconformance.Run(t, backendconformance.Suite{
		NewBackend: func(_ *testing.T, seed map[string]any) (config.Backend, backendconformance.Control) {
			remote := newFakeRemote(seed)

			return newPollingSlowHintBackend(remote, "slow/"),
				&suiteControl{remote: remote, reopen: func(r *fakeRemote) config.Backend {
					return newPollingSlowHintBackend(r, "slow/")
				}}
		},
		Seed:    conformanceSeed(),
		Defines: conformanceDefines(),
	})
}

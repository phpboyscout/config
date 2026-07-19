package config

import (
	"sync"
	"time"
)

// Reader is the read surface a consumer depends on.
//
// It is deliberately small and free of any dependency's types. The previous
// interface exposed the underlying configuration library directly, which made
// the abstraction fictional — anything reachable through that escape hatch
// became part of the contract, and replacing what sat behind it became
// impossible.
type Reader interface {
	Get(path string) any
	GetString(path string) string
	GetBool(path string) bool
	GetInt(path string) int
	GetFloat(path string) float64
	GetDuration(path string) time.Duration
	GetTime(path string) time.Time
	GetStringSlice(path string) []string

	Has(path string) bool
	IsSet(path string) bool
	SectionExists(path string) bool
	Keys() []string

	Unmarshal(target any) error
	UnmarshalKey(path string, target any) error

	// Origin reports which layer supplied a value, and Shadowed lists every
	// layer defining it. Both are what a merge-eager library cannot answer.
	Origin(path string) (Source, bool)
	Shadowed(path string) []Source
	Explain(path string) string
}

// Observed is what an observer is handed when configuration changes.
//
// It is pinned to the snapshot that triggered the notification rather than
// tracking the latest one. Without that, an observer processing one change
// could read values from a later change partway through its own callback and
// produce a result that never existed as a coherent configuration.
type Observed interface {
	Reader

	// Sub returns a scoped view, nil when the key is absent.
	Sub(key string) *View
	// Snapshot exposes the exact configuration this notification describes.
	Snapshot() *Snapshot
}

// Observable is a component that reacts to configuration changes.
type Observable interface {
	Run(cfg Observed) error
}

// ObserverFunc adapts a function to Observable.
type ObserverFunc func(cfg Observed) error

// Run calls the function.
func (f ObserverFunc) Run(cfg Observed) error { return f(cfg) }

// notifier holds observers and the callbacks for rejected reloads.
//
// Both lists are copied under the lock and invoked outside it, so a slow or
// misbehaving observer cannot block a reload, deadlock the Store, or prevent
// other observers from running.
type notifier struct {
	mu             sync.Mutex
	observers      []Observable
	onError        []func(error)
	onObserveError []func(error)
}

func (n *notifier) addObserver(o Observable) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.observers = append(n.observers, o)
}

func (n *notifier) addObserveErrorFunc(f func(error)) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.onObserveError = append(n.onObserveError, f)
}

func (n *notifier) addErrorFunc(f func(error)) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.onError = append(n.onError, f)
}

// notify runs every observer against the snapshot that changed.
//
// Observer errors are collected rather than propagated: one component failing
// to react must not stop the others from being told, and the configuration has
// already changed regardless of whether anything liked it.
func (n *notifier) notify(snap *Snapshot) {
	n.mu.Lock()
	observers := append([]Observable(nil), n.observers...)
	n.mu.Unlock()

	if len(observers) == 0 {
		return
	}

	// Every observer sees the same pinned view, so none of them can observe a
	// later change midway through reacting to this one.
	pinned := NewView(snap)

	n.mu.Lock()
	report := append([]func(error){}, n.onObserveError...)
	n.mu.Unlock()

	for _, o := range observers {
		err := o.Run(pinned)
		if err == nil {
			continue
		}

		// An observer failing is reported but not fatal. The configuration has
		// already changed, and one component being unable to react is not a
		// reason to withhold the change from the others — but it must not pass
		// silently either.
		for _, f := range report {
			f(err)
		}
	}
}

// notifyError reports a rejected reload.
//
// This is a separate channel from observers on purpose. A rejection means
// nothing changed, so telling observers "configuration changed" would be a
// lie — they would re-read values identical to the ones they already have.
func (n *notifier) notifyError(err error) {
	n.mu.Lock()
	funcs := append([]func(error){}, n.onError...)
	n.mu.Unlock()

	for _, f := range funcs {
		f(err)
	}
}

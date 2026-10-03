package config

import (
	"io"
	"sync"
	"sync/atomic"

	"gitlab.com/phpboyscout/go/errors"
)

// ErrStoreClosed is returned by anything that would load, write or watch
// through a Store after [Store.Close].
var ErrStoreClosed = errors.NewSentinel("config.store_closed", "config: store is closed")

// lifecycle is what a Store needs in order to end. Spec 0013.
type lifecycle struct {
	mu        sync.Mutex
	closed    atomic.Bool
	closers   []io.Closer
	watches   map[uint64]func()
	nextWatch uint64
}

// track records a watch's stop so Close can call it, returning false if the
// Store closed while the watch was being set up.
func (l *lifecycle) track(stop func()) (untrack func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed.Load() {
		return nil, false
	}

	if l.watches == nil {
		l.watches = map[uint64]func(){}
	}

	id := l.nextWatch
	l.nextWatch++
	l.watches[id] = stop

	return func() {
		l.mu.Lock()
		delete(l.watches, id)
		l.mu.Unlock()
	}, true
}

// WithCloser hands c to the Store, which closes it when the Store is closed.
//
// The Store owns c from the moment [NewStore] is called: if NewStore returns no
// Store, it has already closed c. Only what is handed over is closed. The Store
// never closes a backend or filesystem because it happens to have a Close
// method, and an outer Store never closes one it nests. Closers run in reverse
// order of declaration, as defers do.
func WithCloser(c io.Closer) StoreOption {
	return func(s *Store) {
		s.life.closers = append(s.life.closers, c)
	}
}

// Close stops every watch started through this Store, waits for a load or
// write already in progress to finish, then closes everything handed to it
// with [WithCloser].
//
// Afterwards the last configuration still reads: Snapshot, View, Plan and the
// other read-only methods keep answering, and nothing can change it. Reload,
// Apply, AddLayer and Watch return [ErrStoreClosed]. A reload that was already
// running when Close was called completes, and its observers may be notified
// just after Close returns.
//
// Close is safe to call more than once; only the first call does anything. A
// closer that fails does not stop the rest, and their errors are joined.
func (s *Store) Close() error {
	s.life.mu.Lock()

	if s.life.closed.Load() {
		s.life.mu.Unlock()

		return nil
	}

	s.life.closed.Store(true)

	stops := make([]func(), 0, len(s.life.watches))
	for _, stop := range s.life.watches {
		stops = append(stops, stop)
	}

	s.life.watches = nil
	closers := s.life.closers
	s.life.closers = nil

	s.life.mu.Unlock()

	// Before anything is closed: a client closed under a live watch leaves the
	// watch retrying silently forever.
	stopAll(stops)

	s.awaitInFlight()

	var errs []error

	for i := len(closers) - 1; i >= 0; i-- {
		if err := closers[i].Close(); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// awaitInFlight returns once no load or write holds the Store lock. Anything
// that takes the lock afterwards sees the Store closed and refuses.
func (s *Store) awaitInFlight() {
	s.mu.Lock()
	s.mu.Unlock() //nolint:staticcheck // SA2001: the empty section is the point; it is a barrier.
}

func (s *Store) isClosed() bool { return s.life.closed.Load() }

// refusedAsClosed reports an error that is this Store declining a request
// after Close, which is not a source failing and so is never published.
func (s *Store) refusedAsClosed(err error) bool {
	return s.isClosed() && errors.Is(err, ErrStoreClosed)
}

// abandon closes what a failed NewStore was handed, so a caller who receives no
// Store has nothing left to release.
func (s *Store) abandon(err error) error {
	if cerr := s.Close(); cerr != nil {
		return errors.Join(err, cerr)
	}

	return err
}

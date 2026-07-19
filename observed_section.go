package config

import (
	"errors"
	"reflect"
	"sync"
)

// ErrNoMergeFunc is returned when a section supplies defaults but no way to
// combine them with the configured values. Silently preferring one over the
// other would drop half the settings without saying so.
var ErrNoMergeFunc = errors.New("config: section defaults require a merge function")

// ObservedSection stores the latest typed snapshot of an observed config
// section.
type ObservedSection[T any] struct {
	mu      sync.RWMutex
	current *Section[T]
	version uint64
	// source is the Snapshot version the held section was derived from, used to
	// reject a notification that arrives out of order.
	source uint64

	// apply is the callback registered with WithSectionApply, retained so the
	// caller can ask for the initial delivery once its own dependencies exist.
	apply func(SectionChange[T]) error
	// primed records that the initial delivery has happened, so asking twice
	// does not run a caller's setup twice.
	primed bool
}

// markPrimed records that an initial delivery is no longer appropriate.
func (s *ObservedSection[T]) markPrimed() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.primed = true
}

// ApplyInitial delivers the section the binding started with to the apply
// callback, exactly once, as an initial change.
//
// It exists because the natural place to react to configuration is the same
// callback that reacts to a change — but a callback usually closes over
// something built after the binding, so firing it during ObserveSection would
// run it against a half-constructed world. Handing the caller the trigger lets
// them say when their dependencies exist.
//
// A consumer that never calls this gets change-only delivery, which is what
// binding has always done. Calling it more than once is a no-op, so it is safe
// in a startup path that may run twice.
//
// Returns nil when no apply callback was registered, and whatever the callback
// returns otherwise.
func (s *ObservedSection[T]) ApplyInitial() error {
	s.mu.Lock()

	if s.primed || s.apply == nil || s.current == nil {
		s.mu.Unlock()

		return nil
	}

	s.primed = true
	apply := s.apply
	section := *s.current
	version := s.version

	s.mu.Unlock()

	// Called outside the lock: the callback is the caller's code, and holding
	// the section's lock across it would let a callback that reads the section
	// deadlock against itself.
	return apply(SectionChange[T]{
		Current: section,
		Initial: true,
		Changed: false,
		Version: version,
	})
}

// Value returns the latest complete settings snapshot.
func (s *ObservedSection[T]) Value() T {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.current == nil {
		var zero T

		return zero
	}

	return s.current.Value
}

// Current returns a pointer to the latest immutable settings snapshot. Callers
// must treat the returned value as read-only and call Current again when they
// need to observe a later reload.
func (s *ObservedSection[T]) Current() *T {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.current == nil {
		return nil
	}

	return &s.current.Value
}

// Exists reports whether the latest snapshot came from an explicit section.
func (s *ObservedSection[T]) Exists() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.current != nil && s.current.Exists
}

// Version returns the latest settings version. It starts at 1 after the initial
// snapshot and increments only when a later reload changes the observed section.
func (s *ObservedSection[T]) Version() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.version
}

// storeIfNewer adopts a section only if it is a genuine change derived from a
// snapshot at least as new as the one already held.
//
// The comparison and the store happen under one lock. Reading the previous
// section, deciding, and then storing was a read-modify-write that two
// concurrent notifications could interleave, so the later writer could be the
// one carrying the older configuration.
//
// The source version is the more important half. A Store publishes under its
// lock but notifies outside it, so nothing orders two notifications against
// each other — and this is the one component that caches what it is told, so an
// out-of-order delivery left it holding older values under a higher version:
// permanently stale, while reporting itself current to anyone watching Version.
func (s *ObservedSection[T]) storeIfNewer(
	section Section[T],
	source uint64,
	equal func(a, b Section[T]) bool,
) (previous Section[T], version uint64, changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.current != nil {
		previous = *s.current

		if source < s.source {
			return previous, s.version, false
		}

		if equal != nil && equal(previous, section) {
			s.source = source

			return previous, s.version, false
		}
	}

	next := section
	s.current = &next
	s.source = source
	s.version++

	return previous, s.version, true
}

// SectionChange describes an observed typed section change.
//
// Initial marks the delivery [ObservedSection.ApplyInitial] makes: the section
// the binding started with, rather than a change to it. Changed is its
// complement, true for every later delivery. A caller that treats both the same
// way can ignore them and read Current.
type SectionChange[T any] struct {
	Previous Section[T]
	Current  Section[T]
	Initial  bool
	Changed  bool
	Version  uint64
}

// SectionBindingOption customises ObserveSection behaviour.
type SectionBindingOption[T any] func(*SectionBindingConfig[T])

// SectionBindingConfig holds ObserveSection option state.
type SectionBindingConfig[T any] struct {
	defaults    T
	hasDefaults bool
	defaultFunc func(Observed) T
	merge       func(defaults, overlay T) T
	validate    func(T) error
	equal       func(previous, current Section[T]) bool
	apply       func(SectionChange[T]) error
}

// WithSectionDefaults starts each observed section from defaults and merges the
// decoded section over it when the section exists.
func WithSectionDefaults[T any](defaults T, merge func(defaults, overlay T) T) SectionBindingOption[T] {
	return func(cfg *SectionBindingConfig[T]) {
		cfg.defaults = defaults
		cfg.hasDefaults = true
		cfg.merge = merge
	}
}

// WithSectionDefaultFunc starts each observed section from defaults derived
// from the current config snapshot and merges the decoded section over it when
// the section exists.
func WithSectionDefaultFunc[T any](defaultFunc func(Observed) T, merge func(defaults, overlay T) T) SectionBindingOption[T] {
	return func(cfg *SectionBindingConfig[T]) {
		cfg.defaultFunc = defaultFunc
		cfg.hasDefaults = true
		cfg.merge = merge
	}
}

// WithSectionValidator validates each settings snapshot before it is published.
func WithSectionValidator[T any](validate func(T) error) SectionBindingOption[T] {
	return func(cfg *SectionBindingConfig[T]) {
		cfg.validate = validate
	}
}

// WithSectionEqual sets the equality function used to decide whether a
// successful rehydrate changed the observed section.
func WithSectionEqual[T any](equal func(previous, current Section[T]) bool) SectionBindingOption[T] {
	return func(cfg *SectionBindingConfig[T]) {
		cfg.equal = equal
	}
}

// WithSectionApply registers a callback that runs after a successful rehydrate
// changes the observed typed section.
func WithSectionApply[T any](apply func(SectionChange[T]) error) SectionBindingOption[T] {
	return func(cfg *SectionBindingConfig[T]) {
		cfg.apply = apply
	}
}

// Binder is something that can be read and will report when it changes.
//
// A Store is one. The interface exists so a typed section can be bound to
// anything that behaves like configuration — a fixed snapshot in a test, for
// instance — without dragging in the machinery that loads one.
type Binder interface {
	// View returns a read surface over the current configuration.
	View() *View
	// AddObserverFunc registers a function to run when configuration changes.
	AddObserverFunc(func(Observed) error)
}

// ObserveSection decodes a typed section and keeps it current.
//
// The returned value is the decoupling boundary this module exists for. A
// reusable package declares a one-method interface of its own —
//
//	type SettingsSource interface { Current() *ServerSettings }
//
// — and *ObservedSection[T] satisfies it structurally, so the package can take
// live, reloadable configuration without importing this module at all.
//
// Each snapshot is decoded in a single operation against a single
// configuration snapshot, so the struct handed out can never hold some fields
// from before a reload and some from after.
func ObserveSection[T any](
	binder Binder,
	key string,
	opts ...SectionBindingOption[T],
) (*ObservedSection[T], error) {
	settings := SectionBindingConfig[T]{}

	for _, opt := range opts {
		if opt != nil {
			opt(&settings)
		}
	}

	observed := &ObservedSection[T]{}

	reader := readerFor(binder)

	initial, err := loadObservedSection(reader, key, settings)
	if err != nil {
		return nil, err
	}

	observed.storeIfNewer(initial, 0, nil)
	observed.apply = settings.apply

	if binder != nil {
		binder.AddObserverFunc(func(next Observed) error {
			section, err := loadObservedSection(next, key, settings)
			if err != nil {
				return err
			}

			previous, version, changed := observed.storeIfNewer(
				section, next.Snapshot().Version(), settings.sectionsEqual)
			if !changed {
				return nil
			}

			// A change closes the initial window. Delivering the section the
			// binding started with after a later one has already been applied
			// would hand the caller stale configuration and call it initial.
			observed.markPrimed()

			if settings.apply != nil {
				return settings.apply(SectionChange[T]{
					Previous: previous,
					Current:  section,
					Initial:  false,
					Changed:  true,
					Version:  version,
				})
			}

			return nil
		})
	}

	return observed, nil
}

func loadObservedSection[T any](
	cfg Observed,
	key string,
	settings SectionBindingConfig[T],
) (Section[T], error) {
	section, err := UnmarshalSection[T](cfg, key)
	if err != nil {
		return Section[T]{}, err
	}

	section, err = applyObservedSectionDefaults(cfg, section, settings)
	if err != nil {
		return Section[T]{}, err
	}

	if settings.validate != nil {
		if err := settings.validate(section.Value); err != nil {
			return Section[T]{}, err
		}
	}

	return section, nil
}

func applyObservedSectionDefaults[T any](
	cfg Observed,
	section Section[T],
	settings SectionBindingConfig[T],
) (Section[T], error) {
	if !settings.hasDefaults {
		return section, nil
	}

	defaults := settings.defaults
	if settings.defaultFunc != nil {
		defaults = settings.defaultFunc(cfg)
	}

	if !section.Exists {
		section.Value = defaults

		return section, nil
	}

	if settings.merge == nil {
		return Section[T]{}, ErrNoMergeFunc
	}

	section.Value = settings.merge(defaults, section.Value)

	return section, nil
}

func (settings SectionBindingConfig[T]) sectionsEqual(previous, current Section[T]) bool {
	if settings.equal != nil {
		return settings.equal(previous, current)
	}

	return reflect.DeepEqual(previous, current)
}

// readerFor returns what a binder currently reads, as an interface that is nil
// when there is nothing to read.
//
// Declared as the interface rather than as *View on purpose: a nil *View boxed
// into an interface is not a nil interface, so a nil check downstream would not
// fire and decoding would run through a nil receiver instead of returning an
// empty section.
func readerFor(binder Binder) Observed {
	if binder == nil {
		return nil
	}

	v := binder.View()
	if v == nil {
		return nil
	}

	return v
}

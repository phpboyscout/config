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

func (s *ObservedSection[T]) snapshot() (Section[T], bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.current == nil {
		return Section[T]{}, false
	}

	return *s.current, true
}

func (s *ObservedSection[T]) store(section Section[T]) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := section
	s.current = &next
	s.version++

	return s.version
}

// SectionChange describes an observed typed section change.
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

	observed.store(initial)

	if binder != nil {
		binder.AddObserverFunc(func(next Observed) error {
			section, err := loadObservedSection(next, key, settings)
			if err != nil {
				return err
			}

			previous, ok := observed.snapshot()
			if ok && settings.sectionsEqual(previous, section) {
				return nil
			}

			version := observed.store(section)

			if settings.apply != nil {
				return settings.apply(SectionChange[T]{
					Previous: previous,
					Current:  section,
					Initial:  !ok,
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

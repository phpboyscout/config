package config

import (
	"fmt"
	"strings"
	"sync"

	"gitlab.com/phpboyscout/go/errors"
)

// ErrKeyCaseMismatch is returned by [NewStore] when a backend, or a nested
// Store, already compares keys in the other mode: a backend is bound to the
// mode of the one Store that owns it, and a nested Store's keys are already
// folded, or not, by the Store it wraps.
var ErrKeyCaseMismatch = errors.NewSentinel("config.key_case_mismatch", "config: backend compares keys in the other case mode")

// WithCaseSensitiveKeys makes the Store compare keys exactly, as each source
// spells them, so `Port` and `port` are two different keys in every layer,
// lookup, write, filter and view. Without it the Store folds keys to lower
// case. Spec 0015.
func WithCaseSensitiveKeys() StoreOption {
	return func(s *Store) { s.keys = keysExact }
}

// KeyCaseBinder is an optional interface for a backend that holds or forwards
// keys, such as a decorator matching patterns against them, so it compares keys
// as the Store that owns it does. [NewStore] calls it once, before the first
// load, and refuses the backend if it returns an error.
//
// A decorator must forward it to the backend it wraps, or a backend beneath it
// keeps folding inside a case-sensitive Store without saying so. A backend that
// addresses keys exactly whatever the mode has no reason to implement it.
type KeyCaseBinder interface {
	BindKeyCase(caseSensitive bool) error
}

// KeyCaseReporter is an optional interface for a [Reader] that can say whether
// it compares keys exactly. [*View] implements it. [ValidateStruct] and
// [UnmarshalSection] read a Reader that does not implement it as folded, so a
// wrapper around a View must forward it to decode in a case-sensitive Store.
type KeyCaseReporter interface {
	CaseSensitiveKeys() bool
}

// keyCase is how a Store compares keys. The zero value folds.
type keyCase uint8

const (
	keysFolded keyCase = iota
	keysExact
)

func keyCaseOf(caseSensitive bool) keyCase {
	if caseSensitive {
		return keysExact
	}

	return keysFolded
}

func (c keyCase) exact() bool { return c == keysExact }

// key applies the mode to one key segment.
func (c keyCase) key(k string) string {
	if c.exact() {
		return k
	}

	return strings.ToLower(k)
}

// split splits a dotted path into segments in the mode, returning nil for a
// path that is empty or malformed.
func (c keyCase) split(path string) []string {
	if strings.TrimSpace(path) == "" {
		return nil
	}

	segs := strings.Split(path, ".")
	out := make([]string, 0, len(segs))

	for _, s := range segs {
		if s == "" {
			return nil
		}

		out = append(out, c.key(s))
	}

	return out
}

// path applies the mode to a whole dotted path.
func (c keyCase) path(path string) string {
	segs := c.split(path)
	if segs == nil {
		return ""
	}

	return strings.Join(segs, ".")
}

func (c keyCase) String() string {
	if c.exact() {
		return "case-sensitive"
	}

	return "folded"
}

// readerKeyCase is the mode a Reader reads in, folded when it cannot say.
func readerKeyCase(r Reader) keyCase {
	if reporter, ok := r.(KeyCaseReporter); ok {
		return keyCaseOf(reporter.CaseSensitiveKeys())
	}

	return keysFolded
}

// keyCaseBinding is the mode a Store bound a backend to, and refuses a second
// Store binding it to the other one.
type keyCaseBinding struct {
	mu    sync.Mutex
	bound bool
	mode  keyCase
}

func (b *keyCaseBinding) bind(caseSensitive bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	mode := keyCaseOf(caseSensitive)
	if b.bound && b.mode != mode {
		return fmt.Errorf("%w: already bound %s, asked for %s", ErrKeyCaseMismatch, b.mode, mode)
	}

	b.bound, b.mode = true, mode

	return nil
}

func (b *keyCaseBinding) keys() keyCase {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.mode
}

// bindKeyCase binds every backend to the Store's mode, and refuses a nested
// Store built in the other one (spec 0015 D12).
func bindKeyCase(mode keyCase, backends []Backend) error {
	for _, b := range backends {
		if inner := innerNestedStore(b); inner != nil && inner.keys != mode {
			return fmt.Errorf("%w: nested store %q is %s, this store is %s",
				ErrKeyCaseMismatch, b.ID(), inner.keys, mode)
		}

		if err := forwardKeyCase(b, mode.exact()); err != nil {
			return fmt.Errorf("%w: %q", err, b.ID())
		}
	}

	return nil
}

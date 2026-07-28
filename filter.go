package config

import (
	"context"
	"strings"
	"sync"
	"time"
)

// FilterOption narrows what a filtered backend contributes. See [Filtered].
//
// A function type over an unexported struct rather than an interface: the set
// of rules is closed by this package, and an option a third party defined could
// not be reasoned about by the sensitive-key handling in [Filtered].
type FilterOption func(*filterRules)

// Allow restricts a backend to the keys matching at least one pattern.
//
// With no Allow, every key is permitted unless a [Deny] excludes it. Several
// Allow options, or several patterns in one, union.
func Allow(patterns ...string) FilterOption {
	return func(r *filterRules) { r.allow = append(r.allow, patterns...) }
}

// Deny excludes the keys matching any pattern, and beats [Allow].
//
// A caller who has written both has expressed a narrowing intention twice, so
// resolving the overlap the other way would let an Allow re-expose something
// explicitly denied.
func Deny(patterns ...string) FilterOption {
	return func(r *filterRules) { r.deny = append(r.deny, patterns...) }
}

// filterRules is a resolved set of allow and deny patterns.
type filterRules struct {
	allow []string
	deny  []string
}

// empty reports whether the rules would permit everything, so [Filtered] can
// return the backend untouched rather than wrapping it in a no-op.
func (r filterRules) empty() bool { return len(r.allow) == 0 && len(r.deny) == 0 }

// permits reports whether a path survives the rules.
func (r filterRules) permits(segs []string) bool {
	if segs == nil {
		return false
	}

	for _, pattern := range r.deny {
		if matchPattern(pattern, segs) {
			return false
		}
	}

	if len(r.allow) == 0 {
		return true
	}

	for _, pattern := range r.allow {
		if matchPattern(pattern, segs) {
			return true
		}
	}

	return false
}

// matchPattern reports whether a dotted-path pattern matches a split path.
//
// Two wildcards, and the difference between them is the segment boundary:
//
//	db.*   one segment  — db.host, not db.primary.host
//	db.**  any depth    — both
//
// Deliberately not regular expressions. A regexp invites patterns that cross
// segment boundaries in ways that are hard to reason about against a tree, and
// a mistake in one silently widens the filter rather than narrowing it — the
// wrong direction when what it is holding back is a secret.
//
// "**" is terminal: it consumes the rest of the path, so it is only meaningful
// as the last segment. Anything after it would be unreachable, and a pattern
// whose tail can never match is a typo worth not honouring quietly.
func matchPattern(pattern string, segs []string) bool {
	parts := strings.Split(pattern, ".")

	for i, part := range parts {
		if part == "**" {
			// Matches one or more remaining segments, never zero: db.** is
			// "everything under db", and db itself is not under db.
			return len(segs) > i
		}

		if i >= len(segs) {
			return false
		}

		if part != "*" && part != segs[i] {
			return false
		}
	}

	return len(parts) == len(segs)
}

// Filtered bounds which keys a backend contributes, and therefore which it can
// be written to.
//
// A decorator rather than a feature of any one backend, so the same rules apply
// to a secrets manager, a parameter store, a file or a nested store:
//
//	config.WithBackend(config.Filtered(vault, config.Allow("db.**")))
//
// A denied key is simply absent — reads fall through to whatever lower layer
// defines it, and a write routes past this backend to the next writable layer,
// because a backend that defines nothing for a key is correctly skipped by
// routing. Neither needs a special case.
//
// Filtering happens after the backend has fetched and parsed, so it is a
// visibility bound and not a fetch optimisation: a denied key is still read
// from the remote system, still crosses the network, and still sits in memory
// briefly. It is not a security control against the backend itself.
//
// With no options the backend is returned unchanged, so the zero configuration
// costs nothing.
func Filtered(b Backend, opts ...FilterOption) Backend {
	var rules filterRules
	for _, opt := range opts {
		opt(&rules)
	}

	if rules.empty() {
		return b
	}

	f := &filtered{inner: b, rules: rules}

	// The returned value must implement exactly the optional interfaces the
	// inner backend does — the capability-honesty rule the filesystem adapters
	// established. Writable and watchable are the two that change how the Store
	// behaves, so they are the two the type must vary on.
	//
	// SourceKindDeclarer and PollIntervalHinter are implemented unconditionally
	// instead, because their absent-behaviour and their delegate-to-nothing
	// behaviour coincide: an undeclared kind defaults to SourceFile either way,
	// and a zero poll hint is ignored either way. Varying on those too would
	// mean sixteen types to say nothing extra.
	writable, isWritable := b.(WritableBackend)
	watchable, isWatchable := b.(WatchableBackend)

	switch {
	case isWritable && isWatchable:
		return &filteredWritableWatchable{
			filteredWritable: filteredWritable{filtered: f, write: writable},
			watch:            watchable,
		}
	case isWritable:
		return &filteredWritable{filtered: f, write: writable}
	case isWatchable:
		return &filteredWatchable{filtered: f, watch: watchable}
	default:
		return f
	}
}

// filtered is the read half of a filtered backend.
type filtered struct {
	inner Backend
	rules filterRules

	// withheld records the sensitive paths this filter removed at the last
	// Load. It exists for the leak guard: a denied key is invisible to the
	// store, so without this a write of that key would route into a plain layer
	// beneath and NOT be refused — the filter would silently re-open the hole
	// ErrSensitiveLeak exists to close.
	//
	// Recorded from real data rather than derived from the rules, so it names
	// only paths the backend genuinely holds. Asking the rules instead would
	// refuse writes to a key nothing owns.
	mu       sync.RWMutex
	withheld []string
}

// ID and Capabilities are forwarded unchanged, and both matter.
//
// The write path finds a backend by matching a layer's Source.Name against
// ID(), so a decorator that renamed itself would contribute layers no write
// could be routed back to. And the Store reads Capabilities().Sensitive
// directly to build the leak guard's source set, so a decorator returning a
// zero Capabilities would strip the sensitive flag from a secrets backend and
// disarm the guard for every key it owns.
func (f *filtered) ID() string { return f.inner.ID() }

func (f *filtered) Capabilities() Capabilities { return f.inner.Capabilities() }

// SourceKind delegates when the inner backend declares one, and otherwise
// reports the same default the Store would have applied itself.
func (f *filtered) SourceKind() SourceKind {
	if d, ok := f.inner.(SourceKindDeclarer); ok {
		return d.SourceKind()
	}

	return SourceFile
}

// PollInterval delegates when the inner backend hints, and otherwise returns
// zero, which the Store reads as "no hint".
func (f *filtered) PollInterval() time.Duration {
	if h, ok := f.inner.(PollIntervalHinter); ok {
		return h.PollInterval()
	}

	return 0
}

func (f *filtered) Load(ctx context.Context, below []Layer) ([]Layer, error) {
	layers, err := f.inner.Load(ctx, below)
	if err != nil {
		return nil, err
	}

	sensitive := f.inner.Capabilities().Sensitive

	var (
		out      []Layer
		withheld []string
	)

	for _, layer := range layers {
		kept, removed := filterValues(layer.Values, f.rules, nil)

		if sensitive {
			withheld = append(withheld, removed...)
		}

		// A layer filtered down to nothing is dropped rather than contributed
		// empty: an empty layer still occupies a place in the precedence order
		// and shows up in provenance as a source that defines nothing, which is
		// noise the caller did not ask for.
		if len(kept) == 0 {
			continue
		}

		out = append(out, Layer{Source: layer.Source, Values: kept})
	}

	f.mu.Lock()
	f.withheld = withheld
	f.mu.Unlock()

	return out, nil
}

// withheldSensitive reports the sensitive paths this filter is hiding.
func (f *filtered) withheldSensitive() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()

	return f.withheld
}

// filterValues walks a value tree, keeping permitted leaves and reporting the
// paths it removed. Parent maps left empty are dropped, so a filtered subtree
// does not linger as an empty map that reads as "defined, but with nothing in
// it".
func filterValues(values map[string]any, rules filterRules, prefix []string) (map[string]any, []string) {
	if len(values) == 0 {
		return nil, nil
	}

	kept := make(map[string]any, len(values))

	var removed []string

	for key, value := range values {
		path := append(append([]string{}, prefix...), key)

		if nested, isMap := asStringMap(value); isMap && len(nested) > 0 {
			sub, subRemoved := filterValues(nested, rules, path)
			removed = append(removed, subRemoved...)

			if len(sub) > 0 {
				kept[key] = sub
			}

			continue
		}

		if !rules.permits(path) {
			removed = append(removed, strings.Join(path, "."))

			continue
		}

		kept[key] = value
	}

	if len(kept) == 0 {
		return nil, removed
	}

	return kept, removed
}

// The optional-interface combinations. Each embeds the read half by POINTER —
// filtered carries a mutex, so embedding it by value would copy a lock — and
// forwards the one capability it adds, so behaviour cannot drift between them.

type filteredWritable struct {
	*filtered

	write WritableBackend
}

func (f *filteredWritable) Prepare(ctx context.Context, edits []Edit) (Pending, error) {
	return f.write.Prepare(ctx, edits)
}

type filteredWatchable struct {
	*filtered

	watch WatchableBackend
}

func (f *filteredWatchable) Watch(ctx context.Context, interval time.Duration, onChange func()) (func(), error) {
	return f.watch.Watch(ctx, interval, onChange)
}

type filteredWritableWatchable struct {
	filteredWritable

	watch WatchableBackend
}

func (f *filteredWritableWatchable) Watch(ctx context.Context, interval time.Duration, onChange func()) (func(), error) {
	return f.watch.Watch(ctx, interval, onChange)
}

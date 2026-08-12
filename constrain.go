package config

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ErrForbiddenKey is returned when a source supplies, or is asked to write, a
// key its constraint forbids. Match it with errors.Is to tell a policy refusal
// apart from a value that merely failed its shape check.
var errForbiddenKey = fmt.Errorf("%w: source supplied a forbidden key", ErrInvalidConfig)

// Constrained bounds what a source is allowed to CONTRIBUTE, and reports it
// when the source oversteps.
//
// It judges only what the backend actually supplies. A layer is never required
// to be complete — a base file may omit a key its overlay provides — so a key
// the constraint describes and this source does not carry is somebody else's to
// supply and is not a failure here.
//
// # This is not Filtered, and the difference is the point
//
// [Filtered] bounds VISIBILITY: a denied key is simply absent, reads fall
// through to whatever lower layer has one, and nothing is said. That is right
// for "this source does not get to contribute here".
//
// Constrained bounds POLICY: a forbidden key stays visible and is reported. That
// is right for "this source contributing here is a fault".
//
// Reaching for the wrong one is costly in exactly the case that matters most. A
// literal credential in a file layer beneath a working environment reference is
// a leak; filtering it out would remove it from the store and the leak would
// never be reported by anything. Filtering hides; constraining tells.
func Constrained(b Backend, opts ...ConstraintOption) Backend {
	var c constraint
	for _, opt := range opts {
		opt(&c)
	}

	if c.empty() {
		return b
	}

	con := &constrained{inner: b, rules: c}

	// Exactly the optional interfaces the inner backend has, and no others —
	// the capability-honesty rule the filesystem adapters established. A
	// decorator that quietly added or dropped one would change how the Store
	// treats the source merely because it was constrained.
	writable, isWritable := b.(WritableBackend)
	watchable, isWatchable := b.(WatchableBackend)

	switch {
	case isWritable && isWatchable:
		return &constrainedWritableWatchable{
			constrainedWritable: constrainedWritable{constrained: con, write: writable},
			watch:               watchable,
		}
	case isWritable:
		return &constrainedWritable{constrained: con, write: writable}
	case isWatchable:
		return &constrainedWatchable{constrained: con, watch: watchable}
	default:
		return con
	}
}

// ConstraintOption configures what a source may contribute.
type ConstraintOption func(*constraint)

type constraint struct {
	forbid []string
	schema Schema
	name   string
}

func (c constraint) empty() bool { return len(c.forbid) == 0 && isNil(c.schema) }

// Forbid declares keys this source must not supply. Supplying one is a failure,
// reported against the key and the source that carried it.
//
// Patterns are the same glob shape [Deny] uses, so "credentials.*" covers the
// subtree. Use it for configuration that is legitimate somewhere else and wrong
// here — a credential in a file, a production endpoint in a checked-in default.
func Forbid(patterns ...string) ConstraintOption {
	return func(c *constraint) { c.forbid = append(c.forbid, patterns...) }
}

// MustMatch declares the shape of what this source supplies.
//
// Only keys the source actually carries are checked, so a schema describing a
// required key does not fail a source that simply does not provide it. What it
// catches is a source supplying a value of the wrong shape — a remote store
// handing back a port that is not a number — at the layer that produced it,
// before the merge has hidden which one that was.
func MustMatch(s Schema) ConstraintOption {
	return func(c *constraint) { c.schema = s }
}

// ConstraintName labels the source in failure messages. Defaults to the
// backend's ID.
func ConstraintName(name string) ConstraintOption {
	return func(c *constraint) { c.name = name }
}

// constrained is the read half.
type constrained struct {
	inner Backend
	rules constraint
}

func (c *constrained) ID() string                 { return c.inner.ID() }
func (c *constrained) Capabilities() Capabilities { return c.inner.Capabilities() }

func (c *constrained) Load(ctx context.Context, prev []Layer) ([]Layer, error) {
	return c.inner.Load(ctx, prev)
}

// constrain reports what this source supplied that it should not have.
//
// The Store calls it with the layers this backend produced, which is what makes
// the check per-source: the same key from a different layer is a different
// question, and one this constraint has no opinion about.
func (c *constrained) constrain(layers []Layer, r *ValidationResult) {
	name := c.rules.name
	if name == "" {
		name = c.inner.ID()
	}

	for _, layer := range layers {
		for _, key := range leafPaths(layer.Values, nil) {
			if !matchesAny(key, c.rules.forbid) {
				continue
			}

			r.AddError(ValidationError{
				Key:         key,
				Message:     "supplied by a source that is forbidden to carry it",
				Hint:        "move this value to a source permitted to hold it, or remove it",
				Contributor: name,
			})
		}
	}

	if isNil(c.rules.schema) {
		return
	}

	// Shape is judged against a snapshot of THIS source alone, and then any
	// failure about a key the source does not carry is discarded.
	//
	// Dropping them is the whole distinction from a registered contribution: a
	// schema's "required" fires on absence, and absence is exactly what a
	// source may legitimately have — the base file omits what the overlay
	// supplies. Only what this layer actually put there is its business.
	snap := newSnapshot(0, layers)

	supplied := &ValidationResult{}
	c.rules.schema.Validate(snap, "", supplied)

	for _, e := range supplied.Errors {
		if !snap.Has(e.Key) {
			continue
		}

		e.Contributor = name
		r.AddError(e)
	}

	for _, w := range supplied.Warnings {
		if !snap.Has(w.Key) {
			continue
		}

		w.Contributor = name
		r.AddWarning(w)
	}
}

// forbids reports whether a path may not be written to this source.
func (c *constrained) forbids(path string) bool { return matchesAny(path, c.rules.forbid) }

type constrainedWritable struct {
	*constrained

	write WritableBackend
}

// Prepare refuses a write to a forbidden key rather than letting it through.
//
// This is deliberately the opposite of [Filtered], where a write to a denied key
// routes PAST the backend and lands somewhere else (see the backend key
// filtering spec, D7). That is right for a visibility bound and wrong for a
// policy one: a forbidden credential that quietly rerouted would be the same
// leak in a different file, with nothing said about it.
func (c *constrainedWritable) Prepare(ctx context.Context, edits []Edit) (Pending, error) {
	for _, e := range edits {
		if c.forbids(e.Path) {
			return nil, fmt.Errorf("%w: %q may not be written to source %q",
				errForbiddenKey, e.Path, c.ID())
		}
	}

	return c.write.Prepare(ctx, edits)
}

type constrainedWatchable struct {
	*constrained

	watch WatchableBackend
}

func (c *constrainedWatchable) Watch(ctx context.Context, interval time.Duration, onChange func()) (func(), error) {
	return c.watch.Watch(ctx, interval, onChange)
}

type constrainedWritableWatchable struct {
	constrainedWritable

	watch WatchableBackend
}

func (c *constrainedWritableWatchable) Watch(ctx context.Context, interval time.Duration, onChange func()) (func(), error) {
	return c.watch.Watch(ctx, interval, onChange)
}

// sourceConstraint is what the Store asks a backend about during validation.
// Implemented only by Constrained, so an ordinary backend costs nothing.
type sourceConstraint interface {
	constrain(layers []Layer, r *ValidationResult)
}

// matchesAny reports whether a dotted path matches any pattern, using the same
// glob semantics [Deny] uses so a caller does not have to learn a second one.
func matchesAny(path string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}

	segs := splitPath(path)
	for _, p := range patterns {
		if matchPattern(p, segs) {
			return true
		}
	}

	return false
}

// leafPaths flattens a layer's values into dotted leaf paths.
func leafPaths(values map[string]any, at []string) []string {
	var out []string

	for k, v := range values {
		path := append(append([]string{}, at...), k)

		if nested, ok := v.(map[string]any); ok && len(nested) > 0 {
			out = append(out, leafPaths(nested, path)...)

			continue
		}

		out = append(out, strings.Join(path, "."))
	}

	return out
}

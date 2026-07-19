package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/cast"
)

// View is a typed read surface over one snapshot.
//
// A View performs no I/O. It resolves values from the snapshot it was built
// with, so what it returns cannot change underneath a caller — a sequence of
// reads through one View is coherent even if the Store publishes a new
// snapshot midway.
//
// Views are cheap: taking one is a pointer copy, not a load.
type View struct {
	snap *Snapshot
	// prefix scopes a derived view to a subtree. Every path is qualified with
	// it before resolution, so a scoped view stays live against the snapshot
	// rather than holding a detached copy of the subtree.
	prefix string
}

// NewView returns a typed view over a snapshot.
func NewView(snap *Snapshot) *View {
	return &View{snap: snap}
}

// View returns a typed read surface over the Store's current snapshot.
func (s *Store) View() *View {
	return NewView(s.Snapshot())
}

// With runs fn against a view pinned to one snapshot.
//
// Individual reads are always coherent, but a sequence of them can straddle a
// reload — read the host from one snapshot and the port from the next, and you
// connect to the new host on the old port. With closes that window for a block
// of related reads.
//
// It is scoped to a closure rather than handed out as a handle because a handle
// can be kept indefinitely: it would pin parsed configuration in memory and
// serve values that quietly grew arbitrarily old.
func (s *Store) With(fn func(*View) error) error {
	return fn(s.View())
}

// Snapshot returns the snapshot this view reads from.
func (v *View) Snapshot() *Snapshot { return v.snap }

// qualify prepends the view's prefix to a path.
func (v *View) qualify(path string) string {
	if v.prefix == "" {
		return path
	}

	if path == "" {
		return v.prefix
	}

	return v.prefix + "." + path
}

// Sub returns a view scoped to a subtree.
//
// The result stays live against the same snapshot rather than holding a
// detached copy: every read is resolved through the full path, so a scoped
// view cannot serve values that the rest of the configuration has moved past.
// Returns nil when the key is absent, so `if sub != nil` guards behave.
func (v *View) Sub(key string) *View {
	full := v.qualify(key)
	if !v.snap.Has(full) {
		return nil
	}

	return &View{snap: v.snap, prefix: full}
}

// Get returns the raw value at a path.
func (v *View) Get(path string) any {
	got, _ := v.snap.Get(v.qualify(path))

	return got
}

// Has reports whether a path is present.
//
// A key whose value is an empty container is present: emptiness is a value.
func (v *View) Has(path string) bool { return v.snap.Has(v.qualify(path)) }

// IsSet is an alias for Has.
//
// The incumbent distinguished "present in a file" from "present anywhere",
// because its file layer and its environment layer were different kinds of
// thing. Here every source is a layer, so there is one honest answer and two
// names for it, kept for familiarity.
func (v *View) IsSet(path string) bool { return v.Has(path) }

// SectionExists reports whether a path holds a mapping.
func (v *View) SectionExists(path string) bool {
	got, ok := v.snap.Get(v.qualify(path))
	if !ok {
		return false
	}

	_, isMap := asStringMap(got)

	return isMap
}

// Origin reports which layer supplied the effective value at a path.
func (v *View) Origin(path string) (Source, bool) { return v.snap.Origin(v.qualify(path)) }

// Shadowed lists every layer defining a path, lowest precedence first.
func (v *View) Shadowed(path string) []Source { return v.snap.Shadowed(v.qualify(path)) }

// Explain describes where a value came from and what else defines it.
//
// This is the question every configuration debugging session starts with, and
// the one a merge-eager library cannot answer at all.
func (v *View) Explain(path string) string {
	full := v.qualify(path)

	value, ok := v.snap.Get(full)
	if !ok {
		return fmt.Sprintf("%s is not set", full)
	}

	src, hasOrigin := v.snap.Origin(full)
	if !hasOrigin {
		return fmt.Sprintf("%s is a subtree assembled from %s",
			full, joinSources(v.snap.Shadowed(full)))
	}

	out := fmt.Sprintf("%s = %v (from %s)", full, value, src)

	if all := v.snap.Shadowed(full); len(all) > 1 {
		out += fmt.Sprintf("; also defined in %s", joinSources(all[:len(all)-1]))
	}

	return out
}

func joinSources(sources []Source) string {
	parts := make([]string, 0, len(sources))
	for _, s := range sources {
		parts = append(parts, s.String())
	}

	return strings.Join(parts, ", ")
}

// Typed accessors. Each coerces the underlying value, returning the zero value
// when the path is absent or cannot be converted.

// GetString returns a path as a string.
func (v *View) GetString(path string) string { return cast.ToString(v.Get(path)) }

// GetBool returns a path as a bool.
func (v *View) GetBool(path string) bool { return cast.ToBool(v.Get(path)) }

// GetInt returns a path as an int.
func (v *View) GetInt(path string) int { return cast.ToInt(v.Get(path)) }

// GetFloat returns a path as a float64.
func (v *View) GetFloat(path string) float64 { return cast.ToFloat64(v.Get(path)) }

// GetDuration returns a path as a duration.
func (v *View) GetDuration(path string) time.Duration { return cast.ToDuration(v.Get(path)) }

// GetTime returns a path as a time.
func (v *View) GetTime(path string) time.Time { return cast.ToTime(v.Get(path)) }

// GetStringSlice returns a path as a string slice.
func (v *View) GetStringSlice(path string) []string { return cast.ToStringSlice(v.Get(path)) }

// Keys returns every leaf path visible through this view, sorted.
func (v *View) Keys() []string {
	all := v.snap.Keys()
	if v.prefix == "" {
		return all
	}

	prefix := v.prefix + "."

	var out []string

	for _, k := range all {
		if strings.HasPrefix(k, prefix) {
			out = append(out, strings.TrimPrefix(k, prefix))
		}
	}

	return out
}

// Unmarshal decodes the whole view into a struct.
func (v *View) Unmarshal(target any) error {
	return decodeInto(v.valuesForPrefix(), target)
}

// UnmarshalKey decodes a subtree into a struct.
//
// Decoding is a single operation against one snapshot, so a struct populated
// this way is internally consistent by construction — it cannot contain some
// fields from before a reload and some from after.
func (v *View) UnmarshalKey(path string, target any) error {
	got, ok := v.snap.Get(v.qualify(path))
	if !ok {
		return nil
	}

	return decodeInto(got, target)
}

func (v *View) valuesForPrefix() any {
	if v.prefix == "" {
		return v.snap.Values()
	}

	got, _ := v.snap.Get(v.prefix)

	return got
}

// decodeInto runs the struct decoder with the conversions configuration
// realistically needs: durations and times written as strings, and comma
// separated values arriving as one string.
func decodeInto(input, target any) error {
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:           target,
		WeaklyTypedInput: true,
		Metadata:         nil,
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
		),
	})
	if err != nil {
		return fmt.Errorf("config: building decoder: %w", err)
	}

	if err := decoder.Decode(input); err != nil {
		return fmt.Errorf("config: decoding: %w", err)
	}

	return nil
}

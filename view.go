package config

import (
	"fmt"
	"reflect"
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
//
// An empty path asks about the view's own root, which is a mapping whenever
// there is any configuration at all — that is what lets a caller check a
// scoped view without special-casing the top level.
func (v *View) SectionExists(path string) bool {
	if v.qualify(path) == "" {
		return len(v.snap.values) > 0
	}

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
	t := reflect.TypeOf(target)
	if t == nil {
		return fmt.Errorf("%w: nil target", ErrInvalidTarget)
	}

	if t.Kind() != reflect.Pointer {
		return fmt.Errorf("%w: target must be a pointer, got %s", ErrInvalidTarget, t)
	}

	if reflect.ValueOf(target).IsNil() {
		return fmt.Errorf("%w: result must be addressable — target pointer is nil", ErrInvalidTarget)
	}

	input = translateKeys(input, t)

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

// translateKeys rewrites input keys onto the names the struct decoder expects.
//
// Configuration structs in the wild are tagged three ways. A type shared with
// an API carries `json`, one shared with a YAML document carries `yaml`, and
// one written for this library carries `mapstructure`. Honouring only the last
// would mean a field tagged either of the other two decodes as its zero value
// — silently, with no error, which is the failure mode this module exists to
// remove.
//
// So all three are honoured, in that order of specificity: an explicit
// mapstructure tag wins, then yaml, then json, then the field name itself.
func translateKeys(input any, target reflect.Type) any {
	target = deref(target)
	if target == nil {
		return input
	}

	// A collection carries the translation down to what it holds. Stopping at
	// the first non-struct meant a struct inside a list or a map was never
	// visited, so a field in it spelled with a yaml or json tag decoded as its
	// zero value — silently, and with no error, which is precisely the failure
	// this function exists to remove.
	if translated, handled := translateCollection(input, target); handled {
		return translated
	}

	values, ok := asStringMap(input)
	if !ok {
		return input
	}

	if target.Kind() != reflect.Struct {
		return input
	}

	out := make(map[string]any, len(values))
	for k, v := range values {
		out[k] = v
	}

	for i := range target.NumField() {
		out = translateField(out, target.Field(i))
	}

	return out
}

// translateField resolves one struct field's aliases within a value map and
// carries translation into whatever that field holds.
func translateField(out map[string]any, field reflect.StructField) map[string]any {
	if !field.IsExported() {
		return out
	}

	canonical, aliases, squash, skip := fieldKeys(field)
	if skip {
		return out
	}

	if squash {
		// A squashed struct's fields live at this level, so its aliases have to
		// be resolved here too.
		squashed, _ := asStringMap(translateKeys(out, field.Type))

		return squashed
	}

	resolveAlias(out, canonical, aliases)

	if nested, present := out[canonical]; present {
		out[canonical] = translateKeys(nested, field.Type)
	}

	return out
}

// translateCollection carries translation into a slice, array or map, and
// reports whether the target was a collection at all.
func translateCollection(input any, target reflect.Type) (any, bool) {
	switch target.Kind() {
	case reflect.Slice, reflect.Array:
		items, ok := input.([]any)
		if !ok {
			return input, true
		}

		out := make([]any, len(items))
		for i, item := range items {
			out[i] = translateKeys(item, target.Elem())
		}

		return out, true

	case reflect.Map:
		entries, ok := asStringMap(input)
		if !ok {
			return input, true
		}

		out := make(map[string]any, len(entries))
		for k, v := range entries {
			out[k] = translateKeys(v, target.Elem())
		}

		return out, true

	default:
		return nil, false
	}
}

// resolveAlias moves a value written under an alias to the canonical key.
//
// Matching is normalised on both sides. A tag is written in whatever case its
// author chose, while a mapping's keys are lower-cased on the way into a
// layer — but the keys inside a sequence element are not, because nothing walks
// them. Comparing raw would therefore work in a list and fail in a map, which
// is how a field tagged only yaml or json came to decode as its zero value in
// one place and not the other.
func resolveAlias(values map[string]any, canonical string, aliases []string) {
	if _, ok := values[canonical]; ok {
		return
	}

	for _, alias := range aliases {
		want := normaliseKey(alias)

		for key, v := range values {
			if normaliseKey(key) == want {
				values[canonical] = v

				return
			}
		}
	}
}

// fieldKeys reports the key a field decodes from, the alternative spellings
// that should also match, and whether it is squashed or skipped.
func fieldKeys(field reflect.StructField) (canonical string, aliases []string, squash, skip bool) {
	mapTag, mapOpts := splitTag(field.Tag.Get("mapstructure"))
	if mapTag == "-" {
		return "", nil, false, true
	}

	if mapOpts["squash"] {
		return "", nil, true, false
	}

	canonical = mapTag
	if canonical == "" {
		canonical = normaliseKey(field.Name)
	}

	for _, name := range []string{"yaml", "json"} {
		if tag, _ := splitTag(field.Tag.Get(name)); tag != "" && tag != "-" && tag != canonical {
			aliases = append(aliases, tag)
		}
	}

	return canonical, aliases, false, false
}

func splitTag(tag string) (name string, opts map[string]bool) {
	parts := strings.Split(tag, ",")
	opts = map[string]bool{}

	for _, o := range parts[1:] {
		opts[o] = true
	}

	return parts[0], opts
}

func deref(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return t
}

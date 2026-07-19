package config

import (
	"fmt"
	"strings"
)

// SourceKind identifies where a layer's values came from.
//
// Provenance is only useful if it can name every kind of source, not just
// files: "came from prod.yaml line 14" and "came from the environment" are
// both answers a user needs when asking why a value is what it is.
type SourceKind string

const (
	// SourceFile is a configuration file.
	SourceFile SourceKind = "file"
	// SourceEnv is an environment variable.
	SourceEnv SourceKind = "env"
	// SourceFlag is a bound command-line flag.
	SourceFlag SourceKind = "flag"
	// SourceDefault is a compiled-in default.
	SourceDefault SourceKind = "default"
	// SourceOverride is an ephemeral in-process override. It is never
	// persisted.
	SourceOverride SourceKind = "override"
)

// Source identifies one contributing layer.
//
// A file that holds several YAML documents contributes one Source per
// document, because a document is a layer: that is what lets routing,
// provenance and precedence treat documents and files uniformly.
type Source struct {
	Kind SourceKind
	// Name is the file path for file sources, the variable name for env,
	// the flag name for flags, and empty otherwise.
	Name string
	// Document is the zero-based index within a multi-document file. Always
	// zero for non-file sources and single-document files.
	Document int
	// Writable reports whether this layer can be persisted to. Env, flags and
	// compiled-in defaults are readable but not writable, and routing must
	// skip them.
	Writable bool
}

// String renders a source for display and for provenance reporting.
func (s Source) String() string {
	switch s.Kind {
	case SourceFile:
		if s.Document > 0 {
			return fmt.Sprintf("%s#%d", s.Name, s.Document)
		}

		return s.Name
	case SourceEnv, SourceFlag, SourceDefault, SourceOverride:
		// Named, because a caller asking where a value came from needs to know
		// which variable, flag, set of defaults or runtime layer supplied it,
		// not merely that it was one of them.
		if s.Name == "" {
			return string(s.Kind)
		}

		return string(s.Kind) + ":" + s.Name
	default:
		return string(s.Kind)
	}
}

// Authored reports whether a source is one a person edits and a schema should
// therefore govern.
//
// A file and a compiled-in default are written deliberately, so a key appearing
// in one that the schema does not describe is worth reporting. The environment,
// command-line flags and layers added at runtime are ambient: a deployment
// platform exporting an unrelated prefixed variable must not be able to stop an
// application starting.
//
// Declared beside the kinds it derives from, so the question is answered once
// rather than by an allowlist in whatever code happens to need it.
func (s Source) Authored() bool {
	switch s.Kind {
	case SourceFile, SourceDefault:
		return true
	case SourceEnv, SourceFlag, SourceOverride:
		return false
	default:
		return false
	}
}

// Layer is one contributing set of configuration values, with the identity of
// where they came from.
type Layer struct {
	Source Source
	Values map[string]any
}

// mergeLayers folds layers in precedence order — earliest first, latest wins —
// returning the effective values and, for every leaf key, the layer that
// supplied it.
//
// Merging is per-leaf and deep: a later layer overriding one nested key does
// not discard its siblings. That is what makes an overlay file able to change
// a single setting without restating the subtree around it.
func mergeLayers(layers []Layer) (map[string]any, map[string]Source) {
	merged := map[string]any{}
	origin := map[string]Source{}

	for _, layer := range layers {
		mergeInto(merged, origin, layer.Values, layer.Source, "")
	}

	return merged, origin
}

// mergeInto folds one layer's values into the accumulator, recording
// provenance as it goes.
func mergeInto(dst map[string]any, origin map[string]Source, src map[string]any, source Source, prefix string) {
	for key, value := range src {
		// Keys arrive normalised: layers are normalised where they enter the
		// Store, so doing it again here would be a second owner of the rule.
		path := joinPath(prefix, key)

		nested, isMap := asStringMap(value)
		if !isMap {
			dst[key] = value
			origin[path] = source

			// A scalar replacing a subtree takes ownership of the whole path:
			// the keys beneath it are no longer reachable, so their provenance
			// would be a lie.
			pruneProvenance(origin, path)

			continue
		}

		existing, ok := asStringMap(dst[key])
		if !ok {
			// Either absent, or a scalar being replaced by a mapping. Start a
			// fresh subtree; the scalar's own provenance no longer applies.
			existing = map[string]any{}

			delete(origin, path)
		}

		mergeInto(existing, origin, nested, source, path)
		dst[key] = existing

		// Provenance belongs to leaves. An empty container is a leaf — it holds
		// a value, just not entries — but a populated one is assembled from
		// however many layers contributed to it, so naming a single source for
		// it would be dishonest. Callers asking "where did this subtree come
		// from" are asking the wrong question; Shadowed answers the right one.
		if len(existing) == 0 {
			origin[path] = source
		} else {
			delete(origin, path)
		}
	}
}

// pruneProvenance drops provenance for every key beneath a path that has been
// replaced by a scalar.
func pruneProvenance(origin map[string]Source, path string) {
	prefix := path + "."

	for k := range origin {
		if strings.HasPrefix(k, prefix) {
			delete(origin, k)
		}
	}
}

// asStringMap normalises the two map shapes a YAML decoder can produce.
//
// A decoder targeting `any` may yield map[any]any rather than
// map[string]any depending on the document, and a merge that handled only one
// of them would silently treat the other as a scalar — replacing a subtree
// instead of merging into it.
func asStringMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))

		for k, val := range m {
			key, ok := k.(string)
			if !ok {
				key = fmt.Sprint(k)
			}

			out[key] = val
		}

		return out, true
	default:
		return nil, false
	}
}

// normaliseKey applies the module's key-casing rule.
//
// Keys are lower-cased. This is not cosmetic: validation compares schema field
// names against configuration keys, and struct decoding derives keys from field
// names, so a case-sensitive model would make `Port` and `port` different
// settings and break both.
func normaliseKey(k string) string {
	return strings.ToLower(k)
}

// cloneValues deep-copies a value tree so a Layer cannot be mutated through a
// reference held elsewhere. Snapshots are immutable, and immutability that
// depends on callers behaving is not immutability.
func cloneValues(v any) any {
	m, ok := asStringMap(v)
	if !ok {
		if s, ok := v.([]any); ok {
			out := make([]any, len(s))
			for i, item := range s {
				out[i] = cloneValues(item)
			}

			return out
		}

		return v
	}

	out := make(map[string]any, len(m))
	for k, val := range m {
		out[k] = cloneValues(val)
	}

	return out
}

// cloneMap deep-copies a value map.
func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneValues(v)
	}

	return out
}

// normalised returns layers whose keys carry the module's casing rule.
//
// Applied where layers enter the Store, so "a Layer's keys are normalised" is a
// property of every layer the Store holds rather than something each consumer
// has to remember. It used to be done when a snapshot was built, which left
// anything reading layers beforehand — the key space handed to a backend's
// Load, or the keys collected for it — normalising again on its own account.
func normalised(layers []Layer) []Layer {
	out := make([]Layer, 0, len(layers))
	for _, l := range layers {
		out = append(out, Layer{Source: l.Source, Values: normaliseValues(l.Values)})
	}

	return out
}

// normaliseValues deep-copies a value map, normalising every key on the way, so
// a layer can be queried with the same normalised paths as the merged view.
func normaliseValues(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))

	for k, v := range m {
		out[normaliseKey(k)] = normaliseNested(v)
	}

	return out
}

func normaliseNested(v any) any {
	if nested, ok := asStringMap(v); ok {
		return normaliseValues(nested)
	}

	if items, ok := v.([]any); ok {
		out := make([]any, len(items))
		for i, item := range items {
			out[i] = normaliseNested(item)
		}

		return out
	}

	// Only a scalar reaches here — maps and sequences are handled above — and a
	// scalar is already a value, so there is nothing to copy.
	return v
}

// leafKeys lists every leaf path the given layers define, which is the key
// space a backend resolves its own input against.
func leafKeys(layers []Layer) []string {
	seen := map[string]bool{}

	var keys []string

	for _, layer := range layers {
		collectKeys(layer.Values, "", seen, &keys)
	}

	return keys
}

// joinPath appends a segment to a dotted path, tolerating either being empty.
//
// One definition, because the rule is the module's contract with dotted keys.
// It existed three times — in the section probe, in the view's scoping, and
// inline in schema construction — so a change to it, such as escaping a literal
// dot in a key, would have had to be made everywhere and would eventually be
// made somewhere.
func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}

	if name == "" {
		return prefix
	}

	return prefix + "." + name
}

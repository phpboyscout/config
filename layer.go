package config

import (
	"fmt"
	"maps"
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
	case SourceEnv, SourceFlag:
		if s.Name == "" {
			return string(s.Kind)
		}

		return string(s.Kind) + ":" + s.Name
	case SourceDefault, SourceOverride:
		return string(s.Kind)
	default:
		return string(s.Kind)
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
	for rawKey, value := range src {
		key := normaliseKey(rawKey)
		path := key

		if prefix != "" {
			path = prefix + "." + key
		}

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
	maps.Copy(out, m)

	for k, v := range out {
		out[k] = cloneValues(v)
	}

	return out
}

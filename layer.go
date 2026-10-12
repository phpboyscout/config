package config

import (
	"fmt"
	"strings"

	"gitlab.com/phpboyscout/go/errors"
)

// ErrKeyCollision is returned when two keys in one layer are the same key once
// folded or stringified, such as `port` beside `Port`, or `1` beside `1.0`. The
// layer is refused rather than loaded with whichever spelling won, because no
// rule for the winner would be anything but a silent choice of value.
var ErrKeyCollision = errors.NewSentinel("config.key_collision", "config: two keys in one layer are the same key")

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
	// A file renders as its bare path, because that is what a user would type
	// to open it. Everything else is qualified by kind, so "9090" can be traced
	// to a variable, a flag, a set of defaults or a remote key rather than
	// merely to "somewhere".
	//
	// Deliberately not a list of known kinds. A backend defined outside this
	// package brings its own, and enumerating kinds meant those sources
	// rendered as the bare kind with their name dropped — so provenance, the
	// one thing this module exists to answer, was least useful for exactly the
	// sources it knows least about.
	name := s.Name
	if s.Kind != SourceFile && s.Kind != "" {
		if name == "" {
			return string(s.Kind)
		}

		name = string(s.Kind) + ":" + name
	}

	// The document index belongs to any multi-document source, not only to a
	// YAML file. A JSONL backend contributes one layer per line and a
	// multi-document source of any format contributes one per document; without
	// this they all render identically and provenance cannot say which one won.
	if s.Document > 0 {
		return fmt.Sprintf("%s#%d", name, s.Document)
	}

	return name
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
			_, replacesSubtree := asStringMap(dst[key])

			dst[key] = value
			origin[path] = source

			// A scalar replacing a subtree takes ownership of the whole path:
			// the keys beneath it are no longer reachable, so their provenance
			// would be a lie. Only then: pruning scans every recorded path, so
			// doing it for each leaf made a large map quadratic (#18), and
			// erased a literal dotted key that merely shares the "path." prefix
			// (#19).
			if replacesSubtree {
				pruneProvenance(origin, path)
			}

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
		// from" are asking the wrong question; DefinedIn answers the right one.
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

// normalised returns layers whose keys carry the module's casing rule, with
// each layer's record of how its source spelled the keys it folded.
//
// Applied where layers enter the Store, so "a Layer's keys are normalised" is a
// property of every layer the Store holds rather than something each consumer
// has to remember. It used to be done when a snapshot was built, which left
// anything reading layers beforehand — the key space handed to a backend's
// Load, or the keys collected for it — normalising again on its own account.
func normalised(layers []Layer, keys keyCase) ([]Layer, []keySpelling, error) {
	out := make([]Layer, 0, len(layers))
	spellings := make([]keySpelling, 0, len(layers))

	for _, l := range layers {
		values, spelling, err := normaliseValues(l.Values, "", keys)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %s %w", ErrKeyCollision, l.Source, err)
		}

		out = append(out, Layer{Source: l.Source, Values: values})
		spellings = append(spellings, spelling)
	}

	return out, spellings, nil
}

// keySpelling records how a layer's source spelled each key whose folded form
// differs, or under which one does, so a write can address an existing key as
// the source spells it (spec 0015 D9). A layer spelled entirely in lower case
// has none.
type keySpelling map[string]spelt

type spelt struct {
	raw   string
	under keySpelling
}

// collision is the detail of an in-layer collision, wrapped with
// ErrKeyCollision and the layer by normalised.
type collision struct{ first, second string }

func (c collision) Error() string {
	first, second := min(c.first, c.second), max(c.first, c.second)

	return fmt.Sprintf("defines both %q and %q", first, second)
}

// normaliseValues deep-copies a value map, normalising every key on the way, so
// a layer can be queried with the same normalised paths as the merged view. It
// refuses two keys that normalise alike, at any depth, whatever their values.
func normaliseValues(m any, at string, keys keyCase) (map[string]any, keySpelling, error) {
	switch typed := m.(type) {
	case map[string]any:
		f := newKeyFolder(at, len(typed), keys)

		for k, v := range typed {
			if err := f.add(k, v); err != nil {
				return nil, nil, err
			}
		}

		return f.out, f.spelling, nil
	case map[any]any:
		f := newKeyFolder(at, len(typed), keys)

		for k, v := range typed {
			if err := f.add(stringKey(k), v); err != nil {
				return nil, nil, err
			}
		}

		return f.out, f.spelling, nil
	}

	return nil, nil, nil
}

// keyFolder accumulates one map's normalised copy for normaliseValues.
type keyFolder struct {
	at       string
	keys     keyCase
	out      map[string]any
	seen     map[string]string
	spelling keySpelling
}

func newKeyFolder(at string, size int, keys keyCase) *keyFolder {
	return &keyFolder{at: at, keys: keys, out: make(map[string]any, size), seen: make(map[string]string, size)}
}

func (f *keyFolder) add(key string, v any) error {
	folded := f.keys.key(key)

	if earlier, dup := f.seen[folded]; dup {
		return collision{first: joinPath(f.at, earlier), second: joinPath(f.at, key)}
	}

	f.seen[folded] = key

	nested, under, err := normaliseNested(v, joinPath(f.at, key), f.keys)
	if err != nil {
		return err
	}

	f.out[folded] = nested

	if key != folded || under != nil {
		if f.spelling == nil {
			f.spelling = keySpelling{}
		}

		f.spelling[folded] = spelt{raw: key, under: under}
	}

	return nil
}

// stringKey renders a map key the way asStringMap does, so the two cannot
// disagree about which keys are the same.
func stringKey(k any) string {
	if s, ok := k.(string); ok {
		return s
	}

	return fmt.Sprint(k)
}

func normaliseNested(v any, at string, keys keyCase) (any, keySpelling, error) {
	switch typed := v.(type) {
	case map[string]any, map[any]any:
		return normaliseValues(typed, at, keys)
	case []any:
		out := make([]any, len(typed))

		for i, item := range typed {
			// A path never addresses into a sequence, so a spelling recorded
			// here could never be used.
			normalisedItem, _, err := normaliseNested(item, fmt.Sprintf("%s[%d]", at, i), keys)
			if err != nil {
				return nil, nil, err
			}

			out[i] = normalisedItem
		}

		return out, nil, nil
	}

	// Only a scalar reaches here — maps and sequences are handled above — and a
	// scalar is already a value, so there is nothing to copy.
	return v, nil, nil
}

// respell rewrites a dotted path into the spelling a layer's source uses, for
// each segment the layer defines. Below the first segment it does not define,
// or below a scalar, the caller's spelling is kept, since nothing in the source
// says otherwise. Spec 0015 D9. Only a folded Store has spellings to restore.
func respell(path string, values map[string]any, spelling keySpelling) string {
	segs := strings.Split(path, ".")

	for i, seg := range segs {
		folded := keysFolded.key(seg)

		value, defined := values[folded]
		if !defined {
			break
		}

		name := spelling[folded]
		if name.raw != "" {
			segs[i] = name.raw
		} else {
			segs[i] = folded
		}

		nested, isMap := value.(map[string]any)
		if !isMap {
			break
		}

		values, spelling = nested, name.under
	}

	return strings.Join(segs, ".")
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

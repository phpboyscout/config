package config

import (
	"strings"
)

// Snapshot is an immutable, fully-resolved view of configuration at a point in
// time: the layers that contributed, the values they merge to, and which layer
// supplied each key.
//
// Immutability is the point. A caller holding a snapshot sees a coherent
// configuration for its whole lifetime, so a sequence of related reads cannot
// straddle a reload and observe a mixture of old and new state. Nothing
// mutates a Snapshot after construction; a reload produces a new one.
type Snapshot struct {
	layers  []Layer
	values  map[string]any
	origin  map[string]Source
	version uint64
}

// newSnapshot folds layers into an immutable snapshot.
//
// The layers are cloned on the way in: a snapshot whose contents can be
// changed through a reference the caller kept is not immutable.
func newSnapshot(version uint64, layers []Layer) *Snapshot {
	owned := make([]Layer, 0, len(layers))

	for _, l := range layers {
		owned = append(owned, Layer{Source: l.Source, Values: cloneMap(l.Values)})
	}

	values, origin := mergeLayers(owned)

	return &Snapshot{
		layers:  owned,
		values:  values,
		origin:  origin,
		version: version,
	}
}

// Version is a monotonically increasing counter, incremented for every
// snapshot the Store emits. It lets a caller tell whether the configuration it
// is looking at has moved on.
func (s *Snapshot) Version() uint64 {
	if s == nil {
		return 0
	}

	return s.version
}

// Get returns the effective value at a dotted path.
func (s *Snapshot) Get(path string) (any, bool) {
	if s == nil {
		return nil, false
	}

	segs := splitPath(path)
	if len(segs) == 0 {
		return nil, false
	}

	found, ok := lookup(s.values, segs)
	if !ok {
		return nil, false
	}

	// A composite is copied on the way out. Returning the snapshot's own map
	// would hand the caller a live reference into published state: mutating what
	// `Get("server")` returned would change what every other reader sees, and
	// inject keys that were never in any source. It is also a data race, and one
	// the race detector cannot see, because the mutation happens in consumer
	// code that the Store never observes.
	//
	// Scalars — the overwhelming majority of reads — cost nothing here.
	return cloneValues(found), true
}

// Has reports whether a path resolves to a value.
//
// A key whose value is an empty map or empty sequence is present: emptiness is
// a value, not an absence, and code may require a parent to exist while it
// holds nothing.
func (s *Snapshot) Has(path string) bool {
	_, ok := s.Get(path)

	return ok
}

// Origin returns the layer that supplied the effective value at a path.
//
// This is the question no merge-eager configuration library can answer, and
// the reason provenance is recorded during the fold rather than reconstructed
// afterwards.
//
// Provenance is defined for leaves — scalars, and containers that are empty.
// A populated subtree is assembled from however many layers contributed to it,
// so naming one source for it would be dishonest; Origin reports not-found and
// [Snapshot.Shadowed] answers what the caller actually wants to know.
func (s *Snapshot) Origin(path string) (Source, bool) {
	if s == nil {
		return Source{}, false
	}

	src, ok := s.origin[normalisePath(path)]

	return src, ok
}

// Shadowed returns every layer that defines a path, in precedence order —
// lowest first, so the last entry is the one in effect.
//
// "Which file do I edit?" and "why is my edit not taking effect?" are the same
// question asked from two directions, and both need the full list rather than
// just the winner.
func (s *Snapshot) Shadowed(path string) []Source {
	if s == nil {
		return nil
	}

	segs := splitPath(path)
	if len(segs) == 0 {
		return nil
	}

	var found []Source

	for _, layer := range s.layers {
		if _, ok := lookup(layer.Values, segs); ok {
			found = append(found, layer.Source)
		}
	}

	return found
}

// Layers returns the contributing layers in precedence order, lowest first.
// The slice is a copy; the layers' values are not exposed for mutation.
func (s *Snapshot) Layers() []Layer {
	if s == nil {
		return nil
	}

	out := make([]Layer, 0, len(s.layers))

	for _, l := range s.layers {
		out = append(out, Layer{Source: l.Source, Values: cloneMap(l.Values)})
	}

	return out
}

// Values returns the merged configuration as a map. The result is a copy, so
// mutating it cannot affect the snapshot.
func (s *Snapshot) Values() map[string]any {
	if s == nil {
		return map[string]any{}
	}

	return cloneMap(s.values)
}

// Keys returns every leaf path in the snapshot, sorted.
//
// Keys whose value is an empty container are included. Excluding them would
// make enumeration disagree with the getters, which is the class of
// inconsistency this design exists to remove.
func (s *Snapshot) Keys() []string {
	if s == nil {
		return nil
	}

	keys := make([]string, 0, len(s.origin))
	for k := range s.origin {
		keys = append(keys, k)
	}

	sortStrings(keys)

	return keys
}

// lookup walks a value tree by path segments.
func lookup(values map[string]any, segs []string) (any, bool) {
	var current any = values

	for _, seg := range segs {
		m, ok := asStringMap(current)
		if !ok {
			return nil, false
		}

		current, ok = m[seg]
		if !ok {
			return nil, false
		}
	}

	return current, true
}

// splitPath splits a dotted path into normalised segments, returning nil for
// a path that is empty or malformed.
func splitPath(path string) []string {
	if strings.TrimSpace(path) == "" {
		return nil
	}

	segs := strings.Split(path, ".")
	out := make([]string, 0, len(segs))

	for _, s := range segs {
		if s == "" {
			return nil
		}

		out = append(out, normaliseKey(s))
	}

	return out
}

// normalisePath applies key normalisation to a whole dotted path.
func normalisePath(path string) string {
	segs := splitPath(path)
	if segs == nil {
		return ""
	}

	return strings.Join(segs, ".")
}

// sortStrings sorts in place without pulling in a dependency for it.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

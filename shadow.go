package config

import "strings"

// Shadow describes one path that more than one layer defines.
//
// It carries paths and sources, never values. That is a decision rather than an
// omission: the report exists to be printed, and the consumer driving it —
// a credential doctor — is forbidden from putting credential values anywhere
// near a log line. A report holding values would put them one `%+v` away.
//
// It is also not a distinction worth having. A literal credential sitting under
// a working environment reference wants removing whether or not it happens to
// match the value in effect: same-value is redundant configuration,
// different-value is dead configuration, and both are a secret in a file. A
// caller that genuinely needs to compare has [Snapshot.Layers].
//
// See the whole-configuration shadow report spec, D7.
type Shadow struct {
	// Path is the leaf this describes, in the terms of whatever produced the
	// report — absolute from a [Snapshot], relative from a scoped [View].
	Path string

	// InEffect is the layer whose value wins, the same one [Snapshot.Origin]
	// reports.
	InEffect Source

	// Shadowed lists the layers beneath it in precedence order, lowest first.
	// It always holds at least one entry, because a path only one layer defines
	// is not in the report at all.
	Shadowed []Source
}

// Shadows reports every path that more than one layer defines, with the layer
// in effect and the layers it shadows, ordered by path.
//
// It answers "which values in this configuration are shadowed, and by what"
// without the caller first knowing which keys to ask about — which is the point,
// because not knowing the keys is usually the situation. "Why is my edit not
// taking effect" and "what in this file is doing nothing" are the same question,
// and [Snapshot.Shadowed] already frames it that way; this simply stops asking
// one path at a time.
//
// # Two deliberate differences from Shadowed
//
// The generalisation is not total, and a reader who knows [Snapshot.Shadowed]
// will reasonably expect it to be:
//
//   - **Leaves only.** Shadowed answers for a populated subtree; this does not
//     report one. Naming the layer in effect for a tree assembled from several
//     would be dishonest, which is exactly why [Snapshot.Origin] refuses it.
//     The report covers what [Snapshot.Keys] returns.
//   - **Only what is actually shadowed.** Shadowed returns a single entry for a
//     path one layer defines; this omits it. Reporting every path with a
//     one-element list would make the common case — a large configuration with
//     a handful of duplicates — need filtering before it could be used.
//
// Ordering is by path so output is stable between runs, which is what a report
// anyone diffs requires.
//
// See the whole-configuration shadow report spec, D1, D2 and D5.
func (s *Snapshot) Shadows() []Shadow {
	if s == nil {
		return nil
	}

	// A path is shadowed only once something sits beneath the winner, so one
	// layer is never enough.
	const shadowedNeeds = 2

	var out []Shadow

	// Keys is sorted and leaves-only, so it supplies both D1 and D5 directly
	// rather than either being re-derived here.
	for _, path := range s.Keys() {
		layers := s.Shadowed(path)
		if len(layers) < shadowedNeeds {
			continue
		}

		// The last entry is the one in effect, which is Shadowed's documented
		// ordering. Copying rather than reslicing keeps a caller from reaching
		// the winner through the shadowed list by extending it.
		shadowed := make([]Source, len(layers)-1)
		copy(shadowed, layers[:len(layers)-1])

		out = append(out, Shadow{
			Path:     path,
			InEffect: layers[len(layers)-1],
			Shadowed: shadowed,
		})
	}

	return out
}

// Shadows reports every shadowed path visible through this view.
//
// A scoped view reports only its own subtree, with paths relative to it — the
// same terms [View.Keys] uses, so a path from the report can be handed straight
// back to [View.Origin] or [View.Get]. An absolute path would resolve to nothing
// through a scoped view, and the mistake would be silent.
//
// See the whole-configuration shadow report spec, D6.
func (v *View) Shadows() []Shadow {
	if v == nil {
		return nil
	}

	all := v.pinned().Shadows()
	if v.prefix == "" {
		return all
	}

	prefix := v.prefix + "."

	var out []Shadow

	for _, s := range all {
		if !strings.HasPrefix(s.Path, prefix) {
			continue
		}

		s.Path = strings.TrimPrefix(s.Path, prefix)
		out = append(out, s)
	}

	return out
}

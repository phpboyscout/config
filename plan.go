package config

import (
	"errors"
	"fmt"
)

// Routing errors.
var (
	// ErrNoWritableLayer is returned when a change has nowhere it can be
	// written: every layer that could hold it is read-only.
	ErrNoWritableLayer = errors.New("config: no writable layer for change")

	// ErrNoChanges is returned when an apply is asked to do nothing.
	ErrNoChanges = errors.New("config: no changes to apply")

	// ErrInvalidPath is returned for a malformed dotted path.
	ErrInvalidPath = errors.New("config: invalid path")

	// ErrInvalidTarget is returned when a target cannot receive what is being
	// asked of it: a decode target that cannot hold values, a layer with no
	// name, or a pinned write target naming no writable source.
	ErrInvalidTarget = errors.New("config: invalid target")

	// ErrSensitiveLeak is returned when a write would land a key a sensitive
	// source defines into a layer that is not sensitive — writing secret-category
	// material into a plain store. Because secrets backends are read-only, a
	// write to a key they own routes down to the next writable layer, typically a
	// file; refusing it is what keeps that safe.
	ErrSensitiveLeak = errors.New("config: refusing to write a sensitive key into a non-sensitive layer")
)

// Change is a single edit to persist.
//
// Setting and removing are one type rather than two methods because they route
// identically and must be applied in one batch — a caller replacing a subtree
// by removing some keys and setting others needs both halves to land together.
type Change struct {
	// Path is the dotted key to change.
	Path string
	// Value is the new value. Ignored when Remove is set.
	Value any
	// Remove deletes the key and its subtree instead of setting it.
	Remove bool
	// Target optionally pins the change to a specific layer, overriding
	// routing. Use when the caller genuinely knows better; routing's default
	// is right far more often.
	Target *Source
}

// ChangeOption adjusts a single [Change] at the point it is written.
//
// A function type rather than an interface, matching StoreOption and
// WatchOption. The set of things a change can carry is closed by Change's own
// fields, so there is nothing for a third party to add that this package could
// still validate.
type ChangeOption func(*Change)

// To pins a change to the layer with the given name, overriding routing.
//
// Reach for it sparingly. Routing's default — edit where the key already lives,
// create where it will be visible — is right far more often than a hand-picked
// target, and pinning is how an edit ends up written somewhere nobody reads.
// [Plan] will say where a change is going, and [Operation.ShadowedBy] will say
// whether anything still outranks it, so a pin can be checked before it lands.
//
// The name is matched against the writable layers that exist, and an unmatched
// one is [ErrInvalidTarget] rather than a silent fall back to routing. Where
// more than one layer carries the name, the highest-precedence one wins: the
// one whose value the caller can read back.
//
// A caller names a layer rather than building a [Source] because only Name and
// Document are matched — a Source built by hand invites filling in Kind or
// Writable and believing they select something.
func To(name string) ChangeOption {
	return func(c *Change) { c.Target = &Source{Name: name} }
}

// ToDocument pins a change to one document of a multi-document file.
//
// Every document in such a file shares a name, so [To] alone cannot address any
// but the first. Separate from To rather than a variadic index on it, because
// To(name, 0) and To(name) would have to mean the same thing while reading as a
// choice.
func ToDocument(name string, doc int) ChangeOption {
	return func(c *Change) { c.Target = &Source{Name: name, Document: doc} }
}

// Set returns a change that assigns a value.
func Set(path string, value any, opts ...ChangeOption) Change {
	return newChange(Change{Path: path, Value: value}, opts)
}

// Remove returns a change that deletes a key and its subtree.
//
// Takes the same options as [Set]: the two route identically, so an option that
// reached only one of them would be a trap.
func Remove(path string, opts ...ChangeOption) Change {
	return newChange(Change{Path: path, Remove: true}, opts)
}

// newChange applies options to a change, so Set and Remove cannot drift on how
// they are handled.
func newChange(c Change, opts []ChangeOption) Change {
	for _, opt := range opts {
		opt(&c)
	}

	return c
}

// Operation is one routed change: what to do, and where it will land.
type Operation struct {
	Change Change
	// Target is the layer the change will be written to.
	Target Source
	// Creates reports whether the key is new to the target layer.
	Creates bool
	// ShadowedBy lists layers that will still outrank the target after the
	// write, so the change will not be visible in the effective configuration.
	//
	// This is not an error — writing the file is what was asked — but a caller
	// that cannot tell the user "written, but the environment still wins" will
	// leave them confused about why nothing happened.
	ShadowedBy []Source
}

// Effective reports whether the change will be visible in the resolved
// configuration once written.
func (o Operation) Effective() bool { return len(o.ShadowedBy) == 0 }

// Plan is a routed set of changes, inspectable before anything is written.
//
// Producing the plan is the expensive part of an apply; executing it is not.
// Exposing it means a caller can show what a save would do — a CLI dry run, or
// a settings screen previewing its own changes — using exactly the routing the
// write will use, rather than an approximation of it.
type Plan struct {
	Operations []Operation
}

// Effective reports whether every operation will be visible once written.
func (p *Plan) Effective() bool {
	for _, op := range p.Operations {
		if !op.Effective() {
			return false
		}
	}

	return true
}

// Targets returns the distinct layers the plan will write to.
func (p *Plan) Targets() []Source {
	seen := map[string]bool{}

	var out []Source

	for _, op := range p.Operations {
		key := op.Target.String()
		if seen[key] {
			continue
		}

		seen[key] = true

		out = append(out, op.Target)
	}

	return out
}

// String renders the plan for a dry run.
func (p *Plan) String() string {
	if len(p.Operations) == 0 {
		return "no changes"
	}

	out := ""

	for _, op := range p.Operations {
		verb := "set"
		if op.Change.Remove {
			verb = "remove"
		}

		line := fmt.Sprintf("%s %s → %s", verb, op.Change.Path, op.Target)

		if op.Creates {
			line += " (new key)"
		}

		if !op.Effective() {
			line += fmt.Sprintf(" — shadowed by %s", op.ShadowedBy[len(op.ShadowedBy)-1])
		}

		out += line + "\n"
	}

	return out
}

// route decides where each change should be written.
//
// The rule is: walk layers in reverse precedence order and take the first
// writable match. An existing key routes to the highest-precedence writable
// layer that defines it; a new key routes to the highest-precedence writable
// layer.
//
// The reason is visibility. Writing to the layer that already wins means the
// value set is the value read back. Writing to the base instead would leave
// the edit immediately shadowed by an overlay, which looks to the user like
// the write silently failed.
func route(snap *Snapshot, targets, order []Source, sensitive map[Source]bool, withheld map[string]Source, changes []Change) (*Plan, error) {
	if len(changes) == 0 {
		return nil, ErrNoChanges
	}

	if snap == nil {
		return nil, ErrNoWritableLayer
	}

	plan := &Plan{Operations: make([]Operation, 0, len(changes))}

	// Indexed once for the whole plan. Shadow detection needs a source's values
	// per change, and a settings screen saving fifty fields would otherwise
	// rebuild the same index fifty times.
	values := indexLayers(snap)

	for _, change := range changes {
		op, err := routeOne(snap, targets, order, values, sensitive, withheld, change)
		if err != nil {
			return nil, err
		}

		plan.Operations = append(plan.Operations, op)
	}

	return plan, nil
}

func routeOne(snap *Snapshot, targets, order []Source, values map[Source]map[string]any, sensitive map[Source]bool, withheld map[string]Source, change Change) (Operation, error) {
	segs := splitPath(change.Path)
	if segs == nil {
		return Operation{}, fmt.Errorf("%w: %q", ErrInvalidPath, change.Path)
	}

	var (
		target  Source
		defines bool
	)

	if change.Target != nil {
		pinned, err := matchTarget(targets, *change.Target)
		if err != nil {
			return Operation{}, err
		}

		target, defines = pinned, layerDefines(snap, pinned, segs)
	} else {
		found, alreadyThere, ok := findTarget(snap, targets, segs)
		if !ok {
			return Operation{}, fmt.Errorf("%w: %s", ErrNoWritableLayer, change.Path)
		}

		target, defines = found, alreadyThere
	}

	// The sensitive-leak guard. A Set to a key a sensitive source owns must not
	// land in a layer that is not itself sensitive: secrets backends are
	// read-only, so such a write routes down to a plain file, materialising
	// secret-category material where it does not belong. A removal writes no
	// value, so it cannot leak one. Pinning the target does not opt out — this is
	// a safety invariant, not a routing preference.
	if !change.Remove && !sensitive[target] {
		if leaker, ok := sensitiveDefiner(values, order, sensitive, segs); ok {
			return Operation{}, fmt.Errorf("%w: %q is defined by the sensitive source %q, so it "+
				"cannot be written to %q", ErrSensitiveLeak, change.Path, leaker, target)
		}

		// A key a sensitive backend holds but a filter is hiding defines
		// nothing in the snapshot, so the check above cannot see it. Refusing
		// here is what stops a deny list quietly turning a secret into a
		// plaintext write.
		if owner, hidden := withheld[change.Path]; hidden {
			return Operation{}, fmt.Errorf("%w: %q is held by the sensitive source %q and hidden "+
				"by a filter, so it cannot be written to %q", ErrSensitiveLeak, change.Path, owner, target)
		}
	}

	// Built once, so the pinned and routed paths cannot drift on how an
	// operation reports what it will do.
	return Operation{
		Change:     change,
		Target:     target,
		Creates:    creates(change, defines),
		ShadowedBy: shadowedAbove(values, order, target, segs),
	}, nil
}

// creates reports whether an operation will add a key that is not there.
//
// A removal never creates anything. Computing it the same way for both meant a
// dry run of removing an absent key read "remove x → app.yaml (new key)",
// which describes the opposite of what would happen.
func creates(change Change, defines bool) bool {
	if change.Remove {
		return false
	}

	return !defines
}

// matchTarget resolves a caller-pinned target against the sources that exist.
//
// Matching by name rather than by whole-struct equality, because a caller
// constructing a Source by hand will not reproduce every field — Writable and
// Document in particular — and an unmatched target used to route silently, plan
// a plausible dry run, and then fail at apply with an internal-invariant error
// blaming the module for the caller's typo.
//
// Walked in reverse precedence, like [findTarget] and for the same reason: more
// than one layer can carry a name — two backends configured over the same path,
// or a nested store agreeing with the store that composes it — and the caller
// naming one means the one whose value they can read. Walking forward returned
// the lowest-precedence copy, which is the one hidden by the others, so a pinned
// write landed where nothing would ever read it back.
func matchTarget(targets []Source, want Source) (Source, error) {
	for i := len(targets) - 1; i >= 0; i-- {
		if targets[i].Name == want.Name && targets[i].Document == want.Document {
			return targets[i], nil
		}
	}

	return Source{}, fmt.Errorf("%w: no writable source named %q", ErrInvalidTarget, want.Name)
}

// findTarget walks the writable targets in reverse precedence for the first
// one that already defines the path, falling back to the highest-precedence
// writable target for a key that is new everywhere.
func findTarget(snap *Snapshot, targets []Source, segs []string) (target Source, defines, ok bool) {
	if len(targets) == 0 {
		return Source{}, false, false
	}

	for i := len(targets) - 1; i >= 0; i-- {
		if layerDefines(snap, targets[i], segs) {
			return targets[i], true, true
		}
	}

	// Nothing defines it, so it is new. The highest-precedence writable target
	// is where it will be visible.
	return targets[len(targets)-1], false, true
}

// layerDefines reports whether a specific layer already holds a path.
func layerDefines(snap *Snapshot, target Source, segs []string) bool {
	for _, layer := range snap.layers {
		if layer.Source != target {
			continue
		}

		if _, ok := lookup(layer.Values, segs); ok {
			return true
		}
	}

	return false
}

// shadowedAbove returns the sources that outrank a target and also define the
// path, so a write to the target will not change the effective value.
//
// The walk is over the precedence order rather than over the snapshot's layers,
// because a target may be a file that does not exist yet. Such a target
// contributes no layer, so searching the layers for it would never find it and
// every write aimed at a new file would be reported as effective — including
// the case the report exists for, where a user sets a value the environment is
// overriding.
func shadowedAbove(values map[Source]map[string]any, order []Source, target Source, segs []string) []Source {
	var (
		found   []Source
		reached bool
	)

	for _, src := range order {
		if src == target {
			reached = true

			continue
		}

		if !reached {
			continue
		}

		if _, ok := lookup(values[src], segs); ok {
			found = append(found, src)
		}
	}

	return found
}

// sensitiveDefiner returns the first sensitive source in the precedence order
// that defines the path, if any. It is what the leak guard consults: a key any
// sensitive source owns must not be written into a non-sensitive layer,
// regardless of where that sensitive source sits in precedence.
func sensitiveDefiner(values map[Source]map[string]any, order []Source, sensitive map[Source]bool, segs []string) (Source, bool) {
	for _, src := range order {
		if !sensitive[src] {
			continue
		}

		if _, ok := lookup(values[src], segs); ok {
			return src, true
		}
	}

	return Source{}, false
}

// indexLayers maps each source to the values it contributed, so shadow
// detection can look a source up rather than rescanning the layer list.
func indexLayers(snap *Snapshot) map[Source]map[string]any {
	if snap == nil {
		return nil
	}

	values := make(map[Source]map[string]any, len(snap.layers))
	for _, layer := range snap.layers {
		values[layer.Source] = layer.Values
	}

	return values
}

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

// Set returns a change that assigns a value.
func Set(path string, value any) Change {
	return Change{Path: path, Value: value}
}

// Remove returns a change that deletes a key and its subtree.
func Remove(path string) Change {
	return Change{Path: path, Remove: true}
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
func route(snap *Snapshot, targets []Source, changes []Change) (*Plan, error) {
	if len(changes) == 0 {
		return nil, ErrNoChanges
	}

	if snap == nil {
		return nil, ErrNoWritableLayer
	}

	plan := &Plan{Operations: make([]Operation, 0, len(changes))}

	for _, change := range changes {
		op, err := routeOne(snap, targets, change)
		if err != nil {
			return nil, err
		}

		plan.Operations = append(plan.Operations, op)
	}

	return plan, nil
}

func routeOne(snap *Snapshot, targets []Source, change Change) (Operation, error) {
	segs := splitPath(change.Path)
	if segs == nil {
		return Operation{}, fmt.Errorf("%w: %q", ErrInvalidPath, change.Path)
	}

	if change.Target != nil {
		return Operation{
			Change:     change,
			Target:     *change.Target,
			Creates:    !layerDefines(snap, *change.Target, segs),
			ShadowedBy: shadowedAbove(snap, *change.Target, segs),
		}, nil
	}

	target, defines, ok := findTarget(snap, targets, segs)
	if !ok {
		return Operation{}, fmt.Errorf("%w: %s", ErrNoWritableLayer, change.Path)
	}

	return Operation{
		Change:     change,
		Target:     target,
		Creates:    !defines,
		ShadowedBy: shadowedAbove(snap, target, segs),
	}, nil
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

// shadowedAbove returns the layers that outrank a target and also define the
// path, so a write to the target will not change the effective value.
func shadowedAbove(snap *Snapshot, target Source, segs []string) []Source {
	var (
		found   []Source
		reached bool
	)

	for _, layer := range snap.layers {
		if layer.Source == target {
			reached = true

			continue
		}

		if !reached {
			continue
		}

		if _, ok := lookup(layer.Values, segs); ok {
			found = append(found, layer.Source)
		}
	}

	return found
}

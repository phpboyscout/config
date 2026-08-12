package config

import "strings"

// Schema is the seam an out-of-module schema implementation plugs into.
//
// This module defines what validating a configuration MEANS and takes no
// position on how a schema is expressed. The tag-derived [Schema] this package
// builds is one implementation; `config-schema` supplies a JSON Schema one,
// with its dialect, its library and its ingestion staying entirely over there.
//
// That split is not tidiness. Twenty-five adapter modules depend on this one and
// each pins its dependency footprint, so a JSON Schema library linked here would
// widen every one of them for a capability most do not use. Behind an interface
// it costs them nothing.
//
// See the composable schema validation spec, D7.
type Schema interface {
	// Validate checks the configuration in snap and appends what it objects to.
	//
	// at is the mount prefix this validator was registered under — "" for the
	// whole configuration. It is passed rather than applied, because a schema
	// written in its own terms has to know where it ended up in order to report
	// a key by the path a user would recognise.
	//
	// Appending rather than returning is deliberate: several validators
	// contribute to one result, and a validator that returned its own would
	// leave the merging — and the attribution — to the caller.
	Validate(snap *Snapshot, at string, r *ValidationResult)
}

// mounted is one registered validator and where it sits.
type mounted struct {
	at     string
	schema Schema

	// required makes the mount point itself mandatory.
	//
	// Without it a contribution is silent when its whole subtree is absent,
	// because a schema constrains a key only when that key is present. A plugin
	// declaring configuration it needs would say nothing at all when given none
	// — the exact case the aggregate report exists to catch. See D9.
	required bool
}

// WithSchemaAt registers a validator against the subtree at prefix.
//
// The contribution is written in its own terms — "enabled", "ttl" — and this
// decides where it lives, so the same schema can be mounted twice at different
// points and a component never hard-codes its own location.
//
// An empty prefix registers against the whole configuration.
//
// Registration is a construction option, never a mutation: a store whose
// validators could change after it was built would let validation move under a
// running configuration. Components self-register before the store is
// constructed, which is already how assets work, so nothing needs to add one
// later.
func WithSchemaAt(prefix string, schema Schema, opts ...MountOption) StoreOption {
	return func(store *Store) {
		if schema == nil {
			return
		}

		m := mounted{at: normalisePath(prefix), schema: schema}
		for _, opt := range opts {
			opt(&m)
		}

		store.validators = append(store.validators, m)
	}
}

// MountOption adjusts how one contribution is mounted.
type MountOption func(*mounted)

// Required makes the mount prefix itself mandatory: the configuration must
// carry something at that path, not merely satisfy the schema if it does.
//
// Reach for it when a component cannot run without configuration. Leave it off
// for a component that is optional — an absent subtree is exactly right for a
// plugin nobody enabled, and exactly wrong for one that is mandatory, which is
// why this is the contribution's choice and not the composer's.
func Required(m *mounted) { m.required = true }

// Validate reports what the registered validators object to in the current
// configuration.
//
// With no arguments it checks everything registered. With one or more paths it
// checks only the contributions mounted at or beneath them, so a component can
// ask about its own branch without hearing about anybody else's.
//
// The result aggregates every contributor, and each failure names the one that
// raised it — a report that cannot say whose expectation was violated is not
// actionable when several components have expectations.
//
// Validation also runs at load, where it is fail-closed: a configuration that
// violates a registered schema never becomes live. This is the same check,
// asked again on demand.
func (s *Store) Validate(paths ...string) *ValidationResult {
	if s == nil {
		return &ValidationResult{}
	}

	return validateAgainst(s.View().Snapshot(), s.validators, paths)
}

// validateAgainst runs the mounts that fall within paths against snap.
func validateAgainst(snap *Snapshot, mounts []mounted, paths []string) *ValidationResult {
	result := &ValidationResult{}

	for _, m := range mounts {
		if !withinAny(m.at, paths) {
			continue
		}

		if !mountPresent(snap, m.at) {
			// D9, in one place rather than in every implementation. A schema
			// constrains a key only when the key is present, so a contribution
			// whose whole subtree is absent would otherwise say nothing at all
			// — right for a plugin nobody enabled, wrong for a mandatory one.
			// The mount decides which it is; implementations do not have to.
			if m.required {
				result.AddError(ValidationError{
					Key:     m.at,
					Message: "required configuration section is missing",
					Hint:    "the component mounted here cannot run without it",
				})
			}

			continue
		}

		m.schema.Validate(snap, m.at, result)
	}

	return result
}

// withinAny reports whether a mount should run for the requested paths. No
// paths means everything; otherwise a mount runs when it sits at or beneath one
// of them.
func withinAny(at string, paths []string) bool {
	if len(paths) == 0 {
		return true
	}

	for _, p := range paths {
		p = normalisePath(p)
		if p == "" || at == p || strings.HasPrefix(at, p+".") {
			return true
		}
	}

	return false
}

// mountPresent reports whether anything is configured at a mount point. A
// mount at the root is always present — there is always a configuration, even
// an empty one.
func mountPresent(snap *Snapshot, at string) bool {
	if at == "" {
		return true
	}

	if snap.Has(at) {
		return true
	}

	// A populated subtree is not itself a key, so Has says no for a path that
	// plainly has configuration beneath it. Keys() is leaves-only for the same
	// reason, so presence has to be asked as "does anything live under here".
	prefix := at + "."
	for _, k := range snap.Keys() {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}

	return false
}

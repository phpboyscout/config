package config

import (
	"fmt"

	"slices"
	"strings"

	"github.com/spf13/cast"
)

// ValidationError contains details about a single validation failure.
type ValidationError struct {
	// Key is the dot-separated config key.
	Key string
	// Message is a human-readable description of the failure.
	Message string
	// Hint is an actionable fix suggestion.
	Hint string
}

func (e ValidationError) String() string {
	s := fmt.Sprintf("%s: %s", e.Key, e.Message)
	if e.Hint != "" {
		s += fmt.Sprintf(" (hint: %s)", e.Hint)
	}

	return s
}

// ValidationResult holds the outcome of schema validation.
type ValidationResult struct {
	Errors   []ValidationError
	Warnings []ValidationError
}

// Valid returns true if no errors were found. Warnings do not affect validity.
func (r *ValidationResult) Valid() bool {
	return len(r.Errors) == 0
}

// Error returns a formatted multi-line error string, or empty string if valid.
func (r *ValidationResult) Error() string {
	if r.Valid() {
		return ""
	}

	var sb strings.Builder

	sb.WriteString("config validation failed:\n")

	for _, e := range r.Errors {
		fmt.Fprintf(&sb, "  %s\n", e.String())
	}

	return sb.String()
}

func (r *ValidationResult) addError(key, message, hint string) {
	r.Errors = append(r.Errors, ValidationError{Key: key, Message: message, Hint: hint})
}

func (r *ValidationResult) addWarning(key, message, hint string) {
	r.Warnings = append(r.Warnings, ValidationError{Key: key, Message: message, Hint: hint})
}

// Validate checks what this view describes against a schema.
//
// A scoped view validates its own subtree, so a schema written for a section
// applies to that section rather than to the whole configuration. The result
// carries errors and warnings separately; check [ValidationResult.Valid].
func (v *View) Validate(schema *Schema) *ValidationResult {
	if v == nil {
		return &ValidationResult{}
	}

	return validateView(v, schema)
}

// validateSnapshot checks a snapshot against a schema.
//
// It validates the resolved configuration rather than any single layer,
// because a layer can be legitimately incomplete on its own: a base file may
// omit a key that an overlay supplies, and rejecting that would reject a
// perfectly valid setup.
func validateSnapshot(snap *Snapshot, schema *Schema) *ValidationResult {
	if snap == nil {
		return &ValidationResult{}
	}

	return validateView(NewView(snap), schema)
}

// validateView checks whatever the view describes against a schema.
//
// Going through the view rather than the snapshot is what makes a scoped view
// validate its own subtree: a schema written for a section is meant to be
// applied to that section, and reading the snapshot directly would silently
// judge the whole configuration against it instead.
func validateView(view *View, schema *Schema) *ValidationResult {
	result := &ValidationResult{}

	if schema == nil || view == nil {
		return result
	}

	for key, field := range schema.fields {
		validateField(key, field, view.Get(key), view.Has(key), result)
	}

	detectUnknownKeys(configuredKeys(view), schema.fields, result, schema.strict)

	return result
}

func validateField(key string, field FieldSchema, value any, present bool, result *ValidationResult) {
	// Required means the key is present and carries a value. Presence is the
	// test for every type except a string, because false and 0 are deliberate
	// values: judging required by zero-ness rejected a boolean set to false, so
	// an operator turning a feature off was told the setting was missing and the
	// application refused to start because they had configured it.
	//
	// An empty string is the exception. YAML writes an absent value as the empty
	// string, so the two are indistinguishable at this layer, and a required
	// credential that is present but blank is not configured in any useful
	// sense.
	if field.Required {
		if !present || isBlankString(value) {
			envKey := envName(key)
			result.addError(key, "required field is missing",
				fmt.Sprintf("add %s to your config file or set the %s environment variable", key, envKey))

			return
		}
	}

	if value == nil {
		return
	}

	// Check type
	if field.Type != "" && !typeMatches(field.Type, value) {
		result.addError(key, fmt.Sprintf("expected type %s but got %T", field.Type, value),
			fmt.Sprintf("ensure %s has a value of type %s", key, field.Type))
	}

	// Check enum
	if len(field.Enum) > 0 {
		strVal := fmt.Sprintf("%v", value)

		allowed := make([]string, len(field.Enum))
		for i, e := range field.Enum {
			allowed[i] = fmt.Sprintf("%v", e)
		}

		if !slices.Contains(allowed, strVal) {
			result.addError(key, fmt.Sprintf("value %q is not allowed", strVal),
				fmt.Sprintf("allowed values: %s", strings.Join(allowed, ", ")))
		}
	}
}

// isBlankString reports an empty string, which YAML uses for an absent value.
func isBlankString(v any) bool {
	s, ok := v.(string)

	return ok && s == ""
}

func typeMatches(expected string, value any) bool {
	switch expected {
	case "string":
		return isString(value)
	case "int":
		return isInt(value)
	case "float64":
		return isFloat(value)
	case "bool":
		return isBool(value)
	case "duration":
		return isDuration(value)
	default:
		return true
	}
}

func isString(v any) bool {
	// Deliberately not cast.ToStringE, which accepts nearly anything. A schema
	// saying "string" means the value is textual, not that it could be rendered
	// as text.
	_, ok := v.(string)

	return ok
}

// The remaining checks defer to the same conversions the accessors use, because
// validation and the accessors must agree: a value GetInt reads happily is an
// int as far as this module is concerned, and validation calling it otherwise
// is validation being wrong.
//
// Hand-rolled tables had already drifted from that in three places — an
// unsigned integer failed both isInt and isDuration while both accessors read
// it, and "500" from an environment variable failed isDuration while
// GetDuration read it as nanoseconds.
func isInt(v any) bool {
	_, err := cast.ToIntE(v)

	return err == nil
}

func isFloat(v any) bool {
	_, err := cast.ToFloat64E(v)

	return err == nil
}

func isBool(v any) bool {
	// Narrowed: cast reads any number as a bool, which is a coercion a schema
	// declaring "bool" should not accept from a config file.
	switch v.(type) {
	case bool:
		return true
	case string:
		_, err := cast.ToBoolE(v)

		return err == nil
	default:
		return false
	}
}

func isDuration(v any) bool {
	_, err := cast.ToDurationE(v)

	return err == nil
}

// configuredKeys lists the keys a schema should police: those someone wrote
// into a configuration source, not those the ambient environment happened to
// supply.
//
// Strict mode exists to catch a typo in a config file. An orchestrator setting
// an unrelated prefixed variable — APP_VERSION, APP_HOME — would otherwise map
// into the key space and be rejected as an unknown key, so a deployment
// platform could stop an application starting by exporting a variable that has
// nothing to do with it.
func configuredKeys(view *View) []string {
	all := view.Keys()
	out := make([]string, 0, len(all))

	for _, key := range all {
		// Shadowed, not Origin. Origin names only the layer that won, so a typo
		// genuinely written into a config file stopped being reported the moment
		// anyone exported a variable that happened to override it.
		if !slices.ContainsFunc(view.Shadowed(key), Source.Authored) {
			continue
		}

		out = append(out, key)
	}

	return out
}

func detectUnknownKeys(allKeys []string, fields map[string]FieldSchema, result *ValidationResult, strict bool) {
	for _, key := range allKeys {
		if _, known := fields[key]; known {
			continue
		}

		if isKnownKeyPrefix(key, fields) {
			continue
		}

		msg := "unknown configuration key"
		hint := fmt.Sprintf("check for typos; remove %s if it is not needed", key)

		if strict {
			result.addError(key, msg, hint)
		} else {
			result.addWarning(key, msg, hint)
		}
	}
}

// isKnownKeyPrefix returns true if key is a parent prefix of any known schema field.
func isKnownKeyPrefix(key string, fields map[string]FieldSchema) bool {
	prefix := key + "."

	for known := range fields {
		if strings.HasPrefix(known, prefix) {
			return true
		}
	}

	return false
}

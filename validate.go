package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
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
	_, ok := v.(string)

	return ok
}

func isInt(v any) bool {
	switch val := v.(type) {
	case int, int8, int16, int32, int64:
		return true
	case string:
		// A layer that can only carry strings still supplies real values. The
		// environment and command-line flags are strings by nature — the
		// operating system offers nothing else — so a schema declaring a field
		// an int must accept "9090" from them exactly as it accepts 9090 from a
		// file. What matters is whether the value denotes the declared type,
		// not how the layer that carried it happened to encode it.
		//
		// The rule underneath: validation and the accessors must agree. GetInt
		// reads such a value happily, so validation calling it the wrong type
		// would be validation being wrong.
		_, err := strconv.Atoi(val)

		return err == nil
	default:
		return false
	}
}

func isFloat(v any) bool {
	switch val := v.(type) {
	case float32, float64:
		return true
	case int, int8, int16, int32, int64:
		// A whole number is a legitimate float. Rejecting 1 for a field
		// declared float64 would fail a document nobody would think to write
		// as 1.0.
		return true
	case string:
		// See isInt: a string-only layer still supplies real values.
		_, err := strconv.ParseFloat(val, 64)

		return err == nil
	default:
		return false
	}
}

func isBool(v any) bool {
	switch val := v.(type) {
	case bool:
		return true
	case string:
		// See isInt. ParseBool accepts the spellings a person actually types
		// into an environment variable: true/false, 1/0, t/f, T/TRUE and so on.
		_, err := strconv.ParseBool(val)

		return err == nil
	default:
		return false
	}
}

func isDuration(v any) bool {
	switch val := v.(type) {
	case time.Duration:
		return true
	case string:
		_, err := time.ParseDuration(val)

		return err == nil
	case int, int8, int16, int32, int64, float32, float64:
		// A bare number is a duration in nanoseconds, which is how the accessor
		// reads it. Rejecting it here would have validation and GetDuration
		// disagree about the same value — the inconsistency the string cases
		// above were added to remove, left in place for numbers.
		return true
	default:
		return false
	}
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
		if src, ok := view.Origin(key); ok && (src.Kind == SourceEnv || src.Kind == SourceFlag) {
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

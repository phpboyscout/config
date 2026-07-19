package config

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestContainer builds a view over a single in-memory config file.
func newTestContainer(t *testing.T, yaml string) *View {
	t.Helper()

	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/config.yaml", []byte(yaml), 0o644))

	s, err := NewStore(context.Background(), WithFiles(fs, "/config.yaml"))
	require.NoError(t, err)

	return s.View()
}

func TestValidate_RequiredFieldPresent(t *testing.T) {
	t.Parallel()

	c := newTestContainer(t, `
github:
  token: "abc123"
`)

	schema, err := NewSchema(WithStructSchema(testAppConfig{}))
	require.NoError(t, err)

	result := c.Validate(schema)
	assert.True(t, result.Valid())
	assert.Empty(t, result.Errors)
}

func TestValidate_RequiredFieldViolation(t *testing.T) {
	// Not parallel — t.Setenv modifies process environment
	t.Setenv("GITHUB_TOKEN", "")

	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "missing key",
			yaml: `
log:
  level: info
`,
		},
		{
			name: "empty value",
			yaml: `
github:
  token: ""
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestContainer(t, tt.yaml)

			schema, err := NewSchema(WithStructSchema(testAppConfig{}))
			require.NoError(t, err)

			result := c.Validate(schema)
			assert.False(t, result.Valid())

			var found bool

			for _, e := range result.Errors {
				if e.Key == "github.token" {
					found = true
					assert.Contains(t, e.Message, "required")
				}
			}

			assert.True(t, found, "should have error for github.token")
		})
	}
}

func TestValidate_EnumValid(t *testing.T) {
	t.Parallel()

	c := newTestContainer(t, `
github:
  token: "abc"
log:
  level: debug
  format: json
`)

	schema, err := NewSchema(WithStructSchema(testAppConfig{}))
	require.NoError(t, err)

	result := c.Validate(schema)
	assert.True(t, result.Valid())
}

func TestValidate_EnumInvalid(t *testing.T) {
	t.Parallel()

	c := newTestContainer(t, `
github:
  token: "abc"
log:
  level: verbose
`)

	schema, err := NewSchema(WithStructSchema(testAppConfig{}))
	require.NoError(t, err)

	result := c.Validate(schema)
	assert.False(t, result.Valid())

	var found bool

	for _, e := range result.Errors {
		if e.Key == "log.level" {
			found = true
			assert.Contains(t, e.Message, "not allowed")
			assert.Contains(t, e.Hint, "debug")
		}
	}

	assert.True(t, found, "should have error for log.level enum violation")
}

func TestValidate_UnknownKey_Warning(t *testing.T) {
	t.Parallel()

	c := newTestContainer(t, `
github:
  token: "abc"
unknown_key: value
`)

	schema, err := NewSchema(WithStructSchema(testAppConfig{}))
	require.NoError(t, err)

	result := c.Validate(schema)
	// Non-strict: unknown keys produce warnings, not errors
	assert.True(t, result.Valid())
	assert.NotEmpty(t, result.Warnings)

	var found bool

	for _, w := range result.Warnings {
		if w.Key == "unknown_key" {
			found = true
		}
	}

	assert.True(t, found, "should warn about unknown_key")
}

func TestValidate_UnknownKey_Strict(t *testing.T) {
	t.Parallel()

	c := newTestContainer(t, `
github:
  token: "abc"
unknown_key: value
`)

	schema, err := NewSchema(WithStructSchema(testAppConfig{}), WithStrictMode())
	require.NoError(t, err)

	result := c.Validate(schema)
	assert.False(t, result.Valid())

	var found bool

	for _, e := range result.Errors {
		if e.Key == "unknown_key" {
			found = true
			assert.Contains(t, e.Message, "unknown")
		}
	}

	assert.True(t, found, "should error on unknown_key in strict mode")
}

func TestValidate_NestedFields(t *testing.T) {
	t.Parallel()

	type nested struct {
		Database struct {
			Host string `config:"host" validate:"required"`
			Port int    `config:"port"`
		}
	}

	c := newTestContainer(t, `
database:
  host: "localhost"
  port: 5432
`)

	schema, err := NewSchema(WithStructSchema(nested{}))
	require.NoError(t, err)

	result := c.Validate(schema)
	assert.True(t, result.Valid())
}

func TestValidationResult_Error(t *testing.T) {
	t.Parallel()

	result := &ValidationResult{
		Errors: []ValidationError{
			{Key: "a.b", Message: "missing", Hint: "add it"},
			{Key: "c.d", Message: "wrong type", Hint: "use int"},
		},
	}

	errStr := result.Error()
	assert.Contains(t, errStr, "config validation failed:")
	assert.Contains(t, errStr, "a.b: missing")
	assert.Contains(t, errStr, "c.d: wrong type")
}

// A Store built with a schema validates before publishing, so an application
// never starts on configuration it has said it cannot use.
func TestStore_WithSchema_Valid(t *testing.T) {
	t.Parallel()

	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/config.yaml", []byte(`
github:
  token: "secret"
log:
  level: info
`), 0o644))

	schema, err := NewSchema(WithStructSchema(testAppConfig{}))
	require.NoError(t, err)

	s, err := NewStore(context.Background(), WithFiles(fs, "/config.yaml"), WithSchema(schema))
	require.NoError(t, err)

	assert.Equal(t, "secret", s.View().GetString("github.token"))
}

func TestStore_WithSchema_Invalid(t *testing.T) {
	t.Parallel()

	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/config.yaml", []byte("log:\n  level: info\n"), 0o644))

	schema, err := NewSchema(WithStructSchema(testAppConfig{}))
	require.NoError(t, err)

	s, err := NewStore(context.Background(), WithFiles(fs, "/config.yaml"), WithSchema(schema))
	require.ErrorIs(t, err, ErrInvalidConfig)
	assert.Contains(t, err.Error(), "github.token")

	// The Store is returned despite the error. A caller doing the usual
	// `if err != nil { return }` still fails fast, but a tool whose job is to
	// repair configuration needs something to repair it through — returning nil
	// would make one missing key unfixable by the surface designed to fix it
	// (D15).
	require.NotNil(t, s)
	assert.Equal(t, "info", s.View().GetString("log.level"))
}

// Validation applies to the resolved configuration, not to any single layer:
// a base that omits a required key is valid when an overlay supplies it.
func TestStore_WithSchema_ValidatesTheResolvedConfiguration(t *testing.T) {
	t.Parallel()

	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/base.yaml", []byte("log:\n  level: info\n"), 0o644))
	require.NoError(t, afero.WriteFile(fs, "/over.yaml", []byte("github:\n  token: from-overlay\n"), 0o644))

	schema, err := NewSchema(WithStructSchema(testAppConfig{}))
	require.NoError(t, err)

	s, err := NewStore(context.Background(),
		WithFiles(fs, "/base.yaml", "/over.yaml"), WithSchema(schema))
	require.NoError(t, err, "neither file is complete alone, but together they are")
	assert.Equal(t, "from-overlay", s.View().GetString("github.token"))
}

// A reload that fails validation leaves the previous configuration live:
// last-known-good beats values the application has said it cannot use.
func TestStore_WithSchema_FailedReloadRetainsLastKnownGood(t *testing.T) {
	t.Parallel()

	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/config.yaml",
		[]byte("github:\n  token: good\nlog:\n  level: info\n"), 0o644))

	schema, err := NewSchema(WithStructSchema(testAppConfig{}))
	require.NoError(t, err)

	s, err := NewStore(context.Background(), WithFiles(fs, "/config.yaml"), WithSchema(schema))
	require.NoError(t, err)

	var reported error

	s.OnReloadError(func(e error) { reported = e })

	require.NoError(t, afero.WriteFile(fs, "/config.yaml", []byte("log:\n  level: info\n"), 0o644))

	require.ErrorIs(t, s.Reload(context.Background()), ErrInvalidConfig)
	require.ErrorIs(t, reported, ErrInvalidConfig, "a rejection must reach the error channel")
	assert.Equal(t, "good", s.View().GetString("github.token"), "the last known good value stands")
}

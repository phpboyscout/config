# Validate configuration

Catch a bad config at load time — a missing required key, a typo, an out-of-range enum —
instead of at a call site three layers deep. A typo like `server.prot` otherwise produces
a silent zero value, discovered only when something fails at runtime.

## Describe the shape with tags

```go
type AppConfig struct {
	Server struct {
		Port int    `config:"server.port" default:"8080"`
		Host string `config:"server.host"`
	}
	Log struct {
		Level string `config:"log.level" enum:"debug,info,warn,error" default:"info"`
	}
	GitHub struct {
		Token string `config:"github.token" validate:"required"`
	}
	Internal string `config:"-"` // never validated
}
```

| Tag | Effect |
|---|---|
| `config:"a.b"` | maps the field to a dot-separated config key |
| `validate:"required"` | fails when the key is absent **or zero-valued** |
| `enum:"a,b,c"` | fails when the value is not in the allowed set |
| `default:"x"` | **appears in error hints — does not set the value** |
| `config:"-"` | skips the field entirely |

!!! warning "Validation reads; it never writes"
    The `default:` tag is **documentation and hint text only**. Validation does not
    inject defaults into the container. Defaults belong in exactly one place — your
    shipped/embedded default config — because a second source would inevitably drift
    from the first. Don't define a default in both.

!!! note "Two different struct tags"
    Validation reads **`config:`**; [section decoding](typed-sections.md) reads
    **`mapstructure:`**. They are separate mechanisms and a struct may carry both.

## Validate in one call

`ValidateStruct[T]` derives the schema from `T` and validates in one step. It takes the
`Containable` **interface**, so callers never need the concrete type:

```go
if err := config.ValidateStruct[AppConfig](cfg); err != nil {
	return fmt.Errorf("configuration invalid: %w", err)
}
```

Do this in your command's `RunE`/`PersistentPreRunE` — after config is loaded.

Schemas derived without options are **cached per type** (`reflect.Type` → schema), so
repeated calls skip the reflection. Calls that pass options build fresh, because options
change the result. (The cache never evicts; for the handful of config structs a tool
defines that is a non-issue.)

## When you need the result itself

`SchemaOf[T]` gives you the `*Schema`, and `Validate` returns a `ValidationResult` with
errors and warnings separated:

```go
schema, err := config.SchemaOf[AppConfig]()
if err != nil {
	return err
}

result := cfg.Validate(schema)
if !result.Valid() { // true iff there are zero ERRORS; warnings don't affect validity
	return errors.New(result.Error())
}
for _, w := range result.Warnings {
	slog.Default().Warn("config", "key", w.Key, "issue", w.Message)
}
```

## Errors are meant to be actionable

Each `ValidationError` carries `Key`, `Message`, and an actionable `Hint` (the env-var
name is derived from the key automatically):

```
config validation failed:
  myfeature.api_key: required field is missing (hint: add myfeature.api_key to your config file or set the MYFEATURE_API_KEY environment variable)
  myfeature.log_level: value "verbose" is not allowed (hint: allowed values: debug, info, warn, error)
```

That combination — key, cause, fix — is the quality bar for any new check.

## Unknown keys: warnings by default

An unrecognised key produces a **warning**, not an error. This is deliberate: in a merged
config that several packages contribute to, an unknown key is usually *someone else's*
key, and failing on it would make the file impossible to share.

When you own a user-facing file and want typos caught hard, opt into strict mode:

```go
err := config.ValidateStruct[AppConfig](cfg, config.WithStrictMode())
// now `myfeature.endpont` is an error, not a warning
```

## Gate hot-reloads on the schema

Attach a schema and every reload is validated against it — a candidate that fails is
rejected and the last-known-good config is retained, so observers never see an invalid
config:

```go
cfg, err := config.LoadFilesContainerWithSchema(afero.NewOsFs(), schema,
	config.WithConfigFiles("config.yaml"))

// or attach later
cfg.SetSchema(schema)
```

See [hot-reload safety](../explanation/hot-reload-safety.md).

## Validate your own slice, not the world

Each package should validate **its own** keys with its own struct. There is deliberately
no global schema: a central one would have to know which features are active in a given
build, and would couple otherwise-independent packages together. See
[Why a wrapper](../explanation/why-a-wrapper.md#decentralised-validation).

## Testing validation

No disk needed — build a container from an in-memory filesystem and assert on the error:

```go
fs := afero.NewMemMapFs()
require.NoError(t, afero.WriteFile(fs, "/config.yaml", []byte("log:\n  level: verbose\n"), 0o644))

cfg, err := config.LoadFilesContainer(fs, config.WithConfigFiles("/config.yaml"))
require.NoError(t, err)

err = config.ValidateStruct[AppConfig](cfg)
require.Error(t, err)
assert.Contains(t, err.Error(), "log.level")
```

## Related

- [Use typed sections](typed-sections.md)
- [Hot-reload safety](../explanation/hot-reload-safety.md)
- [Why a wrapper over Viper](../explanation/why-a-wrapper.md)

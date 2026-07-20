# Validate configuration

Catch a bad configuration at load time — a missing required key, a typo, a value outside
an allowed set — instead of at a call site three layers deep. A typo like `server.prot`
otherwise produces a silent zero value, discovered only when something fails at runtime.

Validation always runs against the **resolved** configuration, never a single layer. A
base file that omits a key its overlay supplies is legitimate, and judging layers
individually would reject a perfectly correct setup.

!!! note "Assumed setup"
    Snippets below use a `store`, a `ctx` and the imports named here:

    ```go
    import (
        "context"
        "errors"
        "fmt"
        "log/slog"

        "gitlab.com/phpboyscout/go/config"
    )
    ```

    ```go
    ctx := context.Background()

    store, err := config.NewStore(ctx,
        config.WithFiles(config.OS(), "/etc/mytool/config.yaml"),
    )
    if err != nil {
        return err
    }
    ```

    The store gains a schema in [attach the schema to the store](#attach-the-schema-to-the-store);
    until then the examples validate a view explicitly.

## Describe the shape with tags

A schema is derived from a tagged struct:

```go
type AppConfig struct {
	Server struct {
		Port int    `config:"server.port" default:"8080"`
		Host string `config:"server.host" validate:"required"`
	}
	Log struct {
		Level string `config:"log.level" enum:"debug,info,warn,error" default:"info"`
	}
	Internal string `config:"-"` // never validated
}
```

| Tag | Effect |
|---|---|
| `config:"a.b"` | maps the field to a dot-separated configuration key |
| `validate:"required"` | fails when the key is **absent** — see below |
| `enum:"a,b,c"` | fails when the value is not one of the listed values |
| `default:"x"` | **appears in documentation and error hints — does not set the value** |
| `description:"…"` | human-readable description carried on the field schema |
| `config:"-"` | skips the field entirely |

!!! info "`required` means present, not non-zero"
    A `bool` set to `false` and an `int` set to `0` are configured values, and
    `required` accepts both. Judging by zero-ness would reject an operator who
    deliberately turned a feature off, telling them the setting was missing.

    Strings are the one exception: a required string that is present but empty fails,
    because YAML writes an absent value as the empty string and the two cannot be told
    apart here. If you need a non-zero number, express that as a check of your own
    rather than as `required` — see [validate your own slice](#validate-your-own-slice-not-the-world).

A nested struct without a `config` tag is walked, and its lower-cased field name becomes a
key prefix for any child tag that contains no dot of its own. So `Server.Port` tagged
`config:"port"` yields the key `server.port`, exactly as the fully-qualified form above
would. Both spellings work; pick one and stay with it.

!!! warning "Validation reads; it never writes"
    The `default:` tag is hint text only. Validation does not inject defaults into the
    configuration. Defaults belong in exactly one place — your shipped or embedded
    defaults layer, added with `WithReaders` — because a second source of defaults will
    drift from the first. Do not define a default in both.

!!! note "Two different struct tags"
    Validation reads **`config:`**; [section decoding](typed-sections.md) reads
    **`mapstructure:`**. They are separate mechanisms, and one struct may carry both.

## Validate in one call

`ValidateStruct[T]` derives the schema from `T` and validates a `*View` in one step:

```go
if err := config.ValidateStruct[AppConfig](store.View()); err != nil {
	return fmt.Errorf("configuration invalid: %w", err)
}
```

The returned error wraps `ErrInvalidConfig`, so `errors.Is` works on it.

Schemas derived without options are **cached per type**, so repeated calls skip the
reflection. Calls that pass options build fresh, because an option can change the result.

## Build a schema you can reuse

`SchemaOf[T]` gives you the `*Schema` itself, which is what you attach to a store:

```go
schema, err := config.SchemaOf[AppConfig]()
if err != nil {
	return err
}
```

`NewSchema` is the longer form of the same thing, for when you want to compose options
explicitly:

```go
schema, err := config.NewSchema(config.WithStructSchema(AppConfig{}))
if err != nil {
	return err
}
```

A schema with no fields is refused with `ErrEmptySchema`. A schema that constrains
nothing would validate everything, which is worse than having none at all: you would
believe your configuration was checked when it was not.

## When you want warnings as well as errors

`View.Validate` returns a `ValidationResult` with the two kept apart:

```go
result := store.View().Validate(schema)

if !result.Valid() { // true only when there are zero errors; warnings do not affect validity
	return errors.New(result.Error())
}

for _, w := range result.Warnings {
	slog.Default().Warn("config", "key", w.Key, "issue", w.Message)
}
```

Each `ValidationError` carries `Key`, `Message` and an actionable `Hint`:

```
config validation failed:
  server.host: required field is missing (hint: add server.host to your config file or set the SERVER_HOST environment variable)
  log.level: value "verbose" is not allowed (hint: allowed values: debug, info, warn, error)
```

Key, cause, fix. That is the quality bar for any check you add.

The environment variable named in a hint is derived mechanically from the key by
upper-casing it and replacing dots with underscores. It does **not** include the prefix
you passed to `WithEnv`, so if you use one, say so in your own surrounding message.

## Unknown keys are warnings by default

An unrecognised key produces a warning, not an error. That is deliberate: in a merged
configuration several packages contribute to, an unknown key is usually *someone else's*
key, and failing on it would make the file impossible to share. A key that is a parent of
a known field is never reported — `server` is fine when `server.port` is in the schema.

When you own the whole file and want typos caught hard, opt into strict mode:

```go
// `server.prot` is now an error rather than a warning
if err := config.ValidateStruct[AppConfig](store.View(), config.WithStrictMode()); err != nil {
	return err
}
```

Or on a schema you build once and attach to the store:

```go
schema, err := config.NewSchema(
	config.WithStructSchema(AppConfig{}),
	config.WithStrictMode(),
)
if err != nil {
	return err
}
```

## Attach the schema to the store

`WithSchema` validates every candidate configuration before it is published — the first
load, every reload, and every write:

```go
store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/mytool/config.yaml"),
	config.WithEnv("MYTOOL"),
	config.WithSchema(schema),
)
```

A reload that fails validation is rejected and the last known good configuration stays
live, so observers never see an invalid configuration. See
[Hot-reload safety](../explanation/hot-reload-safety.md).

## An invalid configuration is still repairable

`NewStore` returns a **usable** store alongside `ErrInvalidConfig` when the configuration
loads and parses but violates the schema. That asymmetry is deliberate. The ordinary
check still fails fast:

```go
store, err := config.NewStore(ctx, opts...)
if err != nil {
	return err // a service refuses to start on a bad config, exactly as you want
}
```

But a tool whose job is to *repair* configuration needs a store to repair it through.
Returning nil would make one missing required key unfixable by the very surface designed
to fix it. Such a caller checks for the specific error and carries on:

```go
store, err := config.NewStore(ctx, opts...)
if err != nil && !errors.Is(err, config.ErrInvalidConfig) {
	return err // genuinely unusable: no sources, unreadable, or unparseable
}

// store is non-nil here, and can be read from and written through.
_, err = store.Apply(ctx, config.Set("server.host", "localhost"))
```

Every other failure — no sources, a source that cannot be read, one that cannot be parsed
— still returns nil, because there is no configuration to hand back.

## Writes are validated too, but only for what they break

A write against a schema-carrying store is validated by building the candidate snapshot it
would produce — including the environment and flag layers — and validating that. It is the
same construction path a reload uses, so pre-write validation and the reload it causes
cannot disagree.

The rule is narrower than "the result must be valid":

> **`Apply` rejects only the violations your change introduces.** Violations that were
> already there do not block the write, and are reported as pre-existing.

```go
_, err := store.Apply(ctx, config.Set("log.level", "verbose"))
if errors.Is(err, config.ErrInvalidConfig) {
	fmt.Println(err)
}
```

```
config: configuration is not valid: this change would make the configuration invalid:
  log.level: value "verbose" is not allowed (hint: allowed values: debug, info, warn, error)

the configuration was already invalid before this change, and these are unaffected by it:
  server.port: expected type int but got string (hint: ensure server.port has a value of type int)
```

Both halves earn their place. Validating the candidate outright would lock an
already-invalid configuration against edits, which is the same repairability problem
`NewStore` solves at construction. Suppressing the pre-existing violations entirely would
hide from the user that their configuration is broken in ways their edit did not cause.
Reporting them without the disclaimer would send someone off debugging a change that was
perfectly fine.

A rejected write modifies nothing. Violations are matched on key plus message, so a change
that swaps one failure for a different one on the same key counts as introducing something
new — which it has.

## The type check sees the layer's own type

A schema field's `Type` is compared against the resolved value's actual Go type. Values
that come from a file are typed by the YAML parser; values that come from an environment
variable or a changed flag are **strings**, because that is all those layers hold.

So an `int` field supplied by `MYTOOL_SERVER_PORT` or `--port` fails its type check:

```
server.port: expected type int but got string
```

The reading path is unaffected — `GetInt` coerces — but validation is not a reading path.
If a key is routinely supplied by the environment or a flag, either leave it out of the
typed schema or declare it as a string.

## Validate your own slice, not the world

Each package should validate its own keys with its own struct. There is deliberately no
global schema: a central one would have to know which features are active in a given
build, and would couple otherwise-independent packages together.

This is also where checks a schema cannot express belong — anything relational, conditional,
or needing a non-zero number rather than merely a present one:

```go
type Pool struct {
	Min int `config:"pool.min" validate:"required"`
	Max int `config:"pool.max" validate:"required"`
}

// validatePool covers what struct tags cannot: `required` means present, so a
// deliberate 0 passes it, and no tag can say "min must not exceed max".
func validatePool(view *config.View) error {
	section, err := config.UnmarshalSection[Pool](view, "")
	if err != nil {
		return err
	}

	pool := section.Value

	if pool.Max <= 0 {
		return fmt.Errorf("pool.max must be greater than zero, got %d", pool.Max)
	}

	if pool.Min > pool.Max {
		return fmt.Errorf("pool.min (%d) must not exceed pool.max (%d)", pool.Min, pool.Max)
	}

	return nil
}
```

Call it after the schema has run, so the cheap structural checks report first and your
rule only sees values that are already the right shape.

For a typed section that stays current across reloads, `WithSectionValidator` validates
each decoded snapshot before it is published, and a snapshot that fails keeps the previous
one in place. See [Use typed sections](typed-sections.md).

## Testing validation

No disk needed. Build a store over an in-memory filesystem and assert on the error:

```go
fsys, err := config.Dir(t.TempDir())
require.NoError(t, err)
require.NoError(t, fsys.WriteFile("/config.yaml",
	[]byte("server:\n  host: localhost\nlog:\n  level: verbose\n"), 0o644))

store, err := config.NewStore(ctx, config.WithFiles(fsys, "/config.yaml"))
require.NoError(t, err)

err = config.ValidateStruct[AppConfig](store.View())
require.Error(t, err)
assert.ErrorIs(t, err, config.ErrInvalidConfig)
assert.Contains(t, err.Error(), "log.level")
```

## Related

- [Load & merge configuration](load-and-merge.md) — building a store and ordering sources
- [Use typed sections](typed-sections.md) — decode and validate one subtree
- [React to changes with hot-reload](hot-reload.md)
- [Hot-reload safety](../explanation/hot-reload-safety.md) — why a rejected reload changes nothing
- [Write configuration](write-config.md) — planning, routing and applying a change

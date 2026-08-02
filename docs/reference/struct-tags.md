---
title: Struct tags
description: Every struct tag config reads — for validation and for decoding — which are honoured, and which are silently ignored.
tags: [reference, tags, validation, decoding]
---

# Struct tags

Two separate mechanisms in this module read struct tags, and they read **different tags**.
Mixing them up is the most common way a field ends up silently at its zero value.

| Mechanism | Reads | Entry points |
|---|---|---|
| Schema validation | `config`, `validate`, `enum`, `default`, `description` | `SchemaOf[T]`, `WithStructSchema`, `ValidateStruct[T]` |
| Section decoding | `mapstructure`, then `yaml`, then `json` | `View.Unmarshal`, `View.UnmarshalKey`, `Value[T]`, `UnmarshalSection[T]`, `ObserveSection[T]` |

One struct may carry both sets.

## Validation tags

```go
type AppConfig struct {
	Server struct {
		Port int    `config:"server.port" default:"8080"`
		Host string `config:"server.host" validate:"required"`
	}
	Log struct {
		Level string `config:"log.level" enum:"debug,info,warn,error"`
	}
	Internal string `config:"-"` // never validated
}
```

| Tag | Effect |
|---|---|
| `config:"a.b"` | Maps the field to a dotted configuration key. **A field without this tag contributes nothing to the schema** — unless it is a struct, which is walked (see below). |
| `config:"-"` | Treated **identically to an absent tag**. On a scalar that means skipped; on a struct field it means walked — see below. |
| `validate:"required"` | Fails when the key is absent, or is a present-but-empty string. |
| `enum:"a,b,c"` | Fails when the value is not one of the listed values. |
| `default:"x"` | Recorded on the field schema. **Never injected into the configuration.** |
| `description:"…"` | Recorded on the field schema. Not currently used by any message this module produces. |

### `validate` honours exactly one word

The tag is split on commas and every token compared against the literal string `required`.
**Nothing else is honoured, and nothing else is reported.** A tag written for another
library —

```go
Port int `config:"port" validate:"required,min=1,oneof=8080 9090"`
```

— applies `required` and silently discards `min=1` and `oneof=8080 9090`. There is no
warning and no error; the constraint simply does not exist.

Anything relational, conditional, or needing a non-zero rather than merely present number
belongs in a check you write yourself. See
[validate your own slice](../how-to/validate-config.md#validate-your-own-slice-not-the-world).

### `required` means present, not non-zero

A `bool` set to `false` and an `int` set to `0` are configured values, and `required` accepts
both. Judging by zero-ness would reject an operator who deliberately turned a feature off.

Strings are the one exception: a required string that is present but empty **fails**,
because YAML writes an absent value as the empty string and the two cannot be told apart at
this layer.

### `enum` compares rendered strings

Each listed value and the configured value are both rendered with `%v` and compared as
strings. So `enum:"1,2"` matches the number `1` and the string `"1"` alike. Values are split
on commas with no whitespace trimming, so `enum:"a, b"` allows `a` and `" b"`.

### How untagged struct fields become key prefixes

A struct field **without** a `config` tag is walked rather than skipped, and how it
contributes depends on whether it is embedded:

| Field | Result |
|---|---|
| Named struct field `Server Inner` | Its lower-cased field name becomes a key prefix: `server.…`. |
| Embedded (anonymous) struct `Emb` | Merged into the parent with **no** prefix. |
| Non-struct field with no `config` tag | Skipped entirely. |

!!! warning "`config:"-"` does not exclude a struct field"
    `-` and "no tag" take the same branch, and that branch **walks a struct**. So:

    ```go
    type C struct {
    	Skipped Sec    `config:"-"`  // still contributes skipped.a
    	Plain   string `config:"-"`  // correctly skipped
    }
    ```

    A tagged struct field you want out of the schema has to be left out of the struct, or
    validated separately with its own type. `-` works as expected on scalars only.

The prefix is applied to a child's tag **only when that tag contains no dot of its own**. So
both of these produce `server.port`:

```go
Server struct {
	Port int `config:"port"`         // prefixed:      server + port
}
Server struct {
	Port int `config:"server.port"`  // already dotted: used as written
}
```

Pick one spelling and stay with it. Nesting compounds — an untagged struct inside an untagged
struct inside the root yields `server.deep.a`.

### What the type check actually compares

A field's schema `Type` is derived from its Go kind, and then compared against the **actual
Go type** of the resolved value:

| Go kind | Schema `Type` |
|---|---|
| `string` | `string` |
| `int`, `int8`, `int16`, `int32`, `int64` | `int` |
| `time.Duration` | `duration` |
| `float32`, `float64` | `float64` |
| `bool` | `bool` |
| **everything else** | `string` |

"Everything else" is broad, and it bites. `uint`, `uint16`, `[]string`, `map[string]string`,
`time.Time` and any struct all become `string`, so a field declared:

```go
Port  uint16   `config:"server.port"`
Hosts []string `config:"server.hosts"`
```

against an ordinary YAML file produces:

```text
config validation failed:
  server.port: expected type string but got int (hint: ensure server.port has a value of type string)
  server.hosts: expected type string but got []interface {} (hint: ensure server.hosts has a value of type string)
```

Use `int` rather than an unsigned type for a validated numeric field, and leave slices and
maps out of the validated struct. Reading is unaffected — `GetUint16` and `Value[[]string]`
both work; it is validation that has the narrower type model.

The same asymmetry catches values arriving as text. Environment variables and flags hold
strings, so an `int` field routinely supplied by `MYTOOL_SERVER_PORT` or `--port` fails with
`expected type int but got string`. Either leave such a key out of the typed schema or
declare it as a `string`.

### Unknown keys

A configured key the schema does not describe produces a **warning**, not an error, unless
`WithStrictMode()` is set. A key that is a parent of a known field is never reported —
`server` is fine when `server.port` is in the schema.

Only keys from *authored* sources are policed: files and compiled-in defaults. Environment
variables, flags and layers added at runtime are ambient, so a deployment platform exporting
an unrelated prefixed variable cannot stop the application starting.

### A schema with no fields is refused

`NewSchema` and `SchemaOf[T]` return [`ErrEmptySchema`](errors.md#erremptyschema) when tag
parsing produced nothing. A schema that constrains nothing would validate everything, which
is worse than having none: you would believe your configuration was checked when it was not.

## Decoding tags

Section decoding reads a different set, in this order of specificity:

1. `mapstructure:"name"` — wins outright. `mapstructure:"-"` skips the field.
2. `yaml:"name"` — honoured as an alias when it differs from the above.
3. `json:"name"` — honoured as an alias when it differs from the above.
4. The **lower-cased field name**, when no tag names it.

Honouring all three is deliberate. A type shared with an API carries `json`, one shared with
a YAML document carries `yaml`, and one written for this library carries `mapstructure`.
Honouring only the last would leave a field tagged either of the other two at its zero value
— silently, with no error.

### Embedding

| Written as | Meaning |
|---|---|
| `mapstructure:",squash"` | The embedded struct's fields belong to the parent. |
| `yaml:",inline"` | The same. `yaml` and `json` call it *inline*; `mapstructure` calls it *squash*. |
| `json:",inline"` | The same. |

An embedded struct with no tag at all is also squashed, matching what `yaml` and `json` do by
default.

### Types that decode from text without help

The decoder is configured with `WeaklyTypedInput`, so a value written as text reads as the
type the field declares. On top of that, these decode from their ordinary written form:

| Target type | Written as |
|---|---|
| `time.Duration` | `"30s"`, `"1h30m"` |
| Any slice, from a single string | `"a,b,c"` — split on commas |
| `netip.Addr`, `netip.AddrPort`, `netip.Prefix` | `"192.0.2.1"`, `"192.0.2.1:80"`, `"192.0.2.0/24"` |
| `net.IP`, `*net.IPNet` | `"192.0.2.1"`, `"192.0.2.0/24"` |
| `url.URL` | `"https://example.com/x"` |
| `*time.Location` | `"Europe/London"` |
| **Anything implementing `encoding.TextUnmarshaler`** | Whatever that implementation accepts |

The last row is the one that matters for your own code: a domain type or an enum with an
`UnmarshalText` method decodes without this module having heard of it.

### Where decoding reports nothing

`View.UnmarshalKey` returns `nil` and leaves the target untouched when the path is absent.
`Value[T]` does the same, yielding the zero value and no error. A caller distinguishing
"absent" from "set to zero" asks `View.Has` first.

A target that cannot receive values — a nil pointer, a non-pointer — returns
[`ErrInvalidTarget`](errors.md#errinvalidtarget).

## Related

- [Validate configuration](../how-to/validate-config.md) — the task guide, with strict mode,
  attaching a schema to the store, and repairing an invalid configuration
- [Use typed sections](../how-to/typed-sections.md) — decoding a subtree and keeping it
  current across reloads
- [Read configuration values](../how-to/read-values.md) — the accessor surface
- [Errors](errors.md) — `ErrInvalidConfig`, `ErrEmptySchema`, `ErrInvalidTarget`

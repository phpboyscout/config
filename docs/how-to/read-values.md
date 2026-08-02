---
title: Read configuration values
description: Typed accessors, the generic Value[T], scoped views and decoding a subtree onto a struct.
tags: [how-to, reading, typed]
---

# Read configuration values

!!! note "Assumed setup"
    Every snippet below assumes a `store` and a `ctx`:

    ```go
    ctx := context.Background()

    store, err := config.NewStore(ctx,
        config.WithFiles(config.OS(), "/etc/app/config.yaml"),
        config.WithEnv("APP"),
    )
    if err != nil {
        return err
    }
    ```

    See [Load & merge configuration](load-and-merge.md) for the full set of options.

Every read goes through a `View`, which resolves values from one immutable snapshot and
performs no I/O. Taking one is a pointer copy, so take them freely:

```go
view := store.View()
```

## Read a scalar

```go
host := view.GetString("server.host")
port := view.GetInt("server.port")
debug := view.GetBool("log.debug")
wait := view.GetDuration("client.timeout")   // "30s", "1h15m"
```

An absent key returns the zero value rather than an error. If you need to tell *absent*
from *set to zero*, ask:

```go
if !view.Has("server.port") {
	return errors.New("server.port is not configured")
}
```

`IsSet` is the same question. `SectionExists("server")` asks it of a subtree without
decoding one.

## Read the type you actually want

There is a named accessor for the common widths and shapes — `GetInt32`, `GetInt64`,
`GetUint` through `GetUint64`, `GetFloat64`, `GetTime`, `GetStringSlice`, `GetIntSlice`,
`GetStringMap`, `GetStringMapString`, `GetStringMapStringSlice`.

For anything else, use `config.Value[T]`, which reads any type at all:

```go
addr, err := config.Value[netip.Addr](view, "server.bind")
hosts, err := config.Value[[]string](view, "upstream.hosts")
limits, err := config.Value[map[string]int](view, "quotas")
```

It is a function rather than a method because Go does not allow type parameters on
methods — the same reason `UnmarshalSection` and `ValidateStruct` are functions. It takes
a `Reader`, so it works against a `View`, an `Observed` handed to an observer, or a mock.

`MustValue[T]` is the panic-on-error variant, for package initialisation and tests.

### Types that decode without you doing anything

Beyond the Go built-ins, these decode from their ordinary written form:

| Written as | Decodes to |
|---|---|
| `30s`, `1h15m` | `time.Duration` |
| `10.0.0.1` | `netip.Addr`, `net.IP` |
| `10.0.0.0/8` | `netip.Prefix`, `*net.IPNet` |
| `10.0.0.1:8080` | `netip.AddrPort` |
| `https://example.com` | `*url.URL` (pointer form) |
| `Europe/London` | `*time.Location` |
| `a,b,c` | `[]string` |

**And any type of your own implementing `encoding.TextUnmarshaler`.** That is how Go
expresses an enum or a domain type, so your types decode without this module having heard
of them:

```go
type Severity int

const (
	Debug Severity = iota
	Info
)

func (s *Severity) UnmarshalText(text []byte) error {
	switch string(text) {
	case "debug":
		*s = Debug
	case "info":
		*s = Info
	default:
		return fmt.Errorf("unknown severity %q", text)
	}

	return nil
}
```

Then read it like anything else:

```go
// log.level: debug
level, err := config.Value[Severity](view, "log.level")
```

The same hooks are used when decoding a struct, so a `Severity` field inside a
[typed section](typed-sections.md) works the same way.

## Read a size

`GetSizeInBytes` reads values written the way an operator writes them:

```go
// max_upload: 10mb
n := view.GetSizeInBytes("max_upload")   // 10485760
```

Units are binary — `kb`, `mb`, `gb`, `tb` are powers of 1024. A bare number is a count of
bytes. Anything unparseable reads as `0`, as the other accessors return their zero value.

## Scope reads to a subtree

`Sub` returns a view rooted at a key, so a component can be handed the part of the
configuration that concerns it and nothing else:

```go
if db := view.Sub("database"); db != nil {
	dsn := db.GetString("dsn")      // reads database.dsn
}
```

It returns `nil` when the key is absent — check it. Provenance still reports the full
path, so a scoped view does not lose track of where its values came from.

## Decode a whole struct

```go
type AppSettings struct {
	Server ServerSettings `mapstructure:"server"`
	Log    LogSettings    `mapstructure:"log"`
}

var settings AppSettings

if err := view.Unmarshal(&settings); err != nil {
	return err
}
```

`UnmarshalKey("server", &srv)` does the same for one subtree. For a typed view that
stays current across reloads, use [typed sections](typed-sections.md) instead.

## Enumerate what is there

```go
for _, key := range view.Keys() {
	fmt.Println(key, "=", view.Get(key))
}
```

`Keys` returns every leaf path, sorted, including keys whose value is an empty container.
`store.Snapshot().Values()` returns the whole merged tree as a map — a copy, so mutating
it cannot affect the store.

## Keep a set of reads consistent

A `View` is already pinned to one snapshot, so reads through it cannot straddle a reload.
When several values must agree and you would otherwise take more than one view, pin one
explicitly:

```go
var (
	host string
	port int
)

err := store.With(func(view *config.View) error {
	host = view.GetString("server.host")
	port = view.GetInt("server.port")

	return nil
})
if err != nil {
	return err
}
```

## Related

- [Use typed sections](typed-sections.md) — decode a subtree onto your own struct
- [Load & merge configuration](load-and-merge.md) — where the values come from
- [Provenance](../explanation/provenance.md) — and why each one won
- [Keys and paths](../reference/keys-and-paths.md) — what a path can and cannot address
- [Struct tags](../reference/struct-tags.md) — the tags decoding reads, and the types that decode from text
- [Defaults and limits](../reference/defaults-and-limits.md#sizes) — the binary suffixes `GetSizeInBytes` understands

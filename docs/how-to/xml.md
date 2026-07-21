# Read XML

The core reads and writes only YAML. XML comes from a sibling module,
[`config-xml`](https://gitlab.com/phpboyscout/go/config-xml), which adds **nothing** to your
dependency graph — it reads XML with the standard library.

```bash
go get gitlab.com/phpboyscout/go/config-xml
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	configxml "gitlab.com/phpboyscout/go/config-xml"
)

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                    // YAML, the default
	config.WithBackend(configxml.New(fsys, "/etc/log4j.xml")),  // outranks it
)
```

## Attributes and elements share one namespace

Real configuration XML is attribute-heavy — log4j and .NET `appSettings` are almost entirely
attributes — so an attribute reads by the same dotted path a child element would:

```xml
<server port="8080"><host>localhost</host></server>
```

```go
port := store.View().GetInt("server.port")     // 8080  (an attribute)
host := store.View().GetString("server.host")   // "localhost"  (a child element)
```

A repeated element becomes a slice, and a single one a scalar that the read path promotes to a
one-element slice when asked — so declaring a repeatable element as a slice reads correctly
whether the file has one entry or many:

```go
aliases := store.View().GetStringSlice("alias") // one <alias> or several, both decode
```

## What it refuses, at load

Two shapes cannot be a value tree, and are refused with `config.ErrBackendUnsafe` naming the
element rather than silently dropping half of it:

- an **attribute and a child element that share a name** — two candidates for one key, and no
  basis to choose between them;
- an element with **both text content and attributes or children** — it is at once a scalar and
  a map, and a key cannot be both.

This is the same treatment an ambiguous environment variable name gets: two candidates, no basis
to choose, so it says so rather than picking one.

## Read-only

No Go library round-trips XML preserving formatting and comments, so `config-xml` reads and does
not write. A write to a key an XML layer defines is routed past the read-only layer and reported
shadowed rather than failing.

## What it costs

Only the config graph — `encoding/xml` is standard library, so there is no parser module. An
allowlist test in the module asserts it. No filesystem library: you supply the `config.FS`.

## Related

- [Support a new file format](format-adapter.md) — how an adapter like this is built and tested
- [Backends & capabilities](../explanation/backends.md) — why a read-only layer is skipped by
  write routing

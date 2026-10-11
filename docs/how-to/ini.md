---
title: Read INI
description: Read INI configuration as a layer through the config-ini sibling module.
tags: [how-to, formats, ini]
---

# Read INI

The core's own codec is YAML. INI comes from a sibling module,
[`config-ini`](https://gitlab.com/phpboyscout/go/config-ini), which adds **nothing** to your
dependency graph: an INI file is read with the standard library.

```bash
go get gitlab.com/phpboyscout/go/config-ini
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	configini "gitlab.com/phpboyscout/go/config-ini"
)

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                  // YAML, the default
	config.WithBackend(configini.New(fsys, "/etc/app.ini")),  // outranks it
)
```

## When you do *not* need this

If you own the format, the core's YAML needs no module.

Reach for INI when the file already exists in that shape and is not yours to change: a tool's
own config, or something a user is used to editing. It is read-only, so pair it with a writable
layer if your application has to persist anything.

## Sections and keys nest on dots

A `[section]` header prefixes the keys beneath it:

```ini
[server]
host = localhost
port = 8080
```

```go
host := store.View().GetString("server.host") // "localhost"
port := store.View().GetInt("server.port")    // 8080
```

An INI layer therefore merges into a nested YAML or JSON one by the same paths. A section name
or a key may itself contain dots, each a nesting separator, so `[db.primary]` with
`pool.size = 4` reads as `db.primary.pool.size`. Keys before any section are top-level.

## What a line may contain

- A line is `key = value` or `key : value`, split on whichever separator comes first, so a
  colon in a value (a URL) is not mis-split.
- A line starting with `;` or `#` is a comment. A `;` or `#` after a value is part of the value.
- One pair of matching single or double quotes around a value is stripped.
- An empty section header, a line with no separator, or an empty key fails the load with
  `config.ErrBackendParse`, naming the file and line.

## Read-only

Values are strings, and the read path casts them. The format is read-only: the backend is not a
write target, so a `store.Apply` is routed to another layer.

## What it costs

| | |
|---|---|
| Adds | **nothing** beyond the `config` graph, and an allowlist test in the module fails if that changes |

An INI file is parsed with the standard library, so there is no parser module, and no
filesystem library either because you supply the `config.FS`.

## Related

- [Support a new file format](format-adapter.md): how an adapter like this is built and tested
- [Read dotenv](dotenv.md) and [Read Java properties](properties.md): the other flat formats

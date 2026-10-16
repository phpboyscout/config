---
title: Read INI
description: Read INI configuration as a layer through the config-ini sibling module.
tags: [how-to, formats, ini]
---

# Read INI

INI files become a config layer through [`config-ini`](https://gitlab.com/phpboyscout/go/config-ini),
a sibling module: the core's own codec is YAML, and this one adds **nothing** to your dependency
graph, because an INI file is read with the standard library. The layer is read-only.

```bash
go get gitlab.com/phpboyscout/go/config-ini
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	configini "gitlab.com/phpboyscout/go/config-ini"
)

store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/app.yaml"),               // real files outrank them
	config.WithBackend(configini.New(config.OS(), "/etc/app.ini")), // outranks it
)
if err != nil {
	return err
}

host := store.View().GetString("server.host") // "localhost"
port := store.View().GetInt("server.port")    // 8080
```

with `/etc/app.ini` containing:

```ini
[server]
host = localhost
port = 8080
```

## Map INI to keys

A `[section]` header prefixes the keys beneath it, so `port` under `[server]` reads as
`server.port`. An INI layer therefore merges into a nested YAML or JSON one by the same paths. A
section name or a key may itself contain dots, each a nesting separator, so `[db.primary]` with
`pool.size = 4` reads as `db.primary.pool.size`. Keys before any section are top-level.

Values are strings, and the read path casts them. When two lines resolve to the same key the later
one wins. A key that is also a section is refused instead: a top-level `db = x` beside a `[db]`
section with `host = y` would make `db` a value and a parent at once, so the load fails with
`config.ErrBackendUnsafe` naming the line. Rename the key or the section.

### What a line may contain

- A line is `key = value` or `key : value`, split on whichever separator comes first, so a
  colon in a value (a URL) is not mis-split.
- A line starting with `;` or `#` is a comment. A `;` or `#` after a value is part of the value.
- One pair of matching single or double quotes around a value is stripped.
- An empty section header, a line with no separator, or an empty key fails the load with
  `config.ErrBackendParse`, naming the file and line.

## Read-only, and where writes go

The backend is never a write target. A `store.Apply` to a key the INI layer provides is routed to
the next writable layer and reported shadowed, because the INI value still outranks it. With no
writable layer at all, the write fails with `config.ErrNoWritableLayer`. Pair the layer with a
writable one if your application has to persist anything.

## Watch for changes

An INI file is watched like any file-backed source: native filesystem notification where it works,
polling where it does not, at the interval you pass to `Store.Watch` with
`config.WithPollInterval` (two seconds by default). See [hot-reload](hot-reload.md).

## Test with a string

`Codec` is exported, so decoding a string needs no store and no filesystem:

```go
docs, err := configini.Codec{}.Decode("app.ini", []byte("[server]\nport = 8080\n"))
// docs[0]["server"].(map[string]any)["port"] == "8080"
```

To test through a store, point `config.Dir(t.TempDir())` at a file you write first.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `config.ErrBackendParse` | An empty section header, a line with no `=` or `:`, or an empty key. The error names the file and line. | Fix the line, or comment it out with `;` or `#`. |
| `config.ErrBackendUnsafe` | A key is also a section, such as `db = x` beside `[db]`. The error names the file and line. | Rename the key or the section. |
| `config.ErrNoWritableLayer` | An `Apply` set a key and no layer in the store can be written. | Add a writable layer, such as a YAML file. |

## When you don't need this

If you own the format, the core's YAML needs no module.

Reach for INI when the file already exists in that shape and is not yours to change: a tool's
own config, or something a user is used to editing.

## What it costs

| | |
|---|---|
| Adds | **nothing** beyond the `config` graph, and an allowlist test in the module fails if that changes |

An INI file is parsed with the standard library, so there is no parser module, and no
filesystem library either because you supply the `config.FS`.

## Related

- [Support a new file format](format-adapter.md): how an adapter like this is built and tested
- [Read dotenv](dotenv.md) and [Read Java properties](properties.md): the other flat formats

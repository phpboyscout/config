---
title: Read Java properties
description: Read Java .properties configuration as a layer through the config-properties sibling module.
tags: [how-to, formats, properties]
---

# Read Java properties

Java `.properties` files become a config layer through
[`config-properties`](https://gitlab.com/phpboyscout/go/config-properties), a sibling module: the
core's own codec is YAML, and this one adds **nothing** to your dependency graph, because a
properties file is read with the standard library. The layer is read-only.

```bash
go get gitlab.com/phpboyscout/go/config-properties
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	configproperties "gitlab.com/phpboyscout/go/config-properties"
)

store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/app.yaml"),               // real files outrank them
	config.WithBackend(configproperties.New(config.OS(), "/etc/app.properties")), // outranks it
)
if err != nil {
	return err
}

host := store.View().GetString("server.host") // "localhost"
port := store.View().GetInt("server.port")    // 8080
```

with `/etc/app.properties` containing:

```properties
server.host=localhost
server.port=8080
```

## Map properties to keys

A properties key is dotted already, and each dot is a nesting separator, so a properties layer
merges into a nested YAML or JSON one by the same paths. In Java a dotted key is a single flat
string. Here it nests, which is what makes it a configuration layer rather than a map.

Values are strings, and the read path casts them. When two lines name the same key the later one
wins, and that includes a key that is a prefix of another, which Java allows and this mapping
cannot hold: `db=x` followed by `db.host=y` leaves only `db.host`, and the reverse order leaves
only `db`, set to `x`. Neither is reported.

### The format it honours

- `key=value`, `key:value` and `key value` are all separators. Leading whitespace and
  whitespace around the separator is ignored.
- A line starting with `#` or `!` is a comment. A `#` after a value is part of the value.
- A line ending in an odd number of backslashes continues onto the next.
- The standard escapes are recognised in keys and values: `\t \n \r \f \\`, an escaped
  separator or space, and `\uXXXX`.
- An empty key, or a truncated or invalid `\u` escape, fails the load with
  `config.ErrBackendParse`, naming the file and line.

## Read-only, and where writes go

The backend is never a write target. A `store.Apply` to a key the properties layer provides is
routed to the next writable layer and reported shadowed, because the properties value still
outranks it. With no writable layer at all, the write fails with `config.ErrNoWritableLayer`.

## Watch for changes

A properties file is watched like any file-backed source: native filesystem notification where it
works, polling where it does not, at the interval you pass to `Store.Watch` with
`config.WithPollInterval` (two seconds by default). See [hot-reload](hot-reload.md).

## Test with a string

`Codec` is exported, so decoding a string needs no store and no filesystem:

```go
docs, err := configproperties.Codec{}.Decode("app.properties", []byte("server.port=8080\n"))
// docs[0]["server"].(map[string]any)["port"] == "8080"
```

To test through a store, point `config.Dir(t.TempDir())` at a file you write first.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `config.ErrBackendParse` | An empty key, or a truncated or invalid `\u` escape. The error names the file and line. | Fix the line. |
| `config.ErrNoWritableLayer` | An `Apply` set a key and no layer in the store can be written. | Add a writable layer, such as a YAML file. |

## When you don't need this

If the file is yours, YAML costs nothing extra.

Reach for `.properties` when you are sharing configuration with a JVM application that already
reads one, so both sides consume the same file rather than one generating the other.

## What it costs

| | |
|---|---|
| Adds | **nothing** beyond the `config` graph, and an allowlist test in the module fails if that changes |

A `.properties` file is parsed with the standard library, so there is no parser module, and no
filesystem library either because you supply the `config.FS`.

## Related

- [Support a new file format](format-adapter.md): how an adapter like this is built and tested
- [Read dotenv](dotenv.md) and [Read INI](ini.md): the other flat formats

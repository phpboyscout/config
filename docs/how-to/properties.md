---
title: Read Java properties
description: Read Java .properties configuration as a layer through the config-properties sibling module.
tags: [how-to, formats, properties]
---

# Read Java properties

The core's own codec is YAML. Java `.properties` come from a sibling module,
[`config-properties`](https://gitlab.com/phpboyscout/go/config-properties), which adds
**nothing** to your dependency graph: a properties file is read with the standard library.

```bash
go get gitlab.com/phpboyscout/go/config-properties
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	configproperties "gitlab.com/phpboyscout/go/config-properties"
)

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                               // YAML, the default
	config.WithBackend(configproperties.New(fsys, "/etc/app.properties")), // outranks it
)
```

## When you do *not* need this

If the file is yours, YAML costs nothing extra.

Reach for `.properties` when you are sharing configuration with a JVM application that already
reads one, so both sides consume the same file rather than one generating the other. It is
read-only, so it is an input rather than a write target.

## Keys nest on dots

A properties key is dotted already, and each dot is a nesting separator:

```properties
server.host=localhost
server.port=8080
```

```go
host := store.View().GetString("server.host") // "localhost"
port := store.View().GetInt("server.port")    // 8080
```

A properties layer therefore merges into a nested YAML or JSON one by the same paths. In Java a
dotted key is a single flat string. Here it nests, which is what makes it a configuration layer
rather than a map.

## The format it honours

- `key=value`, `key:value` and `key value` are all separators. Leading whitespace and
  whitespace around the separator is ignored.
- A line starting with `#` or `!` is a comment. A `#` after a value is part of the value.
- A line ending in an odd number of backslashes continues onto the next.
- The standard escapes are recognised in keys and values: `\t \n \r \f \\`, an escaped
  separator or space, and `\uXXXX`.
- An empty key, or a truncated or invalid `\u` escape, fails the load with
  `config.ErrBackendParse`, naming the file and line.

## Read-only

Values are strings, and the read path casts them. The format is read-only: the backend is not a
write target, so a `store.Apply` is routed to another layer.

## What it costs

| | |
|---|---|
| Adds | **nothing** beyond the `config` graph, and an allowlist test in the module fails if that changes |

A `.properties` file is parsed with the standard library, so there is no parser module, and no
filesystem library either because you supply the `config.FS`.

## Related

- [Support a new file format](format-adapter.md): how an adapter like this is built and tested
- [Read dotenv](dotenv.md) and [Read INI](ini.md): the other flat formats

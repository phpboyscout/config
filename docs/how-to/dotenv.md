---
title: Read dotenv
description: Read dotenv (.env) files as a layer through the config-dotenv sibling module.
tags: [how-to, formats, dotenv]
---

# Read dotenv

The core's own codec is YAML. dotenv (`.env`) comes from a sibling module,
[`config-dotenv`](https://gitlab.com/phpboyscout/go/config-dotenv), which adds **nothing** to
your dependency graph: a `.env` file is read with the standard library.

```bash
go get gitlab.com/phpboyscout/go/config-dotenv
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	configdotenv "gitlab.com/phpboyscout/go/config-dotenv"
)

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                      // YAML, the default
	config.WithBackend(configdotenv.New(fsys, "/etc/app.env")),   // outranks it
)
```

`New` takes a `config.FS` and a path, and the layer takes part in precedence, merge and
provenance like any other file.

## When you do *not* need this

If the values are already **exported** into the process environment (by a container runtime, a
systemd unit or a CI job), then [`WithEnv`](load-and-merge.md) reads them with no module and no
file.

Reach for `config-dotenv` when a `.env` file exists but nothing has applied it: local
development, or a compose file whose `env_file` your process never sees. It is read-only, so it
is a source of values and never a write target.

## Keys nest on underscores

A dotenv key is lower-cased and its underscores become dotted-key separators:

```dotenv
DATABASE_HOST=localhost
DATABASE_PORT=5432
```

```go
host := store.View().GetString("database.host") // "localhost"
port := store.View().GetInt("database.port")    // 5432
```

A dotenv layer therefore merges into a nested YAML or JSON one by the same paths. The rule is
deterministic and is the mapping the environment backend uses for an unmatched variable, so a
key with a literal underscore in its name cannot be expressed, by design.

## What a line may contain

- A line starting with `#` is a comment, and blank lines are skipped. A `#` after a value is
  part of the value, not a comment.
- An `export ` prefix is accepted, so a file meant to be sourced by a shell also reads.
- One pair of matching single or double quotes around a value is stripped.
- A line with no `=` or an empty key fails the load with `config.ErrBackendParse`, naming the
  file and line.

## No interpolation

A `${VAR}` in a value is left as written, never expanded:

```dotenv
# read literally as "${HOME}"
PATH_HINT=${HOME}
```

Expanding it would read the process environment through a side door the Store does not own,
which is the unprefixed-environment leak the environment backend's required prefix exists to
prevent. If you want environment values, add `config.WithEnv("PREFIX")` as an ordinary layer,
where the prefix applies and provenance names the variable.

## Read-only

Values are strings, and the read path casts them. The format is read-only: the backend is not a
write target, so a `store.Apply` is routed to another layer.

## What it costs

| | |
|---|---|
| Adds | **nothing** beyond the `config` graph, and an allowlist test in the module fails if that changes |

A `.env` file is parsed with the standard library, so there is no parser module, and no
filesystem library either because you supply the `config.FS`.

## Related

- [Support a new file format](format-adapter.md): how an adapter like this is built and tested
- [Bind CLI flags](bind-cli-flags.md) and [environment](../explanation/backends.md): the other
  ways flat, external inputs become layers
- [Read INI](ini.md) and [Read Java properties](properties.md): the other flat formats

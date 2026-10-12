---
title: Read dotenv
description: Read dotenv (.env) files as a layer through the config-dotenv sibling module.
tags: [how-to, formats, dotenv]
---

# Read dotenv

dotenv (`.env`) files become a config layer through
[`config-dotenv`](https://gitlab.com/phpboyscout/go/config-dotenv), a sibling module: the core's
own codec is YAML, and this one adds **nothing** to your dependency graph, because a `.env` file is
read with the standard library. The layer is read-only.

```bash
go get gitlab.com/phpboyscout/go/config-dotenv
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	configdotenv "gitlab.com/phpboyscout/go/config-dotenv"
)

store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/app.yaml"),               // real files outrank them
	config.WithBackend(configdotenv.New(config.OS(), "/etc/app.env")), // outranks it
)
if err != nil {
	return err
}

host := store.View().GetString("database.host") // "localhost"
port := store.View().GetInt("database.port")    // 5432
```

with `/etc/app.env` containing:

```dotenv
DATABASE_HOST=localhost
DATABASE_PORT=5432
```

`New` takes a `config.FS` and a path, and the layer takes part in precedence, merge and
provenance like any other file.

## Map dotenv to keys

A dotenv key is lower-cased and its underscores become dotted-key separators, so
`DATABASE_HOST` reads as `database.host`. A dotenv layer therefore merges into a nested YAML or
JSON one by the same paths. The rule is deterministic and is the mapping the environment backend
uses for an unmatched variable, so a key with a literal underscore in its name cannot be
expressed, by design. The rule is the same in a store built with `WithCaseSensitiveKeys`:
it is how a dotenv name becomes a key, not a folding of keys a source spelled.

Values are strings, and the read path casts them: `GetInt("database.port")` turns `"5432"` into
`5432`. When two lines name the same key the later one wins, and that includes a key that is a
prefix of another: `DATABASE=x` followed by `DATABASE_HOST=y` leaves only `database.host`, and the
reverse order leaves only `database`, set to `x`. Neither is reported.

### What a line may contain

- A line starting with `#` is a comment, and blank lines are skipped. A `#` after a value is
  part of the value, not a comment.
- An `export ` prefix is accepted, so a file meant to be sourced by a shell also reads.
- One pair of matching single or double quotes around a value is stripped.
- A line with no `=` or an empty key fails the load with `config.ErrBackendParse`, naming the
  file and line.

### No interpolation

A `${VAR}` in a value is left as written, never expanded:

```dotenv
# read literally as "${HOME}"
PATH_HINT=${HOME}
```

Expanding it would read the process environment through a side door the Store does not own,
which is the unprefixed-environment leak the environment backend's required prefix exists to
prevent. If you want environment values, add `config.WithEnv("PREFIX")` as an ordinary layer,
where the prefix applies and provenance names the variable.

## Read-only, and where writes go

The backend is never a write target. A `store.Apply` to a key the dotenv layer provides is routed
to the next writable layer and reported shadowed, because the dotenv value still outranks it. With
no writable layer at all, the write fails with `config.ErrNoWritableLayer`. Pair the layer with a
writable one if your application has to persist anything.

## Watch for changes

A `.env` file is watched like any file-backed source: native filesystem notification where it
works, polling where it does not, at the interval you pass to `Store.Watch` with
`config.WithPollInterval` (two seconds by default). See [hot-reload](hot-reload.md).

## Test with a string

`Codec` is exported, so decoding a string needs no store and no filesystem:

```go
docs, err := configdotenv.Codec{}.Decode("app.env", []byte("DATABASE_PORT=5432\n"))
// docs[0]["database"].(map[string]any)["port"] == "5432"
```

To test through a store, point `config.Dir(t.TempDir())` at a file you write first.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `config.ErrBackendParse` | A line has no `=`, or an empty key. The error names the file and line. | Fix the line, or comment it out with `#`. |
| `config.ErrNoWritableLayer` | An `Apply` set a key and no layer in the store can be written. | Add a writable layer, such as a YAML file. |

## When you don't need this

If the values are already **exported** into the process environment (by a container runtime, a
systemd unit or a CI job), then [`WithEnv`](load-and-merge.md) reads them with no module and no
file.

Reach for `config-dotenv` when a `.env` file exists but nothing has applied it: local
development, or a compose file whose `env_file` your process never sees.

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

---
title: Read and write TOML
description: Read and write TOML configuration as a layer through the config-toml sibling module.
tags: [how-to, formats, toml]
---

# Read and write TOML

TOML files become a config layer through [`config-toml`](https://gitlab.com/phpboyscout/go/config-toml),
a sibling module: the core's own codec is YAML, so you take this one only if you need TOML. Writes
edit the file in place and keep its comments.

```bash
go get gitlab.com/phpboyscout/go/config-toml
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	configtoml "gitlab.com/phpboyscout/go/config-toml"
)

store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/app.yaml"),                   // YAML, the default
	config.WithBackend(configtoml.New(config.OS(), "/etc/app.toml")), // outranks it
)
if err != nil {
	return err
}

host := store.View().GetString("server.host") // "localhost"
port := store.View().GetInt("server.port")    // 8080
```

with `/etc/app.toml` containing:

```toml
[server]
host = "localhost"
port = 8080
```

A TOML layer takes part in precedence, per-key merge and provenance exactly as a YAML file does.

## Map TOML to keys

Tables become nested keys (`[server]` and `port` give `server.port`), arrays of tables become
slices, and scalar types survive: an integer reads back through `GetInt`, a string through
`GetString`. An empty file contributes no values, the same as an empty YAML source.

## Write back to the file

`Set("server.port", 9090)` on

```toml
# app config
[server]
host = "localhost"  # dev
port = 8080
```

changes `port` to `9090` and leaves the comments, the `[server]` header and the key order exactly as
they were. Re-serialising a decoded document would destroy all three, which is the merged-view
write that [structure-preserving writes](../explanation/write-fidelity.md) exist to avoid. Instead
the backend finds the value's bytes through `go-toml/v2`'s own source ranges and replaces just
those, the same targeted-edit approach JSON uses through `sjson` and HCL through `hclwrite`. It
reports this through `PreservesComments` in its `Capabilities`.

What you can do:

- Set a key that exists, or add one to an existing section, to the top level, or as a top-level
  dotted key.
- Remove a key. This deletes its whole line, including any inline comment, and leaves a comment on
  the line above in place.
- Set a string, bool, integer, float or `[]string`.

Which layer a change lands in is the core's decision, not this adapter's. See
[where a change lands](write-config.md#where-a-change-lands).

## Watch for changes

A TOML file is watched like any file-backed source: native filesystem notification where it works,
polling where it does not. The interval you pass to `Store.Watch` with `config.WithPollInterval`
applies, and defaults to two seconds. This adapter has no cadence of its own. See
[hot-reload](hot-reload.md).

## Test with a string

It is a codec, so there is little to fake. `Codec` is exported, and decoding or editing a string
needs no store and no filesystem:

```go
docs, err := configtoml.Codec{}.Decode("app.toml", []byte("[server]\nport = 8080\n"))
// docs[0]["server"].(map[string]any)["port"] == int64(8080)
```

To test through a store, point `config.Dir(t.TempDir())` at a file you write first.
[Support a new file format](format-adapter.md) covers the conformance suite the adapter itself runs.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `config.ErrBackendParse` | The file is not valid TOML. It is raised at load and on a write, and names the file. | Fix the file. |
| `config.ErrInvalidTarget` | The write is into a sub-path of an inline-table value (`x.a` when the file says `x = {a=1}`), or adds a key to an empty section. | Edit the inline table as a whole, or give the section a key first. |
| `config.ErrBackendUnsafe` | The value is not a string, bool, integer, float or `[]string`, a map for example. | Set the leaf keys one by one. |

## Limits

- **Values are limited to what a `Set` can carry.** Writing a map or any other type is refused
  rather than guessed at.
- **Inline tables and empty sections are read, not written into.** Both are refused with
  `config.ErrInvalidTarget`.

## When you don't need this

The core reads YAML, and YAML is the cheaper answer when **you** choose the format: no module, no
extra dependency.

Reach for TOML when the file is not yours to choose, such as a format your users already write or
one a neighbouring tool in the same repo already emits.

## What it costs

| | |
|---|---|
| Adds | [go-toml](https://github.com/pelletier/go-toml), on top of the `config` graph |

That is the config graph plus one TOML parser (`pelletier/go-toml/v2`, with no dependencies of its
own), asserted by an allowlist test in the module. There is no filesystem library, because you
supply the `config.FS`.

## Related

- [Support a new file format](format-adapter.md): how an adapter like this is built and tested
- [What survives a write](../explanation/write-fidelity.md): the preservation contract TOML is
  held to, the same as YAML

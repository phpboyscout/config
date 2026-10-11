---
title: Read and write TOML
description: Read and write TOML configuration as a layer through the config-toml sibling module.
tags: [how-to, formats, toml]
---

# Read and write TOML

The core's own codec is YAML. TOML comes from a sibling module,
[`config-toml`](https://gitlab.com/phpboyscout/go/config-toml), so a consumer who needs TOML
takes it and one who does not pays nothing for it.

```bash
go get gitlab.com/phpboyscout/go/config-toml
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	configtoml "gitlab.com/phpboyscout/go/config-toml"
)

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                    // YAML, the default
	config.WithBackend(configtoml.New(fsys, "/etc/app.toml")),  // outranks it
)
```

A TOML layer takes part in precedence, per-key merge and provenance exactly as a YAML file does.
Tables become nested keys, arrays of tables become slices, and scalar types survive:

```toml
[server]
host = "localhost"
port = 8080
```

```go
host := store.View().GetString("server.host") // "localhost"
port := store.View().GetInt("server.port")    // 8080
```

## When you do *not* need this

The core reads YAML, and YAML is the cheaper answer when **you** choose the format: no module,
no extra dependency.

Reach for TOML when the file is not yours to choose, such as a format your users already write
or one a neighbouring tool in the same repo already emits.

## Writing preserves the file

Re-serialising a decoded TOML document destroys it: comments gone, sections alphabetised,
indentation injected. That is the merged-view write that
[structure-preserving writes](../explanation/write-fidelity.md) exist to avoid. So a write edits
the document in place. `Set("server.port", 9090)` on

```toml
# app config
[server]
host = "localhost"  # dev
port = 8080
```

changes `port` to `9090` and leaves the comments, the `[server]` header and the key order
exactly as they were. It works by locating the value's bytes through `go-toml/v2`'s own source
ranges and replacing just those, the same targeted-edit approach JSON uses through `sjson` and
HCL through `hclwrite`. The backend reports this through `PreservesComments` in its
`Capabilities`.

Setting or removing a key, and adding a key to an existing section, the top level, or as a
top-level dotted key, are supported. Removing a key deletes its whole line, including any
inline comment, and leaves a comment on the line above in place. Two writes are refused with
`config.ErrInvalidTarget` rather than guessed: into a sub-path of an inline-table value (the
key `x.a` when the file says `x = {a=1}`), and into an empty section. A `Set` can carry a string, bool, integer,
float or `[]string`; any other value type fails with `config.ErrBackendUnsafe`.

## What it costs

| | |
|---|---|
| Adds | [go-toml](https://github.com/pelletier/go-toml), on top of the `config` graph |

That is the config graph plus one TOML parser (`pelletier/go-toml/v2`, with no dependencies of
its own), asserted by an allowlist test in the module. There is no filesystem library, because
you supply the `config.FS`.

## Related

- [Support a new file format](format-adapter.md): how an adapter like this is built and tested
- [What survives a write](../explanation/write-fidelity.md): the preservation contract TOML is
  held to, the same as YAML

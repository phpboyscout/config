# Read and write TOML

The core reads and writes only YAML. TOML comes from a sibling module,
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

## Writing preserves the file

Re-serialising a decoded TOML document destroys it — comments gone, sections alphabetised,
indentation injected — the merged-view write [structure-preserving writes](../explanation/write-fidelity.md)
exist to avoid. So a write edits the document in place. `Set("server.port", 9090)` on

```toml
# app config
[server]
host = "localhost"  # dev
port = 8080
```

changes `port` to `9090` and leaves the comments, the `[server]` header and the key order exactly
as they were. It works by locating the value's bytes through `go-toml/v2`'s own source ranges and
replacing just those — the same targeted-edit approach JSON uses through `sjson` and HCL through
`hclwrite`.

Setting or removing a key, and adding a key to an existing section, the top level, or as a
top-level dotted key, are supported. Writing into a sub-path of an inline-table value (`x = {a=1}`
then `x.a`), or into an empty section, is refused with `config.ErrInvalidTarget` rather than
guessed.

## What it costs

The config graph plus one TOML parser (`pelletier/go-toml/v2`, with no dependencies of its own),
asserted by an allowlist test in the module. No filesystem library: you supply the `config.FS`.

## Related

- [Support a new file format](format-adapter.md) — how an adapter like this is built and tested
- [Backends & capabilities](../explanation/backends.md) — why a read-only layer is skipped by
  write routing rather than failing

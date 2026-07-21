# Read TOML

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

## Read-only, for now

`config-toml` reads TOML and does not yet write it. No existing Go library round-trips TOML
without destroying the file — comments gone, sections alphabetised, indentation injected — which
is the merged-view write [structure-preserving writes](../explanation/write-fidelity.md) exist to
avoid. Structure-preserving TOML writing needs a document editor of its own, and it is a
committed fast follow rather than a prerequisite: reading is most of the value.

So a write to a key a TOML layer defines is **not** applied to it. Routing skips the read-only
layer and the write lands in the next writable layer down, reported as shadowed rather than
failing — the same treatment any read-only backend gets. When write support lands it will be a
minor version, and `configtoml.New` will not change.

## What it costs

The config graph plus one TOML parser (`pelletier/go-toml/v2`, with no dependencies of its own),
asserted by an allowlist test in the module. No filesystem library: you supply the `config.FS`.

## Related

- [Support a new file format](format-adapter.md) — how an adapter like this is built and tested
- [Backends & capabilities](../explanation/backends.md) — why a read-only layer is skipped by
  write routing rather than failing

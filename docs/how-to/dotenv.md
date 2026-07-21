# Read dotenv

The core reads and writes only YAML. dotenv (`.env`) comes from a sibling module,
[`config-dotenv`](https://gitlab.com/phpboyscout/go/config-dotenv), which adds **nothing** to
your dependency graph — a `.env` file is read with the standard library.

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
deterministic — the same mapping the environment backend uses for an unmatched variable — so a
key with a literal underscore in its name cannot be expressed, by design. An `export ` prefix is
accepted, and surrounding single or double quotes are stripped.

## No interpolation

A `${VAR}` in a value is left as written, never expanded:

```dotenv
PATH_HINT=${HOME}   # read literally as "${HOME}"
```

Expanding it would read the process environment through a side door the Store does not own —
exactly the unprefixed-environment leak the environment backend's required prefix exists to
prevent. If you want environment values, add `config.WithEnv("PREFIX")` as an ordinary layer,
where the prefix applies and provenance names the variable.

## Read-only

Values are strings; the read path casts. The format is read-only.

## What it costs

Only the config graph — a `.env` file is parsed with the standard library, so there is no parser
module. An allowlist test in the module states it. No filesystem library: you supply the
`config.FS`.

## Related

- [Support a new file format](format-adapter.md) — how an adapter like this is built and tested
- [Bind CLI flags](bind-cli-flags.md) and [environment](../explanation/backends.md) — the other
  ways flat, external inputs become layers

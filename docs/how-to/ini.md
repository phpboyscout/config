---
title: Read INI
description: Read INI configuration as a layer through the config-ini sibling module.
tags: [how-to, formats, ini]
---

# Read INI

The core reads and writes only YAML. INI comes from a sibling module,
[`config-ini`](https://gitlab.com/phpboyscout/go/config-ini), which adds **nothing** to your
dependency graph — an INI file is read with the standard library.

```bash
go get gitlab.com/phpboyscout/go/config-ini
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	configini "gitlab.com/phpboyscout/go/config-ini"
)

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                  // YAML, the default
	config.WithBackend(configini.New(fsys, "/etc/app.ini")),  // outranks it
)
```

## Sections and keys nest on dots

A `[section]` header prefixes the keys beneath it:

```ini
[server]
host = localhost
port = 8080
```

```go
host := store.View().GetString("server.host") // "localhost"
port := store.View().GetInt("server.port")    // 8080
```

An INI layer therefore merges into a nested YAML or JSON one by the same paths. A section name
or a key may itself contain dots, each a nesting separator — `[db.primary]` with `pool.size = 4`
reads as `db.primary.pool.size`. Keys before any section are top-level. A line is `key = value`
or `key : value`, split on whichever separator comes first so a colon in a value (a URL) is not
mis-split; `;` and `#` begin a comment; and surrounding quotes are stripped.

## Read-only

Values are strings; the read path casts. The format is read-only.

## What it costs

Only the config graph — an INI file is parsed with the standard library, so there is no parser
module. An allowlist test in the module states it. No filesystem library: you supply the
`config.FS`.

## Related

- [Support a new file format](format-adapter.md) — how an adapter like this is built and tested
- [Read dotenv](dotenv.md) and [Read Java properties](properties.md) — the other flat formats

---
title: Ship defaults inside the binary
description: Embed a real YAML defaults file in your binary with embed.FS and config-iofs, layer an operator's file over it, and learn what routing does when the layer a key lives in cannot be written to.
tags: [tutorials, layers, embed, filesystem, adapters, iofs, writes]
---

# Ship defaults inside the binary

A tool that needs a config file before it will start is a tool that fails on a fresh
machine. This tutorial ships a **real YAML file compiled into the binary**, layers an
operator's file over it, and — the part worth staying for — shows what happens when you try
to write to a layer that physically cannot accept a write.

By the end you'll have a program that runs correctly with no configuration on disk at all,
and that knows where a change is allowed to land.

**Time:** about fifteen minutes. **You need:** Go 1.26.5 or newer. Nothing else.

This assumes you've built a layered store before — [Layer defaults, a file, the environment
and flags](layering.md) is the one to do first.

## Why not just use `WithReaders`?

You can, and for a handful of keys you should — that's what the [layering
tutorial](layering.md) does. `WithReaders` takes bytes you already have in a variable.

Reach for an embedded *filesystem* when the defaults have outgrown a string literal: when
you want them in a real `.yaml` file your editor will syntax-check, when there is more than
one of them, or when the same file is also shipped as an example for operators to copy.
It is the same file either way, parsed by the same code path as a file on disk.

## 1. Create a module

```bash
mkdir cfgembed && cd cfgembed
go mod init cfgembed
go get gitlab.com/phpboyscout/go/config gitlab.com/phpboyscout/go/config-iofs
```

`config-iofs` bridges any [`io/fs.FS`](https://pkg.go.dev/io/fs#FS) — an `embed.FS`, a zip,
a tar, an `os.DirFS`, an `fstest.MapFS` — into the `config.FS` the store reads through. It
adds **no third-party dependency**; it is a wrapper over the standard library.

## 2. Write the defaults as a real file

```bash
mkdir defaults
```

`defaults/app.yaml`:

```yaml
# Shipped inside the binary. Every deployment starts from these.
server:
  host: 0.0.0.0
  port: 8080
log:
  level: info
  format: json
```

## 3. Embed it and read through it

`main.go`:

```go
package main

import (
	"context"
	"embed"
	"fmt"
	"log"

	"gitlab.com/phpboyscout/go/config"
	configiofs "gitlab.com/phpboyscout/go/config-iofs"
)

//go:embed defaults
var defaults embed.FS

func main() {
	embedded := configiofs.Wrap(defaults)

	disk, err := config.Dir(".")
	if err != nil {
		log.Fatal(err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithFiles(embedded, "defaults/app.yaml"),
		config.WithFiles(disk, "config.yaml"),
	)
	if err != nil {
		log.Fatal(err)
	}

	v := store.View()
	for _, k := range []string{"server.host", "server.port", "log.level", "log.format"} {
		fmt.Printf("%-14s = %-8v %s\n", k, v.Get(k), v.Explain(k))
	}
}
```

Two filesystems, two layers, one store. `configiofs.Wrap` returns a `config.FS`, and
`WithFiles` neither knows nor cares that one of them lives inside the binary.

`//go:embed defaults` must sit at package scope, immediately above the variable it fills —
not inside a function. The path in `WithFiles` is the path *within* the embedded
filesystem, so it keeps the `defaults/` prefix.

## 4. Run it with nothing on disk

There is no `config.yaml` yet. Run anyway:

```bash
go run .
```

```
server.host    = 0.0.0.0  server.host = 0.0.0.0 (from defaults/app.yaml)
server.port    = 8080     server.port = 8080 (from defaults/app.yaml)
log.level      = info     log.level = info (from defaults/app.yaml)
log.format     = json     log.format = json (from defaults/app.yaml)
```

**A missing file is not an error.** A layer whose file does not exist contributes nothing
and the store loads without it, which is what makes "runs on a fresh machine" true rather
than aspirational.

## 5. Let an operator override it

`config.yaml`:

```yaml
server:
  port: 9090
log:
  level: debug
```

```bash
go run .
```

```
server.host    = 0.0.0.0  server.host = 0.0.0.0 (from defaults/app.yaml)
server.port    = 9090     server.port = 9090 (from config.yaml); also defined in defaults/app.yaml
log.level      = debug    log.level = debug (from config.yaml); also defined in defaults/app.yaml
log.format     = json     log.format = json (from defaults/app.yaml)
```

Two keys overridden, two still coming from inside the binary, and `Explain` naming which is
which.

## 6. Try to write to the embedded layer

Here is the part that will bite you if nobody says it first.

`log.format` is defined **only** in the embedded file. Routing's rule is *edit the key where
it already lives*, so a plain write goes looking for it there:

```go
import "errors"

	if _, err := store.Apply(context.Background(), config.Set("log.format", "text")); err != nil {
		fmt.Println("default routing:", err)
		fmt.Println("  is ErrReadOnlyFS:", errors.Is(err, config.ErrReadOnlyFS))
	}
```

```
default routing: config: preparing defaults: config: filesystem is read-only
  is ErrReadOnlyFS: true
```

!!! warning "A read-only filesystem fails at apply, not at plan"
    `config.FS` has no capability flag saying "I cannot be written to", so the store cannot
    know until it tries. `Plan` will happily target the embedded layer; `Apply` returns
    [`ErrReadOnlyFS`](../reference/errors.md). Match it with `errors.Is` and treat it as
    "this setting has no writable home yet", not as a bug.

    This differs from `WithReaders`, whose layer is *declared* unwritable, so routing skips
    it without trying. If you only ever write through routing, prefer `WithReaders`.

## 7. Name the layer you want the write to land in

Override routing with `config.To`, naming the writable layer:

```go
	change := config.Set("log.format", "text", config.To("config.yaml"))

	p, err := store.Plan(change)
	if err != nil {
		log.Fatal(err)
	}
	for _, op := range p.Operations {
		fmt.Printf("with To(): %s -> %s (effective: %v)\n", op.Change.Path, op.Target, op.Effective())
	}

	if _, err := store.Apply(context.Background(), change); err != nil {
		log.Fatal(err)
	}
```

```
with To(): log.format -> config.yaml (effective: true)
```

`config.yaml` becomes:

```yaml
server:
  port: 9090
log:
  level: debug
  format: text
```

The key was created in the operator's file, in the section it belongs to, and it now
shadows the embedded default. `effective: true` confirms that reading it back gives you the
value you just wrote.

An unmatched name is [`ErrInvalidTarget`](../reference/errors.md) rather than a silent
fallback to routing — a pin that quietly stopped pinning would be worse than one that
failed.

## What you built

| Layer | Lives in | Writable |
|---|---|:---:|
| `defaults/app.yaml` | the binary, via `embed.FS` + `config-iofs` | — (`ErrReadOnlyFS`) |
| `config.yaml` | the working directory | ✓ |

The same shape works for a zip, a tar, an `os.DirFS` or a test's `fstest.MapFS`, because all
of them are an `io/fs.FS` and `config-iofs` takes any of them.

## Where to go next

- **[Compose two stores](composing-stores.md)** — a project config and a global one, with
  promotion between them.
- **[Read configuration from an io/fs filesystem](../how-to/iofs.md)** — the adapter's own
  guide, including what it does with names and why it is read-only.
- **[Write configuration](../how-to/write-config.md)** — routing, targets and the full write
  surface.
- **[How filesystem adapters work](../explanation/filesystem-adapters.md)** — the `config.FS`
  contract every adapter implements.

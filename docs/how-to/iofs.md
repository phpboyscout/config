---
title: Read config from an io/fs filesystem
description: Wrap any io/fs.FS — an embedded FS, a zip or tar, an os.DirFS — as a read-only config source.
tags: [how-to, filesystem, iofs, embed]
---

# Read config from an io/fs filesystem

Configuration compiled into the binary with [`embed`](https://pkg.go.dev/embed), or read from a
zip, a tar, or any other [`io/fs.FS`](https://pkg.go.dev/io/fs#FS), becomes a `config` layer through
the [`config-iofs`](https://gitlab.com/phpboyscout/go/config-iofs) sibling module.

```bash
go get gitlab.com/phpboyscout/go/config-iofs
```

The most common use is defaults baked into the binary as a fallback beneath the real files:

```go
import (
	"embed"

	"gitlab.com/phpboyscout/go/config"
	configiofs "gitlab.com/phpboyscout/go/config-iofs"
)

//go:embed defaults.yaml
var embedded embed.FS

store, err := config.NewStore(ctx,
	config.WithFiles(configiofs.Wrap(embedded), "defaults.yaml"),   // baked-in defaults
	config.WithFiles(config.OS(), "/etc/app.yaml"),                 // real files outrank them
	config.WithEnv("APP"),
)
```

`Wrap` is the whole API. It takes any `io/fs.FS` — `embed.FS`, `os.DirFS`, `zip.Reader`,
`tar`-derived, `fstest.MapFS` in a test — and returns a `config.FS`.

## When you do *not* need this

For a **single** embedded defaults document, [`WithReaders`](load-and-merge.md) takes
bytes you already have and needs no module — the
[layering tutorial](../tutorials/layering.md) does exactly that.

Reach for `config-iofs` when the defaults have outgrown a string literal: several files,
a directory you want to keep as files, or a zip or tar you are reading in place. It is
read-only, which is the point for embedded defaults — see
[Ship defaults inside the binary](../tutorials/embedded-defaults.md).

## It is read-only, and says so

`io/fs.FS` is read-only *by design*: the interface has no write, rename or remove. So `config-iofs`
is read-only, and its write methods return the shared sentinel **`config.ErrReadOnlyFS`**:

```go
_, err := store.Apply(ctx, config.Set("server.port", 9090))
if errors.Is(err, config.ErrReadOnlyFS) {
	// the embedded layer cannot be written — expected
}
```

If a writable layer sits beneath the embedded one, the write routes there and is reported shadowed,
exactly as it would for any read-only source. A layer that is only ever read — baked-in defaults —
never triggers this.

## Names are io/fs names

Pass an `io/fs`-valid name: slash-separated, no leading slash, no `.` or `..` segments (see
[`fs.ValidPath`](https://pkg.go.dev/io/fs#ValidPath)). An invalid name is reported as such rather
than read as a missing file, so a fat-fingered absolute path surfaces instead of silently resolving
to "absent". To root a sub-tree, compose [`fs.Sub`](https://pkg.go.dev/io/fs#Sub) before `Wrap` — it
already works with no help from this module:

```go
sub, _ := fs.Sub(embedded, "config")
store, _ := config.NewStore(ctx, config.WithFiles(configiofs.Wrap(sub), "app.yaml"))
```

## What it costs

| | |
|---|---|
| Modules added | **none** — everything it links comes from the `config` graph, and an allowlist test in the module fails if that changes |

It is `io/fs` and the standard library, which makes it the leanest adapter in the family —
joint with the four read-only flat formats, which also add nothing.

## Related

- [How filesystem adapters work](../explanation/filesystem-adapters.md) — the `config.FS` family and
  what read-only means for reload
- [Use an afero filesystem](afero.md) — the sibling bridge for an `afero.Fs`
- [Backends & capabilities](../explanation/backends.md) — the `config.FS` interface

---
title: Read config from an io/fs filesystem
description: Wrap any io/fs.FS, such as an embedded FS, a zip or tar, or an os.DirFS, as a read-only config source.
tags: [how-to, filesystem, iofs, embed]
---

# Read config from an io/fs filesystem

Configuration compiled into the binary with [`embed`](https://pkg.go.dev/embed), or read from a
zip, a tar, or any other [`io/fs.FS`](https://pkg.go.dev/io/fs#FS), becomes a `config` layer
through the [`config-iofs`](https://gitlab.com/phpboyscout/go/config-iofs) sibling module.

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

`Wrap` is the whole API. It takes any `io/fs.FS` (an `embed.FS`, `os.DirFS`, `zip.Reader`, a
tar-derived filesystem, an `fstest.MapFS` in a test) and returns a `config.FS`.

## When you do *not* need this

For a **single** embedded defaults document, [`WithReaders`](load-and-merge.md) takes bytes you
already have and needs no module. The [layering tutorial](../tutorials/layering.md) does exactly
that.

Reach for `config-iofs` when the defaults have outgrown a string literal: several files, a
directory you want to keep as files, or a zip or tar you are reading in place. The layer is
read-only, which suits embedded defaults. See
[Ship defaults inside the binary](../tutorials/embedded-defaults.md).

## It is read-only, and says so

`io/fs.FS` is read-only *by design*: the interface has no write, rename or remove. So
`config-iofs` is read-only too, and its four write methods return the shared sentinel
**`config.ErrReadOnlyFS`**.

The Store cannot see that from the filesystem, so it only finds out when a write reaches the
layer. A write to a key that is defined **only** in the iofs layer is routed there, because
routing edits a key where it already lives. `Plan` accepts it and `Apply` fails:

```go
_, err := store.Apply(ctx, config.Set("log.format", "text"))
if errors.Is(err, config.ErrReadOnlyFS) {
	// the key lives only in the embedded layer
}
```

To land such a write somewhere useful, name the writable layer with `config.To`. The
[embedded defaults tutorial](../tutorials/embedded-defaults.md) walks through it. A layer that
is only ever read, such as baked-in defaults, never triggers the error.

## Names are io/fs names

Pass an `io/fs`-valid name: slash-separated, no leading slash, no `.` or `..` segments (see
[`fs.ValidPath`](https://pkg.go.dev/io/fs#ValidPath)). An invalid name is refused with an error
wrapping `fs.ErrInvalid`, rather than read as a missing file, so a fat-fingered absolute path
surfaces instead of silently resolving to "absent". A valid name that does not exist still
returns the not-exist error the Store treats as a normal absent layer.

To root a sub-tree, compose [`fs.Sub`](https://pkg.go.dev/io/fs#Sub) before `Wrap`. It already
works with no help from this module:

```go
sub, _ := fs.Sub(embedded, "config")
store, _ := config.NewStore(ctx, config.WithFiles(configiofs.Wrap(sub), "app.yaml"))
```

## Watching

An `io/fs.FS` has no operating-system path and no symlink resolution, so the wrapper satisfies
neither `config.RealPather` nor `config.LinkReader`. The Store polls it, which is harmless for
`embed.FS`: a poll re-reads the same bytes and the Store stays quiet. For native watching over a
real directory, use `config.Dir`.

## Listing a directory

[`config-filekv`](filekv.md) reads a whole directory, so it needs `config.DirLister`. `Wrap`
claims it only where the filesystem can list: one that implements `fs.ReadDirFS`, such as
`embed.FS` or `fstest.MapFS`, or one whose root opens as a listable directory, such as
`zip.Reader`. Checking the root is the only I/O `Wrap` does. A filesystem that can do neither is
wrapped without `ReadDir`, and `configfilekv.New` then returns `ErrNotListable` rather than
contributing nothing later.

## What it costs

| | |
|---|---|
| Adds | **nothing** beyond the `config` graph, and an allowlist test in the module fails if that changes |

It is `io/fs` and the standard library. That makes it as lean as the four read-only flat
formats ([dotenv](dotenv.md), [INI](ini.md), [properties](properties.md) and [XML](xml.md)),
which also add nothing.

## Related

- [How filesystem adapters work](../explanation/filesystem-adapters.md): the `config.FS` family
  and what read-only means for reload
- [Use an afero filesystem](afero.md): the sibling bridge for an `afero.Fs`
- [Backends & capabilities](../explanation/backends.md): the `config.FS` interface

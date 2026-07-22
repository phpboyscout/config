---
title: Use an afero filesystem
description: Bridge an existing afero filesystem to config.FS for code that already threads an afero.Fs.
tags: [how-to, filesystem, afero]
---

# Use an afero filesystem

`config` defines its own six-method [`config.FS`](../explanation/backends.md) rather than
depending on [afero](https://github.com/spf13/afero), so importing it does not oblige you to
import a filesystem library chosen on your behalf. When something upstream already produced an
`afero.Fs` and you have to pass it through, the
[`config-afero`](https://gitlab.com/phpboyscout/go/config-afero) sibling module is the bridge.

```bash
go get gitlab.com/phpboyscout/go/config-afero
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	configafero "gitlab.com/phpboyscout/go/config-afero"
)

store, err := config.NewStore(ctx,
	config.WithFiles(configafero.Wrap(fsys), "/etc/app.yaml"),
	config.WithEnv("APP"),
)
```

`Wrap` is the whole API.

## You probably do not need it

Two implementations ship with `config`, and between them they cover most cases:

| You want | Use |
|---|---|
| the real filesystem | `config.OS()` |
| a directory nothing can escape | `config.Dir(path)` |
| a throwaway directory in a test | `config.Dir(t.TempDir())` |

That last row is worth stating plainly, because reaching for an in-memory filesystem in a test
is the habit it replaces. `config.Dir(t.TempDir())` needs no dependency, gives real file
semantics including watching, and exercises the same code path production does.

Reach for `config-afero` when something upstream produced an `afero.Fs` and you have to pass it
through — a tool built on afero, a copy-on-write overlay, a base-path root over a virtual
worktree.

## Why it is a module rather than twenty lines

It very nearly *is* twenty lines: six methods map onto afero almost directly. The part worth not
rediscovering is which **optional** interfaces the adapter may claim.

`config` decides whether native filesystem notification can work by asking whether the
filesystem satisfies `config.RealPather`. An adapter implementing that method unconditionally —
returning `false` for an in-memory filesystem rather than not having the method — satisfies the
interface for *every* filesystem, so `fsnotify` is selected and the absence of anything to watch
is discovered one path at a time instead of up front. It compiles, it looks right, and it
quietly costs native notification with nothing to report it.

So `Wrap` returns one of four concrete types, claiming `RealPather` and `LinkReader`
independently and only where the filesystem can honour each:

| Filesystem | Real paths | Symlinks | Result |
|---|---|---|---|
| `afero.OsFs` | yes | yes | native notification, writes follow links |
| `afero.BasePathFs` | yes | yes | native notification, verified per path |
| `afero.CopyOnWriteFs` | no | yes | polling, writes follow links |
| `afero.MemMapFs` | no | no | polling, paths used as given |

The copy-on-write row shows why the two capabilities are claimed separately rather than
together: it can read links through its base layer but has no path the operating system can
watch. A `BasePathFs` is taken at face value, because afero exposes no way to ask what it wraps —
which is safe, because `config` treats a real path as a claim rather than proof and confirms with
`os.SameFile` before watching, falling back to polling per path where the claim does not hold.

## What it costs

afero, `config`, and what those two already bring — asserted by an allowlist test in the module,
so an unforeseen transitive addition fails its build rather than arriving quietly.

## Related

- [Backends & capabilities](../explanation/backends.md) — the `config.FS` interface and its
  optional `RealPather` / `LinkReader` companions

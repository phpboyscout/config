---
title: Use an afero filesystem
description: Bridge an existing afero filesystem to config.FS for code that already threads an afero.Fs.
tags: [how-to, filesystem, afero]
---

# Use an afero filesystem

When something upstream already produced an [afero](https://github.com/spf13/afero) `afero.Fs`
and you have to pass it through, the [`config-afero`](https://gitlab.com/phpboyscout/go/config-afero)
sibling module bridges it to `config.FS`. `config` defines its own six-method
[`config.FS`](../explanation/backends.md) rather than depending on afero, so importing it does not
oblige you to import a filesystem library chosen on your behalf.

```bash
go get gitlab.com/phpboyscout/go/config-afero
```

```go
import (
	"github.com/spf13/afero"

	"gitlab.com/phpboyscout/go/config"
	configafero "gitlab.com/phpboyscout/go/config-afero"
)

fsys := afero.NewBasePathFs(afero.NewOsFs(), "/srv/worktree") // whatever you were handed

store, err := config.NewStore(ctx,
	config.WithFiles(configafero.Wrap(fsys), "/etc/app.yaml"),
	config.WithEnv("APP"),
)
if err != nil {
	return err
}

port := store.View().GetInt("server.port")
```

`Wrap` is the whole API. Paths are afero's, passed through unchanged, so through a `BasePathFs`
`/etc/app.yaml` resolves beneath its root.

## Read and write through it

The layer reads and writes through the wrapped filesystem. A `store.Apply` stages the new content
beside the file and renames it into place through afero, so the edit is as atomic as the
filesystem's `Rename`.

Where the filesystem can resolve symlinks (the "Symlinks" column under
[Watch for changes](#watch-for-changes)), a config file reached through one is written *through*
it rather than having the link replaced with a regular file. Elsewhere the path is used as given.

A read-only afero filesystem, such as `afero.NewReadOnlyFs`, refuses writes with afero's own
error, which matches `fs.ErrPermission`. The adapter passes it through unchanged rather than
translating it to `config.ErrReadOnlyFS`.

## Watch for changes

Six methods map onto afero almost directly. The part worth not rediscovering is which
**optional** interfaces the adapter may claim, because they decide how the layer is watched.

`config` decides whether native filesystem notification can work by asking whether the
filesystem satisfies `config.RealPather`. An adapter that implements that method for every
filesystem, returning `false` for an in-memory one, still satisfies the interface. `config` then
selects `fsnotify` and finds out that there is nothing to watch one path at a time instead of up
front. It compiles, it looks right, and it quietly costs native notification with nothing to
report it.

So `Wrap` returns one of four concrete types, claiming `RealPather` and `LinkReader`
independently and only where the filesystem can honour each:

| Filesystem | Real paths | Symlinks | Result |
|---|---|---|---|
| `afero.OsFs` | yes | yes | native notification, writes follow links |
| `afero.BasePathFs` | yes | yes | native notification, verified per path |
| `afero.CopyOnWriteFs` | no | yes | polling, writes follow links |
| `afero.MemMapFs` | no | no | polling, paths used as given |

The copy-on-write row shows why the two capabilities are claimed separately. It can read links
through its base layer but has no path the operating system can watch. A `BasePathFs` is taken
at face value, because afero offers no way to ask what it wraps. That is safe because `config`
treats a real path as a claim rather than proof: it confirms with `os.SameFile` before watching,
and falls back to polling for any path where the claim does not hold.

Polling runs at the interval you pass to `Store.Watch` with `config.WithPollInterval`. See
[hot-reload](hot-reload.md).

## List a directory

All four types satisfy `config.DirLister`, because `Readdir` is mandatory on every afero file.
That lets [`config-filekv`](filekv.md) read a directory of single-value files through any afero
filesystem, so a mounted secrets directory can be tested over an `afero.MemMapFs`. Entries come
back sorted by name, and a symlink is reported as a symlink rather than followed.

## Test with an afero filesystem

If your code already holds an `afero.Fs`, hand the test an `afero.MemMapFs` with the file written
first:

```go
mem := afero.NewMemMapFs()
_ = afero.WriteFile(mem, "/etc/app.yaml", []byte("server:\n  port: 8080\n"), 0o600)

store, err := config.NewStore(ctx, config.WithFiles(configafero.Wrap(mem), "/etc/app.yaml"))
```

If it does not, prefer `config.Dir(t.TempDir())`: it needs no dependency, gives real file
semantics including native watching, and exercises the same code path production does.

## Errors you'll see

The adapter has no errors of its own. Whatever afero returns reaches you unchanged.

| Error | Cause | Fix |
|---|---|---|
| `fs.ErrNotExist` | The file is not there. | Nothing, if the source is optional: the store treats it as an empty layer unless it is the first source and you set `RequireFirstSource`. |
| `fs.ErrPermission` | A write reached a read-only afero filesystem, such as `afero.NewReadOnlyFs`. | Name a writable layer with `config.To`, or wrap a writable filesystem. |

## When you don't need this

Two implementations ship with `config`, and between them they cover most cases:

| You want | Use |
|---|---|
| the real filesystem | `config.OS()` |
| a directory nothing can escape | `config.Dir(path)` |
| a throwaway directory in a test | `config.Dir(t.TempDir())` |

The last row replaces the habit of reaching for an in-memory filesystem in a test.

Reach for `config-afero` when something upstream produced an `afero.Fs` and you have to pass it
through: a tool built on afero, a copy-on-write overlay, a base-path root over a virtual
worktree.

## What it costs

| | |
|---|---|
| Adds | [afero](https://github.com/spf13/afero), on top of the `config` graph |

That is afero, `config`, and what those two already bring. An allowlist test in the module
asserts it, so an unforeseen transitive addition fails the build rather than arriving quietly.

## Related

- [Backends & capabilities](../explanation/backends.md): the `config.FS` interface and its
  optional `RealPather`, `LinkReader` and `DirLister` companions
- [How filesystem adapters work](../explanation/filesystem-adapters.md): real-path against
  polled reload
- [Use a go-billy filesystem](billy.md): the equivalent bridge for a `billy.Filesystem`

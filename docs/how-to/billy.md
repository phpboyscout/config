---
title: Use a go-billy filesystem
description: Bridge a go-billy filesystem, as used by go-git, to config.FS, with reads and writes.
tags: [how-to, filesystem, billy, go-git]
---

# Use a go-billy filesystem

A tool that already holds a `billy.Filesystem` (an in-memory worktree, a chroot, an on-disk root)
can point `config` at it through the [`config-billy`](https://gitlab.com/phpboyscout/go/config-billy)
sibling module. [go-billy](https://github.com/go-git/go-billy) is the filesystem abstraction
[go-git](https://github.com/go-git/go-git) is built on.

```bash
go get gitlab.com/phpboyscout/go/config-billy
```

```go
import (
	"github.com/go-git/go-billy/v5/osfs"

	"gitlab.com/phpboyscout/go/config"
	configbilly "gitlab.com/phpboyscout/go/config-billy"
)

bfs := osfs.New("/etc/app") // or an in-memory memfs, a chroot, a worktree

store, err := config.NewStore(ctx,
	config.WithFiles(configbilly.Wrap(bfs), "config.yaml"),
	config.WithEnv("APP"),
)
if err != nil {
	return err
}

port := store.View().GetInt("server.port")
```

`Wrap` is the whole API.

## Name files relative to the root

Paths are forward-slash and **relative to the filesystem's root**, like `config.Dir` and unlike
`config.OS()`. An `osfs.New("/etc/app")` reads `"config.yaml"`, not `"/etc/app/config.yaml"`.
`Wrap` passes names through unchanged and does not validate them.

## Read and write through it

The layer reads **and writes**: a `store.Apply` commits back into the billy filesystem through
its native rename, so an edit lands atomically and structure is preserved.

`billy.Filesystem` mandates `Readlink`, so `config-billy` satisfies `config.LinkReader` for
every wrapped filesystem. A config file reached through a symlink is written *through* it rather
than having the link replaced with a regular file. Where a path is not a link, billy says so and
the path is used as given.

### Read-only billy filesystems

A `billy.Filesystem` can be mounted without write capability. When it is, `config-billy` checks
`billy.Capabilities` once, in `Wrap`, and every write method returns the shared sentinel
**`config.ErrReadOnlyFS`** up front. A read-only filesystem then gives you the same `errors.Is`
result whether it is read-only by design or by how it was mounted:

```go
_, err := store.Apply(ctx, config.Set("server.port", 9090))
if errors.Is(err, config.ErrReadOnlyFS) {
	// the wrapped billy filesystem reports no WriteCapability
}
```

A filesystem that is writable but fails a write returns the underlying error unchanged.

## Watch for changes

A `billy.Filesystem` cannot honestly report an operating-system path: the same wrapper type
fronts a disk and memory, and `Root()` is a logical root. The adapter therefore does not satisfy
`config.RealPather`, and the Store watches it by polling, at the interval you pass to
`Store.Watch` with `config.WithPollInterval`. That is cheap for both `osfs` and `memfs`. See
[React to changes with hot-reload](hot-reload.md).

The adapter serialises its own calls, because go-billy's `memfs` is not safe for concurrent use
and a poll reads on its own goroutine while a commit writes. A watched `memfs` needs no lock of
yours.

## List a directory

`billy.Filesystem` mandates `ReadDir` too, so `config-billy` satisfies `config.DirLister` for
every wrapped filesystem. [`config-filekv`](filekv.md) needs that to read a directory of
single-value files, so a mounted secrets directory can be tested over `memfs`. Entries come
back sorted by name, and a symlink is reported as a symlink rather than followed.

## Test with memfs

`memfs` gives a test a writable billy filesystem with nothing on disk:

```go
bfs := memfs.New()
_ = util.WriteFile(bfs, "config.yaml", []byte("server:\n  port: 8080\n"), 0o600)

store, err := config.NewStore(ctx, config.WithFiles(configbilly.Wrap(bfs), "config.yaml"))
```

`util` is `github.com/go-git/go-billy/v5/util`. If your code does not already hold a billy
filesystem, `config.Dir(t.TempDir())` needs no dependency and watches natively.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `config.ErrReadOnlyFS` | A write reached a billy filesystem that reports no `WriteCapability`. | Name a writable layer with `config.To`, or mount the filesystem writable. |
| `fs.ErrNotExist` | The file is not there under the filesystem's root, often because an absolute path was passed. | Pass the name relative to the root. The store treats a missing optional source as an empty layer. |
| Anything else from billy, unchanged | A writable filesystem failed a write or a read. | Whatever the underlying error says. |

## When you don't need this

If the file is on local disk, `config.OS()` or `config.Dir(path)` already cover it.

Reach for `config-billy` when something upstream handed you a billy filesystem and you have to
pass it through. The usual case is a go-git worktree, in memory or on disk, where the
configuration lives inside the repository you are operating on.

## What it costs

| | |
|---|---|
| Adds | [go-billy](https://github.com/go-git/go-billy), on top of the `config` graph |

That is go-billy, `config`, and what those two already bring. An allowlist test in the module
asserts it, so an unforeseen transitive addition fails the build rather than arriving quietly.

## Related

- [How filesystem adapters work](../explanation/filesystem-adapters.md): the `config.FS` family
- [Use an afero filesystem](afero.md): the equivalent bridge for an `afero.Fs`
- [Backends & capabilities](../explanation/backends.md): the `config.FS` interface and
  `LinkReader`

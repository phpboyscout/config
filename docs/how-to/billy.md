---
title: Use a go-billy filesystem
description: Bridge a go-billy filesystem — as used by go-git — to config.FS, with reads and writes.
tags: [how-to, filesystem, billy, go-git]
---

# Use a go-billy filesystem

[go-billy](https://github.com/go-git/go-billy) is the filesystem abstraction [go-git](https://github.com/go-git/go-git)
is built on, so a tool that already holds a `billy.Filesystem` — an in-memory worktree, a chroot, an
on-disk root — can point `config` at it through the
[`config-billy`](https://gitlab.com/phpboyscout/go/config-billy) sibling module.

```bash
go get gitlab.com/phpboyscout/go/config-billy
```

```go
import (
	"github.com/go-git/go-billy/v5/osfs"

	"gitlab.com/phpboyscout/go/config"
	configbilly "gitlab.com/phpboyscout/go/config-billy"
)

bfs := osfs.New("/etc/app")   // or an in-memory memfs, a chroot, a worktree

store, err := config.NewStore(ctx,
	config.WithFiles(configbilly.Wrap(bfs), "config.yaml"),
	config.WithEnv("APP"),
)
```

`Wrap` is the whole API, and the layer reads **and writes** — a `store.Apply` commits back into the
billy filesystem through its native rename, so an edit lands atomically and structure is preserved.

Paths are forward-slash and **relative to the filesystem's root**, like `config.Dir` and unlike
`config.OS()`: an `osfs.New("/etc/app")` reads `"config.yaml"`, not `"/etc/app/config.yaml"`.

## Read-only billy filesystems

A `billy.Filesystem` can be mounted without write capability. When it is, `config-billy` maps that to
the shared sentinel **`config.ErrReadOnlyFS`** up front, so a read-only filesystem gives a consistent
`errors.Is` story whether it is read-only by design or by how it was mounted:

```go
_, err := store.Apply(ctx, config.Set("server.port", 9090))
if errors.Is(err, config.ErrReadOnlyFS) {
	// the wrapped billy filesystem reports no WriteCapability
}
```

## Symlinks

`billy.Filesystem` mandates `Readlink`, so `config-billy` satisfies `config.LinkReader` for every
wrapped filesystem — a config file reached through a symlink is written *through* it, rather than
having the link replaced with a regular file. Where a path is not a link, the filesystem says so and
the path is used as given.

## What it costs

| | |
|---|---|
| Modules added | **10** — 1 for [go-billy](https://github.com/go-git/go-billy), 9 for the `config` graph |

go-billy, `config`, and what those two already bring — asserted by an allowlist test in the module,
so an unforeseen transitive addition fails its build rather than arriving quietly.

## Related

- [How filesystem adapters work](../explanation/filesystem-adapters.md) — the `config.FS` family
- [Use an afero filesystem](afero.md) — the equivalent bridge for an `afero.Fs`
- [Backends & capabilities](../explanation/backends.md) — the `config.FS` interface and `LinkReader`

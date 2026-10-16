---
title: Read a directory of single-value files
description: Use a mounted ConfigMap, Docker secrets or systemd credentials as a config layer, where each file's name is a key and its contents are the value.
tags: [how-to, backends, kubernetes, secrets]
---

# Read a directory of single-value files

A directory where each file's name is a key and its contents are the value becomes a config layer
through [`config-filekv`](https://gitlab.com/phpboyscout/go/config-filekv), a sibling module that
adds nothing to your dependency graph:

```
/etc/config/
  database.host    →  db.internal
  database.port    →  5432
  log.level        →  debug
```

Three unrelated systems present configuration exactly that way, which is why the module is named
for the shape rather than for any one of them:

| System | Location |
|---|---|
| Kubernetes ConfigMap or Secret, mounted as a volume | the mount path |
| Docker and Podman secrets | `/run/secrets/<name>` |
| systemd `LoadCredential` | `$CREDENTIALS_DIRECTORY/<name>` |

```bash
go get gitlab.com/phpboyscout/go/config-filekv
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	configfilekv "gitlab.com/phpboyscout/go/config-filekv"
)

backend, err := configfilekv.New(config.OS(), "/etc/config")
if err != nil {
	return err
}

store, err := config.NewStore(ctx,
	config.WithBackend(backend),               // the mount, lowest
	config.WithFiles(config.OS(), "app.yaml"), // ordinary settings
	config.WithEnv("MYAPP"),                   // overrides
)
if err != nil {
	return err
}

host := store.View().GetString("database.host") // "db.internal"
port := store.View().GetInt("database.port")    // 5432
```

## Point it at the directory

`New` takes any `config.FS` that also satisfies `config.DirLister`, the optional interface
for listing a directory. `config.OS()`, `config.Dir`, and the
[afero](afero.md) and [billy](billy.md) wrappers all do, and [io/fs](iofs.md) does where the
underlying filesystem can list. A filesystem that cannot returns `configfilekv.ErrNotListable`
from `New`, rather than a layer that silently contributes nothing. A directory that does not
exist yet contributes nothing and is not an error. There is no connection, and nothing to close.

### Why not just read the directory yourself

Because a mounted ConfigMap does not contain plain files:

```
..2026_07_29_10_00_00.1234/      real directory, timestamped
..data  ->  ..2026_07_29_.../    symlink, repointed atomically on update
database.host -> ..data/database.host
log.level     -> ..data/log.level
```

The kubelet writes a whole new timestamped directory and then **repoints `..data` in
one operation**, which is what makes a ConfigMap update impossible to observe
half-applied.

List that naively and you get `..data` and a timestamped directory **alongside** the
real keys, which invents two configuration keys out of the update mechanism. This
adapter skips dot-prefixed entries, and skips them *before* recursing, so the whole
staging tree stays invisible rather than being read twice.

!!! info "Verified, not assumed"

    That layout, and the behaviours described below, are asserted against a real
    Kubernetes cluster in the adapter's integration suite, currently k3s
    **v1.34.9**, rather than taken from documentation. The version is recorded so
    the claim can be re-checked when it changes.

## Map files to keys

- **Filenames split on `.`.** `database.host` nests as `database` then `host`. A name
  with no dot is a single top-level key.
- **Subdirectories nest too.** A [projected
  volume](https://kubernetes.io/docs/concepts/storage/projected-volumes/) with an
  `items[].path` of `sub/key` produces a real subdirectory, so `sub/database.host`
  becomes `sub.database.host`. A symlink to a directory is followed.
- **An empty file is an empty value**, not an absent key. Otherwise "set to empty"
  and "not set" would be indistinguishable.
- **A key cannot be a value and a parent at once.** Files named `database` and
  `database.host`, or two files resolving to one key such as `database.host` beside
  `database/host`, refuse the load with `config.ErrBackendUnsafe`. A write that would create
  that shape is refused with `config.ErrInvalidTarget` before anything is written.

### Values are byte-exact

No trailing newline is trimmed unless you ask:

```go
configfilekv.New(config.OS(), "/etc/config", configfilekv.WithTrimTrailingNewline())
```

All three systems write values exactly, so a three-byte value stays three bytes and a
stray newline only appears when a human created the file with `echo`. Trimming by
default would silently alter a value that legitimately ends in whitespace, and when
that value is a credential the result is an authentication failure with nothing to
point at. A visible stray newline is diagnosable; a mangled secret is not.
`WithTrimTrailingNewline` strips exactly one trailing newline, and the carriage return
before it if there is one.

### Files that hold documents

By default every file is a string, and the read path casts it. If some files hold JSON or YAML,
pass a codec and a file that decodes to a mapping becomes a subtree:

```go
configfilekv.New(config.OS(), "/etc/config", configfilekv.WithValueCodec(config.YAMLCodec{}))
```

A file that does not decode, or decodes to something other than a mapping, falls back
to its string, so a directory that mixes documents and scalars works. Only the first
document of a multi-document file is taken.

### Nest the directory under a prefix

```go
configfilekv.New(config.OS(), "/run/secrets", configfilekv.WithPrefix("secrets"))
```

`WithPrefix` nests the directory under a path, so `db_password` reads as
`secrets.db_password` rather than sitting at the root beside your ordinary settings.

## Write back, when you opt in

Off by default, because every layout above is read-only: a ConfigMap volume is mounted
read-only, Docker secrets are `0444`, systemd credentials `0400`. Without the option the backend
is not a write target, and a write to one of its keys is routed to another layer. A plain
directory is a perfectly good small writable store, so it is opt-in:

```go
configfilekv.New(config.OS(), "/var/lib/myapp/config", configfilekv.WithWritable())
```

A write to `database.host` lands in a file named `database.host` in the directory, and a removal
deletes the file.

!!! warning "What writing cannot promise"

    - **Not atomic across keys, or within one.** Three keys is three file writes, and
      each goes straight to the filesystem's `WriteFile` with no staging rename. Rollback
      undoes a partial batch best-effort, restoring the old value or removing a file
      that did not exist before. There is no `..data` trick available to a general
      directory. That is the kubelet's mechanism, not a filesystem primitive.
    - **Conflict detection compares content**, because a file has no version. It
      compares the whole directory against what the last load saw, so a change to a
      sibling key counts as a conflict, as does a key gained or lost. It catches
      another writer, which is the case worth catching, but cannot tell "changed and
      changed back".
    - **A value a single file cannot hold is refused** with
      `configfilekv.ErrUnwritableValue`. A map or a slice has no one-file
      representation, and inventing one would invent a format nothing else in the
      directory agrees on.

New files are `0600`, because a writable directory of single-value files may well hold
credentials and the looser default fails silently. `WithFileMode` widens it. The
directory itself is created on the first write, not at construction. With `WithPrefix`
set, a write to a path outside the prefix is refused with `config.ErrInvalidTarget`.

## Watch for changes

The adapter polls the directory, recursing into subdirectories. It makes one `Stat` per
entry and reads no files, and it reports a possible change when a name, size or
modification time moves. Its own cadence is 30 seconds, which `configfilekv.WithPollInterval`
overrides, and an interval you pass to `Store.Watch` with `config.WithPollInterval` overrides
both.

The adapter only reports that something may have changed. The Store then waits for a
settle window (250ms by default, bounded at four windows) so a burst of writes becomes
one reload, and compares the values before it notifies anyone. A spurious wake costs one
reload that tells nobody. [React to changes with hot-reload](hot-reload.md) covers the
options.

## Keep secrets out of plainer layers

```go
configfilekv.New(config.OS(), "/run/secrets",
	configfilekv.WithPrefix("secrets"),
	configfilekv.WithSensitive())
```

`WithSensitive` marks the layer, so the core refuses to write one of these values into
a layer that is not itself sensitive. It is the same
[`ErrSensitiveLeak`](../explanation/backends.md) protection a secrets manager gets,
over a local directory. Leave it off for an ordinary ConfigMap, where it would refuse
legitimate writes to the file beneath it.

## Test without a cluster

The adapter needs only a listable `config.FS`, so a test writes files into
`config.Dir(t.TempDir())`, or builds the directory in memory with an
[`fstest.MapFS`](https://pkg.go.dev/testing/fstest#MapFS) through [`config-iofs`](iofs.md):

```go
fsys := fstest.MapFS{"conf/database.host": {Data: []byte("db.internal")}}

backend, err := configfilekv.New(configiofs.Wrap(fsys), "conf")
```

A `MapFS` is read-only, so a test of `WithWritable` wants `config.Dir(t.TempDir())`, an afero
`MemMapFs` or a billy `memfs` instead.

The module's integration suite, under `test/integration`, mounts real ConfigMaps in a k3s cluster
in a container and is skipped unless `INT_TEST_INTEGRATION=1` is set; it needs a Docker daemon.
It is what keeps the layout and the `subPath` warning below true.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `configfilekv.ErrNotListable` | `New` was given a filesystem that cannot list a directory. | Use `config.OS()`, `config.Dir`, or a wrapper that satisfies `config.DirLister`. |
| `configfilekv.ErrUnwritableValue` | A write carried a map or a slice. | Write a scalar: a list or a map has no one-file form. |
| `config.ErrInvalidTarget` | With `WithPrefix`, a write named a path outside the prefix; or a write would make a key both a value and a parent, such as `database` beside an existing `database.host`. | Write under the prefix, or to another layer; or remove the other key in the same change. |
| `config.ErrBackendUnsafe` | One key is both a value and a parent, such as files `database` and `database.host`, or two files resolve to one key. | Rename or remove one of the files. |
| `config.ErrConflict` | Something in the directory changed, or a key appeared or vanished, since the last load. | Reload and apply again. |
| `config.ErrSensitiveLeak` | With `WithSensitive`, a write to one of the layer's keys would land in a plainer layer. | Change the value at its source, or make the backend writable. |

## Limits

- **A same-size edit within one modification-time granule can be missed.** Rare in
  practice, and the trade is deliberate: hashing every file per tick would make the
  watch scale with data volume rather than directory size.
- **A `subPath` mount never updates.** Kubernetes does not rewrite it for the life of
  the pod, so a value read through one is frozen no matter what the ConfigMap says.
  This is a Kubernetes behaviour rather than an adapter limitation, and it is
  [asserted in the integration
  suite](https://gitlab.com/phpboyscout/go/config-filekv) so the warning stays true.

## When you don't need this

A ConfigMap holding **one key with a whole YAML or JSON document** mounts as a single
file, and [`WithFiles`](load-and-merge.md) already reads it, including hot reload,
because the kubelet rewrites the mount in place. Consumed with `envFrom`, a ConfigMap
is environment variables, and `WithEnv` already reads those.

A ConfigMap holding **several documents**, such as `base.yaml` and `site.yaml`,
mounts as several files, and each should be its own layer rather than one layer of
keys. `WithFiles` takes any number of paths in precedence order, with the first as the
base and the last as the winner:

```go
store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(),
		"/etc/config/base.yaml",
		"/etc/config/site.yaml", // outranks base.yaml
	),
	config.WithEnv("MYAPP"),
)
```

Name the files rather than listing the directory. Their order is a precedence
decision, and taking it from whatever the directory holds would make a file added
to the ConfigMap later change which value wins without anyone choosing that. Each
file is its own layer with its own provenance, so `Explain` names the one that
supplied a value.

Reach for this adapter when the ConfigMap holds **many scalar keys**, which mounts as
many files, or when you are reading a secrets directory that never had a document in
the first place.

## What it costs

| | |
|---|---|
| Adds | **nothing** beyond the `config` graph, and a test fails if that changes |
| Requires | the `config` version named in this module's `go.mod`, which `go get` brings |
| Capability since | `config` **v0.12.0**, the release adding `config.DirLister`, the optional interface needed to enumerate a directory at all |

That zero is the point. The alternative is a Kubernetes API client to read a ConfigMap
the pod has already mounted. That costs the whole Kubernetes client, plus RBAC on
configmaps and a service account, and only works in-cluster.

## Related

- [Bound what a backend contributes](filter-a-backend.md): expose only part of a directory
- [Keep tokens in the OS keychain](keychain.md): the other local, sensitive backend
- [The adapter ecosystem](../explanation/adapters.md): every adapter, status and roadmap

---
title: Read a directory of single-value files
description: Use a mounted ConfigMap, Docker secrets or systemd credentials as a config layer, where each file's name is a key and its contents are the value.
tags: [how-to, backends, kubernetes, secrets]
---

# Read a directory of single-value files

Some configuration arrives as a directory rather than a document:

```
/etc/config/
  database.host    →  db.internal
  database.port    →  5432
  log.level        →  debug
```

Three unrelated systems present it exactly that way, which is why
[`config-filekv`](https://gitlab.com/phpboyscout/go/config-filekv) is named for the
shape rather than for any one of them:

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
	config.WithBackend(backend),        // the mount, lowest
	config.WithFiles(fsys, "app.yaml"), // ordinary settings
	config.WithEnv("MYAPP"),            // overrides
)
```

## When you do *not* need this

A ConfigMap holding **one key with a whole YAML or JSON document** mounts as a single
file, and [`WithFiles`](load-and-merge.md) already reads it — including hot reload,
because the kubelet rewrites the mount in place. Consumed with `envFrom`, a ConfigMap
is environment variables, and `WithEnv` already reads those.

Reach for this adapter when the ConfigMap holds **many scalar keys**, which mounts as
many files, or when you are reading a secrets directory that never had a document in
the first place.

## Why not just read the directory yourself

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
real keys — two configuration keys invented out of the update mechanism. This adapter
skips dot-prefixed entries, and skips them *before* recursing, so the whole staging
tree stays invisible rather than being read twice.

!!! info "Verified, not assumed"

    That layout, and the three behaviours below, are asserted against a real
    Kubernetes cluster in the adapter's integration suite — currently k3s
    **v1.34.9** — rather than taken from documentation. The version is recorded so
    the claim can be re-checked when it changes.

## How names become keys

- **Filenames split on `.`** — `database.host` nests as `database` → `host`. A name
  with no dot is a single top-level key.
- **Subdirectories nest too.** A [projected
  volume](https://kubernetes.io/docs/concepts/storage/projected-volumes/) with an
  `items[].path` of `sub/key` produces a real subdirectory, so `sub/database.host`
  becomes `sub.database.host`.
- **An empty file is an empty value**, not an absent key — otherwise "set to empty"
  and "not set" would be indistinguishable.

### Values are byte-exact

No trailing newline is trimmed unless you ask:

```go
configfilekv.New(config.OS(), "/etc/config", configfilekv.WithTrimTrailingNewline())
```

All three systems write values exactly — a three-byte value stays three bytes — so a
stray newline only appears when a human created the file with `echo`. Trimming by
default would silently alter a value that legitimately ends in whitespace, and when
that value is a credential the result is an authentication failure with nothing to
point at. A visible stray newline is diagnosable; a mangled secret is not.

## Secrets directories

```go
configfilekv.New(config.OS(), "/run/secrets",
	configfilekv.WithPrefix("secrets"),
	configfilekv.WithSensitive())
```

`WithPrefix` nests the directory under a path, so `db_password` reads as
`secrets.db_password` rather than sitting at the root beside your ordinary settings.

`WithSensitive` marks the layer, so the core refuses to write one of these values into
a layer that is not itself sensitive — the same
[`ErrSensitiveLeak`](../explanation/backends.md) protection a secrets manager gets,
over a local directory.

## Writing

Off by default, because every layout above is read-only: a ConfigMap volume is mounted
read-only, Docker secrets are `0444`, systemd credentials `0400`. A plain directory is
a perfectly good small writable store, so it is opt-in:

```go
configfilekv.New(config.OS(), "/var/lib/myapp/config", configfilekv.WithWritable())
```

!!! warning "What writing cannot promise"

    - **Not atomic across keys.** Three keys is three file writes. Each is atomic in
      itself via write-then-rename, and rollback undoes a partial batch best-effort —
      but there is no `..data` trick available to a general directory. That is the
      kubelet's mechanism, not a filesystem primitive.
    - **Conflict detection compares content**, because a file has no version. It
      catches another writer, which is the case worth catching, but cannot tell
      "changed and changed back".
    - **A value a single file cannot hold is refused.** A map has no one-file
      representation, and inventing one would invent a format nothing else in the
      directory agrees on.

New files are `0600`, because a writable directory of single-value files may well hold
credentials and the looser default fails silently. `WithFileMode` widens it. The
directory itself is created on the first write, not at construction.

## Hot reload

The adapter polls the directory — one `Stat` per entry, no file reads — and reports a
possible change when a name, size or modification time moves. `WithPollInterval`
overrides the 30-second default.

Two things worth knowing:

- **A same-size edit within one modification-time granule can be missed.** Rare in
  practice, and the trade is deliberate: hashing every file per tick would make the
  watch scale with data volume rather than directory size.
- **A `subPath` mount never updates.** Kubernetes does not rewrite it for the life of
  the pod, so a value read through one is frozen no matter what the ConfigMap says.
  This is a Kubernetes behaviour rather than an adapter limitation, and it is
  [asserted in the integration
  suite](https://gitlab.com/phpboyscout/go/config-filekv) so the warning stays true.

## What it costs

| | |
|---|---|
| Modules added | **none** — everything it links comes from the `config` graph, and a test fails if that changes |
| Requires | the `config` version named in this module's `go.mod` — `go get` brings it |
| Capability since | `config` **v0.12.0**, the release adding `config.DirLister` — the optional interface needed to enumerate a directory at all |

That zero is the point. The alternative — a Kubernetes API client to read a ConfigMap
the pod has already mounted — costs 38 modules, plus RBAC on configmaps and a service
account, and only works in-cluster.

## Related

- [Bound what a backend contributes](filter-a-backend.md) — expose only part of a directory
- [Keep tokens in the OS keychain](keychain.md) — the other local, sensitive backend
- [The adapter ecosystem](../explanation/adapters.md) — every adapter, status and roadmap

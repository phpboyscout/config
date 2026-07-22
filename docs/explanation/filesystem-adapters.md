---
title: How filesystem adapters work
description: The config.FS family — wrapping any filesystem, from an embedded FS to a cloud object store, as an ordinary source.
tags: [adapters, filesystem, ecosystem]
---

# How filesystem adapters work

A *filesystem adapter* teaches the store to read and write its configuration file on a filesystem
that is not the local disk — a filesystem [compiled into the binary](../how-to/iofs.md), one a tool
[already threads around](../how-to/afero.md), a [remote host over SSH](../how-to/sftp.md), or a
cloud object store. Each ships as its own `config-<fs>` module that satisfies the module's own
[`config.FS`](backends.md) interface, and — like the [dynamic backends](dynamic-backends.md) — they
are more alike than not. This page explains what the family shares and where its members
legitimately differ. For what exists, see [the adapter ecosystem](adapters.md); the
[filesystem adapters spec](../development/specs/2026-07-22-filesystem-adapters.md) is the umbrella
that governs the whole family.

## A file, not a key–value store

This is the distinction that decides whether you want a filesystem adapter or a
[dynamic backend](dynamic-backends.md), and the two are easy to confuse for a cloud store.

A **filesystem adapter** reads a configuration *file* — one `app.yaml`, parsed by a codec — that
happens to live somewhere other than local disk. The bytes in an S3 object are a YAML document; the
adapter's whole job is to hand those bytes to the store and write them back. A **dynamic backend**
treats each *remote key as a config key* — a Consul prefix, a tree of SSM parameters. Same cloud,
different model: put a `config.yaml` in a bucket and you want `config-aws-s3`; map a hierarchy of
individual parameters and you want a backend like `config-aws-ssm`.

## The shape they all share

- **The filesystem is injected; the adapter owns no credentials.** You build and configure the
  underlying thing — an `fs.FS`, a `billy.Filesystem`, an `*s3.Client`, an `*sftp.Client` — and hand
  it to the module's `Wrap`. The adapter re-decides no authentication, region, endpoint or host; it
  only translates the six `config.FS` calls. This is exactly [`config-afero`](../how-to/afero.md)'s
  `Wrap(afero.Fs)`, and it is why a fake or in-memory filesystem drives each unit suite with no
  network.
- **Six methods, and the load-bearing one is `Rename`.** `config.FS` is `ReadFile`, `WriteFile`,
  `Stat`, `Rename`, `Remove` and `MkdirAll`. A write is committed by staging content beside the
  target and **renaming over it**, so a reader never observes a half-written file. An adapter
  implements those six and, where the filesystem can honour them, the optional
  [`RealPather`](backends.md) and [`LinkReader`](backends.md).
- **The layer is an ordinary layer.** Once wrapped, the filesystem feeds a normal file layer:
  precedence, per-key merge, provenance, coherent snapshots and fail-closed reload all work exactly
  as they do for `config.OS()`. `Explain` names the file, wherever it physically lives.

## Read-only is a first-class capability

Some filesystems cannot be written, by design. [`config-iofs`](../how-to/iofs.md) wraps an
[`io/fs.FS`](https://pkg.go.dev/io/fs#FS), which the standard library defines with no write, rename
or remove — so the adapter is read-only, and its write methods return the shared sentinel
**`config.ErrReadOnlyFS`**. A consumer can `errors.Is` that regardless of which read-only filesystem
produced it, rather than guess from a bare `fs.ErrPermission` a genuine permission failure is
indistinguishable from. A write routed at a read-only layer fails loudly at the point of the write,
exactly as it would for a read-only file on disk — the same first-class outcome a read-only
[backend](dynamic-backends.md) gets.

Capability follows the underlying filesystem honestly. `config-billy` is read+write, but if the
`billy.Filesystem` it wraps reports no write capability, its write methods return
`config.ErrReadOnlyFS` too — so a read-only filesystem is a read-only filesystem whether it is one
by design or by how it was mounted.

## Watching: native where there is a real path, polled where there is not

Hot-reload needs the adapter to notice a change, and this turns on one question: does a name map to
a real operating-system path?

- **A real path** (`config.OS()`, `config.Dir`, an `afero.OsFs`) satisfies `config.RealPather`, so
  the watcher uses `fsnotify` — native, push-fast notification.
- **No real path** (an embedded FS, an object store, an SFTP host) is **polled** instead. Polling
  inherits everything else for free: the Store coalesces a burst, re-reads, re-merges and stays quiet
  if the resolved configuration did not actually change. The only visible difference is latency.

The default poll cadence — `DefaultPollInterval`, two seconds — suits a local filesystem, but for a
remote one every poll is a round-trip, often a *billed* API call, so two seconds is far too eager. A
remote adapter therefore implements the optional **`config.PollIntervalHinter`** to declare a
sensible default (the cloud object stores hint 60 seconds; [`config-sftp`](../how-to/sftp.md) hints
15). The watcher adopts the hint unless you set one explicitly with `WithPollInterval`, which always
wins. A local or in-memory adapter does not implement the interface, so it keeps the responsive
two-second default. (An embedded FS never changes, so watching it is pointless but harmless.)

## Object stores are not POSIX — and the commit meets them honestly

S3, GCS and Azure Blob are object stores, not disks, and the adapter maps `config.FS` onto them
without pretending otherwise:

- **`MkdirAll` is a no-op** — there are no directories, only key prefixes.
- **`Stat` synthesises** a `fs.FileInfo` from object metadata; the mode is nominal (`0o644`).
- **The commit uses the store's best atomic primitive.** The `config.FS` contract makes `Rename` the
  atomic commit, and each adapter honours it with whatever the store actually offers:

  | Store | Commit primitive | Note |
  |---|---|---|
  | GCS | native `ObjectHandle.Move` | server-side, atomic, no orphan window |
  | SFTP | `posix-rename@openssh.com` | atomic overwrite where the server advertises it |
  | S3 | copy-then-delete | no atomic move; a crash between the two can orphan a staging object |
  | Azure Blob | copy-then-delete | as S3; a recognisable staging key makes an orphan findable |

  Where a store has a real move, the adapter uses it. Where it does not, copy-then-delete still
  replaces the *target* atomically (`PutObject`/`CopyObject` are atomic per object) — the only
  imperfection is a staging object left behind on a crash: garbage to clean up, never a corrupted
  target.

- **Conflict detection still holds.** The write path fingerprints content by SHA-256 at load and
  refuses a write if the source changed underneath it. That is content-based, not rename-based, so it
  works unchanged over an object store — these stores are strongly read-after-write consistent.

## Testing them without the cloud

Every adapter's unit suite runs against a fake or in-memory underlying filesystem, with no network —
`fstest.MapFS` for `io/fs`, billy's `memfs`, an in-process SFTP server, an injected fake for the
object stores. Each drives a real `config.Store` through `Wrap`: load, write via the stage-and-rename
commit, reload, and confirm round-trips and permission handling exactly as `config.OS()` would. The
cloud adapters are then additionally proven against a **real emulator** in an env-gated
Docker-in-Docker job — LocalStack (S3), fake-gcs-server (GCS) and Azurite (Azure Blob) all run under
testcontainers, so unlike some backends the whole cloud filesystem trio has real integration tests.

## Related

- [The adapter ecosystem](adapters.md) — every adapter, its capability and status
- [Backends and capabilities](backends.md) — `config.FS` and its optional `RealPather` / `LinkReader`
  companions
- [How dynamic backends work](dynamic-backends.md) — the sibling family, for remote *key–value*
  systems rather than files
- [Use an afero filesystem](../how-to/afero.md) — the original filesystem bridge

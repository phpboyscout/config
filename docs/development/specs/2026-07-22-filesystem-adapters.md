---
title: Filesystem adapters — wrapping other filesystems as config.FS sibling modules
date: 2026-07-22
author: matt.cockayne
status: draft
---

# Filesystem adapters

## Problem

The module reads and writes configuration through its own small
[`config.FS`](../../reference) interface. Two implementations exist: the built-in `config.OS()`
(the real operating-system filesystem) and `config.Dir` (one rooted at a directory), plus the
sibling [`config-afero`](https://gitlab.com/phpboyscout/go/config-afero), which bridges an
`afero.Fs` a consumer already holds. But a large and growing share of configuration lives on a
filesystem that is none of those: **compiled into the binary** (`embed.FS`), in a **go-billy**
filesystem a tool already uses, on a **remote host over SSH**, or in a **cloud object store** —
AWS S3, GCP Cloud Storage, Azure Blob Storage. A tool whose configuration lives in one of those
cannot point this module at it today.

The seam to reach them already exists and is proven: `WithFiles`, `NewFileBackend` and
`NewWatcher` all take a `config.FS`, and `config-afero` shows a sibling module can supply one. So
the question is not *can* the module read from these filesystems — it demonstrably can — but which
ones we ship as first-class sibling adapters, and under what shared conventions.

This spec is the **umbrella**. It establishes the family of `config-<fs>` filesystem adapters and
the decisions that cut across all of them. It deliberately does **not** specify any single adapter:
an `io/fs` wrapper is a dozen lines with no dependency, while an S3 wrapper carries an SDK, an
object-store consistency model and a rename that is not a rename. So, as with the backend family,
**each adapter gets its own approved spec before it is built** (D2) — proportionate to how much it
actually diverges.

## The contract every adapter meets

`config.FS` is six methods, and the load-bearing one is `Rename`:

```go
type FS interface {
	ReadFile(name string) ([]byte, error)          // fs.ErrNotExist when absent
	WriteFile(name string, data []byte, perm fs.FileMode) error
	Stat(name string) (fs.FileInfo, error)
	Rename(oldpath, newpath string) error          // the atomic commit
	Remove(name string) error
	MkdirAll(path string, perm fs.FileMode) error
}
```

A write is committed by staging content beside the target and **renaming over it**, so a reader
never observes a half-written file. Two optional interfaces refine behaviour: `RealPather`
(does a name map to a real OS path?) — which is what the fsnotify hot-reload watcher needs — and
`LinkReader` (symlink resolution). An adapter implements the six methods and optionally the two.

## Decisions

### D1 — One sibling `config-<fs>` module per filesystem, cloud-qualified where it names a cloud

Every filesystem ships as its own module, depended on only by consumers who use it — the same
decision, for the same reason, as the format and backend adapters. It is sharpest for the cloud
stores, whose SDKs are large: a consumer reading config from S3 must not acquire the GCP or Azure
SDK. Naming follows the backend family (dynamic backend adapters D1): a cloud service carries its
cloud — `config-aws-s3`, `config-gcp-gcs`, `config-azure-blob` — and a vendor-neutral filesystem
stays bare — `config-iofs`, `config-billy`, `config-sftp`, `config-http`.

Two things are **not** adapters and must not become modules: the operating-system filesystem is
already `config.OS()` in the core, and an `embed.FS` is an `io/fs.FS`, so it is served by
`config-iofs` — there is no `config-os` and no `config-embed` to build.

### D2 — Each adapter has its own approved spec, proportionate to its divergence

No `config-<fs>` adapter is implemented until it has its own `status: approved` spec, kept
centrally here alongside this umbrella. But filesystem adapters vary enormously in how much they
diverge, and the spec should match: `config-iofs` is a read-only wrapper over a stdlib interface
and its spec is a page; `config-aws-s3` carries an SDK, an eventual-consistency story, a
rename-is-copy-then-delete model and an emulator test rig, and its spec earns every section. Each
per-adapter spec settles: the underlying type it wraps and how each `config.FS` method maps onto
it; its capability (read-only or read+write; real-path/watchable or not); how writes commit and
what atomicity actually holds; its dependency footprint with the allowlist; and its testing
surface (fake/in-memory and, where one exists, an emulator).

### D3 — The adapter wraps an injected client or filesystem; it owns no credentials

An adapter's constructor takes the already-configured underlying thing — an `io/fs.FS`, a
`billy.Filesystem`, an `*s3.Client`, an `sftp.Client` — behind the module's own `Wrap`, never
re-deciding authentication, region, endpoint or host. This is exactly `config-afero`'s
`Wrap(afero.Fs)` pattern and the backend family's D3: the consumer configures the client (which is
where the SDK dependency honestly lives), and the adapter only translates the six `config.FS`
calls. A fake or in-memory underlying filesystem then drives the unit suite with no cloud.

### D4 — Capability is what the underlying filesystem genuinely supports

A read-only filesystem yields a read-only adapter: `ReadFile` and `Stat` work, and the write
methods (`WriteFile`, `Rename`, `Remove`, `MkdirAll`) return a clear, sentinel error rather than
pretending. `io/fs` is read-only *by design* (it has no write, rename or remove), so `config-iofs`
and `config-http` are read-only; `config-billy`, the cloud stores and `config-sftp` are read+write.
A read-only filesystem is a first-class citizen: a write routed at it fails loudly at the point of
the write, exactly as it would for a read-only file on disk.

### D5 — Hot-reload needs a real path; filesystems without one are read-at-load

The fsnotify watcher works only on real OS paths, reported through `RealPather`. An OS-backed
filesystem (a billy `osfs`, an SFTP mount that is really local) can report real paths and be
watched; an `embed.FS`, an object store and an HTTP URL cannot, so they are **read at load and not
natively watched**. This is honest, not a gap: compiled-in defaults never change, and a config
object in a bucket changes rarely and out of band. A **polling reload** — re-read on an interval
and let the Store coalesce and stay quiet if nothing resolved differently — is a possible future
option for the remote adapters, decided per adapter, not a requirement of this umbrella.

### D6 — Object stores are not POSIX: the rename is a copy, and that is fine

S3, GCS and Azure Blob are object stores, and the `config.FS` contract meets them honestly rather
than pretending they are disks:

- **`Rename` is copy-then-delete.** There is no atomic rename. But `WriteFile` maps to `PutObject`
  and rename's copy maps to `CopyObject`, both of which are **atomic per object** — so the *target*
  is still replaced atomically and a reader never sees a half-written object. The only imperfection
  is that a crash between the copy and the delete leaves the staged object behind: garbage to be
  cleaned up, never corruption of the target.
- **`MkdirAll` is a no-op** — object stores have no directories, only key prefixes.
- **`Stat` synthesizes** a `fs.FileInfo` from object metadata (size, last-modified); mode bits are
  nominal.
- **Conflict detection still holds.** The write path fingerprints content by SHA-256 at load and
  refuses a write if the source changed underneath it — this is content-based, not rename-based, so
  it works unchanged over an object store (these stores are strongly read-after-write consistent).

The consequence — extra API calls per commit and a possible orphaned staging object on crash — is
stated in each cloud adapter's spec, not hidden.

### D7 — The SDK is the honest cost, stated per adapter

`config-iofs` adds **zero** dependencies (it is stdlib). `config-billy` adds a small one. The
cloud adapters each carry their provider's storage SDK, which is the largest thing in their graph,
asserted by an allowlist `depfootprint` test and stated in the README — and, because each is its
own module, a consumer reading from S3 never compiles the GCS or Azure SDK.

### D8 — Testing is a fake/in-memory filesystem plus an emulator where one exists

Every adapter's unit suite runs against an in-memory or fake underlying filesystem, needing no
network — `fstest.MapFS` for `io/fs`, billy's `memfs`, a fake object store for the cloud ones.
The cloud stores are then additionally proven against a **real emulator** in an env-gated
integration suite: **LocalStack** (S3), **fake-gcs-server** (GCS) and **Azurite** (Azure Blob) all
exist and run under testcontainers, so — unlike some backend adapters — all three cloud filesystem
adapters can have real integration tests in a Docker-in-Docker job. `config-sftp` tests against an
in-process SSH/SFTP server. Each per-adapter spec names its fake and its emulator.

## Rejected alternatives

**A `config-os` module.** The OS filesystem is already `config.OS()` in the core; wrapping it in a
sibling module would duplicate what ships built in.

**A `config-embed` or `config-zip` module.** `embed.FS`, `archive/zip` and `archive/tar` all
expose `io/fs.FS`, so `config-iofs` serves them directly. One read-only stdlib adapter covers the
whole `io/fs` ecosystem.

**A FUSE adapter.** A FUSE-mounted filesystem is already an operating-system path, reached through
`config.OS()`; there is nothing to wrap. Go FUSE libraries *implement* a mount (expose a filesystem
to the OS), which is the opposite direction. Exposing the merged configuration *as* a FUSE mount is
a genuinely different feature, not a `config.FS` adapter, and is out of scope.

**Treating the cloud stores as Backends instead.** A `Backend` over object *keys as config keys*
(the parameter-store shape) is a different, legitimate thing — but it is not what "filesystem
adapter" means. These adapters read a config *file* that happens to live in a bucket, through the
same codec and write machinery a local file uses. The object-as-key model, if ever wanted, is a
separate spec.

**One `config-fs` module with every filesystem.** Rejected on the same dependency ground as one
`config-formats` or one `config-remote` module: it would pull every cloud's storage SDK into every
consumer's graph. See D1.

## Public API

Each adapter exports a `Wrap` that adapts its underlying filesystem to `config.FS`, mirroring
`config-afero`:

```go
configiofs.Wrap(embeddedFS)                 // fs.FS → read-only config.FS
configbilly.Wrap(billyFS)                    // billy.Filesystem → config.FS
configs3.Wrap(s3Client, "my-bucket")         // *s3.Client + bucket → config.FS
configsftp.Wrap(sftpClient)                  // *sftp.Client → config.FS
```

The result is passed to `WithFiles`/`NewFileBackend` exactly as `config.OS()` is. No change to the
`config` core is required — `config.FS`, `RealPather` and `LinkReader` already exist and are
sufficient, proven by `config-afero` implementing them against the public API.

## Testing strategy

Per adapter: an in-memory/fake-driven unit suite (D3, D8); for the cloud stores, an env-gated
emulator integration suite under testcontainers (D8); an allowlist `depfootprint` test (D7). The
umbrella is verified by the adapters passing the module's existing filesystem behaviour — a wrapped
filesystem must read, write, stage-and-rename, and preserve permissions exactly as `config.OS()`
does, which the module's own suite already asserts against any `config.FS`.

## Migration & compatibility

Additive for consumers: a consumer adds a module and passes its `Wrap` to `WithFiles`, exactly as
for `config-afero`. No core change; nothing breaks.

## Open questions

To resolve with the human before this moves to `approved`:

1. **The adapter set and order.** Proposed: `config-iofs` and `config-billy` first (trivial,
   zero/light dependency, POSIX-shaped), then the cloud trio (`config-aws-s3`, `config-gcp-gcs`,
   `config-azure-blob`), then `config-sftp`. Is `config-http` (read-only URL fetch) in or out, and
   is `config-git` (read a file at a ref) wanted or deferred?
2. **Polling reload for the remote adapters (D5).** Ship read-at-load only, or offer an opt-in poll
   for the cloud/SFTP adapters? Per-adapter, or a shared convention set here?
3. **Read-only write errors (D4).** A shared sentinel (e.g. a `config.ErrReadOnlyFS`) the read-only
   adapters return, or each returning `fs.ErrPermission`? A shared sentinel a consumer can branch on
   is the cleaner story if worth a small core addition.

## Implementation phases

**Phase 0 — this umbrella spec.**

**Phase 1 — the POSIX-shaped adapters.** `config-iofs` (read-only, zero-dependency) and
`config-billy` (read+write), each its own approved spec then module. They validate the family
against the simplest cases and add no cloud surface.

**Phase 2 — the cloud object stores.** `config-aws-s3`, `config-gcp-gcs`, `config-azure-blob` — the
symmetric trio, each D2-gated, each with an emulator integration suite (D8). The object-store
atomicity model (D6) is proven here.

**Phase 3 — remote and revisit.** `config-sftp`, and `config-http`/`config-git` if adopted (OQ1).
Anything the earlier adapters showed this umbrella got wrong is corrected here by dated revision.

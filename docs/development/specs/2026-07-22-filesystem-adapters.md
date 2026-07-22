---
title: Filesystem adapters — wrapping other filesystems as config.FS sibling modules
date: 2026-07-22
author: matt.cockayne
status: approved
approved: 2026-07-22
revised: 2026-07-22
---

# Filesystem adapters

## Problem

The module reads and writes configuration through its own small
[`config.FS`](../../explanation/backends.md) interface. Two implementations exist: the built-in `config.OS()`
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
stays bare — `config-iofs`, `config-billy`, `config-sftp`. (A read-only HTTP-URL adapter and a
git-ref adapter were considered and dropped as too niche for the cost; see Resolved.)

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
methods (`WriteFile`, `Rename`, `Remove`, `MkdirAll`) return **`config.ErrReadOnlyFS`** — a shared
sentinel added to the core for this family, so a consumer can `errors.Is` it regardless of which
read-only filesystem produced it, rather than each adapter returning a bare `fs.ErrPermission` a
real permission error is indistinguishable from. `io/fs` is read-only *by design* (it has no write,
rename or remove), so `config-iofs` is read-only; `config-billy`, the cloud stores and
`config-sftp` are read+write. A read-only filesystem is a first-class citizen: a write routed at it
fails loudly at the point of the write, exactly as it would for a read-only file on disk.

### D5 — Hot-reload is native where there is a real path, and polled where there is not

The watcher uses fsnotify for a filesystem that reports real OS paths (`RealPather`), and — this
**already ships in the core** — falls back to a `pollWatcher` for one that does not. So a
filesystem without real paths (an `embed.FS`, an object store, SFTP) is not un-watchable: when the
consumer calls `Watch`, the Store polls it, re-reading and staying quiet if nothing resolved
differently, with the existing `WithPollInterval` setting the cadence (`DefaultPollInterval`, 2s).
No new mechanism is needed — the poll watcher and `WithPollInterval` are already there, so **the
remote adapters get opt-in polled reload for free**.

The one thing the adapters owe here is a *sensible* interval, not a mechanism: for a cloud store a
poll is a billed API call and the 2-second default is far too eager, so each cloud adapter's spec
recommends a much longer default and documents the cost. (An `embed.FS` never changes, so watching
it is pointless but harmless.) The shared convention is therefore: rely on the core's poll watcher,
tuned per adapter — not a new per-adapter watch API.

> **Revised — see R1 below.** "Recommends a default in its spec" left the interval as prose an adapter could only honour by asking the consumer to pass `WithPollInterval`. R1 adds a small optional core interface, `config.PollIntervalHinter`, so an adapter *declares* its cadence and the watcher adopts it — the mechanism D5 said was unnecessary turned out to be one narrow interface, matching `RealPather`/`LinkReader`.

### D6 — Object stores are not POSIX: the rename is a copy, and that is fine

S3, GCS and Azure Blob are object stores, and the `config.FS` contract meets them honestly rather
than pretending they are disks:

- **`Rename` is copy-then-delete.** There is no atomic rename. But `WriteFile` maps to `PutObject`
  and rename's copy maps to `CopyObject`, both of which are **atomic per object** — so the *target*
  is still replaced atomically and a reader never sees a half-written object. The only imperfection
  is that a crash between the copy and the delete leaves the staged object behind: garbage to be
  cleaned up, never corruption of the target. *(Revised — see R2 below: a store with a native
  atomic move uses it instead of copy-then-delete.)*
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

The result is passed to `WithFiles`/`NewFileBackend` exactly as `config.OS()` is. The core needs
**two** small additions: the exported sentinel `config.ErrReadOnlyFS` (D4) that the read-only
adapters return from their write methods, and the optional interface `config.PollIntervalHinter`
(R1) that a polled adapter implements to declare its own reload cadence. Everything else the
adapters need —
`config.FS`, `RealPather`, `LinkReader`, and the poll watcher that reloads a non-real-path
filesystem (D5) — already exists and is sufficient, proven by `config-afero` implementing the
interfaces against the public API.

## Testing strategy

Per adapter: an in-memory/fake-driven unit suite (D3, D8); for the cloud stores, an env-gated
emulator integration suite under testcontainers (D8); an allowlist `depfootprint` test (D7). The
umbrella is verified by the adapters passing the module's existing filesystem behaviour — a wrapped
filesystem must read, write, stage-and-rename, and preserve permissions exactly as `config.OS()`
does, which the module's own suite already asserts against any `config.FS`.

## Migration & compatibility

Additive for consumers: a consumer adds a module and passes its `Wrap` to `WithFiles`, exactly as
for `config-afero`. No core change; nothing breaks.

## Resolved (2026-07-22)

1. **The adapter set.** Six adapters: `config-iofs`, `config-billy`, `config-aws-s3`,
   `config-gcp-gcs`, `config-azure-blob`, `config-sftp`. A read-only `config-http` (URL fetch) and a
   `config-git` (file at a ref) were considered and **dropped** — HTTP overlaps with simply fetching
   the file yourself, and git-at-a-ref is genuinely niche; neither earns its surface yet. Either can
   be added later by a dated revision if a consumer needs it.
2. **Polling reload.** No new mechanism — the core **already** polls a filesystem without real
   paths (D5): `Watch` builds a `pollWatcher` for it, tuned by the existing `WithPollInterval`. So
   the remote adapters get opt-in polled reload for free; the shared convention is to rely on that
   and have each cloud adapter recommend a sensible (long) default interval, because a poll is a
   billed API call. This was verified against `watch.go` before deciding.
3. **Read-only write errors.** A shared **`config.ErrReadOnlyFS`** sentinel is added to the core
   (D4), returned by every read-only adapter's write methods, so a consumer can `errors.Is` it
   rather than guess from a bare `fs.ErrPermission`. It is the one small core addition this family
   needs.

## Implementation phases

**Phase 0 — this umbrella spec.**

**Phase 1 — the core precursor.** Add and release the exported `config.ErrReadOnlyFS` sentinel (D4)
that the read-only adapters depend on. Small and additive; everything else the family needs already
ships (the poll watcher, `RealPather`, `LinkReader`).

**Phase 2 — the POSIX-shaped adapters.** `config-iofs` (read-only, zero-dependency, over any
`io/fs.FS` including `embed.FS`) and `config-billy` (read+write), each its own approved spec then
module. They validate the family against the simplest cases and add no cloud surface.

**Phase 3 — the cloud object stores.** `config-aws-s3`, `config-gcp-gcs`, `config-azure-blob` — the
symmetric trio, each D2-gated, each with an emulator integration suite (LocalStack, fake-gcs-server,
Azurite; D8). The object-store atomicity model (D6) is proven here.

**Phase 4 — remote and revisit.** `config-sftp`. Anything the earlier adapters showed this umbrella
got wrong is corrected here by dated revision, never silently.

## Revisions

### R1 (2026-07-22) — a filesystem declares its poll cadence via an optional interface

**Revises D5.** D5 said the remote adapters get polled reload "for free" and owe only "a sensible
interval, not a mechanism", to be recommended in each adapter's spec. Finalising the per-adapter
specs exposed the gap: a recommendation in prose is not a cadence the adapter can actually apply.
The store hands every backend's `Watch` the resolved interval — `DefaultPollInterval` (2s) unless
the consumer set `WithPollInterval` — so a cloud adapter that "wants" 5 minutes has no way to say
so; it can only hope the consumer passes `WithPollInterval`, and a consumer who forgets silently
gets 2-second billed polling. That is the eager default D5 set out to avoid, reintroduced.

The fix is one narrow, optional interface in the core — the same shape as `RealPather` and
`LinkReader`, which is precisely the extensibility pattern the `config.FS` contract was designed
around (filesystem-abstraction spec D1):

```go
// PollIntervalHinter optionally reports how often a polled filesystem should be
// checked for changes.
type PollIntervalHinter interface {
	PollInterval() time.Duration
}
```

`NewWatcher` consults the hint **only where the caller left the default**: an explicit
`WithPollInterval` still wins, a filesystem that does not implement the interface keeps the
responsive 2-second default, and a remote adapter that does implement it gets its own sensible
cadence without the consumer having to know the number. This touches nothing in the store or the
remote-backend watch path, so it cannot alter any shipped backend's polling, and the existing
watch-conformance timing is unaffected. `config.PollIntervalHinter` joins `config.ErrReadOnlyFS`
(D4) as the second and final small core addition this family needs; each read+write cloud and
`config-sftp` adapter implements it to declare a minutes-scale default, and each per-adapter spec
states the value it picks.

### R2 (2026-07-22) — rename uses each store's native move where it has one

**Revises D6.** D6 modelled every object store's `Rename` as copy-then-delete on the grounds that
"there is no atomic rename". That is true of S3 and Azure Blob, but not universally: **GCS has a
native `Move`** (a server-side rename within a bucket) and **SFTP has `PosixRename`** (an atomic
rename extension). Copy-then-delete leaves a staged object behind on a crash between the two calls
(D6's stated imperfection); a native move has no such window and is a single round-trip. So the
decision is refined: **an adapter uses its store's native atomic move where the store has one, and
falls back to copy-then-delete only where it does not.**

- `config-gcp-gcs` — `Object.Move` (server-side, atomic).
- `config-sftp` — `PosixRename` (atomic rename extension), falling back to plain `Rename`.
- `config-aws-s3`, `config-azure-blob` — copy-then-delete, exactly as D6 describes, because these
  stores genuinely have no atomic rename. The orphaned-staging-object caveat applies to these two,
  and each states it.

The `config.FS` contract is unchanged — `Rename` is still the atomic commit — and so is the
conflict-detection story (SHA-256 at load, D6). Only the *implementation* of the commit differs per
store, which is exactly what a per-adapter spec is for (D2). Each adapter's spec names the primitive
it uses.

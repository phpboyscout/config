---
title: config-billy — wrapping a go-billy filesystem as config.FS
date: 2026-07-22
author: matt.cockayne
status: approved
approved: 2026-07-22
---

# config-billy

## Problem

The [filesystem adapters umbrella](2026-07-22-filesystem-adapters.md) establishes the family of
`config-<fs>` sibling modules and settles the decisions that cut across all of them, but by D2 it
deliberately specifies no single adapter. This is that per-adapter spec for **`config-billy`**, a
Phase 2 POSIX-shaped adapter (umbrella Implementation phases) that wraps a
[`go-billy`](https://github.com/go-git/go-billy) `billy.Filesystem` as a read+write
[`config.FS`](../../reference).

`go-billy` is the storage-agnostic filesystem abstraction underneath `go-git` and a large family of
Git tooling; a tool built on it already holds a `billy.Filesystem` — a real on-disk tree
(`osfs`), an in-memory one (`memfs`), a chrooted subtree, or its own implementation — and today it
cannot point this module at that filesystem. `config-billy` is the bridge, and it is the closest
sibling to [`config-afero`](https://gitlab.com/phpboyscout/go/config-afero): both wrap a
read+write filesystem *abstraction* a consumer chose, not a specific storage backend, so both are
pure translation of the six `config.FS` methods with no credentials and no network (umbrella D3).

The whole of the design work is one question the umbrella leaves open (D2): **can a
`billy.Filesystem` honestly report a real operating-system path, so `config-billy` may implement
`config.RealPather` and get native fsnotify hot-reload?** The answer, verified against the library,
is **no** — and that is what most of this spec settles.

## The contract

`config.FS` is six methods, and every one maps cleanly onto `go-billy`:

| `config.FS`  | `go-billy`                                        |
| ------------ | ------------------------------------------------- |
| `ReadFile`   | `util.ReadFile(bfs, name)`                        |
| `WriteFile`  | `util.WriteFile(bfs, name, data, perm)`           |
| `Stat`       | `bfs.Stat(name)` (`billy.Basic`)                  |
| `Rename`     | `bfs.Rename(old, new)` (`billy.Basic`)            |
| `Remove`     | `bfs.Remove(name)` (`billy.Basic`)                |
| `MkdirAll`   | `bfs.MkdirAll(path, perm)` (`billy.Dir`)          |

`billy.Filesystem` is `Basic + TempFile + Dir + Symlink + Chroot`, so every one of these methods is
part of the mandatory interface — no capability probing is needed for the six. `util.ReadFile` and
`util.WriteFile` (package `github.com/go-git/go-billy/v5/util`, same module) are the idiomatic
whole-file helpers; `util.WriteFile` creates with `perm` and truncates otherwise, matching
`os.WriteFile` and this module's `config.OS()`.

## Decisions

### D1 — `config-billy`, package `configbilly`, read+write, `Wrap(billy.Filesystem) config.FS`

One sibling module (umbrella D1), a bare vendor-neutral name (`go-billy` names no cloud). The public
surface is a single constructor mirroring `configafero.Wrap` and the API shape the umbrella prints:

```go
configbilly.Wrap(bfs) // billy.Filesystem → config.FS
```

The result is passed to `WithFiles`/`NewFileBackend` exactly as `config.OS()` is. It is
**read+write** (umbrella D4): a `billy.Filesystem` mandates the write methods, so all six work and
none returns `config.ErrReadOnlyFS` in the ordinary writable case. (A `billy` filesystem mounted
read-only is caught by inspecting `billy.Capabilities()`: a missing `WriteCapability` makes the
write methods return `config.ErrReadOnlyFS` up front — see Resolved (2026-07-22), item 4.)

### D2 — Map the six methods through `util`, staging and rename done by the Store

`config-billy` translates the six calls and nothing more. A write is committed the way the whole
module commits one — the Store stages content beside the target and `Rename`s over it, through the
`config.FS` methods — so the adapter needs no staging logic of its own. It maps `WriteFile` to
`util.WriteFile` and `Rename` to billy's **native `Rename`** — a real filesystem-level rename (the
umbrella's rename primitive, R2), never a copy-then-delete emulation — and the atomicity is whatever
the wrapped filesystem gives: on `osfs` a same-directory `Rename` is `os.Rename` (atomic on POSIX);
on `memfs` it is an in-process map swap (atomic within the process). Either way a reader never
observes a half-written file, which is the contract's promise (`fs.go`, `Rename` doc). The
*overwrite* semantics of `Rename` — whether renaming over an existing target succeeds or errors —
vary by billy backend, so the README documents the assumption (the Store stages beside the target
and renames over it, which osfs and memfs both honour).

`go-billy` *has* a `TempFile(dir, prefix)` method, but `config.FS` has no `TempFile` and the Store
builds the staging path itself, so the adapter does not use it — noted only so a future reader knows
it was considered and is available if the Store's staging model ever changes.

### D3 — Missing files satisfy `errors.Is(err, fs.ErrNotExist)`, verified

The load-bearing half of `ReadFile`'s contract (`fs.go`) is that an absent file returns an error
satisfying `errors.Is(err, fs.ErrNotExist)` — the Store treats a missing overlay as normal and a
missing base as fatal, and only that distinction lets it. This was **verified by execution** against
both backends: `util.ReadFile` and `Stat` on a missing name return, respectively,
`open …: no such file or directory` (osfs) and `file does not exist` (memfs), and
`errors.Is(err, fs.ErrNotExist)` is `true` for both. No error translation is needed in the adapter.

### D4 — No `config.RealPather`: `go-billy` cannot honestly report an OS path through its interface

**This is the central decision.** `config-afero` claims `config.RealPather` by type-switching on the
concrete afero types that genuinely have OS paths (`*afero.OsFs`, `*afero.BasePathFs`). The
equivalent is **not possible** through `billy.Filesystem`, verified against `go-billy` v5.9.0:

- **`Root()` exists on every `billy.Filesystem`** (it is part of the mandatory `billy.Chroot`
  interface) — but it returns a *logical* root, not necessarily an OS path. `osfs.New(dir).Root()`
  returns the real `dir`; `memfs.New().Root()` returns `"/"`, a real-looking path for files that
  exist only in memory. `Root()` therefore cannot be trusted as an OS path.
- **The concrete type does not distinguish them.** Both `osfs.New(dir)` (default) and `memfs.New()`
  surface as the *same* type, `*chroot.ChrootHelper`. A type switch — the mechanism `config-afero`
  relies on — cannot tell a disk-backed billy filesystem from an in-memory one.
- **Unwrapping does not rescue it.** `ChrootHelper.Underlying()` returns `*polyfill.Polyfill` for
  *both* osfs and memfs — another internal wrapper — so recursively unwrapping to find an `osfs`
  concrete type reaches into unexported helper internals and is version-fragile. Only the non-default
  `osfs.New(dir, osfs.WithBoundOS())` surfaces a distinguishable `*osfs.BoundOS`; the common paths do
  not.
- **Capabilities do not answer it.** `billy.Capable.Capabilities()` differs between osfs (`0b111111`)
  and memfs (`0b011111`) — but only by `LockCapability`. The capability flags describe file-I/O
  features (write/read/seek/truncate/lock), not whether a path is a real OS path.

`fs.go` is explicit that "an implementation must not satisfy an optional interface it cannot honour",
and that satisfying `RealPather` unconditionally "makes every filesystem look watchable, so native
notification is selected and the absence of anything to watch is discovered one path at a time." A
`billy.Filesystem` gives no reliable interface-level signal to distinguish the honourable case, so
`config-billy` **omits `RealPather` entirely** and is polled.

### D5 — Hot-reload via the core poll watcher (umbrella D5), no new mechanism

Because it claims no real path, a wrapped billy filesystem is watched by the core's `pollWatcher`
(umbrella D5, verified there against `watch.go`): `Watch` re-reads on the existing
`WithPollInterval` cadence (`DefaultPollInterval`, 2s) and stays quiet when nothing resolves
differently. This is free and needs no per-adapter watch API. The 2s default is fine for the common
cases — an `osfs` tree and an in-process `memfs` both poll cheaply, unlike a billed cloud API — so
`config-billy` recommends no special interval. Because it wraps a *local* filesystem (in-memory
`memfs` or on-disk `osfs`), not a billed remote store, it does **not** implement
`config.PollIntervalHinter` and keeps the responsive default cadence.

### D6 — Footprint is exactly one module: `github.com/go-git/go-billy/v5`

Measured with `go list -deps` (umbrella D7): the production library graph
(`configbilly` importing `billy` + `billy/v5/util`, `util` being in the same module) adds **exactly
one** third-party module, `github.com/go-git/go-billy/v5`, and nothing else. This is asserted by an
allowlist `depfootprint` test copied from `config-afero`. `go-billy` is the "small one" the umbrella
predicts (D7).

The disk-backed `osfs` sub-package additionally pulls `github.com/cyphar/filepath-securejoin` and
`golang.org/x/sys`, but `osfs` is only reached in tests — and D7 (below) keeps even the test suite
on `memfs`, which pulls nothing extra — so those never enter the library graph a consumer inherits.

### D7 — Tests run against billy's `memfs`, matching umbrella D8

The unit suite drives the adapter against `memfs.New()` — billy's in-memory filesystem, named as
this adapter's fake by umbrella D8 — needing no disk and no network. `memfs` exercises every one of
the six methods (read, write, stat, rename-over-target, remove, mkdirall) and preserves the write
`perm` (verified: a `0o600` write reads back mode `-rw-------`). A small companion set may wrap an
`osfs.New(t.TempDir())` to prove the adapter is agnostic to the concrete billy backend, but the core
coverage — and the negative `RealPather` assertion — lives on `memfs`.

## Rejected alternatives

**Claim `RealPather` and lean on the watcher's `os.SameFile` guard.** The watcher does confirm a
claimed real path with `os.SameFile` before pointing fsnotify at it and falls back to polling per
path when they disagree (`fs.go`, `RealPather` doc). But `fs.go` calls satisfying the interface
unconditionally exactly the mistake — it discovers the absence of anything to watch "one path at a
time" instead of up front, and `config-afero`'s own suite asserts an in-memory filesystem must *not*
claim it. `config-billy` gets the same negative test (D7). See D4.

**Unwrap `ChrootHelper`/`Polyfill` to sniff for a real `osfs` underneath.** Reaches through two
layers of internal wrappers into unexported state, tied to go-billy's current wrapping and fragile
across versions — for a signal (D4) that even when found only covers the non-default `WithBoundOS`
construction. Polling is correct, cheap for both osfs and memfs, and version-proof.

**Build staging/TempFile logic into the adapter.** The Store already stages-and-renames through the
six methods; duplicating it in the adapter would fork the atomicity story. `billy.TempFile` is left
unused (D2).

**A `config-git` adapter reading a file at a Git ref, folded in here.** Out of scope by the umbrella
(Resolved 1): git-at-a-ref was considered and dropped as too niche. `config-billy` wraps the billy
*filesystem*, not a git object database.

## Public API

```go
package configbilly

// Wrap adapts a go-billy filesystem to config.FS. The result reads and writes
// through the wrapped filesystem and is watched by polling — a billy.Filesystem
// cannot honestly report an operating-system path (see the spec's D4), so it does
// not satisfy config.RealPather.
func Wrap(bfs billy.Filesystem) config.FS
```

`Wrap` returns a single unexported adapter type satisfying `config.FS` and *only* `config.FS` — no
four-way type ladder as in `config-afero`, because `RealPather` is the only optional interface it
declines (D4); `config.LinkReader` it claims unconditionally, since `Readlink` is mandatory on every
`billy.Filesystem` (Resolved (2026-07-22), item 2). The core needs no addition: everything used
(`config.FS`, the poll watcher) already ships, and `config.ErrReadOnlyFS` (umbrella Phase 1) is not
returned because the adapter is read+write.

Compile-time assertions mirror `config-afero`:

```go
var _ config.FS = adapter{}
```

## Testing strategy

- **Unit suite on `memfs`** (D7, umbrella D8): read, write (with `perm` preserved), stat,
  stage-and-`Rename`-over-target, remove, mkdirall — the same behaviours the module's own suite
  asserts against any `config.FS` (umbrella Testing strategy), driven here through `Wrap(memfs.New())`.
- **The negative `RealPather` assertion** — the reason the module exists rather than being a handful
  of lines each consumer writes: `Wrap(memfs.New())` must **not** satisfy `config.RealPather`
  (copied in spirit from `config-afero`'s `TestWrap_InMemoryDoesNotClaimRealPaths`), and polling
  must still deliver a change over a filesystem fsnotify cannot see, exercised through
  `config.NewWatcher(wrapped, …).Watch(…)`.
- **Missing-file error** (D3): `ReadFile`/`Stat` of an absent name satisfy
  `errors.Is(err, fs.ErrNotExist)`.
- **Backend-agnostic spot check**: a subset run against `osfs.New(t.TempDir())` to prove the six
  methods work identically over the disk-backed billy filesystem.
- **`depfootprint` allowlist test** (D6): the library graph adds only `github.com/go-git/go-billy/v5`.

`testify` is a test-only dependency and does not reach consumers, exactly as in `config-afero`.

## Resolved (2026-07-22)

All four open questions were answered by the human on 2026-07-22. Each records what was decided and
why, so the choice rests on facts rather than intuition; no decision above is renumbered.

1. **Opt-in real-path escape hatch — deferred.** v0.1.0 ships **no** `WrapReal`/disk-backed opt-in.
   D4 omits `RealPather` because `billy.Filesystem` cannot prove an OS path, and polling already
   works for *every* billy filesystem today — an `osfs` tree and an in-process `memfs` both poll
   cheaply (D5). Restoring native fsnotify for the known-on-disk case would mean a second constructor
   a caller can lie through, and even then the watcher's `os.SameFile` guard would confirm the
   claimed path and degrade a lie back to polling per path — so the escape hatch buys little over the
   default. Deferrable to a dated revision if a real demand appears; never a silent addition.

2. **`LinkReader` — claim it unconditionally.** `billy.Filesystem` mandates `Readlink` (via the
   `billy.Symlink` interface), so `config-billy` satisfies `config.LinkReader` for **every** wrapped
   filesystem — unlike afero, where the claim is conditional. Where the target is not a link,
   `Readlink` returns an honest negative — `memfs` a "not a symlink" error, `osfs` an
   `invalid argument` error — and the Store must fall back to using the path as given. This is not
   assumed: an acceptance test asserts exactly that fallback (a regular file wrapped, written through
   its plain path) so the claim rests on **observed** Store behaviour on a `Readlink` error, not on
   an untested presumption.

3. **Path convention — documentation, no normalisation.** `go-billy` names are forward-slash and
   interpreted *relative to the wrapped filesystem's root* (an `osfs.New("/etc/app")` reads
   `"config.yaml"`, not `"/etc/app/config.yaml"`) — like `config.Dir`, unlike `config.OS()`. Names
   pass through `Wrap` **unchanged**: the adapter does not normalise or validate them, and the Store
   composes paths itself. The README states the relative-to-root convention plainly so a consumer
   does not pass an absolute OS path expecting `config.OS()` semantics. Documentation is sufficient;
   silent normalisation would only hide the mismatch it is meant to prevent.

4. **Runtime-read-only billy filesystems — map to `config.ErrReadOnlyFS`.** `config-billy` inspects
   `billy.Capabilities()` and, when `WriteCapability` is absent, its write methods return
   `config.ErrReadOnlyFS` **up front** — so from a consumer's `errors.Is` perspective a read-only
   filesystem is a read-only filesystem whether it is read-only *by design* (`config-iofs`) or
   *by mount*. This deliberately widens the umbrella D4 sentinel's use slightly beyond by-design
   adapters, for one consistent read-only story across the family rather than a raw underlying error
   for one case and a sentinel for another. A write to a genuinely *writable* filesystem that then
   fails still surfaces the underlying error unchanged — the mapping applies only where the
   capability is absent, not to every write failure.

**No open questions remain.**

## Implementation phases

**Phase 0 — this spec**, approved 2026-07-22 with all four open questions resolved (Phase 2 of the
umbrella).

**Phase 1 — the module.** Scaffold `config-billy` from `config-afero`'s layout (go.mod, CI, lint,
justfile, renovate, LICENSE, README skeleton). Implement `Wrap` and the single six-method adapter
over `util.ReadFile`/`util.WriteFile`/`Stat`/`Rename`/`Remove`/`MkdirAll` (D2). Land the resolved
decisions — unconditional `LinkReader`, the `WriteCapability`→`config.ErrReadOnlyFS` mapping, and
the relative-to-root path convention (Resolved (2026-07-22), items 2–4) — into the code and the
README before merge.

**Phase 2 — tests.** The `memfs` unit suite, the negative `RealPather` assertion, the missing-file
error test, the `osfs` backend-agnostic spot check, and the `depfootprint` allowlist (D6, D7).

**Phase 3 — docs & release.** A README page (bundled into the parent config docs site,
page-per-adapter, as the other adapters are), then the v0.1.0 release MR. Any real-path escape hatch
(deferred, Resolved (2026-07-22), item 1) is a later dated revision, never a silent addition.

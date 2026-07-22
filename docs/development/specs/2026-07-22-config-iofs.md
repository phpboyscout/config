---
title: config-iofs — wrapping any io/fs.FS as a read-only config.FS
date: 2026-07-22
author: matt.cockayne
status: approved
approved: 2026-07-22
revised: 2026-07-22
---

# config-iofs

The first filesystem adapter, and the trivial case the [filesystem adapters
umbrella spec](2026-07-22-filesystem-adapters.md) names as the one whose spec is
"a page": a read-only wrapper over a stdlib interface, with **zero**
dependencies. Per the umbrella's **D2**, no adapter is built without its own
approved spec; this is that spec for `io/fs`. It cites the umbrella and settles
the little the umbrella leaves to an adapter this simple.

## Problem

A large and growing share of configuration lives on an `io/fs.FS`: **compiled
into the binary** via `embed.FS` — the headline case, a tool shipping its own
defaults — or in a `zip`/`tar` archive (`archive/zip`, `archive/tar`), under an
`os.DirFS`, or in an `fstest.MapFS` a test builds. Every one of those satisfies
`io/fs.FS`, and the module can demonstrably read through any `config.FS`, but it
cannot point at an `io/fs.FS` today: `config.FS` has six methods and `io/fs.FS`
has none of them by that name.

`io/fs` is read-only *by design* — it has no write, rename or remove — so the
adapter is read-only (umbrella **D4**), and there is exactly one honest way to
build it: map the two read methods onto the two stdlib helpers, and fail every
write loudly. That is the whole module.

## Decisions

### D1 — Module `config-iofs`, package `configiofs`

A bare, vendor-neutral name (umbrella D1): `io/fs` is a stdlib interface, not a
cloud, so there is nothing to qualify. The module is
`gitlab.com/phpboyscout/go/config-iofs`, package `configiofs`, matching the
`config-<x>` / `configx` convention every adapter uses. The repo carries a
README only; this spec lives centrally here (umbrella D2). It requires the
`config` release that ships the exported **`config.ErrReadOnlyFS`** sentinel
(umbrella Phase 1) — the one core symbol this adapter depends on.

### D2 — Map ReadFile→`fs.ReadFile`, Stat→`fs.Stat`; let the stdlib dispatch

`Wrap(fsys fs.FS)` returns an adapter whose `ReadFile(name)` calls
`fs.ReadFile(fsys, name)` and whose `Stat(name)` calls `fs.Stat(fsys, name)` —
the two top-level `io/fs` helpers. Those helpers **already** use the underlying
filesystem's optimisation interface when it has one (`fs.ReadFileFS`,
`fs.StatFS`) and fall back to `Open` otherwise, so the adapter must **not**
type-switch on those interfaces itself — doing so would reimplement, worse, a
dispatch the standard library performs for free. Verified: `embed.FS` implements
`ReadFileFS` but *not* `StatFS`; `os.DirFS` and `fstest.MapFS` implement both;
`*zip.Reader` implements neither — and `fs.ReadFile`/`fs.Stat` serve all four
identically because the fallback is transparent.

A missing name yields an error satisfying `errors.Is(err, fs.ErrNotExist)`
(verified — `fs.ReadFile` on an absent name returns exactly that), which is the
contract `config.FS.ReadFile` requires so the Store can tell a normal missing
overlay from a broken installation.

**Names are `io/fs` names, not OS paths.** `io/fs` uses unrooted,
forward-slash-separated names with no leading slash (`fs.ValidPath` rejects a
leading `/`, a `.` or `..` segment, an empty name and a trailing slash — but not
a backslash, which is an ordinary byte in an `io/fs` path element; see R1). A consumer wrapping an `embed.FS`
passes the embedded name — `"defaults/config.yaml"`, not `"/etc/app.yaml"` —
exactly as they would to `embed.FS.ReadFile` directly. The adapter passes the
name through unchanged; it does not rewrite, root, or clean it. Per Resolved
(2026-07-22), an `io/fs`-invalid name is rejected early with an `fs.ErrInvalid`-
wrapping error rather than allowed to surface as `fs.ErrNotExist`.

### D3 — Read-only: writes return `config.ErrReadOnlyFS`

The four write methods — `WriteFile`, `Rename`, `Remove`, `MkdirAll` — each
return **`config.ErrReadOnlyFS`** (umbrella D4), the shared sentinel a consumer
can `errors.Is` regardless of which read-only filesystem produced it, rather than
a bare `fs.ErrPermission` a genuine permission error is indistinguishable from. A
write routed at a compiled-in `embed.FS` fails loudly at the point of the write,
which is correct: there is nothing to write to.

### D4 — One plain type: not a `RealPather`, not a `LinkReader`

Unlike `config-afero`, which returns one of four types to claim `RealPather`
and/or `LinkReader` per wrapped filesystem, `config-iofs` returns a **single**
type that satisfies neither optional interface. `io/fs` exposes no
operating-system path (that is the whole point of the abstraction), so the
adapter cannot honour `RealPather` and, per the interface's own contract, must
not satisfy it — a filesystem that returns `false` from `RealPath` still looks
watchable and sends the fsnotify watcher after nothing. Because it is not a
`RealPather`, a wrapped filesystem is **polled** when watched (umbrella D5), via
the core's existing `pollWatcher` and `WithPollInterval` — no new mechanism, and
nothing to configure. `io/fs` has no symlink resolution either, so no
`LinkReader`.

For the headline case this is ideal: an `embed.FS` **never changes** at runtime,
so watching it is pointless — but harmless, because a poll that always re-reads
the same bytes simply stays quiet. The adapter does **not** implement the core's
`config.PollIntervalHinter`; it keeps the default poll cadence, because an
arbitrary `io/fs.FS` could be dynamic and there is no billed-call cost (unlike the
cloud backends) to justify slowing the default down.

### D5 — Zero dependencies

`config-iofs` adds **nothing** beyond the `config` graph (umbrella D7): it
imports only `io/fs` (stdlib) and `config`. A `depfootprint_test.go` allowlist —
copied from `config-afero`'s — asserts exactly the `config` transitive set and
no more, so a dependency creeping in via a version bump is a failing test. The
sentinel package that proves the scan ran is `gitlab.com/phpboyscout/go/config`
itself (there is no third-party module being adapted, unlike afero).

### D6 — Testing: `fstest.MapFS` for the units, an `embed.FS` fixture for the real thing

The unit suite drives the adapter over an in-memory **`fstest.MapFS`** (umbrella
D8) — reads, stat, a missing name asserting `fs.ErrNotExist`, and each write
method asserting `errors.Is(err, config.ErrReadOnlyFS)`. A second, small
suite wraps a **real `embed.FS`** fixture compiled into the test binary — the
headline use — proving that the module reads compiled-in defaults exactly as it
reads a `MapFS`, and that `embed.FS`'s missing `StatFS` is served by the
`fs.Stat` fallback (D2). Negative assertions — that the returned type is *not* a
`config.RealPather` and *not* a `config.LinkReader` — cover D4, mirroring how
`config-afero` tests its plain adapter's absence of capability. There is nothing
to emulate, so there is no integration suite (unlike the cloud adapters, umbrella
D8).

## Rejected alternatives

**A `config-embed`, `config-zip` or `config-tar` module.** `embed.FS`,
`*zip.Reader` and a `tar` filesystem all expose `io/fs.FS`, so one read-only
stdlib adapter covers the whole ecosystem — the umbrella rejects these by name,
and this module is why.

**Type-switching on `fs.ReadFileFS` / `fs.StatFS` inside the adapter.** The
stdlib `fs.ReadFile`/`fs.Stat` helpers already perform that dispatch and fall
back to `Open`; re-doing it here would be a slower, buggier copy of code that
ships in the standard library (D2).

**Sniffing `os.DirFS` to become a `RealPather`.** An `os.DirFS` *is* backed by
real OS paths, and its root is technically recoverable (its concrete type is a
`string`-kinded `os.dirFS`, readable by reflection — verified). Rejected: it is
an unexported type with no stable contract, sniffing it is exactly the
concrete-type guessing the `RealPather` doc warns against, and a consumer who
wants native watching over a real directory already has the right tool —
**`config.Dir`**, which is a `RealPather` by construction. `config-iofs` is for
the cases that genuinely have no OS path. (Confirmed in Resolved (2026-07-22):
documentation is sufficient, and no opt-in real-path bridge ships in v0.1.0.)

**Making it writable via a copy-on-write overlay.** `io/fs` is read-only by
design; synthesising writes into a side buffer would invent a filesystem the
consumer did not ask for and could never persist. A consumer needing writes wraps
a writable filesystem (`config-afero` over `afero.MemMapFs`, or `config.OS`),
not this (umbrella D4).

## Public API

```go
// Wrap adapts any io/fs.FS to a read-only config.FS. ReadFile and Stat map onto
// fs.ReadFile and fs.Stat; the write methods return config.ErrReadOnlyFS.
func Wrap(fsys fs.FS) config.FS
```

Used exactly as `config.OS()` is:

```go
//go:embed defaults/config.yaml
var defaults embed.FS

store, err := config.NewStore(ctx,
	config.WithFiles(configiofs.Wrap(defaults), "defaults/config.yaml"))
```

The result satisfies `config.FS` and, deliberately, neither `config.RealPather`
nor `config.LinkReader` (D4). No new core surface is added by this module; the
one symbol it needs — `config.ErrReadOnlyFS` — is the umbrella's Phase-1 core
precursor, not this adapter's to define.

## Testing strategy

Per D6: an `fstest.MapFS` unit suite (read, stat, missing-is-`ErrNotExist`, each
write-is-`ErrReadOnlyFS`); an `embed.FS`-fixture suite proving the headline case
and the `StatFS` fallback; negative capability assertions (not a `RealPather`,
not a `LinkReader`); and a `depfootprint_test.go` allowlist asserting the empty
footprint (D5). What would falsely pass: a write test asserting only "an error"
rather than `errors.Is(err, config.ErrReadOnlyFS)` — the suite asserts the
sentinel specifically, so a future refactor that returned a bare
`fs.ErrPermission` would fail.

## Migration & compatibility

Purely additive for consumers: add the module and pass `configiofs.Wrap(fsys)`
to `WithFiles`, exactly as for `config-afero`. No `config` core change beyond the
umbrella's already-planned `ErrReadOnlyFS` precursor; nothing breaks. The module
ships read-only at v0.1.0; because `io/fs` can never gain write, there is no
later read→write promotion to communicate.

## Resolved (2026-07-22)

All open questions were answered by the human on 2026-07-22, and the decisions
above stand as drafted. Each records what was verified, so the choice rests on
facts.

1. **Invalid-name handling — validate and surface the mistake** (sharpens D2).
   `Wrap`'s `ReadFile` and `Stat` call `fs.ValidPath(name)` **first** and, for an
   invalid name — a leading slash, a `..` segment, a trailing slash — return a distinct
   error that wraps `fs.ErrInvalid`, deliberately **not** one satisfying
   `fs.ErrNotExist`. Rationale: the Store treats `fs.ErrNotExist` as a *normal*
   missing optional source, so without this check a fat-fingered absolute path
   (`"/etc/app.yaml"`) would silently read as "absent" instead of "you passed a
   bad name", hiding the mistake behind ordinary optional-source semantics. A
   valid-but-absent name is untouched: it still returns the `fs.ErrNotExist`-shaped
   error `fs.ReadFile` produces, unchanged, so the Store's missing-overlay path
   keeps working exactly as D2 describes. The two cases are now distinguishable —
   a bad name is a caller error, an absent name is a normal one.

2. **`os.DirFS` watchability — documentation is sufficient.** `config-iofs` over
   an `os.DirFS` is polled even though the directory has real OS paths behind it.
   The guidance already recorded in Rejected — "use `config.Dir` for a real
   directory", which is a `RealPather` by construction and watches natively — is
   the right answer, and the README states it. Rationale: an opt-in real-path
   bridge would resurrect exactly the unexported-type sniffing D4's Rejected entry
   turns down, for a consumer who by definition already has the correct tool to
   hand. No such bridge ships in v0.1.0; if a concrete need appears later it can be
   added by a dated revision here, but nothing today justifies the surface.

3. **A scoping helper — none.** `config-iofs` adds no convenience wrapper for
   rooting a sub-filesystem. Composing `fs.Sub(fsys, dir)` before `Wrap` already
   produces exactly the scoped `config.FS` a consumer wants, with no help from this
   module, so a helper would only wrap a one-liner the standard library already
   provides. The README shows that `fs.Sub` one-liner instead. This keeps the
   module to its single honest job (D2) — map two read methods, fail the writes —
   and adds no API the stdlib does not already cover.

**No open questions remain.**

## Implementation phases

**Phase 0 — this spec.** Approved 2026-07-22, with its open questions resolved
(see Resolved (2026-07-22)). Blocked on the umbrella's Phase 1
(`config.ErrReadOnlyFS`) being released before Phase 1 here can start.

**Phase 1 — the module.** Scaffold `config-iofs` (README-only, no microsite;
umbrella D2 / adapter docs model), `Wrap` returning the single read-only type
(D2, D4), the four write methods returning `config.ErrReadOnlyFS` (D3), the
`fstest.MapFS` and `embed.FS` suites and the negative capability assertions (D6),
and the `depfootprint` allowlist asserting the empty footprint (D5). Then publish
v0.1.0: the module `main`, the `config` docs page bundled into the parent site,
and the landing card — the same rollout every adapter takes.

## Revisions

### R1 (2026-07-22) — `fs.ValidPath` does not reject a backslash

**Corrects D2 and Resolved item 1.** Both stated, in a parenthetical, that
`fs.ValidPath` rejects a backslash "verified". Implementing the invalid-name
check falsified that: on the `io/fs` abstraction a backslash is an ordinary byte
in a path element, so a name containing one is **valid** and reads as
valid-but-absent, not rejected. The decision is unchanged — invalid
names (a leading `/`, a `.` or `..` segment, an empty name, a trailing slash) are
still rejected early with an `fs.ErrInvalid`-wrapping error — only the incorrect
backslash claim is removed. A backslash is correctly treated as a valid `io/fs`
name that simply does not exist. Caught during implementation; the shipped
`config-iofs` behaves as this revision describes.

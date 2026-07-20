---
title: Filesystem abstraction — a narrow interface owned by this module
date: 2026-07-20
author: matt.cockayne
status: approved
approved: 2026-07-20
revised: 2026-07-20
issue: phpboyscout/go/config#3
---

# Filesystem abstraction

## Revisions

### R1 — 2026-07-20: the consumer testing story is `Dir`, not an afero adapter

**D4 said `MemMapFs` remains what the testing guide recommends. For this module's own suite
that stands. For consumers it was wrong, and it was wrong in a way only implementing it
exposed.**

Dropping afero from the public API left consumers with no in-memory filesystem and no
adapter, because `config-afero` (D5) does not exist yet. Every test example in the guides
became uncompilable, which is how it surfaced.

`Dir` — introduced by D3 in the same spec — turns out to be the better answer, and it needed
writing the docs to notice:

```go
fsys, err := config.Dir(t.TempDir())
require.NoError(t, err)
require.NoError(t, fsys.WriteFile("config.yaml", []byte(body), 0o600))
```

Zero dependencies, real file semantics including watching, and cleanup handled by
`t.TempDir`. For a consumer testing their own configuration handling it is better than an
in-memory filesystem, not merely adequate: it exercises the same code path production does.

D4 is amended to: **`Dir(t.TempDir())` is the recommended consumer testing story.** afero
remains this module's own test dependency, because the suite is large, parallel and benefits
from staying off disk, and because it already has it.

D5 is unaffected but less urgent. `config-afero` now serves consumers who already hold an
`afero.Fs` and want to pass it through, rather than being the only route to a testable
filesystem.

### R2 — 2026-07-20: `Readlink` joins `RealPath` as an optional interface

Not specified, and found while migrating: `resolveTarget` resolves symlinks so a write goes
through a link rather than replacing it, using afero's `LinkReader`. The six-method
interface has no equivalent and should not grow one — most filesystems have no notion of a
link.

Added as `LinkReader`, a second optional interface alongside `RealPather`, on the same
principle: capability beyond the minimum is something an implementation may simply not have.

This is evidence for open question 3. Two optional interfaces appeared where the spec
anticipated one, so the pattern is load-bearing rather than incidental, and the six required
methods held.

### R3 — 2026-07-20: an implementation must not satisfy an optional interface it cannot honour

Found by a failing test, and worth recording because the mistake is easy and the failure is
quiet.

The first afero adapter implemented `RealPath` unconditionally, returning `("", false)` for
in-memory filesystems. But `realPathResolver` asks whether the *type* satisfies `RealPather`,
so every filesystem looked watchable: the watcher selected fsnotify and discovered there was
nothing to watch one path at a time, rather than choosing polling up front.

The fix is two concrete types — an afero filesystem that has real paths satisfies the
interface, and one that does not, does not. `RealPather`'s documentation now states this as a
requirement on implementations rather than leaving it implied.


## Problem

`afero.Fs` is in the public API. `WithFiles`, `NewFileBackend` and `NewWatcher` all take one,
so every consumer of this module imports afero whether or not they wanted it, and every
adapter module will inherit that decision the moment it is published.

Three things make it worth revisiting now rather than later.

**It is 23% of the dependency graph.** afero brings six of the twenty-six non-stdlib packages
this library links — itself, and `golang.org/x/text`, which nothing else pulls. The landing
page claims a smaller graph than the incumbent's while doing more; six packages for a
filesystem seam is the largest single item in it.

**We use very little of it.** Roughly eight operations: `ReadFile`, `WriteFile`, `Stat`,
`Rename`, `Remove`, `MkdirAll`, `OpenFile`, `ReadDir`. That is a narrow interface pretending
to be a broad dependency.

**Two concrete type-switches leak through the abstraction.** `realPathResolver` switches on
`*afero.OsFs` and `*afero.BasePathFs` to decide whether a path is one the operating system
can watch. The code comment already half-apologises for it. An interface whose users must
inspect concrete implementations is not doing its job.

The timing argument is the decisive one. Every format adapter takes a filesystem in its
constructor. Deciding this after publishing eight of them means changing eight public APIs.

## What the alternatives can actually do

Measured, not assumed.

| | Read | Write | Rename | In-memory for tests | Non-stdlib packages |
|---|---|---|---|---|---|
| `io/fs.FS` | yes | **no** | **no** | yes (`fstest.MapFS`) | 0 |
| `os.Root` (Go 1.24+) | yes | yes | yes | **no** — concrete type | 0 |
| `afero.Fs` | yes | yes | yes | yes (`MemMapFs`) | **6** |

**`io/fs` is read-only by design** and has no write, rename or remove. It cannot support the
write path at all, which rules it out as the sole abstraction — this module's headline
feature is writing.

**`os.Root` has everything.** In Go 1.26 it offers `ReadFile`, `WriteFile`, `Stat`, `Lstat`,
`Rename`, `Remove`, `RemoveAll`, `Chmod`, `MkdirAll`, `OpenFile`, `Readlink` and `Symlink` —
a complete match for what this module needs, and hardened against traversal escaping the
root, which is a security property afero does not offer. But it is a concrete struct with no
interface and no in-memory implementation, so it cannot be substituted in a test. The
existing suite is built almost entirely on `MemMapFs`.

Neither stdlib option is sufficient alone. That is precisely why afero exists, and it is why
the answer is an interface of our own rather than adopting one of them wholesale.

## Decisions

### D1 — The module defines its own filesystem interface

```go
// FS is the filesystem surface this module needs. It is deliberately small:
// eight operations, all of which a caller can implement over os, os.Root,
// afero, or anything else.
type FS interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm fs.FileMode) error
	Stat(name string) (fs.FileInfo, error)
	Rename(oldpath, newpath string) error
	Remove(name string) error
	MkdirAll(path string, perm fs.FileMode) error
}
```

Named for what it is rather than what it wraps. A consumer supplies whatever satisfies it,
and the module states the whole of what it will ever call.

`ReadDir` and `OpenFile` are deliberately absent: both have exactly one caller each today,
and both are expressible through the six above. Adding a method to this interface later is a
breaking change for every implementation, so it starts as small as the module can justify.

### D2 — The concrete type-switch becomes an optional interface

> **Extended by [R2](#r2--2026-07-20-readlink-joins-realpath-as-an-optional-interface) and
> [R3](#r3--2026-07-20-an-implementation-must-not-satisfy-an-optional-interface-it-cannot-honour).**
> A second optional interface was needed for symlinks, and satisfying one conditionally
> turns out to be a requirement rather than a detail.

```go
// RealPather optionally reports the operating-system path backing a name, so
// native filesystem notification can be used instead of polling. A filesystem
// with no OS path — an in-memory one, a network abstraction — does not
// implement it, and watching falls back to polling.
type RealPather interface {
	RealPath(name string) (realPath string, ok bool)
}
```

This replaces `realPathResolver`'s switch on `*afero.OsFs` and `*afero.BasePathFs` with a
question the filesystem answers about itself. It is strictly better than what it replaces:
the current code cannot see through a wrapper it does not know about, so a consumer wrapping
`OsFs` in their own decorator silently loses native notification and gets polling with no
diagnosis.

`isReallyOnDisk` stays. A filesystem claiming a real path is still verified with
`os.SameFile` before fsnotify is pointed at it, because a base-path wrapper over an in-memory
filesystem produces real-looking paths for files that exist only in memory.

### D3 — The module ships one implementation: the real filesystem

```go
// OS returns an FS backed by the operating system.
func OS() FS

// Dir returns an FS rooted at a directory, backed by os.Root, so no operation
// can escape it.
func Dir(path string) (FS, error)
```

`Dir` is the interesting one: `os.Root` refuses any path resolving outside its root, which is
a containment property nothing in the current design offers. A tool reading configuration
from a user-supplied directory gets that for free.

Both are stdlib-backed and cost nothing in the dependency graph.

### D4 — afero moves to test scope, and stays the recommended testing story

> **Amended by [R1](#r1--2026-07-20-the-consumer-testing-story-is-dir-not-an-afero-adapter).**
> It stays this module's own testing story. For consumers the answer is `Dir(t.TempDir())`,
> which needed writing the docs to notice.

The module's own tests keep using afero's `MemMapFs` through a small adapter in
`export_test.go` or a testing helper. A test-only import does not appear in
`go list -deps .`, so it does not reach a consumer's graph and `depfootprint_test.go`
continues to measure what a consumer actually inherits.

`MemMapFs` remains what the testing guide recommends, because it is genuinely the best
in-memory filesystem in Go and writing a worse one to avoid a test dependency would be
misplaced purity. The change is where it sits, not whether it is used.

### D5 — An afero adapter ships as `config-afero`, and is about twenty lines

Consumers with an existing `afero.Fs` need an adapter. It is small enough to document inline,
and shipping it means nobody writes it twice or writes it subtly wrong:

```go
config.NewStore(ctx, config.WithFiles(aferofs.Wrap(myAferoFs), "/etc/app.yaml"))
```

Placed in its own module for the same reason the format adapters are: a consumer who does not
use afero should not acquire it.

### D6 — This ships in the core module, in the same release as the Store architecture

Not a prerequisite to be scheduled, but part of the core delivery: `config.FS` lands in the
`config` package alongside the Store rewrite and YAML support, and the format adapters follow
after it.

Two reasons, and the second is the stronger one.

Every adapter takes a filesystem in its constructor, so publishing eight modules against
`afero.Fs` and changing them afterwards is the same work done twice plus eight consumer
migrations.

More importantly, **`go-tool-base` depends entirely on this module today and has to migrate
to the Store architecture regardless.** `WithFiles` changing its parameter type is a break for
it either way. Landing both in one release means it migrates once; landing them separately
means it migrates twice, for no benefit, against a module it cannot avoid.

The delivery order is therefore: core Store plus YAML plus `config.FS` in one release, then
adapters as fast follows.

## Rejected alternatives

**Keep `afero.Fs`.** Zero migration, no adapters, and the testing story needs no explanation.
Rejected on the public-API argument rather than the dependency count: six packages is
meaningful but survivable, whereas forcing every consumer and every adapter module to import
a specific third-party filesystem library is a decision the module should not be making on
their behalf.

**Adopt `io/fs.FS`.** The obvious stdlib answer, and impossible: it has no write. A
read-only interface cannot express a module whose headline feature is writing configuration
back.

**Adopt `os.Root` directly.** Complete, stdlib, security-hardened. Rejected because it is a
concrete type — there is no way to substitute an in-memory implementation, and a test suite
that must touch real disk for every case is slower, harder to parallelise, and worse at
simulating failure. `Dir` (D3) gives consumers the benefit without imposing the constraint.

**A larger interface mirroring `afero.Fs`.** Would make adapters trivial and migration a
rename. Rejected because it re-creates the problem: a broad interface every implementation
must satisfy in full, most of which this module never calls.

## Public API

**Changed** — the filesystem parameter, in three places:

```go
func WithFiles(filesystem FS, paths ...string) StoreOption      // was afero.Fs
func NewFileBackend(filesystem FS, path string) Backend         // was afero.Fs
func NewWatcher(filesystem FS, interval time.Duration) Watcher  // was afero.Fs
```

**Added:** `FS`, `RealPather`, `OS()`, `Dir(path)`.

**Removed:** the afero import from the library graph. Six packages, leaving twenty.

## Testing strategy

**The existing suite is the proof.** Every test constructs a filesystem; if they pass with an
afero adapter substituted at the boundary and nothing else changed, the interface covers what
the module actually needs. A test requiring more than the six methods means the interface is
too small and the gap is found before consumers find it.

**`Dir` needs containment tests**: a path escaping the root via `..`, an absolute path, and a
symlink pointing outside must all be refused. These are `os.Root`'s guarantees rather than
ours, but they are the reason `Dir` exists and a regression in the Go version would otherwise
pass silently.

**`RealPather` needs the negative case**: a filesystem not implementing it must degrade to
polling and still deliver change notifications, and a filesystem *claiming* a real path that
does not match must be caught by `isReallyOnDisk` and also degrade.

## Migration & compatibility

Breaking, for anyone calling `WithFiles`, `NewFileBackend` or `NewWatcher`.

For the overwhelmingly common case — the real filesystem — the change is smaller than it
sounds:

```go
config.WithFiles(afero.NewOsFs(), "/etc/app.yaml")   // before
config.WithFiles(config.OS(), "/etc/app.yaml")       // after
```

For a consumer with an existing `afero.Fs` to pass through, `aferofs.Wrap` (D5) is a
one-line change at the call site.

This lands in the same release as the Store architecture migration, so consumers taking
v0.3.0 absorb both at once rather than migrating twice. The migration guide gains a section.

`go-tool-base` is the migration that matters, being wholly dependent on this module. Its port
to the Store architecture is outstanding anyway, so `WithFiles` changing type costs it one
extra edit per call site rather than a second migration.

## Open questions

1. **Does `Dir` belong in this spec or its own?** It is additive and independently useful —
   containment for a tool reading a user-supplied directory — and could ship separately. It
   is here because `os.Root` is what makes dropping afero viable, so the two arguments are
   the same argument.
2. **Should `config-afero` exist at all, or just be documented?** The adapter is roughly
   twenty lines. A module is friendlier; documentation is one fewer repository to release.
3. **Is six methods right?** `ReadDir` and `OpenFile` were cut because each has one caller.
   If either turns out to be needed by a format adapter — a directory-scanning backend, for
   instance — adding it later breaks every implementation. Worth a second look while writing
   Phase 3 of the adapter spec, which is the first real external implementation.

## Implementation phases

**Phase 1 — define `FS`, `RealPather`, `OS` and `Dir`**, with the existing suite running
against an afero adapter. Done when the suite passes unmodified and `go list -deps .` no
longer contains afero.

**Phase 2 — replace the concrete type-switch** in `realPathResolver` with `RealPather`,
keeping `isReallyOnDisk` as the verification step.

**Phase 3 — `config-afero`**, if open question 2 resolves in its favour.

**Phase 4 — migration guide and release**, as part of v0.3.0 alongside the Store architecture,
so consumers — `go-tool-base` first among them — absorb one break rather than two.

Format adapters follow as fast follows once this has shipped.

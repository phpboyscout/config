---
title: config-filekv — a directory of single-value files as a config layer
date: 2026-07-29
author: matt.cockayne
status: draft
---

# config-filekv

A backend over the **one-file-per-key** directory layout: each file's name is a
configuration key and its contents are the value.

It arrives from [umbrella R4](2026-07-21-dynamic-backend-adapters.md), which
rejected `config-k8s` and identified this as the one thing a mounted ConfigMap
genuinely does not reach. But it is deliberately **not** a Kubernetes adapter —
three unrelated ecosystems converged on this layout, and none of them needs a
client to read it.

## Problem

```
/etc/config/
  database.host    →  db.internal
  database.port    →  5432
  log.level        →  debug
```

Three systems present configuration exactly this way:

| System | Location | Written by |
|---|---|---|
| **Kubernetes ConfigMap / Secret**, mounted as a volume | the mount path | kubelet |
| **Docker / Podman secrets** | `/run/secrets/<name>` | the container runtime |
| **systemd `LoadCredential`** | `$CREDENTIALS_DIRECTORY/<name>` | systemd |

A ConfigMap holding *one* key with a whole YAML document is already a file, and
`WithFiles` reads it. A ConfigMap holding *many scalar keys* is many files, and
nothing here reads that. Neither do the other two, which are secrets delivery
mechanisms with no document to point `WithFiles` at.

### Why this is not "just glob the directory"

The reason this earns a module rather than four lines in a consumer.

A Kubernetes ConfigMap volume does not contain plain files. It contains the
**atomic writer** layout:

```
..2026_07_29_10_00_00.1234/      real directory, timestamped
..data  ->  ..2026_07_29_.../    symlink, repointed atomically on update
database.host -> ..data/database.host
log.level     -> ..data/log.level
```

The kubelet writes a whole new timestamped directory and then **repoints
`..data` in one operation**, which is what makes a ConfigMap update impossible to
observe half-applied. A naive `ReadDir` therefore yields `..data` and a
timestamped directory alongside the real keys, and a consumer who did not know
that would invent two configuration keys out of the update mechanism.

Skipping dot-prefixed entries and reading only regular files (through symlinks)
is small, specific, and exactly the kind of knowledge that belongs in one tested
place rather than rediscovered per consumer.

## Facts to verify before implementation

Stated here as **claims to test, not established facts** — the atomic-writer
layout is documented Kubernetes behaviour and matches its source, but nothing
below has been probed on this machine, and an adapter built on a
misremembered layout would fail only in a cluster.

- **K1** — a mounted ConfigMap presents `..data` plus a `..<timestamp>` directory,
  and each key as a symlink into `..data`.
- **K2** — an update repoints `..data`; the key symlinks themselves are stable.
- **K3** — a ConfigMap value is written **byte-exact**, with no trailing newline
  added.
- **K4** — a `subPath` mount is *not* updated in place (relevant to the docs, not
  the code).
- **K5** — Docker secrets are mode `0444` at `/run/secrets/<name>`, byte-exact.
- **K6** — systemd credentials are mode `0400` under `$CREDENTIALS_DIRECTORY`,
  byte-exact.

K3, K5 and K6 underpin **D10** (values are byte-exact), so they are the ones to
settle first — if any of the three does append a newline, that decision is wrong
for that system and the spec needs a revision, not a quiet code change.

## Decisions

### D1 — Module `config-filekv`, package `configfilekv`

Named for the shape, not for any of the three systems, because naming it after
one would make the other two look like a workaround. Alternatives considered:
`config-dirkv` (same idea, reads worse), `config-secretfiles` (excludes
ConfigMaps, which are not secrets), `config-configmap` (excludes the other two
and invites the Kubernetes-client expectation R4 rejected).

### D2 — Zero third-party dependencies

It reads a directory through `config.FS`. Nothing else. Like `config-dotenv`,
this adapter adds **no module** to a consumer's graph, which is much of why it is
worth having at all — the alternative it replaces costs 38.

### D2a — Listing needs a new optional interface on `config.FS`

**`config.FS` cannot list a directory.** It has `ReadFile`, `WriteFile`, `Stat`,
`Rename`, `Remove` and `MkdirAll`, and nothing that enumerates — so this adapter
could not be built as first drafted. Found by reading the interface rather than
assuming it.

The core therefore gains an optional interface, beside the two it already has:

```go
// DirLister optionally enumerates a directory.
type DirLister interface {
    ReadDir(name string) ([]fs.DirEntry, error)
}
```

Optional, not a new method on `FS`, for the reason `RealPather` and `LinkReader`
are: adding it to the interface is a **breaking change to seven filesystem
adapters** — afero, billy, iofs, sftp, aws-s3, gcp-gcs, azure-blob — each needing
a coordinated release, to serve one consumer. An optional interface costs the
others nothing.

The same warning `RealPather` carries applies: *an implementation must not
satisfy an optional interface it cannot honour*. A filesystem that cannot
enumerate should not have the method rather than return an error from it.

`configfilekv.New` type-asserts and **fails at construction** against a
filesystem that cannot list, rather than returning an empty layer at load — an
adapter whose whole job is enumeration silently contributing nothing is the worst
available outcome.

### D3 — The filename is the key; `.` splits it into the tree

`database.host` becomes `database` → `host`. A name with no dot is a single
top-level key.

Only `.` splits. `config-dotenv` maps underscore to dot because environment
variables cannot contain a dot; that constraint does not exist here — ConfigMap
keys permit `[-._a-zA-Z0-9]+`, and Docker and systemd names are freer still — so
inventing a second separator would make `db_password` and `db.password` collide
for no reason.

### D4 — Dot-prefixed entries are skipped; subdirectories nest

The atomic-writer rule, and it doubles as the ordinary Unix hidden-file
convention. Directories are skipped, symlinks are followed and read for their
target's contents.

**Recursive: a subdirectory is a path segment.** `sub/database.host` becomes
`sub` → `database` → `host`, which is the same rule D3 applies to dots.

An earlier draft skipped subdirectories as speculative. That was wrong: a
Kubernetes **projected volume** with `items: [{key: x, path: sub/x}]` produces
exactly this layout, so nesting is legitimate usage rather than a mistake, and
silently skipping it would make a supported configuration disappear with no
diagnosis.

Dot-prefixed directories are still skipped, which is what keeps the atomic
writer's `..2026_...` staging directory out — and it is skipped *before*
recursion, so the whole staged tree is invisible rather than enumerated twice.

### D5 — Read-only by default; `WithWritable()` opts in

Every one of the three motivating mounts is read-only — a ConfigMap volume is
mounted read-only, Docker secrets are `0444`, systemd credentials `0400` — so a
write path is off unless asked for. A default that fails on the filesystem for
all three real consumers would be a capability lie.

But a plain directory of files is a perfectly good small writable store, and
adding it later would mean a capability promotion and a release note. So it is
opt-in from the start:

```go
configfilekv.New(config.OS(), "/var/lib/myapp/config", configfilekv.WithWritable())
```

**Writability is a type, not a flag.** `New` returns a plain `Backend` without
the option and a `WritableBackend` with it, mirroring `config.Filtered` and
`config.Nested` — so a store never routes a write at a directory nobody said
could be written to.

### D5a — What writing means, and what it cannot promise

Four things the write path has to answer, and none of them is free:

- **One file per key.** Setting `database.host` writes the file `database.host`;
  removing it deletes the file. A key whose value is a subtree has no
  single-file representation, so a `Set` of a map is refused with
  `ErrInvalidTarget` rather than invented — the same refusal
  `config-keychain` makes for the same reason.
- **`AtomicMultiKey: false`.** Writing three keys is three file writes. There is
  no `..data` trick available to a general directory — that is the kubelet's
  mechanism, not a filesystem primitive — so a failure part-way leaves the
  earlier writes in place and rollback undoes them best-effort. Declared, not
  pretended.
- **Each file is written atomically in itself**, via write-temp-then-rename, so
  a reader never sees a half-written value. That is per key, and it is the only
  atomicity available.
- **Conflict detection compares content**, because a file has no version. Verify
  re-reads each touched file and compares against what Load saw, exactly as
  `config-keychain` does — it catches another writer, cannot distinguish
  "changed and changed back", and leaves a window between check and write. The
  honest position is that this is a check, not a guarantee.

### D5b — A new file's mode is the caller's decision, defaulting to `0600`

A writable directory may well be holding credentials, so the default is
owner-only. `WithFileMode(0o644)` widens it where the values are ordinary
configuration.

Defaulting to `0644` and letting people discover their secrets were
world-readable is the wrong way round: the tighter default fails visibly (a
sibling process cannot read it) while the looser one fails silently.

### D5c — A missing directory is created on first write, not at construction

`WithWritable()` against a path that does not exist yet is the ordinary first-run
case, and `config.FS` already offers `MkdirAll`.

It is created **on the first write**, not at construction, matching the file
backend: a declared-but-missing file is a routing candidate and `Apply` creates
it. Constructing a store should not have a filesystem side effect for a write
that may never come — a read-only run of a tool that merely *could* write would
otherwise leave an empty directory behind.

Created under the same mode policy as D5b, so a credentials directory is not
world-traversable by default.

### D6 — An empty file is an empty value, not an absent key

A ConfigMap key with an empty value mounts as a zero-byte file. It is declared,
so it defines the key as `""` and shadows anything beneath it. Dropping it would
make "set to empty" and "not set" indistinguishable, which is the distinction the
layering model exists to keep.

### D7 — `WithSensitive()` marks the layer secret

Two of the three systems deliver **secrets**. A layer built over `/run/secrets`
or `$CREDENTIALS_DIRECTORY` should carry `Capabilities{Sensitive: true}`, so the
core's leak guard refuses to write one of those values into a plain config file —
the same protection `config-vault` gets, for free, over a local directory.

Off by default, because a mounted ConfigMap usually is not secret and marking it
so would refuse ordinary writes to the file beneath it.

### D8 — `WithPrefix()` nests the whole directory

```go
configfilekv.New(config.OS(), "/run/secrets", configfilekv.WithPrefix("secrets"))
```

`db_password` becomes `secrets.db_password`. Without it the directory's keys sit
at the root, which is right for a ConfigMap that *is* the configuration and wrong
for a secrets directory sharing a store with everything else.

### D9 — Watch by polling, reporting *possible* change

`WatchableBackend`, polled. The backend compares a cheap directory fingerprint —
each entry's **name, size and modification time**, one `Stat` per entry and no
file reads — and reports possible change when it moves.

Hashing contents would be exact, but it makes the watch scale with data volume
rather than directory size, to catch an edit that preserves name, size *and*
mtime — which is vanishingly rare outside a test that constructs it.

Deliberately liberal: the `Backend` contract says a watch *"reports possible
change rather than actual change"*, because only the Store can decide whether the
resolved configuration actually moved, and it already does. So this does not need
to read every file per tick to be correct, and a spurious wake costs one reload
that notifies nobody.

`NativeWatch: false` — it is a poll, and saying otherwise would be the kind of
capability lie the family's `config-afero` lesson is about.

### D10 — Values are the file's bytes, verbatim

The file's bytes, as a string, **byte-exact** — no trailing newline is trimmed
unless `WithTrimTrailingNewline()` asks for it.

All three systems write the value exactly (K3, K5, K6), so a stray newline only
appears when a human created the file with `echo`. Trimming by default would
silently alter a value that legitimately ends in whitespace — and when that
value is a secret, the result is an authentication failure with nothing to point
at. A visible stray `\n` shows up in the first log line and is diagnosable; a
silently mangled credential is not. The friendlier default is the one that fails
invisibly, so it is the opt-in. Umbrella **R1** applies: a consumer whose files
hold JSON or YAML injects a `config.Codec`, and this adapter takes no codec
dependency of its own.

**Binary values are out of scope.** A ConfigMap's `binaryData` mounts as binary
files, and rendering those as strings would produce mojibake rather than an
error. The documentation must say so plainly rather than let someone discover it.

### D11 — Testing: memory FS, conformance, and a real mount

- Unit against an in-memory `config.FS` seeded with the atomic-writer layout —
  `..data`, a timestamped directory, key symlinks — so D4 is exercised against
  the shape it exists for rather than a flat directory.
- **`config/backendconformance` is the gate, run twice**: read-only, and again
  with `WithWritable()` so the write, conflict and rollback branches all run.
  Two runs, because the read-only default and the opt-in write path are
  different types and a single run would leave one of them unproven.
- An **integration test against a real ConfigMap mount** verifying K1–K3, which
  is the only way those stop being assumptions. It needs a cluster, so it is
  env-gated and skipped by default like the cloud adapters — with the difference
  that `kind` makes a real cluster cheap in CI, unlike Key Vault or Secret
  Manager.

## Public API

```go
func New(fsys config.FS, dir string, opts ...Option) config.Backend

func WithPrefix(path string) Option
func WithSensitive() Option
func WithValueCodec(codec config.Codec) Option
func WithPollInterval(d time.Duration) Option

// Writing (D5, D5a, D5b, D5c)
func WithWritable() Option
func WithFileMode(mode fs.FileMode) Option

// Values (O1)
func WithTrimTrailingNewline() Option

const SourceKind = config.SourceKind("filekv")
```

## Migration & compatibility

A new module. Nothing existing changes.

## Resolved (2026-07-29)

- **O1, trailing newlines.** Byte-exact by default; `WithTrimTrailingNewline()`
  opts in (D10). The friendlier default is the one that fails invisibly, and for
  a secret it fails as an authentication error with nothing to point at.
- **O2, the poll fingerprint.** Name, size and mtime (D9). Hashing would be
  exact but scales the watch with data volume to catch a case that essentially
  does not occur outside a test.
- **O3, recursive directories.** Recurse; a subdirectory is a path segment (D4).
  The earlier YAGNI lean was wrong — a projected volume with an `items[].path`
  produces exactly this, so skipping would make supported configuration vanish.
- **O5, a missing directory.** Created on first write, not at construction
  (D5c), matching how the file backend treats a declared-but-missing file.
- **The `ReadDir` gap.** A new optional `config.DirLister` (D2a), not a method on
  `config.FS` — the latter is a breaking change to seven adapters to serve one
  consumer.
- **O4, a writable variant.** In scope from the start, opt-in via
  `WithWritable()` (D5), rather than deferred — adding it later would be a
  capability promotion and a release note. Writability is a type, not a flag, so
  a store never routes a write at a directory nobody opted in. What writing can
  and cannot promise is D5a; file modes are D5b.

## Open questions

None outstanding. Anything implementation turns up — including any of K1–K6
proving false — is recorded as a dated revision rather than a silent edit.

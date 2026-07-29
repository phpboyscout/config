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

K3, K5 and K6 decide **O1** below, so they are worth settling early.

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

### D3 — The filename is the key; `.` splits it into the tree

`database.host` becomes `database` → `host`. A name with no dot is a single
top-level key.

Only `.` splits. `config-dotenv` maps underscore to dot because environment
variables cannot contain a dot; that constraint does not exist here — ConfigMap
keys permit `[-._a-zA-Z0-9]+`, and Docker and systemd names are freer still — so
inventing a second separator would make `db_password` and `db.password` collide
for no reason.

### D4 — Dot-prefixed entries are skipped; only regular files are read

The atomic-writer rule, and it doubles as the ordinary Unix hidden-file
convention. Directories are skipped, symlinks are followed and read for their
target's contents.

**Non-recursive.** ConfigMap keys cannot contain `/`, so no nesting arises from
the case that motivated this. A projected volume can nest, and mapping a
subdirectory to a path segment is a coherent extension — **open question O3**,
deliberately not built on speculation.

### D5 — Read-only

Every one of the three motivating mounts is read-only: a ConfigMap volume is
mounted read-only, Docker secrets are `0444`, systemd credentials `0400`.
Offering a write path would be offering one that fails on the filesystem for all
three real consumers.

A plain writable directory used as a key-value store is a coherent *different*
want — **open question O4**.

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
each entry's name, size and modification time — and reports possible change when
it moves.

Deliberately liberal: the `Backend` contract says a watch *"reports possible
change rather than actual change"*, because only the Store can decide whether the
resolved configuration actually moved, and it already does. So this does not need
to read every file per tick to be correct, and a spurious wake costs one reload
that notifies nobody.

`NativeWatch: false` — it is a poll, and saying otherwise would be the kind of
capability lie the family's `config-afero` lesson is about.

### D10 — Values are strings; a codec is available for the odd structured one

The file's bytes, as a string. Umbrella **R1** applies: a consumer whose files
hold JSON or YAML injects a `config.Codec`, and this adapter takes no codec
dependency of its own.

**Binary values are out of scope.** A ConfigMap's `binaryData` mounts as binary
files, and rendering those as strings would produce mojibake rather than an
error. The documentation must say so plainly rather than let someone discover it.

### D11 — Testing: memory FS, conformance, and a real mount

- Unit against an in-memory `config.FS` seeded with the atomic-writer layout —
  `..data`, a timestamped directory, key symlinks — so D4 is exercised against
  the shape it exists for rather than a flat directory.
- **`config/backendconformance` is the gate**, read-only branch.
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

const SourceKind = config.SourceKind("filekv")
```

## Migration & compatibility

A new module. Nothing existing changes.

## Open questions

- **O1 — Trailing newlines: trim, or byte-exact?** *(the one that needs deciding
  first)* Kubernetes, Docker and systemd all write values byte-exact (K3, K5,
  K6), so a stray newline only appears when a human created the file with `echo`.
  Trimming silently alters a value that legitimately ends in whitespace — and for
  a *secret* that means an authentication failure with no clue why. Not trimming
  means the hand-made cases carry a `\n` that shows up in the first log line.
  **Leaning: byte-exact by default, with `WithTrimTrailingNewline()` to opt in**,
  because a visible stray newline is diagnosable and a silently mangled secret is
  not. Wants confirming, since it is the default people meet daily.
- **O2 — Should the poll fingerprint include content?** Name, size and mtime miss
  an edit that preserves all three. Rare, and cheap to rule out by hashing — but
  hashing every file per tick makes a watch proportional to the data rather than
  the directory. Probably fine as specified given the Store re-reads on any
  reported change; worth measuring.
- **O3 — Recursive directories.** A projected volume can nest. Mapping a
  subdirectory to a path segment is coherent and unneeded by the motivating
  cases. Left out until something asks.
- **O4 — A writable variant.** A plain directory as a writable key-value store is
  a different want from the three read-only mounts here. Separate decision, and
  possibly a separate adapter, since making this one writable would offer a path
  that fails on every consumer it was built for.

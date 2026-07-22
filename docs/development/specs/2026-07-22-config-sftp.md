---
title: config-sftp — the SFTP remote-filesystem adapter
date: 2026-07-22
author: matt.cockayne
status: draft
---

# config-sftp

The remote-host filesystem adapter, and the closest of the remote adapters to
POSIX: SFTP is a file protocol, not an object store, so it has directories, real
metadata, and — the load-bearing part — a genuine server-side rename. Per the
[filesystem-adapters umbrella spec](2026-07-22-filesystem-adapters.md) **D2**, no
adapter is built without its own approved spec; this is that spec for SFTP. It
cites the umbrella and settles the things the umbrella left to each adapter: the
underlying type it wraps and how each `config.FS` method maps onto it, its
capability, how a write commits and what atomicity actually holds, its dependency
footprint, and its testing surface.

## Problem

A tool whose configuration lives on a remote host reached over SSH cannot point
this module at it today. The core ships `config.OS()` and `config.Dir`, and the
sibling `config-afero` bridges an `afero.Fs`, but none of those reaches a file on
another machine over SFTP. The seam to reach it exists and is proven (umbrella
D-preamble: `WithFiles`, `NewFileBackend` and `NewWatcher` all take a
`config.FS`, and `config-afero` shows a sibling module can supply one). What is
missing is the shipped module — `config-sftp` — and the per-system decisions the
umbrella cannot make: which SFTP rename primitive gives a safe stage-and-commit,
how a missing file surfaces as `fs.ErrNotExist`, how a remote path is watched
when it is not a local OS path, and how the whole thing is tested without an
external SSH server.

SFTP is the right remote-file adapter because it is the least surprising: unlike
the object stores (umbrella D6), it is not pretending to be a disk — it *is* a
remote POSIX-shaped filesystem, with `Open`/`Create`/`Stat`/`Rename`/`Remove`/
`Mkdir` that mean what they say. The one genuine subtlety is that SFTP's plain
rename does not overwrite, and the atomic-overwrite rename is a server extension —
which is exactly the kind of divergence D2 exists to record.

## Decisions

### D1 — Module `config-sftp`, package `configsftp`

A bare, vendor-neutral name (umbrella D1): SFTP is a protocol, not a cloud, so
there is nothing to qualify. The module is
`gitlab.com/phpboyscout/go/config-sftp`, package `configsftp`, matching the
`config-<x>` / `configx` convention every filesystem adapter uses. The repo
carries a README only; this spec lives centrally here (umbrella D2 / the adapter
docs model — a page per adapter bundled into the parent site, no standalone
microsite).

It requires the `config` release carrying the filesystem-adapter precursor
(`config.ErrReadOnlyFS`, MR !39) as the family baseline, but `config-sftp` is
read+write and returns no such sentinel — so it uses only the public surface
`config-afero` already uses (`config.FS`, the optional `config.LinkReader`, the
poll watcher) and needs **no new core change of its own**.

### D2 — Wraps an injected `*sftp.Client`; the adapter owns no connection or credentials

`Wrap(client *sftp.Client) config.FS` takes an already-connected
`github.com/pkg/sftp` client and translates the six `config.FS` calls onto it,
exactly as `config-afero.Wrap(afero.Fs)` does (umbrella D3). The consumer builds
the `*sftp.Client` over their own `*ssh.Client` — which is where every credential,
host-key, known-hosts, auth-method and dial decision honestly lives, and where the
`golang.org/x/crypto/ssh` dependency is genuinely used. The adapter never
re-decides host, port, user, key or host-key verification; it is handed a live
client and does file operations through it. A `sftp.NewServer`-backed in-process
client then drives the whole unit suite with no network (D8).

`Wrap` is the entire API. There is no `FromClient`/dial convenience — building the
SSH transport is the consumer's, and folding it in would drag the auth surface
back into the adapter the umbrella's D3 keeps out.

### D3 — Method mapping onto `*sftp.Client`

Verified against `github.com/pkg/sftp v1.13.11`:

| `config.FS` | SFTP mapping |
|---|---|
| `ReadFile(name)` | `client.Open(name)` → `io.ReadAll` → `Close`; a missing file surfaces as `fs.ErrNotExist` (see D4) |
| `WriteFile(name, data, perm)` | `client.OpenFile(name, O_WRONLY\|O_CREATE\|O_TRUNC)` → `Write(data)` → `f.Chmod(perm)` → `Close` |
| `Stat(name)` | `client.Stat(name)` — returns a real `os.FileInfo` from the server's attrs |
| `Rename(old, new)` | `client.PosixRename` where the server offers it, else `client.Rename` — see D5 |
| `Remove(name)` | `client.Remove(name)` |
| `MkdirAll(path, perm)` | `client.MkdirAll(path)` (SFTP `Mkdir` carries no perm arg; mode is the server's umask) |

`WriteFile` sets the mode explicitly with `f.Chmod(perm)` after create, because
SFTP's `Create`/`OpenFile` do not take a mode the way `os.WriteFile` does — the
adapter must apply `perm` in a second step so a staged file lands with the
permissions the write path asked for. `MkdirAll` cannot honour `perm` at all
(the SFTP mkdir request has no mode field in the client's `MkdirAll`); this is a
documented lossy edge, not a failure.

### D4 — A missing file surfaces as `fs.ErrNotExist`, from the library itself

The `config.FS` contract turns on `ReadFile` returning an error satisfying
`errors.Is(err, fs.ErrNotExist)` when a file is absent, because the Store treats
a missing optional overlay as normal and anything else as fatal. `pkg/sftp`
already does this normalisation: its `normaliseError` maps the server's
`SSH_FX_NO_SUCH_FILE` status to `os.ErrNotExist` (and `SSH_FX_PERMISSION_DENIED`
to `os.ErrPermission`), and `Open`/`Stat` return the normalised form. So the
adapter passes the library's error through unchanged for the absent-file case and
the contract holds without a bespoke mapping. The adapter's tests assert
`errors.Is(err, fs.ErrNotExist)` against a real missing file over the in-process
server, so a future library change that broke the mapping would fail the suite.

### D5 — Rename is native and server-side, preferring the POSIX-overwrite extension

This is the decision the whole adapter turns on, and the one genuine SFTP
subtlety. A `config.FS` write commits by staging content beside the target and
renaming **over** it, so a reader never sees a half-written file (`fs.go`
`Rename` doc; umbrella "the load-bearing one is `Rename`"). SFTP has two renames,
and they differ exactly on overwrite:

- **`client.Rename`** issues `SSH_FXP_RENAME`, whose specified behaviour is to
  **fail if the destination already exists**. OpenSSH's server refuses to
  overwrite through it. Used as the commit rename it would fail on every write
  after the first, because the target is already there.
- **`client.PosixRename`** issues the `posix-rename@openssh.com` extension, which
  is documented to *"replace newname if it already exists"* — i.e. POSIX
  `rename(2)` semantics: atomic replace of the destination, server-side.

So the adapter's `Rename` **prefers `PosixRename`** and falls back to `Rename`.
The atomicity is real but it is the **remote server's**, not the adapter's: on an
OpenSSH server `PosixRename` is an atomic replace and the commit is clean; on a
server that does not advertise the extension the adapter has only the
non-overwriting `Rename`, and the commit's rename-over-existing cannot be atomic.
How to behave in that second case is the sharpest open question (see Open
questions) — the safe default under discussion is to `Remove` the target then
`Rename`, accepting a narrow non-atomic window, versus failing loudly so the
consumer knows their server cannot give the guarantee the write path assumes. The
adapter documents that atomic-overwrite commit **requires a server that supports
`posix-rename@openssh.com`** (OpenSSH and most others do), rather than silently
degrading.

### D6 — Capability: read and write; not a `RealPather`; watched by polling

`config-sftp` is a **read+write** `config.FS` (umbrella D4): all six methods work
against a normal SFTP server, so — unlike `config-iofs` — it never returns
`config.ErrReadOnlyFS`. A write routed at a read-only remote directory fails
loudly at the point of the write with the server's own permission error
(`fs.ErrPermission` via D4's normalisation), exactly as a read-only file on disk
would.

It deliberately does **not** implement `config.RealPather`. A remote SFTP path is
*not* a local operating-system path — fsnotify on the machine running the adapter
would watch a local path that does not exist, or worse a same-named local file —
so claiming `RealPath` would send native notification after the wrong thing
(`fs.go` `RealPather`: *"An implementation must not satisfy an optional interface
it cannot honour"*). Not implementing it routes the Store to its **poll watcher**
(umbrella D5), which re-reads the remote file on an interval and stays quiet if
nothing resolved differently. No new mechanism is needed — the poll watcher and
`WithPollInterval` already ship. Unlike the cloud stores (umbrella D5), an SFTP
poll is a network round-trip but **not a billed API call**, so the core's 2-second
`DefaultPollInterval` is tolerable; the README nonetheless recommends a modestly
longer interval for a busy remote, since each poll is an SSH round-trip.

### D7 — Symlinks: implement `config.LinkReader`, because SFTP genuinely resolves them

SFTP has real symlinks and `client.ReadLink` resolves them, so the adapter
implements the optional `config.LinkReader` (`Readlink(name)` →
`client.ReadLink(name)`). This is honest capability, not speculative: a
configuration file reached through a remote symlink should be *written through*
it, not have the link replaced by a regular file that detaches every other path
pointing at the target (`fs.go` `LinkReader` doc). Because SFTP genuinely has the
feature, `config-sftp` claims it unconditionally — which is correct here precisely
*because* it is always true for an SFTP server, unlike `config-afero` where link
support varies by underlying filesystem and must be probed per type. (If a
particular server rejects a `ReadLink`, that surfaces as the per-path error the
write path already handles.)

### D8 — Testing: an in-process SFTP server, no external emulator

`pkg/sftp` makes a real, in-process SFTP client/server pair trivial and needs no
Docker, no `sshd`, and no network — verified against the library's own test
harness (`server_test.go`):

1. **Primary — pure pipe, no SSH transport.** A pair of `io.Pipe()`s wires a
   `sftp.NewServer(...)` (serving a `t.TempDir()`) directly to a
   `sftp.NewClientPipe(...)`, which returns a real `*sftp.Client`. This is exactly
   `pkg/sftp`'s own primary test harness: the true SFTP wire protocol, end to end,
   in one process, against real files in a temp dir — so `Wrap`'s six methods,
   the `fs.ErrNotExist` mapping (D4), the `PosixRename` overwrite (D5), symlink
   resolution (D7) and permission preservation (D3) are all exercised for real.
2. **Optional higher-fidelity — SSH loopback.** A second, smaller suite stands up
   a `golang.org/x/crypto/ssh` server on `localhost:0` with an `sftp` subsystem
   (`sftp.NewServer` over the accepted channel), dials it with `ssh.Dial` and a
   throwaway host key, and wraps the resulting `sftp.NewClient`. This proves the
   real transport + auth path the consumer actually uses, still with no external
   dependency. It can be env-gated if its host-key/handshake cost is unwanted in
   the ordinary unit run.

Because a full server runs in-process, `config-sftp` needs **no testcontainers /
DIND job** — unlike the cloud object stores (umbrella D8), whose emulators are
separate processes. A `depfootprint_test.go` allowlist (D9) rounds out the suite.

### D9 — Dependency footprint: pkg/sftp, plus the SSH transport it references

Measured with `go list -deps .` at `pkg/sftp v1.13.11`, the module adds a small,
honest footprint (umbrella D7):

| Module | Version | Why |
|---|---|---|
| `github.com/pkg/sftp` | v1.13.11 | the SFTP client the adapter wraps |
| `github.com/kr/fs` | v0.1.0 | pulled by `pkg/sftp` (its filesystem walker) |
| `golang.org/x/crypto` | v0.54.0 | the `ssh` transport `pkg/sftp` is built on |
| `golang.org/x/sys` | v0.47.0 | pulled transitively; **already in the `config` graph** |

Net-new to a consumer's graph is therefore just **three** modules — `pkg/sftp`,
`kr/fs`, `x/crypto` — since `x/sys` already arrives with `config` (and
`config-afero`). This is far smaller than a cloud storage SDK, matching SFTP's
place as the least-divergent remote adapter. A `depfootprint_test.go` allowlist
asserts exactly this set plus the `config` library graph, so a transitive
addition through a version bump fails a test rather than arriving quietly in a
consumer's `go.sum`. The allowlist is scoped to `go list -deps .` (the library
graph), so `testify` and any SSH-server test helper — test-only — never reach a
consumer and are not listed.

## Rejected alternatives

**Take an `*ssh.Client` (or dial parameters) in the constructor.** Simpler to
call once, but it drags SSH auth, host-key verification and dial configuration —
all the consumer's — into the adapter, and couples it to a transport it does not
need to know about. `Wrap(*sftp.Client)` keeps the adapter to the six file calls
and leaves every credential decision where it belongs (D2, umbrella D3), exactly
as `config-afero` does.

**Use plain `client.Rename` for the commit.** It is the obvious mapping, but
`SSH_FXP_RENAME` is specified not to overwrite and OpenSSH refuses it, so the
stage-and-rename commit would fail on every write after the first. Preferring
`PosixRename` (D5) is what makes the write path's atomic-replace-over-target
actually work on the servers consumers use.

**Implement `config.RealPather` returning the remote path.** It would make the
Store attempt fsnotify — on the *local* machine, against a path that is remote.
The watcher would watch nothing, or the wrong local file, and the absence would
be discovered one path at a time (`fs.go` `RealPather`). Not implementing it
routes to the poll watcher, which is correct for a remote filesystem (D6).

**A testcontainers `sshd` integration suite.** Unnecessary here: `pkg/sftp` runs
a real SFTP server in-process (D8), so the full protocol is exercised without
Docker. The cloud adapters need emulators because their stores cannot run
in-process; SFTP can, so it does not pay the DIND cost.

## Public API

The module's entire exported surface:

- `func Wrap(client *sftp.Client) config.FS` — adapts a connected SFTP client to
  `config.FS`. The returned value additionally satisfies `config.LinkReader`
  (D7) and deliberately **not** `config.RealPather` (D6); the Store discovers
  both by type assertion, so no capability flag is exported.

The result is passed to `WithFiles`/`NewFileBackend` exactly as `config.OS()` is:

```go
sshClient, _ := ssh.Dial("tcp", host, sshCfg) // consumer's transport + auth
sftpClient, _ := sftp.NewClient(sshClient)     // consumer's SFTP client

store, err := config.NewStore(ctx,
    config.WithFiles(configsftp.Wrap(sftpClient), "/etc/app/config.yaml"))
```

No change to the `config` core is required: `config-sftp` uses only the surface
`config-afero` already proves against the public API (D1).

## Testing strategy

Per D8: the primary suite is an **in-process `sftp.NewServer` ↔ `NewClientPipe`
pair over `io.Pipe`** serving a `t.TempDir()`, driving a real `*sftp.Client`
through `Wrap` — reads, writes, stage-and-rename commit, permission
preservation, the `fs.ErrNotExist` case (D4), the `PosixRename` overwrite (D5),
and symlink resolution (D7); an optional **SSH-loopback** suite proves the real
transport path; a `depfootprint_test.go` allowlist asserts the footprint (D9).
What would falsely pass: a rename test that never renames *over an existing
target* — the overwrite is the whole point of D5, so the commit test stages onto
a path that already holds the previous content and asserts the reader sees only
the new bytes, never a mix and never a failure. The umbrella is further verified
by the adapter passing the module's existing filesystem behaviour: a wrapped SFTP
filesystem must read, write, stage-and-rename and preserve permissions exactly as
`config.OS()` does, which the module's own suite already asserts against any
`config.FS`.

## Migration & compatibility

Purely additive for consumers: add the module and pass
`configsftp.Wrap(sftpClient)` to `WithFiles`, exactly as for `config-afero`. No
`config` core change, nothing breaks. The module ships at v0.1.0 as a full
read+write adapter, so there is no later read→write promotion to communicate.

## Open questions

*Surfaced, not resolved — for the approval discussion.*

1. **Non-OpenSSH rename fallback (the sharpest).** `PosixRename` gives the atomic
   overwrite the commit needs, but it is the `posix-rename@openssh.com`
   extension; a server that does not advertise it leaves only the non-overwriting
   `Rename`. Do we (a) fall back to `Remove(target)` then `Rename`, accepting a
   narrow window where a reader could see the target absent; (b) fail the commit
   loudly so the consumer learns their server cannot give the guarantee; or (c)
   detect extension support up front (a capability the SFTP handshake exposes) and
   pick per-connection? All three keep atomicity honest rather than pretended; the
   choice is which failure mode to hand the consumer.
2. **Rename atomicity is ultimately server-dependent.** Even with `PosixRename`,
   the atomicity is the remote server's implementation of `rename(2)` over its own
   storage — a server backing SFTP with a non-POSIX store may not honour it. The
   adapter can document the assumption and prefer the strongest primitive, but it
   cannot *enforce* what the far side does. How loudly should the README state
   this, and is any runtime probe worth its cost?
3. **In-process test server: which fidelity as the default gate?** The pure-pipe
   `NewServer`/`NewClientPipe` harness is fast and dependency-free but skips the
   SSH transport; the SSH-loopback harness exercises the real path but pays a
   handshake per test. Is loopback the ordinary unit gate, or an env-gated second
   suite with pipe as the default? (D8 leans pipe-primary; confirm.)
4. **Path conventions and separators.** SFTP paths are always `/`-separated and
   interpreted by the server (absolute, or relative to the login directory). The
   adapter must use `path`, never `filepath`, so it stays POSIX even when the
   adapter runs on Windows — but the *staging path* the core's write layer
   computes may use `filepath.Join`. Does the commit path already pass
   `/`-separated names down, or does `config-sftp` need to normalise separators at
   its boundary? This needs checking against the write path before v0.1.0.
5. **`MkdirAll` mode loss.** SFTP's mkdir carries no mode through `client.MkdirAll`,
   so a directory created for a config file lands at the server's umask, not the
   `perm` the write path passed. Is documenting the loss enough, or should the
   adapter follow up with an explicit `Chmod` on each created directory?

## Implementation phases

**Phase 0 — this spec.** Approve it, resolving the open questions above (chiefly
the rename fallback, #1).

**Phase 1 — the module, read + write over the in-process server.** Scaffold
`config-sftp` (README-only, no microsite; umbrella D2 / adapter docs model),
`Wrap`, the six-method mapping (D3), the `fs.ErrNotExist` pass-through (D4), the
`PosixRename`-preferring commit rename (D5), `config.LinkReader` (D7), and the
pure-pipe in-process server suite (D8) covering read, write, stage-and-rename,
permissions, missing-file and symlink. `depfootprint` allowlist (D9).

**Phase 2 — transport-fidelity and polish.** The optional SSH-loopback suite (D8),
the non-OpenSSH rename-fallback behaviour chosen at approval (D5 / OQ1), and the
`MkdirAll`/separator edges (OQ4, OQ5). Confirm polled reload end to end against
the in-process server with `WithPollInterval` (D6).

**Phase 3 — publish v0.1.0.** The module `main`, the `config` docs page
(`how-to/sftp.md`) bundled into the parent site, and the landing card — the same
rollout every adapter takes. Anything this adapter shows the umbrella got wrong is
corrected there by dated revision, never silently.

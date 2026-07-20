---
title: Comment-safe, layer-correct config writes
date: 2026-07-18
author: matt.cockayne
status: superseded
issue: phpboyscout/go/config#1
superseded-by: 2026-07-19-store-architecture.md
---

# Comment-safe, layer-correct config writes

> ## SUPERSEDED — 2026-07-19 by `2026-07-19-store-architecture.md`
>
> This design added a Writer beside the existing container. It was correct on paper and
> **fragile by construction**: the commit lease, fingerprint checks, feedback-loop breaking and
> provenance-index invalidation were all consequences of three components (container, writer,
> watcher) sharing the filesystem with no single owner.
>
> The successor removes the sharing rather than protocolising it. Its decisions on the
> document layer, comment ownership, routing, validation and map-valued writes carry over
> largely intact; the coordination machinery does not.

## Problem

`Containable.WriteConfigAs(dest)` delegates to Viper's `WriteConfigAs`, which encodes
`v.AllSettings()`. That produces two defects, the second more serious than the first:

1. **It reserialises the whole document.** Comments are stripped and the file is rewritten. A
   documented, hand-authored config is destroyed on first save.
2. **It writes the *merged* view, not the file's own layer.** `AllSettings()` resolves every
   key through the full precedence chain, so writing "back" to one file of a multi-file setup
   flattens the hierarchy into it — materialising values contributed by other files, and by
   environment variables and bound flags, into the target. **Where an environment variable
   supplies a secret, that secret is written into a version-controlled file.**

The module is built for multi-file configuration — `ConfigFiles()` exists precisely because a
container is routinely merged from several files — yet has no write that respects that
layering. A tool wanting to offer "edit your config from the CLI or UI" must bypass the
container and hand-roll a writer.

**Motivating consumer.** keryx edits its project `.keryx.yaml` from two surfaces: CLI commands
(`theme edit`, `avatar add`) and a local web studio (theme/avatar managers, a settings
screen). The file is hand-authored, checked into the consuming project's repo, and carries
explanatory comments. Both surfaces currently stomp it. keryx has a local workaround — read as
`map[string]any`, mutate, re-marshal — which fixes the layering problem but still loses
comments. keryx runs its studio over **in-memory worktrees** for remote projects, so any
writer must work against an arbitrary `afero.Fs`. It also **writes at runtime and reacts to
its own writes** via hot-reload.

## Goal and guarantee

> Write a config file sensitively so that comments do not get lost, and write back to the
> source files that originated the config, assuming multiple files are defined.
> — owner, 2026-07-18

**Required:** the data structure is preserved, and **comments survive attached to the correct
keys**. Writes target the originating layer, never the resolved view.

**Not required:** blank lines and blank-line grouping, key order, indentation, inline-comment
alignment, the `---` document marker, byte-identity.

The originating issue demanded untouched regions be *byte-identical*. That is stricter than
the goal, unachievable on any re-emitting substrate, and was the source of most cost in the
first draft of this spec. It is out of scope — see Rejected alternatives.

## Terminology

| Term | Meaning |
|---|---|
| **read** / **get** | retrieve a value from the container. Never mutates, never persists. |
| **edit** / **set** | set a value *in the container*. Touches no persistence. |
| **write** | persist a change to a backend. |
| **layer** | one contributing config file, in merge order. |
| **provenance** | which layer supplies the effective value for a key. |
| **shadowed** | a value that exists in a layer but is outranked by a higher-precedence layer, or by env/flags. |

**`edit` and `write` are separate operations on separate components, not two halves of one
flow.**

- **`edit` is `Container.Set()`** — it already exists, writes to the ephemeral override layer,
  and is correctly *not* persistable.
- **`write` is the `Writer`** (D4) — it takes **explicit changes** and routes them to layers.

The Writer **MUST NOT** read pending edits from the container. The container cannot
distinguish "the user changed this" from "an environment variable supplied this" — and that
inability is precisely the flattening defect this spec exists to remove. Anything the Writer
persists must have been handed to it explicitly.

The loop closes without coupling them: after a write commits, the watch manager (D8) reloads
and the container observes the new values. **A caller never calls `Container.Set` to persist a
change** — that is keryx's actual flow. `Container.Set` remains for genuinely ephemeral runtime
overrides, which is all it should ever have meant.

If the module later adopts backend-pluggable writes, the file writer becomes one
implementation behind this same `Writer` surface; nothing here forecloses that.

---

## Decisions

### D1 — Substrate is `github.com/goccy/go-yaml`, used as a document parser

Measured against a hand-authored fixture across five candidates. goccy preserves comments, key
order, quoting styles and block scalars, and — decisively — does not alter content.

Not `gopkg.in/yaml.v3` or `go.yaml.in/yaml/v3`. Their losses split in two, and only one half is
forgivable under the scope above:

| Loss | Acceptable? |
|---|---|
| Blank lines, indent, comment alignment, `---` marker | **Yes** — formatting, out of scope |
| Merge keys rewritten `<<: *base` → `!!merge <<: *base` | **No** — changes what the user sees |
| Folded `>` scalars reflowed onto one line | **No** — changes content presentation |
| Astral-plane emoji → `"\U0001F680"` | **No** — visible change to the user's data |

Also: `github.com/go-yaml/yaml` was **archived 2025-04-01** and `go.yaml.in/yaml/v3` is
declared frozen legacy, security fixes only — round-trip fixes will not land.

> Anyone re-evaluating this decision must score on **content preservation and comment
> attachment**, not on blank-line retention. On the latter the margin is narrow; on the former
> it is not.

Also rejected: `braydonk/yaml` (a fork built for this purpose — measurably *worse* than
yaml.v3, dormant since Feb 2025, and abandoned as a dependency by `google/yamlfmt`, its own
primary consumer); `kyaml` (requires `spf13/cobra`, which `depfootprint_test.go` forbids).

**`go.yaml.in/yaml/v4` was re-evaluated and rejected** (measured at v4.0.0-rc.6, the newest
tag; there is no stable release). It genuinely fixes merge keys — `<<: *base` survives clean,
no `!!merge` — and adds `WithExplicitStart` for the `---` marker. It still fails on content:

- **Astral-plane characters are escaped unconditionally.** `WithUnicode` defaults to `true`
  and governs BMP characters only (`café`, `Zoë`, `✨` pass through); 4-byte characters do
  not, and no option changes it.
- **That escaping cascades and destroys block scalars.** A literal block containing an emoji
  cannot be emitted as a block (blocks cannot carry escapes), so v4 rewrites it as a
  double-quoted string:

  ```yaml
  motd: |                  →   motd: "literal block \U0001F680\n"
    literal block 🚀
  ```

  Controlled comparison in the same run: a folded scalar with no emoji stayed `>`; the literal
  block with one became quoted.
- **Folded scalars are reflowed** onto one line regardless of `WithLineWidth(-1)`. This is
  arguably correct — a folded scalar's *value* is one line, and the source breaks are
  presentation — but it is inherent to any re-emit that round-trips through the value. goccy
  avoids it by retaining the raw token.

### D2 — goccy is a document parser only. It must never be a value source.

Hard invariant. goccy and the module's existing YAML parser decode identical input to
different Go types:

```
99999999999999999999   yaml/v3 = float64(1e+20)   goccy = string    ← v3 destroys it
8080                   yaml/v3 = int(8080)        goccy = uint64(8080)
2020-01-02             yaml/v3 = time.Time        goccy = string
```

`validate.go:167`'s `isInt` accepts `int, int8, int16, int32, int64` — **not `uint64`**. If
goccy-decoded values reached the read or validation path, every integer in every schema would
fail validation.

This is **not** "goccy's typing is bad" — it is the safer parser of the two, since yaml/v3's
large-integer coercion to `float64` is irrecoverable. The invariant exists because **two
parsers with two type systems coexist and `validate.go` targets one of them.** The boundary
must not be crossed in either direction: goccy nodes never become config values; existing
parser values never become document nodes.

Enforce with a test and a lint rule. "We already have it parsed, let's read from it" is the
obvious future optimisation, and it is wrong.

### D3 — The document layer is a self-contained internal package

Parse, locate by dotted path, mutate, apply comment rules, re-emit. Input is bytes plus a
path; output is bytes. No dependency on `Container`, `Containable`, or Viper.

Keeps the substrate swappable, keeps the type boundary in D2 enforceable by package
structure, and means the piece transfers intact if the module's foundations ever change.

### D4 — Placement: a standalone `Writer`, plus a non-breaking container entry point

`Containable` is unchanged. No constructor signatures change. Writing needs only an
`afero.Fs` and a path — routing writes *away* from the merged view is best enforced
structurally rather than by documentation.

```go
// multi-file: container as entry point
w, err := cfg.Writer()

w.Put("avatars.matt.likeness", "a bearded developer")  // stage a change
w.Remove("themes.neon")                                 // stage a delete

plan, err := w.Plan()    // dry run: targets, shadowing warnings, no I/O
err = w.Commit()         // route, prepare, verify, commit (D10)

// single file, no container involved — UNCOORDINATED (D15): no manager, so no
// commit lease and no notification control. One-shot CLI use only; not safe in a
// long-running process that also watches these files.
fw, err := config.WriteFile(fs, "/proj/.keryx.yaml")
fw.Put("avatars.matt.likeness", "a bearded developer")
err = fw.Commit()

// provenance query, available independently
path := cfg.Defines("themes.neon")   // originating file, "" if no file defines it
```

**Method naming is deliberate.** `Put`/`Remove`, not `Set`/`Delete` — `Set` is the
container's ephemeral edit (see Terminology) and the two must not be confusable. `Commit`
rather than `Save` signals the plan/apply model of D10.

`Writer()` is a method on `*Container` only, **not** on `Containable`, so external
implementers and hand-rolled fakes keep compiling.

Rejected: adding to `Containable` (breaking, and the method does not need the container); a
separate `Editable` interface (permanent type-assertion friction at every call site).
Downstreams needing to fake the write path should use afero's in-memory FS, which covers the
case better than a mock.

### D5 — Provenance comes from per-file **raw** parses

Viper cannot answer "which file defines this key" — `ConfigFileUsed()` reports only the last
file read, and the merged view has no provenance. The editor therefore parses each file in
`ConfigFiles()` separately.

**Those per-file views MUST be raw document parses: no env, no flags, no defaults.** A view
built on `Container`/`newResolverViper` would be env-aware (`AutomaticEnv` and the `.`→`_`
replacer are set on every instance) and would answer *"yes, this file defines `themes.neon`"*
merely because `THEMES_NEON` is set — reintroducing, inside the provenance index, exactly the
layer-bleed this feature exists to prevent.

This is the genuinely novel work here. No Go library provides it: seven offer per-key
provenance and all are read-only; every library that writes serialises the resolved view.

### D6 — Edit routing: reverse merge order, first writable match

Merge order is guaranteed (lowest→highest precedence). Routing walks it in reverse:

- **key exists** → the highest-precedence writable layer that defines it
- **key is new** → the highest-precedence writable layer

Rationale: it makes an edit *visible*. Writing to the highest-precedence layer that defines a
key guarantees the value just set is the value read back; writing to the base could leave the
edit silently shadowed.

**Non-writable layers are skipped.** Environment variables, bound flags and embedded defaults
appear in precedence order but cannot be written.

**A written-but-shadowed edit MUST be reported, not swallowed.** If `db.host` is currently
supplied by `KEYRX_DB_HOST`, routing skips the env layer and writes the file beneath it. The
write is correct; the subsequent read still returns the env value. Not an error — but the
caller must be able to learn *"written, currently shadowed by `env:KEYRX_DB_HOST`"* so a CLI
can warn and a UI can badge it.

**Node placement is a separate rule from layer routing.** Layer routing answers *which file*;
node placement answers *where inside that file*. Setting `avatars.matt.likeness` where
`avatars` exists but `matt` does not: **attach at the deepest existing ancestor, synthesise
missing intermediates, append as the last child.** Appending rather than inserting leaves
existing keys' comments and order untouched, and is what the library does natively.

### D7 — Comment ownership on delete is explicit and tested

**This is the load-bearing decision.** With formatting and ordering out of scope, "comments
stay grouped with the right keys" is the *entire* fidelity guarantee — and deletion is exactly
where encoders misbehave, attaching a section's trailing comment to the last key in that
section, so that deleting `server.tls` silently destroys `# foot comment for the server
block`.

Rules:

- a **head** comment directly above a key, with no blank line between, belongs to that key and
  is removed with it
- a head comment separated by a blank line is a **section** comment and survives
- an **inline** comment on the key's own line is removed with it
- a **foot/trailing** comment attached to the deleted node is **hoisted onto the preceding
  sibling** before the node is dropped

**Validated against real files (spike, 2026-07-18).** Three of the five rules match goccy's
native behaviour; **two must be implemented explicitly**:

| Rule | Native |
|---|---|
| Head comment directly above key → removed with it | **free** |
| Head comment after a blank line = section comment → survives | **FAILS — must implement** |
| Inline comment on the key's line → removed with it | **free** |
| Trailing comment after the last key → hoisted to preceding sibling | **FAILS — must implement** |
| No bleed onto the following key's head comment | **free** |

Reproduced on demand — deleting `port`, a key unrelated to the comment, destroys it:

```yaml
# input                                  # goccy native output
server:                                  server:
  host: localhost                          host: localhost
  port: 8080                             database:
  # end of the server block — applies      host: db.internal
  #   to the whole section
database:
  host: db.internal
```

These rules are therefore **work, not documentation of existing behaviour**. Distinguishing
"head comment directly above" from "head comment after a blank line" requires inspecting
source positions, since both parse to the same attachment.

**Corollary for creates.** Appending a key to a mapping causes it to **absorb any trailing
comment block** at the end of that mapping as its head comment. On the real corpus this was
ideal — `keryx auth` writing `access_token` landed it directly beneath the two-line comment
already describing `access_token` — but it is incidental. Where the trailing block is a
section note, the new key steals it. Same positional analysis as the delete rules; implement
together.

### D8 — A watch manager coordinates writes and reloads

The current 250 ms debounce is a timing heuristic and is **nondeterministic for multi-file
saves**: a `Save()` writing two layers produces two fsnotify bursts, and whether they coalesce
into one reload or two depends on whether they straddle the window. A single logical edit can
fire observers once or twice, raced.

A manager owning both the watcher and the write path makes notification **exactly-once by
construction** — commit lands all renames, then the manager notifies. Timing-based coalescing
cannot provide this; only the component that knows a write's extent can distinguish "three
files changed" from "one logical change touched three files".

Required behaviours:

1. **Exactly-once notification per logical change** — notify after all renames, never
   per-rename.
2. **Pluggable watcher.** fsnotify over `OsFs`; **polling** over any other `afero.Fs`. The
   current implementation calls `fsnotify.Add()` regardless of filesystem, fails on
   `MemMapFs`, and swallows the error at Debug — so reload is silently dead on the in-memory
   worktrees keryx uses. Polling is also the required fallback for NFS/SMB/FUSE and for hosts
   that have exhausted `max_user_watches`.
3. **Break write→reload→react feedback loops.** keryx writes at runtime and reacts to config
   changes, so `write → fsnotify → reload → observer fires → observer writes → reload → …` is
   reachable, and "react by normalising a value and writing it back" is a natural consumer
   implementation. The manager is the only component that knows which writes it originated; it
   MUST detect self-triggered cascades and either suppress them or fail loudly — never spin.
4. **Own the provenance index** (see D9).

**Not gated on anything else.** The module already owns its watcher (`container.go:344` is a
bespoke fsnotify implementation, not Viper's `WatchConfig`), so this can proceed
independently.

Two limits, recorded so they are not assumed away: it coordinates only *our* writes — a human
with the file open still races, so D10's fingerprint check remains necessary; and lock
ordering needs care, since `reload()` already takes `c.mu` and runs observers outside it. The
manager must follow the existing copy-under-lock, invoke-outside discipline.

### D9 — The provenance index is maintained by the manager, exposed lazily

`Writer()` is **not** built by the constructors: that would change every factory signature,
make readers pay for a write path they do not use, and start N redundant watchers.

But it is **not rebuilt from scratch on every write** either. keryx writes at runtime — its
studio persists config on the fly and reacts to its own writes — so a per-write rebuild of
every file's parse is the wrong trade. The manager (D8) knows when files change and when it
originated a write, so it can keep the index current: invalidate on reload, update on its own
writes.

*(This reverses an earlier decision that had the index rebuilt lazily per call. That reasoning
depended on there being no watch manager and on writes being occasional; both premises
failed.)*

### D10 — Execution is plan/apply: prepare → verify → commit

Routing requires multiple passes — locate the node for placement, then walk the layer tree to
validate which layer owns the key. That costs time. **Accepted:** a config write is
user-initiated and human-scale, so correctness beats latency.

Three constraints follow, none optional:

**Batch per save, not per edit.** Accumulate edits, resolve layers once, parse each affected
file once, write once. A settings screen saving 50 fields must not trigger 50 × N × M parses;
the obvious per-edit implementation is quadratic and only shows up under real UI load.

**Three phases:**

1. **Prepare** — build each target's new content and write it to a temp file in the same
   directory. The slow phase, and the one that fails safely: nothing is visible, and
   abandoning means deleting temps.
2. **Validate** — simulate the reload this commit will cause and schema-check the resolved
   candidate (D14). Expensive, so it precedes the cheap race-sensitive check.
3. **Verify** — re-check every fingerprint (size + mtime, or content hash, captured at read).
   If any file changed since routing, abort the whole set with a distinguishable conflict
   error. Nothing is committed, so abort is clean.
4. **Commit** — rename all temps into place, back to back.

Multi-pass routing widens the window for concurrent external modification, and there are three
concurrent mutators in practice: the watcher, the studio serving concurrent requests, and a
human with the file open. Optimistic concurrency turns a silent lost update into a visible,
retryable error.

**Atomic per file.** Temp-then-rename. The failure mode of a non-atomic write here is
*truncating a hand-authored, version-controlled config file*. The module already assumes
atomic-rename saves — `container.go:438`'s `rewatch` exists because "atomic-rename saves
replace the inode".

**Sequential, not concurrent.** Fanning writes across goroutines was considered and rejected:
it does not address the race (which is against external mutators and is dominated by the
routing passes), it makes partial application across files *more* likely while forfeiting the
"everything up to file N succeeded" property, the workload is one to four small files, and
concurrent writes through an arbitrary caller-supplied `afero.Fs` turn per-implementation
thread-safety into a question that must be answered rather than assumed.

Two-phase commit is not cross-file transactionality — that needs a journal, not worth it here
— but it pushes every likely failure into **prepare** and shrinks the observable
partial-application window to N renames.

**Dry run falls out for free.** The plan is computed before anything is committed, so expose
it: *"would write `db.host` to `prod.yaml`; currently shadowed by `env:KEYRX_DB_HOST`"*. That
satisfies D6's shadowing report, gives a CLI `--dry-run`, and gives the studio the preview it
needs.

### D11 — Validate by re-parse before commit

After producing the new document bytes, re-parse them and assert the semantic tree equals the
intended result. Cheap, and it converts silent corruption into a returned error.

Follows Renovate's precedent — splicing since 2016, using the parser only as an oracle.
Distinct from schema validation (see O2).

### D12 — Non-YAML formats fall back, but the layer guarantee is absolute

JSON and TOML fall back to a whole-file re-marshal without a typed warning. Recorded
consequence, accepted knowingly: `encoding/json` sorts map keys on marshal, so a JSON file
comes back **alphabetically reordered**. TOML cannot be edited structurally at all —
`pelletier/go-toml/v2` has no editing API at any path (verified at v2.4.3; its `unstable/` AST
*can* retain comments via `Kind: Comment` and `parser.KeepComments`, but there is no
write-back API and no compatibility guarantee). Neither format supports comments, so no
comments are lost.

**Non-negotiable:** the fallback writes **only that file's own layer**, never the merged view.
A fallback write re-marshals the file's own parsed document, not `AllSettings()`. Layer
flattening is the more serious defect and must not survive in the fallback path.

---

### D13 — Map-valued `Put` replaces the addressed subtree. The caller owns the consequences.

*(Resolves the former O1. Decided 2026-07-18.)*

`Put` accepts scalars and maps. A **map-valued `Put` replaces the addressed node**: keys
absent from the map are removed, so removals take effect. Deep-merge was rejected — it makes
"this subtree is now exactly this" inexpressible, which is precisely what the consumers need.

**Scalars-and-deletes-only was considered and is not viable.** Both real consumers already
perform map-valued writes: `avatarcmd.go:57` does `v.Set("avatars."+name, map[string]any{…})`,
and every theme command routes through `Catalog.WriteTo`, which the code documents as
*"replacing the whole subtree (so removals take effect)"*.

**The contract: a caller supplying a map is asserting ownership of that subtree, and accepts
that comments, anchors and block styles within it may not survive.** The library does not
attempt to guess intent. This must be documented prominently — it is a sharp edge by design,
not an oversight.

**Documented consequence, measured on the real file.** The `themes:` subtree that
`Catalog.WriteTo` targets contains **2 anchors** (`&palette`, `&voice`), **8 aliases**
(`*palette` ×6, `*voice` ×2), **18 comment lines** and **9 block scalars**. A naive replace
expands each alias into a duplicated copy, so the DRY structure the author built silently
becomes six literal copies, and the comments are lost. The file's own header directs users to
`keryx theme edit` as the supported editing path, so this is a mainline flow, not an edge
case.

**Therefore, a migration note for consumers:** porting `Catalog.WriteTo` unchanged — swapping
the write call but keeping the whole-subtree map — will damage the file on the first
`keryx theme edit`. The correct port computes a diff (the catalog is known before and after)
and issues targeted `Put`/`Remove` calls. The domain knowledge for that diff lives in the
consumer, which owns the tree; it does not belong in a generic differ here.

**`Remove` is the primitive that makes this port possible, and it is why whole-subtree
replacement was ever needed.** `Catalog.WriteTo` replaces wholesale *"so removals take
effect"* — a workaround for viper having no delete at any visibility (verified: viper v1.21.0
exposes no `Unset`, `Delete`, `Remove` or `Clear`; `Set` writes into the unexported
`v.override` and has no inverse). `Writer.Remove` (D4) supplies the missing operation, with
D7 governing what happens to the comments around the removed key. With it, a consumer
expresses "this theme is gone" directly instead of rebuilding the subtree to imply it.

**Upgrade path, no API change required.** Replace-vs-diff is an implementation quality
decision, not an interface one: both share a signature and the same contract ("the subtree
matches the map afterward"), and diffing merely preserves more. The library MAY later
internally diff — touching only genuinely changed values, leaving anchors, aliases and
comments on unchanged nodes intact — and strengthen the guarantee without breaking any
caller. Ship replace; revisit if the loss proves to matter in practice.

### D14 — Writes are schema-validated before commit, by simulating the reload they will cause

*(Resolves the former O2. Decided 2026-07-19. In the first cut, not deferred.)*

**Validate the resolved candidate, never the layer in isolation.** A single file may
legitimately be invalid on its own and valid only once merged — a base omitting a required key
that an overlay supplies. Layer-scoped validation would reject correct writes, which is
unacceptable in a module built for multi-file merge.

**Mechanism: simulate the reload the commit will trigger.** `reload()` already builds a
candidate from all files, validates it against the schema, and swaps only on success (see
`container.go` `buildCandidate` / `validateViper`). Pre-write validation is that same
operation with the pending changes applied. Reuse the code path rather than building a
parallel one.

This yields a real guarantee: **if pre-write validation passes, the reload the commit triggers
will also pass** — modulo an environment change between the two, which would equally affect an
ordinary reload and is out of scope. It also means validation and reload can never disagree,
which a separate validation path would eventually allow.

The candidate MUST be built the way reload builds it — including env and flag layers — because
that is the effective configuration after the write lands. Validating without them could pass
a write that the subsequent reload rejects, leaving the file changed and the config
last-known-good: the worst outcome.

**Reject only violations the write introduces.** Validate before and after; fail only if the
after-set is a strict superset. Rationale: a config that is *already* invalid must stay
editable. Refusing to write an unrelated key because some other key is missing would make an
invalid config unfixable through the very surface intended to fix it. The error MUST
distinguish "your change is invalid" from "your config was already invalid" — the two demand
different user action.

**Pipeline placement:** `prepare → validate → verify → commit` (extending D10). Validation is
expensive (full merge plus schema check); the fingerprint check is cheap and race-sensitive,
so it stays closest to commit.

**Schema source:** the container's schema, as set by `SetSchema`/`WithSchema` and already used
by reload; overridable per call. **No schema set → no validation**, matching reload's
behaviour. Note this module's validation is deliberately decentralised (each package validates
its own slice), so many containers will carry no schema and this step will be a no-op for
them — that is expected, not a gap.

**Dry-run reports validation.** D10's plan already computes targets and shadowing; it MUST
also report what validation would say, so a studio can show "this change is invalid" before
the user commits rather than after.

### D15 — Commits are serialised by a manager-owned lease

Separating the Writer from the container leaves four things unhandled. A single mechanism
closes all of them.

**The problem, precisely.**

1. **Verify→commit is not atomic between concurrent in-process Writers.** `cfg.Writer()`
   returns a fresh Writer per call, so two studio requests hold two Writers. Both stage edits,
   both reach Verify, both observe the same fingerprints (neither has renamed yet), both pass,
   both Commit. Last writer wins and the first one's changes are lost **silently**. D10's
   fingerprint check defends against *external* mutators over a long window; it does nothing
   about two of our own Writers interleaving.
2. **The manager cannot know a commit is in flight.** D8 requires notification *after all
   renames*, but the manager only learns of a multi-file commit if the Writer tells it — and
   the two are deliberately separate components. Without a channel, the watcher fires on the
   first rename while the rest are still landing, and exactly-once does not hold.
3. **Partial commit failure has no recovery.** Once a temp is renamed the original is gone;
   there is nothing left to roll back to.
4. **The standalone writer has no manager**, so it participates in none of this.

**The mechanism.** The watch manager owns a **commit lease**. A Writer acquires it before
Verify and releases it after the provenance index is updated:

```
acquire lease ──► prepare (temps) ──► validate ──► verify fingerprints
                                                          │
    notify once ◄── release lease ◄── update index ◄── commit (renames)
```

- Holding the lease across **verify→commit** serialises in-process commits, closing (1).
- Acquiring the lease **is** the announcement, closing (2) with no separate channel. The
  manager holds notifications for the lease's duration and emits exactly one afterwards.
- The manager marks the provenance index pending for the window, so a concurrent reader does
  not route against an index about to change.

**Scope: per-process.** Two processes writing the same file still race, and D10's fingerprint
check remains the only defence — correctly so. Cross-process locking is not portably
achievable without lock files, which is a larger commitment than this feature warrants. State
this in the docs rather than implying stronger protection than exists.

**Partial commit failure (3).** Retain each target's original bytes for the duration of the
commit phase. If a rename fails partway, restore the already-renamed targets from those
originals, then report the failure. This is best-effort rollback, not a journal: if a
*restore* also fails, the error MUST name precisely which files are in which state. A caller
must never be left guessing what landed.

**The standalone writer (4).** `config.WriteFile` is **uncoordinated by construction** — it
has no container and therefore no manager. That is acceptable for one-shot CLI use, and must
be documented as such: it is not safe for a long-running process that also watches the same
files. Consumers with a container MUST use `cfg.Writer()`. This asymmetry is the cost of the
standalone entry point in D4 and is worth it, but it must not be discovered at runtime.

## Architecture

```
                    ┌───────────────────────────────┐
  cfg.Writer() ───► │  Writer                       │
                    │  routing (D6), plan/apply(D10)│
                    └───────┬───────────────┬───────┘
                            │               │
              provenance index         document package (D3)
              (raw per-file parses,    goccy: parse, locate,
               env-free — D5)          mutate, comment rules (D7),
                            │          re-emit.  NEVER yields values (D2)
                            │               │
                    ┌───────┴───────────────┴───────┐
                    │  Watch manager (D8)           │
                    │  fsnotify | polling           │
                    │  exactly-once notify          │
                    │  loop breaking                │
                    │  owns provenance index (D9)   │
                    └───────────────────────────────┘
```

The read path is untouched. Values continue to resolve through the existing container.

---

## Acceptance criteria

1. A file with head, inline and foot comments survives a `Set` on one key: that key's value
   changes and **every comment is still present and still attached to the same key**. Blank
   lines, key order and indentation are explicitly not asserted.
2. Repeated writes converge — the second and subsequent writes of unchanged content produce no
   further drift or accretion.
3. A merge key (`<<: *base`), a folded (`>`) scalar, a literal (`|`) block, an anchor/alias
   pair and an astral-plane emoji all survive a write **unmangled**.
4. `Delete` removes a key and its whole subtree with no residue, and obeys D7's four
   comment-ownership rules — including hoisting a trailing comment onto the preceding sibling
   rather than destroying it.
5. Writing to one file of a multi-file container pulls in **no** values contributed by other
   files, defaults, env or flags. Verified with: a key set in a lower layer, a key overridden
   by env, and a bound changed flag.
6. `Defines(key)` returns the highest-precedence file literally containing the key, and `""`
   when no file does — **unaffected by an environment variable of the matching name** (guards
   D5).
7. An edit whose effective value remains shadowed is reported as such, with the shadowing
   source named.
8. Setting a key that does not exist creates it, at the deepest existing ancestor, without
   disturbing sibling comments or order.
9. Repeated edits from a long-running surface (N successive saves to the same keys) route to
   the **originating file every time** — the source never becomes progressively shadowed by an
   accumulating overlay.
10. A concurrent external modification between routing and commit produces a distinguishable
    conflict error and leaves every target file unmodified.
11. A crash or failure during a multi-file save leaves no target partially written.
12. Reload fires exactly once for a `Save()` touching multiple files.
13. Hot-reload functions on a non-OS `afero.Fs` (guards the silent-failure defect).
14. An observer that writes config on reload does not produce an unbounded cascade.
15. A round-trip on a non-existent file produces a valid new file.
16. A write that would make the **resolved** configuration violate the schema is rejected
    before any file is modified; and a write that passes pre-commit validation never triggers
    a reload that fails validation.
17. A write to a file that is invalid *in isolation* but valid once merged — a base omitting a
    required key its overlay supplies — is **accepted**. Layer-scoped validation must not
    cause false rejection.
18. A write to an **already-invalid** configuration succeeds when it introduces no new
    violation; and a genuine failure distinguishes "your change is invalid" from "your config
    was already invalid".
19. A container with no schema set writes without validation and without error.
20. **Two Writers obtained from the same container and committed concurrently do not lose
    updates.** One succeeds; the other either succeeds after it or fails with a
    distinguishable conflict — never a silent last-writer-wins.
21. A multi-file commit emits exactly one reload notification even though renames land
    individually, and no notification is emitted mid-commit.
22. A rename failure partway through a multi-file commit restores the already-renamed targets;
    if restoration also fails, the error names precisely which files are in which state.
23. Everything above holds against an in-memory `afero.Fs`.

---

## Non-goals

- Changing config **reads**. This spec touches the write path only.
- A general-purpose YAML editing library.
- Byte-identical round-trips; preserving blank lines, key order, indentation or comment
  alignment.
- Migrating files already flattened by `WriteConfigAs` — they heal on first structured write.
- Emitting the merged/resolved view from any API in this feature.
- Remote config, config search paths, or any Viper surface not already used.
- **An ephemeral `Container.Unset`.** `Container.Set` has no inverse, and viper supplies none
  to build on (v1.21.0 exposes no `Unset`/`Delete`/`Remove`/`Clear`; `Set` writes into the
  unexported `v.override`). This is a genuine API asymmetry and it is **deliberately left
  open**: every consumer inspected wants *persisted* removal, which `Writer.Remove` provides,
  and none has asked for in-memory removal.

  Recorded so the shape is known if it is ever demanded: "unset" is not well-defined against a
  file-sourced value — the file still says so — so the coherent implementation is a
  **tombstone in the ephemeral override layer** (`Get` returns nil, `IsSet` false, never
  persisted). That needs nothing from viper, but every read path must consult it, and
  `Unmarshal`, `UnmarshalKey`, `AllSettings`, `ToJSON` and `Validate` bypass the accessors —
  so a partial implementation would reproduce the same getters-disagree-with-everything-else
  defect already present in `Sub()`. Add it when a downstream consumer actually demands it,
  not before.

---

## Open questions

*None. O1 resolved as D13, O2 as D14.*

---

## Rejected alternatives

Recorded so they are not re-proposed.

**Byte-span splicing.** The only route to true byte-identity, which is not a requirement.
Costly regardless: goccy's `Position.Offset` is a **1-based rune offset that drifts −1 per
preceding comment** ([goccy#856]), and no candidate library exposes an end position or byte
range — a splice writer must build and maintain its own line index, plus block-scalar span
walking and materially harder key insertion.

**Blank-line placeholder technique.** yamlfmt's approach (substitute a sentinel comment per
blank line, strip after encode) does round-trip byte-identically on goccy. Unnecessary: blank
lines are out of scope. It would also rest the guarantee on a text pass the module owns, with
a sentinel that must be unguessable.

**`yaml.Node` re-emit.** See D1. Content corruption (emoji), archived upstream.

**Explicit write targets.** Proposed so a studio could save to a separate file
(`.keryx.local.yaml`) rather than the repo source. Rejected: **overlay accumulation defeats
layering.** A surface that edits repeatedly and always writes to an overlay causes that
overlay to progressively shadow the source — not a growth in file count, but the top layer
monotonically absorbing every key beneath it, until the hand-authored file's comments annotate
values nobody reads. Edit-in-place is correct for any repeated-edit surface. Every candidate
use case was checked: first-run creation is covered by D6's create rule; in-memory worktrees
*are* the source; export means the merged view, which is prohibited; deliberate machine-local
overrides are occasional and hand-authored.

Two ideas from that design are worth reviving if it is ever revisited: writes should emit a
**sparse overlay of edited keys only** (structurally immune to the env-secret leak rather than
guarded against it), and a target outside `ConfigFiles()` must be reported as
written-but-not-effective.

**Suppressing the self-inflicted reload.** The original framing was "suppress or not", answered
correctly on those terms. The framing was wrong — the available gain is determinism (D8), not
suppression.

**Unifying the document and value models.** Retaining comments in the value model used for
reads. No system in any language does this successfully: Rust's `toml` was built on
`toml_edit` and was unwound in v0.9 for a 542µs→267µs parse win; Python's ruamel reverted a
decoupling attempt in six weeks and pays 15–30×; HCL states the rationale in-source. Decisive
here: env, flag and default layers have no document at all.

[goccy#856]: https://github.com/goccy/go-yaml/issues/856

---

## Appendix — measured findings

Recorded so requirements are not later mistaken for over-engineering. All measured on this
machine unless noted.

| Finding | Bears on |
|---|---|
| `yaml.Node` no-op round-trip loses blank lines and `---`, rewrites merge keys, reflows folded scalars, escapes astral-plane emoji to `\U0001F680`; encoder overrides `Style` so there is no workaround | D1 |
| Blank-line sentinel on yaml.v3 **diverges**: +2 lines per save, monotonically. On goccy it converges (byte-identical) | Rejected alternatives |
| goccy vs yaml/v3 decode identical scalars to different Go types; yaml/v3 turns `99999999999999999999` into `float64(1e+20)`, irrecoverably. Threshold is `uint64` overflow (~1.8e19) — smaller values survive, so re-tests must use a value above it | D2 |
| `validate.go:167` `isInt` rejects `uint64` | D2 |
| goccy `Path.ReplaceWithNode` **destroys the node's inline comment**; mutate the node in place instead | D7, implementation |
| **Scalar setting must be type-aware.** goccy renders `StringNode` from its own `Value` field but `IntegerNode`/`FloatNode`/`BoolNode` from `Token.Value`. Mutating only the token — the advice from the earlier survey — **silently no-ops for strings**, the most common config value type. Verified: the same setter changed `enabled: false→true` correctly while leaving `log.level` and `content.profile` untouched. A test suite exercising booleans would pass while string writes did nothing | implementation |
| Quoting style is preserved automatically on set — `StringNode` takes its style from `Token.Type`, so `""` stays double-quoted | D1 |
| A freshly-parsed node appended to a mapping lands at **column 0**; copy a sibling's `Position.Column`/`IndentNum`/`IndentLevel` | D6 placement |
| Deleting the last key in a section destroys that section's trailing comment (attached as `FootComment` on the key node). No bleed onto the following key's head comment | D7 |
| goccy has 189 open issues including comment-fidelity bugs in **flow style** and multi-doc streams (#821, #820, #870). Fixture was block-style only — flow style is known-broken and untested | D1 risk |
| `fsnotify.Add()` fails on `MemMapFs`; `container.go:358` swallows it at Debug, so reload is silently dead on in-memory filesystems | D8 |
| fsnotify has documented polling as "not yet" since 2012; `radovskyb/watcher` hardcodes `os` and cannot observe an afero FS | D8 |
| No Go config library performs atomic writes — `grep os.Rename\|CreateTemp` across 19 candidates returned zero non-test files | D10 |
| Seven Go libraries offer per-key provenance; all are read-only. Every library that writes serialises the resolved view. The sets do not intersect | D5 |
| koanf's `Load()` discards the Provider before `merge` runs, so provenance is unreachable even from a fork; its `Parser` contract makes comment preservation structurally impossible | D5 |
| konf is **fail-open** on reload — calls `onChange(values)` with `err != nil, values == nil`, silently vanishing the whole layer | D8 (avoid) |
| goccy adds ~0.5 MB to a binary; CUE 14.4 MB; HCL 4.2 MB | D1 |
| `yaml/v4` rc.6 fixes merge keys but escapes astral-plane characters unconditionally (`WithUnicode` covers BMP only), and that escaping **cascades**: a literal block containing an emoji is rewritten as a double-quoted string. Folded scalars reflow regardless of `WithLineWidth(-1)`. No stable release exists | D1 |

### Corpus spike — completed 2026-07-18

Run against eight real files: `keyrx/pkg/cmd/root/assets/init/config.yaml` (232 lines, 60
comment lines, 21 inline), the **live** `.keryx.yaml` and `.agent/blog-style-rules.yml` from
the blog project where the studio is actually running, plus `go-tool-base` setup and coverage
policy, `.golangci.yaml`, and a 905-line generated manifest.

An independent raw-`#`-count check ran alongside the AST harvester, so a harvester blind to
some comments could not report a false pass.

| Experiment | Result |
|---|---|
| No-op round trip, 8 files | **Clean.** Every comment preserved and still attached to the same key. 81/81 raw comment references and 111/111 keys on the largest file |
| Idempotency, 3 passes | Converges at pass 1 (or pass 0 where input was already stable). No drift, no accretion |
| Targeted set × 4 real operations | **1 line changed** vs the re-emitted baseline, in every case. Inline comments survive the edit; quote style preserved |
| Delete on real files | No collateral comment loss |
| Delete, synthetic per-rule fixtures | **3 of 5 D7 rules free, 2 must be implemented** (see D7) |
| Create new key | Lands correctly; absorbs any trailing comment block (see D7 corollary) |

**Verdict: D1 and D2 are validated on real data.** The substrate does what the spec assumes.
The residual work is D7's two positional rules and the type-aware setter — both identified,
both bounded.

Spike sources: `scratchpad/goccy-spike/` (corpus, harvester, mutators, per-rule fixtures).

---

## Related defects

Found while investigating this feature. **Independent of it** — they should be raised and
fixed separately.

1. **Hot-reload discards flag bindings and `Set` overrides.** `buildCandidate()`
   (`container.go:496`) builds a fresh viper reading only files; `reload()` swaps it in at
   line 486. Every `BindPFlag` binding and `Set` value is silently lost. *(Bare viper does not
   have this bug — it keeps separate maps and only replaces `v.config`. It was introduced by
   the wrapper.)*
2. **Hot-reload never fires on a non-OS `afero.Fs`** — see the appendix. Fixed by D8's
   pluggable watcher.
3. **Sub-container observers are a silent no-op.** `Sub()` returns a container with a fresh
   `observers` slice; `notify()` only ever iterates the root's.
4. **`WriteConfigAs` materialises env-sourced secrets into files.** Security-relevant; closed
   by this feature but should not wait for it.

Additionally: `Sub()` remains **half-trapped** — reads route correctly through
`root`+`prefix`, but `WriteConfigAs`, `Dump`, `ToJSON` and `Validate` on a sub-container still
use the stale `subV` snapshot. And `SetTypeByDefaultValue(true)` (`config.go:53`) is **dead
code** — it only acts when `v.defaults` is populated, and `SetDefault` is never called.

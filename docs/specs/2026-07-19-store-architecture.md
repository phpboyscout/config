---
title: Store architecture — single-owner config I/O with layer-correct writes
date: 2026-07-19
author: matt.cockayne
status: approved
approved: 2026-07-19
issue: phpboyscout/go/config#1
supersedes: 2026-07-18-structure-preserving-config-writes.md
---

# Store architecture

## Why this exists

The module needs to write configuration back to the files it was loaded from — preserving
comments, targeting the originating layer, never flattening the merged view. A first design
kept the existing container and added a writer beside it. That design worked on paper and was
**fragile by construction**, which is not acceptable for a component every tool in the toolkit
depends on.

This spec replaces it.

### The diagnosis

Every mitigation the previous design accumulated — a commit lease, fingerprint checks,
two-phase commit, feedback-loop breaking, provenance-index invalidation, debounce tuning —
existed for one reason:

> **The filesystem was shared mutable state with no single owner.** The container read files,
> the writer wrote them, the watcher observed them. Three components, one resource, so every
> interaction needed a protocol — and every protocol is a place to get it wrong.

Moving responsibilities *between* those three components does not help; all three still touch
the files. The fix is to remove the sharing.

### The principle

**One component owns config I/O. Everything else is a view over what it produces.**

## Terminology

| Term | Meaning |
|---|---|
| **read** / **get** | retrieve a value from a container. Never mutates, never persists. |
| **edit** / **set** | set a value *in the container's ephemeral override layer*. Touches no persistence. |
| **write** | persist a change through the Store. |
| **Store** | sole owner of config I/O: load, parse, provenance, write, watch. Serialises all access. |
| **Backend** | a concrete source of config data behind the Store (local FS today; Vault/etcd/SSM later). |
| **Layer** | one contributing source of configuration, in precedence order. |
| **Snapshot** | an immutable, fully-resolved set of layers plus provenance, emitted by the Store. |
| **Container** | a view over one Snapshot. Provides typed reads, `Sub`, unmarshalling, env/flag resolution. Never touches I/O. |
| **provenance** | which layer supplied a key's effective value. |
| **shadowed** | a value present in a layer but outranked by a higher-precedence layer, or by env/flags. |

`edit` and `write` remain distinct operations on distinct components. `Container.Set` is an
ephemeral override and is never persisted; persistence happens only through the Store.

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│  Store — sole owner of config I/O                        │
│                                                          │
│   Backend(s)      parse + provenance      watch          │
│   ─────────       ──────────────────      ─────          │
│   local FS        document layer          fsnotify | poll│
│   (afero)         (goccy, D4/D5)          FOREIGN changes│
│                                           only (D8)      │
│                                                          │
│   Apply(ChangeSet) ──► validate ──► write ──► NEW        │
│                                              SNAPSHOT    │
│   ALL access serialised internally (D3)                  │
└───────────────────────────┬──────────────────────────────┘
                            │ immutable Snapshot
                            ▼
┌──────────────────────────────────────────────────────────┐
│  Container — a view over one Snapshot                    │
│   typed reads · Sub · Unmarshal · env · flags · override  │
│   viper as resolution engine only (D6)                   │
│   NEVER touches the filesystem                           │
└───────────────────────────┬──────────────────────────────┘
                            │ notify (exactly once, D9)
                            ▼
                    observers · ObservedSection[T]  (D10 — FROZEN)
```

---

## Decisions

### D1 — The Store is the sole owner of config I/O

No other component reads, writes, or watches config sources. The Container performs no I/O.

This is the decision every other property depends on. It is what removes the need for a
cross-component commit lease, makes the provenance index consistent by construction, and makes
the write→notify path deterministic.

### D2 — Snapshots are immutable; the Container is a view

The Store emits a **Snapshot**: parsed layers, merged data, and provenance, frozen at
emission. A Container holds one snapshot and swaps its pointer atomically when a new one
arrives.

Consequence: **a caller holding a snapshot sees a coherent configuration for that snapshot's
whole lifetime.** A sequence of related reads cannot straddle a reload and observe a mixture
of old and new state. Under the previous design this was a latent flake nobody had noticed;
here it is structural.

### D3 — All Store access is serialised internally

One owner, one internal lock, no protocol. Concurrent `Apply` calls serialise; a reader never
observes a partially-applied change set.

The previous design's commit lease was a cross-component mechanism attempting this from the
outside, and it had a hole: two writers could both pass their fingerprint check before either
committed, and the second silently overwrote the first. Internal serialisation makes that
unrepresentable.

**Scope: in-process.** Cross-process races remain, and D14's fingerprint check is the defence.
Portable cross-process locking needs lock files and is out of scope — state the limit, do not
imply stronger protection than exists.

### D4 — Own writes construct the next Snapshot directly. The watcher is for foreign changes only.

Previously a write propagated as: *write file → fsnotify → debounce → re-read every file →
rebuild → swap → notify.* A round trip through the filesystem, gated on a timing heuristic, to
learn something already known.

`Store.Apply` produces the new snapshot **directly from the content it just wrote**, then
notifies. The watcher's sole job becomes detecting changes the Store did not make.

This single change eliminates, structurally rather than by mitigation:

- **notification nondeterminism** — own writes notify exactly once, by construction, with no
  coalescing heuristic
- **the write→reload→react feedback loop** — own writes never re-enter through the watcher, so
  the cascade is unrepresentable rather than detected and broken
- **debounce sensitivity** for own saves
- **redundant re-parsing** of unchanged files

### D5 — The document layer preserves comments; goccy is a document parser only

File backends parse through **`github.com/goccy/go-yaml`**, retaining comments, key order,
quoting styles and block scalars.

**Guarantee:** the data structure is preserved and **comments survive attached to the correct
keys**. **Not guaranteed:** blank lines, key order, indentation, comment alignment, the `---`
marker, byte-identity.

Not `yaml.v3`/`go.yaml.in/yaml/v3`: their losses divide into forgivable formatting and
unforgivable content changes — merge keys rewritten to `!!merge`, folded scalars reflowed,
astral-plane emoji escaped. `go.yaml.in/yaml/v4` (rc.6, no stable tag) fixes merge keys but
still escapes astral-plane characters unconditionally, and that escaping **cascades**: a
literal block containing an emoji is rewritten as a double-quoted string. Details in the
appendix.

**Comment style is the consumer's choice; the guarantee is retention, not style.** Any input
style is accepted. Normalising on write — including flow → block — is acceptable **provided no
comment is lost**. What matters is that a comment survives attached to its key.

**One construct is unsafe and MUST be refused at load: a multi-line flow collection containing
interior comments.** Measured — goccy does not merely lose the comment, it **corrupts the
document**, swallowing the closing delimiter into the comment:

```yaml
# input                          # goccy output — INVALID YAML
bounds: {                        bounds: {min: 1, max: 10 # lower bound}
  min: 1,   # lower bound
  max: 10   # upper bound        tiers: [core, ingress # only in prod]
}
tiers: [
  core,      # always
  ingress    # only in prod
]
```

Both goccy and `yaml/v3` reject that output (`',' or '}' must be specified`). This is upstream
(#821, #820, #870), not a usage error.

Therefore: **detect this construct when a document is loaded for editing and refuse the
document up front**, with an error naming the location and the reason. Refusing at commit —
which D11's re-parse would do anyway — is too late: the user has already made their edits and
the failure looks arbitrary.

**Everything else in real use is safe.** Measured across six fixtures and the corpus: head and
inline comments *around* flow nodes, single-line flow mappings **with anchors** (the real
`voice: &voice {…}` at `keryx-init.yaml:152`), single-line flow sequences with quoted members,
comments on siblings of flow nodes, and edits/deletes adjacent to flow nodes all preserve
comments and anchors intact. **Multi-line flow collections occur zero times** across the blog,
keyrx, go-tool-base and all 20 `go/*` modules; single-line flow appears in 30 files.

**Hard invariant: goccy is never a value source.** goccy and the resolution engine decode
identical input to different Go types (`8080` → `uint64` vs `int`; large integers → `string`
vs an irrecoverable `float64`). `validate.go`'s `isInt` rejects `uint64`, so goccy-decoded
values reaching the read path would fail every schema integer. The boundary is
**documents vs values**, and it must not be crossed in either direction. Enforce with a lint
rule and a test.

### D6 — Viper is reduced to a resolution engine behind the snapshot boundary

The Store hands the Container **pre-merged data plus provenance**. Viper never sees a file
again: it resolves typed values, env bindings, flag precedence and unmarshalling from data
supplied via a reader.

This removes three defects structurally:

- `WriteConfigAs` has no file to write, so the env-secret leak cannot exist
- provenance lives in the Store, natural rather than reconstructed
- flag bindings surviving reload becomes the Container's own concern — it rebuilds from the
  new snapshot and **re-applies its own bindings**, instead of a reload silently discarding
  them

It also contains a future decision: with viper behind a snapshot boundary, replacing it later
is a swap of one "resolve typed values from merged data + env + flags" implementation, not a
rewrite.

### D6a — A document is a layer. Multi-document files are fully supported.

**Viper silently reads the first document of a multi-document file and discards the rest, with
no error.** Measured:

```
input : alpha: 1, shared: from-doc-one  ---  beta: 2, shared: from-doc-two
viper : map[alpha:1 shared:from-doc-one]     beta = <nil>
```

A user with a two-document config file has half of it silently ignored. That is a pre-existing
defect, and because D6 puts the Store between the files and viper, **this design can simply
fix it** — viper never sees the file, so its limitation does not apply.

**The model: a document *is* a layer.** Not "files, some of which have several documents" —
an ordered sequence of layers, each identified by `(source, documentIndex)`. A single-document
file contributes one layer; an N-document file contributes N.

Everything else then works **unchanged**:

- **Merge order** flattens across files and documents alike; documents overlay in the sequence
  the file defines, exactly as files overlay in load order. One rule, no special case.
- **Routing** (D13): reverse merge order, first writable match — naturally selects the right
  document. No extra dimension.
- **Provenance** (D7): gains a document index for free — `config.yaml#1:14`.
- **Writability**: a document is writable on the same terms as any other layer.

**Semantics are the module's choice, and must be documented as such.** YAML defines a
multi-document stream as a sequence of *independent* documents, not an overlay chain — k8s
treats them as separate resources. Later-document-wins is an interpretation this module
adopts because it is the only one consistent with how it already treats files. State it
plainly; do not imply it is YAML semantics.

**Verified writable.** Editing inside one document and re-emitting the whole file preserves
every other document, all separators, and every comment — measured on a 3-document fixture
with per-document comment blocks: sets in document 0 and document 1 each produced the intended
change with the other documents byte-untouched, 6/6 comments retained, and a clean re-parse.

**Not supported:** creating or removing whole documents. Writes target existing documents.

### D6b — An empty container is a value, not an absence

**Principle: emptiness never implies removal.** `key: {}` and `key: []` are present values,
distinct from `key` being absent. Code may legitimately require a parent to exist while it
holds no entries.

Consequences across the write surface:

- Deleting the last key of a mapping yields `key: {}` — it does **not** remove the parent
  (D17).
- Removing the last element of a sequence yields `key: []`.
- A map-valued `Put` with an empty map (D16) sets the subtree to `{}`. It is **not** a delete;
  `Remove` is the only way to remove a key.
- Empty containers round-trip through a write unchanged, and carry provenance like any other
  value.

**Viper is inconsistent here, in two opposite directions.** Measured:

| Input | `IsSet` | `Get` | in `AllKeys()` | in `AllSettings()` |
|---|---|---|---|---|
| `emptymap: {}` | **true** | `map[]` | **NO** | **NO** |
| `emptylist: []` | true | `[]` | yes | yes |
| `emptymapblock:` (no value) | **false** | nil | **yes** | NO |
| `nullval: null` | false | nil | **yes** | NO |
| `emptystr: ""` | true | `""` | yes | yes |

An empty **map** is visible to the getters but invisible to enumeration and serialisation; a
**null-valued** key is the reverse. Empty *lists* behave consistently, so the defect is
specific to empty maps.

This is why the previous write path was unsafe for empty containers: `WriteConfigAs`
serialised `AllSettings()`, so **`emptymap: {}` was silently deleted on every write** — the
flattening defect family again.

**The Store's position:**

1. **The document model is authoritative.** The Store parses and writes documents directly, so
   an empty container survives a write regardless of viper's view of it. The write-side defect
   is fixed by D1/D6.
2. **The Store's key enumeration is authoritative for structure**, and includes empty
   containers. Anything needing a complete key list — notably `detectUnknownKeys` in
   validation — MUST use it rather than viper's `AllKeys()`, which omits empty maps and
   includes null-valued keys the getters deny.
3. **Getter behaviour is unchanged**, so no consumer adapts: `IsSet("emptymap")` remains true,
   `Get` still returns an empty map.

### D7 — The Store owns file-layer provenance; the Container owns env/flag shadowing

The Store answers *"which file supplies `db.host`, at what line"*. The Container answers
*"…and it is currently shadowed by `env:KEYRX_DB_HOST`"*, because env, flags and the ephemeral
override layer have no file and belong to resolution, not storage.

Callers get the combined answer through the Container, which is the only component that can
see both halves.

### D8 — Watching is pluggable and never silently absent

The Store owns change detection for foreign writes, with a backend-appropriate mechanism:
fsnotify over an OS filesystem, **polling** over any other `afero.Fs`, and polling as the
required fallback for NFS/SMB/FUSE and hosts with exhausted watch descriptors.

**A watcher that cannot function MUST fail loudly at construction.** The present
implementation calls `fsnotify.Add()` regardless of filesystem, fails on `MemMapFs`, and
swallows the error at Debug level — so hot-reload is silently dead on the in-memory worktrees
keryx uses. Silent absence of a declared capability is prohibited.

### D9 — Notification is exactly-once per logical change, after the swap, and fail-closed

- **Exactly once** per logical change. A multi-file `Apply` emits one notification, not one
  per file. Own writes achieve this by construction (D4); foreign changes coalesce.
- **After the swap.** An observer reading the container it is handed must see the new values.
  Copy observers under lock, invoke outside it — the existing discipline, preserved.
- **Fail-closed.** A snapshot that fails to build or validate is rejected: no swap, no
  observer run, last-known-good retained. `OnReloadError` remains the separate channel for
  rejections and MUST NOT be conflated with the observer path.
- **Derived views forward registration.** Observers registered on a `Sub` view MUST fire.
  Today they never do, precisely because notification was an emergent property of who held the
  watcher rather than an owned responsibility.

### D10 — The typed-section surface is a FROZEN public contract

This is the module's extraction mechanism and may not churn, however much else changes.

Reusable packages declare a tiny local interface and never import config:

```go
// go/transport/http/config.go — no config import
type ServerSettingsSource interface {
    Current() *ServerSettings
}
```

`*config.ObservedSection[T]` satisfies it **structurally**. That is what let
`go/transport/{http,grpc,gateway}` and the otel core be extracted, with
`go-tool-base/pkg/*/config_adapter.go` as the only meeting point.

**Unchanged, guaranteed:** `ObservedSection[T].Current() *T`, `Value()`, `Exists()`,
`Version()`; `ObserveSection(cfg Containable, key string, opts...)`; all five `WithSection*`
options including `WithSectionDefaultFunc(func(next Containable) T, merge)`; `SectionChange[T]`
with change-only delivery and monotonic `Version`.

**No downstream adapter changes.** Observers still receive a `Containable`; only what sits
behind it changes.

Two improvements come free: observers gain snapshot consistency (D2), and a section's `apply`
callback can no longer run twice for one logical edit (D9).

**Optimisation available, not required:** the snapshot knows which subtrees changed, so
`ObservedSection` may skip decoding untouched sections instead of decoding everything and
discarding most of it via `DeepEqual`.

### D11 — Backends are asymmetric interfaces with declared capabilities

A single `Backend{Load; Save}` interface is a trap. Reading is *fetch, parse, normalise*.
Writing carries ownership, partial-vs-whole update, CAS, conflict detection,
validation-before-commit, secret handling, audit, IAM failure, eventual consistency, rollback,
and multi-key transactions most stores lack. A uniform interface either lies about those or
degrades to the weakest member.

```go
type Source interface {                        // every backend
    ID() string                                // provenance identity
    Load(ctx context.Context) (Layer, error)
}

type Writable interface {                      // optional
    Apply(ctx context.Context, cs ChangeSet) (Layer, error)
}

type Watchable interface {                     // optional
    Watch(ctx context.Context, fn func(Layer)) (stop func(), err error)
}

type Capabilities struct {
    PreservesComments bool   // document-like backends only
    AtomicMultiKey    bool   // etcd yes; SSM no
    CompareAndSwap    bool
    NativeWatch       bool   // vs poll-only
    Sensitive         bool   // e.g. Vault — never mirror into a file layer
}
```

**The coordination logic stays in the Store for every backend** — serialisation, snapshot
construction, provenance assembly, notification. It must never become per-backend, or the
fragility this design removes returns N times over.

### D12 — Ship one backend. Define the seam; do not build the plugin system.

The first cut ships the **local filesystem backend** only.

**An in-memory backend is not needed for testing.** Afero already provides file-and-memory
under one interface, so the in-memory store *is* the file backend over `MemMapFs`. Building a
second backend to serve tests would earn no abstraction.

Add `Writable`/`Watchable`/`Capabilities` implementations when a genuinely different backend
arrives (Vault, etcd, SSM). Designing those interfaces against imagined requirements is how
such abstractions go wrong.

Four consequences to settle **when** a second backend lands, recorded now so they are not
discovered then:

1. **Write routing must respect capability *and* sensitivity.** A value sourced from a
   `Sensitive` backend must never route to a file layer. This is the env-secret leak in a new
   costume — a security property, not a preference.
2. **The comment guarantee is document-backend-only.** D5's promise must be scoped in the
   docs, not implied universally.
3. **Cross-backend atomicity is impossible.** A ChangeSet spanning a file and an SSM parameter
   cannot be atomic. Either refuse cross-backend commits or declare them explicitly
   non-atomic and report exactly what landed. Do not paper over it.
4. **Foreign-change latency is a per-backend property** (etcd: native events; SSM: polling)
   and must be declared. Own-write notification remains deterministic regardless.

### D13 — Edit routing: reverse merge order, first writable match

Merge order is deterministic (lowest→highest precedence). Routing walks it in reverse and
selects the **first writable match**: an existing key routes to the highest-precedence
writable layer defining it; a new key to the highest-precedence writable layer. Callers may
override with an explicit target.

Rationale: it makes an edit **visible** — the value written is the value read back. Writing to
a base layer could leave the edit immediately shadowed.

**Non-writable layers are skipped** (env, flags, compiled-in defaults) and are identifiable as
such.

**A written-but-shadowed edit MUST be reported**, not swallowed: *"written to `prod.yaml`,
currently shadowed by `env:KEYRX_DB_HOST`"*. Correct to write, but the caller must be able to
tell the user.

**Node placement is a separate rule.** Within the target document: attach at the deepest
existing ancestor, synthesise missing intermediates, append as the last child — leaving
existing keys' comments and order untouched.

### D14 — Apply is plan/apply: prepare → validate → verify → commit

Writes are **slow, batched and correct** in preference to fast. A config write is
user-initiated and human-scale.

**Batch per Apply, not per change.** Accumulate a ChangeSet, resolve layers once, parse each
affected document once, write once. A settings screen saving 50 fields must not trigger 50
routing passes.

1. **Prepare** — build each target's new content; stage it (temp file in the same directory
   for the FS backend).
2. **Validate** — D15.
3. **Verify** — re-check every fingerprint captured at read. Any foreign change aborts the
   whole set with a distinguishable conflict error. Cheap and race-sensitive, so it sits
   closest to commit.
4. **Commit** — atomically replace each target (rename for the FS backend), back to back.

**Retain originals across commit.** On a partial failure, restore already-committed targets;
if restoration also fails, the error MUST name precisely which targets are in which state. A
caller must never be left guessing.

**Sequential, not concurrent.** Parallel commits do not address the race (which is against
foreign mutators), make partial application more likely, forfeit the "everything up to N
succeeded" property, and would push per-implementation thread-safety onto every `afero.Fs`.

**The plan is inspectable without executing** — targets, shadowing warnings, and what
validation would say. That gives a CLI `--dry-run` and a studio the preview it needs.

### D15 — Writes are schema-validated by simulating the reload they will cause

**Validate the resolved candidate, never a layer in isolation.** A single file may
legitimately be invalid alone and valid once merged — a base omitting a key its overlay
supplies. Layer-scoped validation would reject correct writes.

**Mechanism:** build the candidate snapshot the write will produce, including env and flag
layers, and validate that. Because it is the same construction path a reload uses, validation
and reload cannot disagree — **if pre-write validation passes, the resulting snapshot will
also pass.** A separate validation path would eventually drift, and the failure mode is nasty:
a write that validates, lands, then gets rejected by the reload it triggers, leaving the file
changed and the config on last-known-good.

**Reject only violations the write introduces.** Validate before and after; fail only if the
after-set is a strict superset. A config that is *already* invalid must stay editable —
otherwise one missing key makes the config unfixable through the surface designed to fix it.
Errors MUST distinguish "your change is invalid" from "your config was already invalid".

**Schema source:** the container's schema, overridable per call. **No schema → no validation**,
without error. Validation here is deliberately decentralised, so many containers carry no
schema and this is a no-op for them — expected, not a gap.

### D16 — Map-valued `Put` replaces the addressed subtree; the caller owns the consequences

`Put` accepts scalars and maps. A map-valued `Put` **replaces** the addressed node — keys
absent from the map are removed, so removals take effect. Deep-merge was rejected: it makes
"this subtree is now exactly this" inexpressible, which is what consumers need.

Scalars-and-deletes-only is not viable: `avatarcmd.go:57` does
`v.Set("avatars."+name, map[string]any{…})`, and every theme command routes through
`Catalog.WriteTo`, documented as *"replacing the whole subtree (so removals take effect)"*.

**A caller supplying a map asserts ownership of that subtree and accepts that comments,
anchors and block styles within it may not survive.** The library does not guess intent. This
must be documented prominently — a sharp edge by design.

**Measured consequence.** The `themes:` subtree `Catalog.WriteTo` targets holds **2 anchors,
8 aliases, 18 comment lines and 9 block scalars**. A naive replace expands each alias into a
duplicated copy, so a deliberately DRY structure silently becomes six literal copies.

**Migration note:** porting `Catalog.WriteTo` unchanged will damage the file on the first
`keryx theme edit`. The correct port diffs the catalog (known before and after) and issues
targeted `Put`/`Remove` calls. **`Remove` is the primitive that makes this possible** — and
the reason whole-subtree replacement existed at all, since viper exposes no
`Unset`/`Delete`/`Remove`/`Clear` at any visibility.

**Upgradeable without API change:** internal diffing — touching only genuinely changed values
— is strictly more preserving under the same contract. Ship replace; revisit if the loss
proves to matter.

### D17 — Comment ownership on delete is explicit and tested

With formatting and ordering out of scope, **"comments stay grouped with the right keys" is
the entire fidelity guarantee** — and deletion is where encoders misbehave.

| Rule | Native goccy |
|---|---|
| Head comment directly above a key → removed with it | **free** |
| Head comment after a blank line = section comment → survives | **must implement** |
| Inline comment on the key's own line → removed with it | **free** |
| Trailing comment after the last key → hoisted to the preceding sibling | **must implement** |
| No bleed onto the following key's head comment | **free** |

Reproduced — deleting `port`, unrelated to the comment, destroys it:

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

Distinguishing "directly above" from "after a blank line" requires inspecting source
positions; both parse to the same attachment.

**Deleting the last key of a nested mapping MUST be handled explicitly — the naive
implementation emits invalid YAML.** Measured: removing the only key of a nested mapping leaves
an empty mapping that goccy emits at column 0.

```yaml
# delete feature.beta, where beta is the only key
feature:
{}            ← column 0 — REPARSE FAILS: "unexpected map key"
other: 1
```

Deleting one of several keys is fine; deleting the only *root* key is fine (`{}` at root is
valid). Only the nested-becomes-empty case corrupts. The corpus tests missed it because every
deletion there left a non-empty parent — it was found only by constructing the case
deliberately.

**Required behaviour:** when a delete empties a nested mapping, emit `feature: {}` on the key's
own line. Per D6b, an empty container is a **value**, not an absence — code may require the
parent to exist while holding no entries. Cascade-deleting the now-empty parent is the
alternative and is **rejected**: it would remove a key the caller did not ask to remove, and
would cascade further if that parent were itself then left empty.

**Corollary for creates:** appending a key to a mapping makes it **absorb any trailing comment
block** as its head comment. On the real corpus this was ideal (`keryx auth` writing
`access_token` landed it under the comment already describing it) but it is incidental — where
the trailing block is a section note, the new key steals it. Same positional analysis;
implement together.

### D18 — Snapshot consistency: implicit reads, pinned observers, scoped `With`

*(Resolves the former O1. Decided 2026-07-19.)*

Three mechanisms, chosen because the exposure is narrower than it first appears.

**Reads are implicit.** `cfg.GetString(...)` resolves against the latest snapshot. Each
individual read is atomic (a single pointer load), so no read ever sees a torn value. **No API
churn** — every existing call site is unaffected.

**Observers receive a pinned container.** When an observer fires it is handed a container bound
to *the snapshot that triggered the notification*, not "latest". Without this, an observer
processing snapshot N can read values from N+1 partway through its callback. This is a real
defect, and the fix is **invisible**: the signature is unchanged, so no consumer adapts to it.
The same applies to `ObservedSection` rehydration.

**`With` provides scoped multi-read consistency.**

```go
err := cfg.With(func(pinned config.Containable) error {
    host := pinned.GetString("db.host")
    port := pinned.GetInt("db.port")
    return dial(host, port)
})
```

A pinned view **is a `Containable`** — no new type, no second getter surface, and it composes
with everything that already accepts one. Scoping to a closure removes both footguns of a
bare handle: a snapshot cannot be held indefinitely (pinning parsed data in memory) and cannot
silently serve arbitrarily stale config.

**Why not require pinning for all reads.** It would make the straddle unrepresentable, at the
cost of changing every call site across 20 modules, `go-tool-base` and keryx. Disproportionate,
because the exposure is already small:

- `Unmarshal`, `UnmarshalKey` and `UnmarshalSection` are **single operations** — they decode a
  whole struct from one snapshot and cannot straddle.
- Therefore the **entire typed-section pattern is already immune**. Every extracted package
  consumes `Current() *Settings`, receiving one immutable struct. That is the pattern the docs
  already recommend, and it is the one used by `go/transport/{http,grpc,gateway}` and the otel
  core.

What remains exposed is code making several *individual* `Get*` calls — adapters, CLI wiring,
legacy call sites — and `With` covers exactly that.

**State the limit honestly in the docs.** The default read path does **not** carry the
multi-read guarantee. D2's consistency property applies within a single call, within a `With`
block, and across a typed section. That must be documented plainly rather than implied by the
existence of snapshots.

---

## Breaking changes

Carte blanche was granted; these are deliberate.

| Change | Reason |
|---|---|
| Construction: a Store is created and a Container binds to it | D1 |
| `WriteConfigAs` removed from `Containable` | D6 — viper has no file; the method's semantics were the env-secret leak |
| `GetViper()` removed | Incoherent once viper is a resolution engine over a snapshot; returning it exposed a live internal that made the abstraction fictional |
| `NewContainerFromViper` removed | Replaced by construction from a Store or an in-memory Layer |
| `Sub()` semantics restated against snapshots | Closes the still-open half-trap where `Write`/`Dump`/`ToJSON`/`Validate` read a stale sub-snapshot |
| `SetTypeByDefaultValue` deleted | Verified dead code — only acts when `v.defaults` is populated, and `SetDefault` is never called |

**Not breaking, guaranteed:** the entire typed-section surface (D10), and the `Containable`
read surface.

Ecosystem impact is bounded: `GetViper()` is reached for exactly five distinct methods across
all repos (`Set`, `MergeConfig`, `GetStringSlice`, `ConfigFileUsed`, `AllSettings`), three of
which become first-class methods. The larger downstream task is keryx's ~27 `*viper.Viper`
signatures, concentrated in the write-back paths this design replaces — so they are ported,
not merely retyped.

## Defects resolved structurally

All four are eliminated by construction, not fixed.

| Defect | Why it cannot occur |
|---|---|
| Reload discards flag bindings and `Set` overrides | The Container rebuilds from a snapshot and re-applies its own bindings; nothing rebuilds a viper from files behind its back (D6) |
| Hot-reload silently dead on a non-OS `afero.Fs` | Watching is the Store's, with a pluggable mechanism that fails loudly when unavailable (D8) |
| Sub-container observers never fire | One notification path, with derived-view forwarding required (D9) |
| `WriteConfigAs` writes env-sourced secrets into files | Viper never sees a file; there is no `AllSettings()` write path (D6) |
| **Multi-document files silently lose everything after the first `---`** (pre-existing; viper reads document 1 and discards the rest with no error) | **Fixed, not merely refused.** The Store parses every document and merges them as ordered layers before viper sees anything (D6a) |
| **Empty maps are silently deleted on write** (pre-existing; `WriteConfigAs` serialises `AllSettings()`, which omits `key: {}`) | The Store writes documents, not `AllSettings()`. An empty container is a value and survives (D6b) |

## Acceptance criteria

**Fidelity**

1. A file with head, inline and foot comments survives a `Put` on one key: the value changes
   and every comment remains attached to the same key. Blank lines, key order and indentation
   are not asserted.
2. Repeated writes converge — no drift or accretion.
3. Merge keys, folded and literal block scalars, anchor/alias pairs and astral-plane emoji
   survive a write unmangled.
4. `Remove` deletes a key and its subtree with no residue, obeying D17's five rules —
   including hoisting a trailing comment rather than destroying it.
5. Creating a key attaches at the deepest existing ancestor without disturbing sibling
   comments or order.
5a. Comments around and on **single-line** flow collections survive a write, including a flow
   mapping carrying an anchor; edits and deletes on siblings of a flow node leave its comments
   and anchor intact.
5b. A document containing a **multi-line flow collection with interior comments** is refused
   **at load** with an error naming the location — never partially written, never written
   invalid.
5c. A **multi-document** file contributes every document as an ordered layer; a value defined
   in a later document overrides the same key in an earlier one. Nothing is silently
   discarded.
5d. Editing inside one document of a multi-document file leaves every other document, every
   separator and every comment untouched.
5e. Deleting the last key of a **nested** mapping produces valid YAML (`key: {}`), not a
   column-0 `{}`; the parent key is not cascade-deleted.
5f. An empty map or empty sequence present in a source file **survives a write unchanged** and
   is never silently removed — including when the write targets an unrelated key.
5g. A map-valued `Put` with an empty map sets the subtree to `{}`; it does not remove the key.
5h. The Store's key enumeration includes keys whose value is an empty container, and excludes
   null-valued keys the getters report as unset — so validation's unknown-key detection cannot
   disagree with the getters.

**Layering and provenance**

6. Writing to one layer pulls in no value contributed by another layer, defaults, env or
   flags — verified with a lower-layer key, an env-overridden key, and a bound changed flag.
7. Provenance returns the highest-precedence file literally containing a key, and empty when
   none does — **unaffected by an environment variable of the matching name**.
8. An edit that remains shadowed after writing is reported as such, naming the shadowing
   source.
9. Repeated saves from a long-running surface route to the originating file every time; the
   source is never progressively shadowed by an accumulating overlay.

**Consistency and concurrency**

10. A sequence of reads inside a `With` block observes no mixture of pre- and post-reload
    state, even when a reload lands mid-block.
10a. An observer that reads several keys observes only the snapshot that triggered it, even
    when a further reload lands mid-callback.
10b. A single `Get*` call never returns a torn or partially-updated value.
11. Two concurrent `Apply` calls do not lose updates: one succeeds and the other either
    succeeds after it or fails with a distinguishable conflict — never silent
    last-writer-wins.
12. A foreign modification between routing and commit produces a conflict error and leaves
    every target unmodified.
13. A commit failure partway through a multi-target set restores the already-committed
    targets; if restoration also fails, the error names precisely which targets are in which
    state.

**Lifecycle**

14. A multi-file `Apply` emits exactly one notification, and none mid-commit.
15. Hot-reload functions on a non-OS `afero.Fs`; a watcher that cannot function fails loudly
    at construction rather than silently.
16. An observer that writes config on notification does not produce an unbounded cascade.
17. Flag bindings and ephemeral overrides survive a reload.
18. Observers registered on a `Sub` view fire.
19. A rejected snapshot swaps nothing and notifies no observer; the rejection reaches
    `OnReloadError`.

**Validation**

20. A write making the **resolved** configuration invalid is rejected before any target is
    modified; a write passing pre-commit validation never triggers a rejected snapshot.
21. A write to a file invalid *in isolation* but valid once merged is accepted.
22. A write to an already-invalid configuration succeeds when it introduces no new violation,
    and a genuine failure distinguishes the two cases.
23. A container with no schema writes without validation and without error.

**Contract**

24. Every existing typed-section consumer compiles and behaves unchanged (D10), verified
    against the real `config_adapter.go` files.
25. Everything above holds against an in-memory `afero.Fs`.

## Non-goals

Split deliberately: some of these are **permanent** (they conflict with the design's premises)
and some are **deferred** (excluded from this cut, expected to be revisited once there is a
working implementation to battle-test). A future reader must not treat the second group as
decided against.

### Permanent — these conflict with the design

- **Byte-identical round trips**; preserving blank lines, key order, indentation or comment
  alignment. Out of scope by the stated goal, and pursuing it costs disproportionately (see
  Rejected alternatives).
- **A general-purpose YAML editing library.** Comment-preserving editing serves configuration;
  it is not a product.
- **Emitting the merged/resolved view from any API.** This is the defect the design exists to
  remove; re-adding it under any name would reintroduce the env-secret leak.
- **Unifying the document and value models** (see Rejected alternatives).
- **Cross-process locking.** Not portably achievable without lock files; D14's fingerprint
  check is the stated defence and the limit is documented rather than hidden.

### Deferred — revisit after the first implementation is battle-tested

- **Additional backends** (Vault, etcd, SSM, ConfigMaps). D11 defines the seam; D12 explains
  why the plugin system is not built speculatively. This is the most likely of these to
  become a goal.
- **Cross-backend transactional writes.** Impossible to make atomic today (D12.3), but if
  multi-backend use arrives, "refuse" versus "declare non-atomic and report precisely" is a
  real decision to take then.
- **An ephemeral `Container.Unset`.** `Set` has no inverse and viper supplies none to build
  on. Left open because every consumer inspected wants *persisted* removal, which `Remove`
  provides. Recorded shape if demanded: a tombstone in the ephemeral override layer — needs
  nothing from viper, but every read path must consult it, and `Unmarshal`, `UnmarshalKey`,
  `AllSettings`, `ToJSON` and `Validate` bypass the accessors, so a partial implementation
  reproduces the getters-disagree defect.
- **Internal diffing for map-valued `Put`** (D16). Strictly more preserving under the same
  contract, so it can be added without an API change if the comment/anchor loss proves to
  matter in practice.
- **Skipping decode for unchanged subtrees** in `ObservedSection` (D10). A performance
  property, available once the snapshot tracks per-subtree change.
- **Creating or removing whole documents** within a multi-document file. Documents are read
  and written as layers (D6a), but the set of documents in a file is fixed by the file.
- **Requiring pinned reads everywhere** (D18). Would make the multi-read straddle
  unrepresentable; currently disproportionate, but reconsider if `With` proves
  under-reached-for in practice.
- **Remote config as a coordination layer** (as distinct from D11/D12 backends).

## Open questions

*None. O1 resolved as D18; the flow-style verification is complete and folded into D5.*

## Rejected alternatives

**Adding a writer beside the existing container.** The previous design. Rejected for the
fragility it required: a commit lease, fingerprint checks, feedback-loop breaking and index
invalidation, all consequences of three components sharing the filesystem with no owner.

**Replacing viper.** Considered at length this session and rejected: no Go library exceeds
~42% of a greenfield requirement set, none combines provenance with layer-correct writes, and
the replacement is an estimated 8,000–12,000 LOC. D6 makes it a contained future change rather
than a prerequisite.

**Unifying document and value models.** Retaining comments in the read-path value model. No
system in any language does this successfully: Rust's `toml` was built on `toml_edit` and
unwound in v0.9 for a 542µs→267µs parse win; ruamel reverted a decoupling attempt in six weeks
and pays 15–30×; HCL states the rationale in-source. Decisive here: env, flag and default
layers have no document at all.

**A single symmetric `Backend` interface.** See D11.

**Byte-span splicing** and the **blank-line placeholder technique.** Both existed only to
deliver byte-identity, which is not a requirement. Splicing is also expensive: goccy's
`Position.Offset` is a 1-based **rune** offset that drifts −1 per preceding comment, and no
candidate library exposes an end position, so a splice writer must maintain its own line
index.

**Explicit separate write targets** (a studio saving to `.keryx.local.yaml` rather than the
source). Rejected: **overlay accumulation defeats layering.** A surface editing repeatedly and
always writing to an overlay causes that overlay to progressively shadow the source, until the
hand-authored file's comments annotate values nobody reads. Edit-in-place is correct for any
repeated-edit surface. Every candidate use case was checked and none survived.

## Appendix — measured findings

All measured on this machine unless noted.

| Finding | Bears on |
|---|---|
| Corpus spike over 8 real files (incl. the **live** blog `.keryx.yaml` and a 232-line/60-comment keryx template): no-op round trip preserves every comment attached to the same key — 81/81 raw comment references, 111/111 keys on the largest | D5 |
| Idempotency: converges at pass 1 (or 0 where input was already stable). No drift | D5 |
| Targeted set × 4 real operations: **1 line changed** vs the re-emitted baseline in every case; inline comments survive; quote style preserved | D5 |
| D17 rules: 3 of 5 free, 2 require positional analysis | D17 |
| **Scalar setting must be type-aware.** goccy renders `StringNode` from its own `Value` field but `Integer`/`Float`/`Bool` from `Token.Value`. Mutating only the token — the advice from an earlier survey — **silently no-ops for strings**, the most common config value type. Observed: the same setter changed `enabled: false→true` correctly while leaving `log.level` and `content.profile` untouched | implementation |
| `Path.ReplaceWithNode` destroys the node's inline comment; mutate in place | implementation |
| A freshly-parsed node appended to a mapping lands at **column 0**; copy a sibling's `Position.Column`/`IndentNum`/`IndentLevel` | D13 |
| yaml/v3 turns `99999999999999999999` into `float64(1e+20)` irrecoverably; threshold is `uint64` overflow (~1.8e19), so re-tests must use a value above it | D5 |
| `yaml/v4` rc.6 fixes merge keys but escapes astral-plane characters unconditionally (`WithUnicode` covers BMP only), and that escaping **cascades**: a literal block containing an emoji is rewritten as a double-quoted string. No stable release | D5 |
| goccy adds ~0.5 MB to a binary; CUE 14.4 MB; HCL 4.2 MB | D5 |
| **Flow style, measured (2026-07-19).** 4 of 6 fixtures pass. Comments around flow nodes, single-line flow mappings *with anchors* (the real `voice: &voice {…}`), single-line flow sequences, and edits/deletes adjacent to flow nodes all preserve comments and anchors. The 2 failures are **multi-line flow collections with interior comments**, and the failure is **corruption, not loss** — the closing `}`/`]` is swallowed into the comment, producing YAML that *both* goccy and yaml/v3 reject | D5 |
| **Multi-line flow collections occur zero times** across the blog, keyrx, go-tool-base and all 20 `go/*` modules. Single-line flow appears in 30 files | D5 scope |
| **Multi-document read, measured (2026-07-19).** goccy passes 4/4: documents and per-document comments preserved, leading `---` retained, and a `---` *inside a block scalar* correctly **not** treated as a separator. **Viper silently reads only the first document** and discards the rest with no error | D6a |
| **Multi-document write, measured.** Editing inside document 0 and document 1 of a 3-document fixture each produced only the intended change: other documents byte-untouched, all separators intact, 6/6 comments retained, clean re-parse | D6a |
| **Deleting the last key of a nested mapping emits invalid YAML** — `{}` at column 0, re-parse fails with "unexpected map key". Deleting one of several keys is fine; deleting the only *root* key is fine. Found only by constructing the case deliberately; every corpus deletion left a non-empty parent | D17 |
| **Viper is inconsistent about empty containers.** `emptymap: {}` — `IsSet` true and `Get` returns an empty map, but the key is **absent from both `AllKeys()` and `AllSettings()`**. Conversely `nullval: null` and a valueless `key:` are **listed by `AllKeys()`** while `IsSet` reports false. Empty *lists* are consistent; empty maps are not. Consequence: the old `WriteConfigAs` (which serialised `AllSettings()`) **silently deleted empty maps on every write** | D6b |
| Zero real config files across the blog, keyrx, go-tool-base and all 20 `go/*` modules contain a document separator — so multi-document support is a capability gain, not a compatibility need | D6a |
| goccy carries 189 open issues, including the flow-style comment-fidelity bugs above (#821, #820, #870) | D5 risk |
| `fsnotify.Add()` fails on `MemMapFs`; the error is swallowed at Debug, so reload is silently dead on in-memory filesystems | D8 |
| fsnotify has documented polling as "not yet" since 2012; `radovskyb/watcher` hardcodes `os` and cannot observe an afero FS | D8 |
| No Go config library performs atomic writes — `grep os.Rename\|CreateTemp` across 19 candidates returned zero non-test files | D14 |
| Seven Go libraries offer per-key provenance; all are read-only. Every library that writes serialises the resolved view. The sets do not intersect | D7 |
| viper exposes no `Unset`/`Delete`/`Remove`/`Clear`; `Set` writes into the unexported `v.override` with no inverse | D16, non-goals |
| The `themes:` subtree holds 2 anchors, 8 aliases, 18 comment lines, 9 block scalars | D16 |

**No verifications outstanding.** Both flagged risks were measured and neither resolved as
expected:

- **Flow style** corrupts rather than degrades — the closing delimiter is swallowed into the
  comment — so the narrow failing construct is refused at load (D5).
- **Multi-document** turned out to be sound in goccy and broken in **viper**, which made it a
  capability to *gain* rather than a risk to avoid (D6a).

The same spike also surfaced an unrelated corruption case on the mainline delete path
(nested mapping emptied → invalid YAML, D17), which the corpus had not exercised because every
real deletion left a non-empty parent. That is the argument for constructing adversarial cases
rather than testing only against real data.

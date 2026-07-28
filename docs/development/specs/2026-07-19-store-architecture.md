---
title: Store architecture — single-owner config I/O with layer-correct writes
date: 2026-07-19
author: matt.cockayne
status: approved
approved: 2026-07-19
revised: 2026-07-19
issue: phpboyscout/go/config#1
supersedes: 2026-07-18-structure-preserving-config-writes.md
---

# Store architecture

## Revisions

### R1 — 2026-07-19: viper is removed outright, superseding D6

**D6 said viper is retained as a resolution engine behind the snapshot boundary, and the
"Replacing viper" alternative was rejected at an estimated 8,000–12,000 LOC. Both are now
wrong. Viper is gone: absent from `go.mod`, `go.sum` and every source file.**

This was not decided; it fell out of implementing D6, and was noticed only after the fact
during a dependency review. Recording it properly rather than editing D6 silently, because the
reasoning for the original estimate is worth keeping — it was sound against what it measured,
and it measured the wrong thing.

**Why the estimate was ten times too high.** It priced replacing viper *whole*: config search
paths, merge, precedence, provenance, watching, writing, multi-document handling. But D6 made
moving every one of those into the Store a **prerequisite**. Once that landed, viper's residual
job was two things — cast a resolved value to a type, and decode a map into a struct — and its
implementations of those are `spf13/cast` and `go-viper/mapstructure`, which this module
already imported **directly**. The estimate was made before D6 and never revisited after it.

**Measured residue:** 781 LOC across `view.go` (392), `env.go` (175), `unmarshal.go` (121) and
`flag.go` (93), against a 4,084 LOC module.

**What this changes:**

- D6's mechanism stands unchanged — the Store hands the read surface pre-merged data plus
  provenance, and nothing below that boundary sees a file. Only the identity of what sits
  *behind* the boundary changed, from viper to `View`.
- D6's closing note — "replacing it later is a swap … not a rewrite" — was correct, and the
  swap has happened. It cost less than the note implied because D6 itself was the expensive
  part.
- Every defect D6 claims to resolve structurally is still resolved, by the same argument.
- The breaking changes in D6 (`GetViper()`, `NewContainerFromViper`, `WriteConfigAs`) are
  unaffected: all three were already removed.

**Process note.** The approved spec said viper was retained while the implementation removed
it, for four commits. An implementation MUST NOT diverge from an approved decision without the
spec being amended first; this revision is the correction, not a precedent.

### R2 — 2026-07-19: writing from inside an observer is refused

**D8's claim that a cascade is "unrepresentable" is true only of the watcher loop. It was read
as covering observer-initiated writes, which it never did.** Those are now refused with
`ErrWriteFromObserver`.

**What was actually unrepresentable.** A write does not come back round through the watcher —
the Store builds the next snapshot from what it just wrote, so there is no filesystem round
trip to re-trigger it. That still holds and is unchanged.

**What was not.** `Apply → notify → observer → Apply → notify` is a loop entirely inside the
Store, and nothing prevented it. Measured depth 21 in a probe, bounded only because the probe
bailed out.

**Two defects were masking it,** and both are fixed independently of this decision:

1. **The file fingerprint never advanced past load.** `fileBackend.loaded` was set only when
   reading, and the Store deliberately never re-reads after committing, so the *second* write
   to any file compared against load-time content and was reported as a conflict with the
   first. The cascade test's nested write failed with `ErrConflict` before it could publish, so
   the test saw a bounded loop and passed. **Two consecutive writes to one file could never
   succeed** — a defect well beyond the cascade it was hiding.
2. **`Apply` notified unconditionally; `Reload` did not.** Reload has always had
   `if !changed { return nil }`. Apply had no equivalent, so a write resolving to the
   configuration already published was still announced as a change. An *idempotent* observer
   therefore looped forever: its second write was a no-op, notified anyway, and asked for the
   same no-op again. The two entry points disagreed about what counts as a change.

**Why refusal rather than accommodation.** With (2) fixed, a convergent observer settles by
itself. A non-convergent one — every reaction producing a genuinely new value — cannot
converge under any design; the only choice is what it gets. Suppressing nested notification
would leave other observers indefinitely stale; coalescing needs an iteration cap that is
itself arbitrary; a depth ceiling turns a crash into an error without making the program
correct. None is worth the complexity for a program that is already broken. **Writing while
reacting to a write is a breakdown of the ownership model and is refused.**

**The sanctioned pattern.** An observer that must change configuration records what is needed,
returns, and writes from outside the observation — a worker goroutine, a queue, the next tick.
Serialising and de-duplicating those deferred writes is the consumer's responsibility, being
the only party that knows which still matter.

**Why the check is goroutine-scoped.** Notification runs outside the Store lock, by design
(holding it across an observer deadlocks the Store). An unrelated goroutine may therefore
write while observers execute, and that write is legitimate. A flag or mutex cannot separate it
from the observer's own re-entrant call, and refusing it would be a worse defect than the
cascade. `goroutine.go` documents why goroutine identity is required and why a marked context
threaded through `Observable.Run` was rejected — an observer passing `context.Background()`
would defeat it silently, turning a guaranteed refusal into a cooperative one.

**Breaking:** `Store.Apply` and `Store.Reload` return `ErrWriteFromObserver` when called from
within an observer callback.

### R3 — 2026-07-19: invisible and bidirectional characters are escaped by design

**AC3 said astral-plane emoji "survive a write unmangled", which read as a promise that every
character comes back byte-for-byte. It does not, and should not.** The criterion is amended
above to state the actual rule.

**Measured behaviour.** Every character a reader can *see* survives verbatim — astral-plane
emoji, CJK, accented Latin, Greek, Cyrillic. Every **invisible or bidirectional-control**
character is emitted as an escape:

| Escaped | Preserved verbatim |
|---|---|
| `U+202E` RLO, `U+202D` LRO, `U+2067` RLI, `U+2069` PDI | astral-plane emoji (`U+1F30D`) |
| `U+200E` LRM, `U+200F` RLM, `U+061C` ALM | CJK ideographs |
| `U+200B` ZWSP, `U+200C` ZWNJ, `U+200D` ZWJ | accented Latin, Greek, Cyrillic |
| `U+2060` word joiner, `U+FEFF` ZWNBSP, `U+00AD` soft hyphen | |

**This is a security property, not a formatting artefact.** The bidi controls are the Trojan
Source construct (CVE-2021-42574): they make a document *render* one way and *parse* another,
so a reviewer approves what they see while the parser reads something else. Configuration is
exactly where that matters — these files gate deployments and carry credentials. The
invisible-space family is the quieter version: `admin` and `ad<ZWSP>min` are indistinguishable
on screen and are different strings. Escaping puts them in front of whoever reads the file,
which is what the Go compiler and the forge renderers do.

**Escaping is lossless.** An escape in a double-quoted scalar denotes exactly the character it
replaces, so a round trip returns the original string byte for byte. Asserted rather than
assumed, by decoding the emitted document and comparing.

**Where it is enforced.** The behaviour comes from the emitter beneath `yamldoc`, not from
either module, so a substrate change could withdraw it silently. It is pinned by a test in
`yamldoc` covering thirteen dangerous characters and five visible ones. Nothing else in the
toolkit would notice the regression.

**How this was misread.** A family emoji is several astral-plane characters joined by `U+200D`.
The emoji survive; the joiners between them are escaped. Read against AC3's original wording
that looked like a fidelity failure, and was initially recorded as one. It is the feature
working.

**Also corrected in the same pass:** a map-valued `Put` over a subtree whose first key differed
in length from the replacement was misaligned — silently promoting content to the top level
when the replaced mapping led with a merge key, and panicking outright when the replacement key
was longer. Fixed upstream in `yamldoc`; AC3's merge-key clause holds again once that release
lands.

### R4 — 2026-07-19: four criteria reconciled with the implementation

Four acceptance criteria described an implementation that does not exist. Each was resolved
against measured downstream usage rather than by preference, and the evidence is recorded
because in three of the four the criterion was wrong rather than the code.

**AC17 — ephemeral overrides are replaced by `Store.AddLayer`.** `SourceOverride` was a
declared constant with no API behind it: nothing could create one. Rather than build an
invisible layer that every read path, `Unmarshal`, `Keys` and validation would have to consult,
the capability is provided as what it always was — *another layer*. `AddLayer(ctx, name,
io.Reader)` contributes an in-memory source at the highest precedence, taking part in
precedence, provenance, merging and shadowing like any other, and needing nothing remembered
about it.

Being in-memory it is not writable, so routing already skips it and a later write lands in the
file beneath and is reported as shadowed. That property is inherited from the existing source
model rather than special-cased, which is the argument for this shape over a bespoke override.

Measured before deciding: every downstream `Set` is the first half of stage-then-persist —
`setTelemetryEnabled` does `Set` then `WriteConfig`, `writeBitbucketCredentials` is named for
it. **Not one use is an override.** That idiom is now `Apply`, in one step and without the
`WriteConfigAs` env-secret leak. Compiled-in defaults remain `WithReaders` at construction,
where they sit at the bottom of the order rather than the top.

**AC18 — withdrawn. A derived view is a read scope, not an observable.** `*View` has no
registration method, so the criterion was not merely unimplemented but inexpressible.
Measured: `Sub` is used downstream purely to scope reads (`vcs/config_adapter.go:28`,
`vcs/release/registry.go:24`), with **zero** observer registrations on a `Sub` in either
consumer. The old `Containable` did expose `AddObserverFunc`, so this was possible and never
fired — advertised, broken, unused. Scoped change notification remains available through
`ObserveSection`, which is what all four downstream adapters actually use.

*Migration note:* `Sub` returns `*View`, which satisfies `Reader` but **not** `Binder`. An
adapter chain taking a scoped container must be typed `Reader`.

**AC7 — provenance answers the combined question, not the split one.** D7 imagined the Store
answering "which file" and the container adding "and it is shadowed by". Env and flags are
ordinary layers, so `Origin` returns whichever won — env included — and the tests assert
exactly that. The combined answer is strictly more information than the split one, and this
spec's own non-goals section already describes `Explain` as the intended diagnostic. A
`FileOrigin` accessor was rejected: its difference from `Origin` is subtle, which is how
callers end up using the wrong one.

**AC24 — the D10 break is accepted and documented.** `ObserveSection` takes `Binder` and
`WithSectionDefaultFunc` takes `Observed`, so "no downstream adapter changes" is false. The
scale is smaller than the raw count suggests. Measured across `go-tool-base`: 160
`config.Containable` references, of which **89 are bare parameter declarations** that become
`config.Reader` by rename; only **12 call methods `Reader` lacks** (`GetViper` 6,
`WriteConfigAs` 4, `BindPFlag` 1, `ToJSON` 1); and `ObserveSection` has **4 call sites**, with
`*Store` already satisfying `Binder`.

So D10's guarantee is amended to what it can honestly promise: the typed-section *semantics*
are frozen — `ObservedSection[T]`, change-only delivery, monotonic `Version`, the five
`WithSection*` options — and the parameter types are not. The port is a rename plus twelve
genuine sites, which belongs in the migration document D19 requires.

### R5 — 2026-07-19: the Watchable seam D11 names is built

**D11 declared `Watchable` alongside `Writable`. `Writable` was implemented as
`WritableBackend`; `Watchable` was not, so watching type-asserted the unexported
`*fileBackend` and read its unexported `fs` and `path` fields.**

The consequence was not cosmetic. `WithBackend` exists so a consumer can supply its own
source, and such a backend could never be watched — `Store.Watch` reported
`ErrWatchUnavailable` with a real file on disk, or, in a set that also held a file backend,
silently omitted it while reporting success. That is the failure `Store.Watch`'s own doc
comment says the design prevents.

**This is not what D12 defers.** D12 defers building additional *backends* and explicitly asks
that the seam be defined now, so the coordination logic never becomes per-backend. It already
had: the Store knew that a source is a path on an `afero.Fs`, which is a backend's fact about
itself.

`WatchableBackend` is now declared next to `WritableBackend`, `fileBackend` implements it, and
`Store.Watch` collects watchable backends by interface assertion. The Store knows only that
some backends can say when something moved, and decides for itself whether the resolved
configuration actually changed.

**Also fixed by the same change:** watching previously took the first file backend's
filesystem and used it for every path, so a store mixing a real file with an in-memory one
statted half its sources against the wrong filesystem and never noticed a change to them. Each
backend now watches its own source, so the question cannot arise.

### R6 — 2026-07-19: Capabilities carries no writability flag

**D11 expresses writability through an optional interface and its `Capabilities` struct has no
such field. The implementation added `Capabilities.Writable`, which created a second gate that
disagreed with the first.**

Writability was decided three ways: by `Layer.Source.Writable` for a backend that had
contributed layers, by the `WritableBackend` assertion **and** `Capabilities().Writable` for one
that had not, and by the assertion alone in `prepare`. A backend implementing `WritableBackend`
while reporting `Writable: false` was therefore excluded as a write target while its file was
missing and routed the moment it existed — the same source writable or not depending on whether
it happened to exist yet.

The flag is removed. Writability is the `WritableBackend` assertion, as D11 designs it and as
`prepare` already required, so the type system checks it once rather than every caller checking
a flag at runtime — and a backend can no longer claim one thing and do another. R5 puts
watchability on the same footing.

**The remaining four fields stay, and are not dead weight.** D12 records a specific consequence
for each, deliberately, so it is not discovered when a second backend lands: a value from a
`Sensitive` source must never be written into a layer that is not, the comment guarantee is
document-backend-only and must be scoped rather than implied, cross-backend atomicity is
impossible and must be refused or declared, and foreign-change latency differs per backend and
must be stated. Deleting them would delete that record.

**`CompareAndSwap`, which D11 lists, is not implemented and is not restored.** The
prepare/verify/commit protocol already expresses compare-and-swap: `Pending.Verify` is the
comparison and `Commit` is the swap. Declaring a field for it would describe the same property
twice, in two places that could disagree.

### R7 — 2026-07-19: a backend's key-space dependency moves into Load's signature

**`keyAware` was an unexported interface carrying an undocumented temporal contract:
`observeKnownKeys` had to be called before `Load`, and only the Store methods that remembered
to do so enforced it.**

Being unexported, no backend outside this package could participate — so it was the environment
backend special-cased, wearing an interface's clothes. The contract was invisible in `Backend`,
and it was forgotten once on this branch: `rebuild` skipped the step, and the result was a write
that validated, landed, and then broke the reload it triggered, leaving the file changed and the
process on last-known-good.

`Load` now takes the layers beneath it:

```go
Load(ctx context.Context, below []Layer) ([]Layer, error)
```

Most backends ignore the argument. The dependency is real, so it belongs in the signature rather
than in a side channel: no caller can skip a step that does not exist separately, and the
environment backend stops holding Store-derived mutable state between two calls it does not
control.

**Breaking** for anyone implementing `Backend`, which is this package and the generated mocks.

Written up for users in `explanation/backends.md`, along with the reasoning behind R5 and R6 —
so the next person to wonder why writability is an interface and `Capabilities` is a set of
unread fields finds the answer in the documentation rather than rediscovering it.

### R8 — 2026-07-19: `SectionChange.Initial` becomes meaningful, on the caller's terms

**`Initial` was always false and `Changed` always true.** The section a binding starts with is
stored before the observer registers, so a delivery to an apply callback was never the first
one — two fields that looked like data and were constants.

The obvious repair, firing `apply` once at bind so `Initial` occurs, is wrong for a reason worth
recording. An apply callback is a consumer's reaction to configuration, and it routinely closes
over something constructed *after* the binding — a server, a pool, a template cache. Firing it
inside `ObserveSection` would run it against a half-built world, which is a nil dereference at
startup in a callback whose author reasonably assumed it only ran on change.

**Measured before deciding:** `go-tool-base` has **no production `WithSectionApply` call sites**;
the option appears in four test files, each asserting an exact delivery count. So the blast
radius was small — but "nothing uses it yet" is a reason to get the shape right, not a licence
to break it later.

`ObservedSection.ApplyInitial()` gives the caller the trigger:

```go
settings, err := config.ObserveSection[T](cfg, "server", config.WithSectionApply(reconfigure))
// ... construct whatever reconfigure closes over ...
if err := settings.ApplyInitial(); err != nil { return err }
```

Both fields are now honest: `Initial: true, Changed: false` for that delivery, the reverse for
every later one. A consumer that never calls it gets change-only delivery, exactly as before, so
the addition changes nothing that exists. Calling it twice is a no-op, and calling it after a
change has landed is also a no-op — delivering the section the binding started with, once a
later one has been applied, would hand the caller stale configuration and call it initial.

**Additive**, so D10's freeze holds: no existing signature or behaviour moves.

### R9 — 2026-07-19: one owner for struct-tag precedence

**Two functions answered "what config key does this struct field come from", and disagreed.**
`configFieldName` took the first non-empty tag in order mapstructure → json → yaml and returned
one name; `fieldKeys` treats mapstructure as canonical with yaml then json as aliases. They sat
either side of the same call in `UnmarshalSection`: the first decided whether the section
existed, the second decided what it decoded to.

**Why there were two, since it is instructive.** `configFieldName` is inherited — it arrived
with the extraction from `go-tool-base` and is byte-identical to the pre-rewrite container's.
`fieldKeys` was written for this rewrite, when honouring all three tag families became a
requirement, and it needs a different model: mapstructure can only match one name, so an
alternate spelling has to be resolved *into* the canonical key before decoding. The probe only
ever needed *a* name to test with, so first-non-empty was adequate on its own terms.

The rewrite replaced the decode path and inherited the probe. Three things kept it hidden: they
are in different files, the probe only decides anything when `SectionExists` is false, and no
test covered a field carrying both a `json` and a `yaml` tag under a prefix-less binding.

This is the same shape as R6 — an inherited mechanism left in place beside its replacement,
quietly answering the same question differently.

The probe now asks `fieldKeys` and tests the canonical name plus every alias, so a field written
under any spelling the decoder accepts is found by the check that decides whether to decode at
all.

**Also fixed, found while reproducing it:** `UnmarshalKey("")` resolved an empty path, found
nothing and returned without decoding — so a prefix-less binding reported its section present
and handed back an untouched target, while `Unmarshal` on the same view decoded correctly. An
empty key now means the scope the view describes, which is what such a binding plainly intends.

### R10 — 2026-07-20: D8's foreign-change coalescing is implemented

**D8 required two things and only one was built.** It says notification is "exactly once per
logical change… Own writes achieve this by construction (D4); **foreign changes coalesce**."
The first half was implemented and is what makes `Apply` exactly-once regardless of how many
files it touches. The second half was not: a foreign change spanning two files produced two
reloads and two notifications, and the first carried a combination nobody intended — the first
file updated, the second not yet.

Measured before being fixed: two files written 5ms apart gave two notifications with the first
showing the half-applied state, and only writes landing inside the same reload window (0ms
apart, by luck) gave one. The behaviour was documented as a caveat before it was recognised as
a divergence from an approved decision, which is the same failure mode as [R1](#r1--2026-07-19-viper-is-removed-outright-superseding-d6)
— documenting what the code does rather than checking it against what was agreed.

**Why the removed debounce was right to remove, and why this is not it.** D8 rejected the
pre-Store 250ms debounce because it was "nondeterministic for multi-file saves": a `Save()`
writing two layers could fire observers once or twice depending on whether the bursts straddled
the window. That criticism is correct and stands. The answer was to make the Store's own writes
exactly-once *by construction* — the component that performs a write knows its extent, and needs
no timing heuristic to recognise it.

That reasoning applies only to writes this Store performs. For a foreign change there is no
construction to appeal to: the filesystem reports that two files changed and not that they
changed together, and no amount of architecture recovers information the operating system never
supplied. Timing is genuinely the only signal available, so it is used here and nowhere else.

**What is implemented.** A settle window on the watcher-triggered path only. `Apply` never
reaches it and remains exactly-once by construction. `WithSettleInterval` configures it;
`DefaultSettleInterval` is 250ms, inherited from the pre-Store default where it was chosen to
tolerate slow and networked filesystems. An injected watcher (`WithWatcher`) defaults to no
window, because such a caller is supplying precisely the information the window compensates for
— overriding that would make `WithWatcher` non-deterministic, which is the only reason to use it.

Settling is bounded at four times the window from the first change in a burst. A pure
wait-for-quiet window never fires while changes keep arriving faster than it, which would leave
observers told nothing at all — the silent-failure mode refused everywhere else in this design.

**What this does not do, stated because the honest limit matters more than the mitigation.**
It reduces the problem; it does not remove it. Writes spaced further apart than the window are
indistinguishable from two separate changes, because that is what they look like. A logical
change spanning several files is atomic only if whoever makes it makes it atomically. The
guarantee belongs to the writer — by writing atomically, or by keeping settings that change
together in one file, which is always read atomically. Both the residual case and the coalescing
are asserted in tests so neither is folklore.

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
│   local FS        yamldoc module           fsnotify | poll│
│   (afero)         (documents only, D5)     FOREIGN changes│
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

### D5 — The document layer is the `yamldoc` module

File backends read and write documents through
**`gitlab.com/phpboyscout/go/yamldoc`** — a standalone library for comment-preserving YAML
editing, extracted from this design and published separately.

> **Amended 2026-07-19.** This decision originally specified an internal package built on
> `goccy/go-yaml`. The work was extracted to its own module before implementation, on the
> grounds that comment-preserving YAML editing is independently useful and a config library is
> a poor home for it: nobody wanting a YAML editor should have to depend on a precedence
> engine. The mechanism now lives there; the policy stays here.

**What `yamldoc` provides** (its spec is the authority; summarised here so this document
remains readable on its own):

- parse bytes into ordered documents, locate nodes by dotted path, set, remove, re-emit
- comments preserved and attached to the same keys; key order, quoting, block scalars,
  anchors, aliases and merge keys preserved
- **never emits bytes that do not parse** — every emit re-parses its own output
- detects constructs it cannot round-trip safely and **reports** them
- no file I/O, no merging, no precedence, and **no meaning assigned to document order**

**What stays here.** Everything `yamldoc` deliberately refuses to decide: which layer a write
targets (D13), what document order *means* (D6a), how empty containers affect routing and
enumeration (D6b), validation (D15), and whether an unsafe document is refused (below).

**Guarantee inherited from `yamldoc`:** the data structure is preserved and **comments survive
attached to the correct keys**. **Not guaranteed:** blank lines, indentation, comment
alignment, the `---` marker on a single-document file, byte-identity.

**Comment style is the consumer's choice; the guarantee is retention, not style.** Any input
style is accepted, and normalising on write — including flow → block — is acceptable provided
no comment is lost.

**Unsafe documents are refused at load — and refusal is this module's decision, not
`yamldoc`'s.** `yamldoc` reports constructs it cannot round-trip (currently: a multi-line flow
collection with interior comments, which corrupts rather than degrades — the closing delimiter
is swallowed into the comment). This module turns that report into a **load-time refusal**
naming the file and location, because discovering it at commit means the user has already made
their edits and the failure looks arbitrary.

```go
f, err := yamldoc.Parse(src)
if u := f.Unsupported(); len(u) > 0 {
    return fmt.Errorf("%s cannot be safely edited: %s", path, u[0])
}
```

**Hard invariant: `yamldoc` is never a value source.**

`yamldoc` and the resolution engine decode identical input to different Go types (`8080` → `uint64` vs `int`; large integers → `string`
vs an irrecoverable `float64`). `validate.go`'s `isInt` rejects `uint64`, so `yamldoc`-decoded
values reaching the read path would fail every schema integer. The boundary is
**documents vs values**, and it must not be crossed in either direction. Enforce with a lint
rule and a test.


### D6 — Viper is reduced to a resolution engine behind the snapshot boundary

> **Superseded in part by [R1](#r1--2026-07-19-viper-is-removed-outright-superseding-d6).**
> The snapshot boundary and everything it structurally resolves stand. Viper no longer sits
> behind it — the boundary is now `View` over a `Snapshot`, and viper is not a dependency.
> Read "viper" below as "the resolution engine".

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

**Access is `yamldoc`'s; meaning is ours.** `yamldoc` exposes every document and lets any of
them be edited, verified against a 3-document fixture: editing one leaves the others
byte-untouched with all separators and comments intact. It deliberately assigns **no meaning**
to their order — later-document-wins is this module's interpretation, layered on top.

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

> **Scope clarified by [R2](#r2--2026-07-19-writing-from-inside-an-observer-is-refused).** The
> cascade this decision makes unrepresentable is the *watcher* loop: an own write never returns
> through the watcher. It was read more broadly than that. An observer that writes back
> cascades entirely inside the Store, and is refused rather than prevented.

**A watcher that cannot function MUST fail loudly at construction.** The present
implementation calls `fsnotify.Add()` regardless of filesystem, fails on `MemMapFs`, and
swallows the error at Debug level — so hot-reload is silently dead on the in-memory worktrees
keryx uses. Silent absence of a declared capability is prohibited.

### D9 — Notification is exactly-once per logical change, after the swap, and fail-closed

> **Extended by [R2](#r2--2026-07-19-writing-from-inside-an-observer-is-refused).** Two points
> below were stated but not implemented: a write that resolves to no change is not a logical
> change and MUST NOT notify (`Apply` lacked the guard `Reload` had), and an observer MUST NOT
> write from within its callback — that is now refused, not merely discouraged.

- **Exactly once** per logical change. A multi-file `Apply` emits one notification, not one
  per file. Own writes achieve this by construction (D4); foreign changes coalesce.
  > **Implemented by [R10](#r10--2026-07-20-d8s-foreign-change-coalescing-is-implemented).**
  > The construction half shipped with the rewrite; foreign coalescing did not, and is now a
  > settle window on the watcher path alone.
- **A write that changes nothing is not a logical change.** A write may still rewrite the file
  — reflowing comments, moving a key into the layer that should own it — without altering the
  resolved configuration. Observers react to configuration, so that write MUST NOT notify.
- **After the swap.** An observer reading the container it is handed must see the new values.
  Copy observers under lock, invoke outside it — the existing discipline, preserved.
- **Fail-closed.** A snapshot that fails to build or validate is rejected: no swap, no
  observer run, last-known-good retained. `OnReloadError` remains the separate channel for
  rejections and MUST NOT be conflated with the observer path.
- **Derived views forward registration.** Observers registered on a `Sub` view MUST fire.
  Today they never do, precisely because notification was an emergent property of who held the
  watcher rather than an owned responsibility.

### D10 — The typed-section surface is a FROZEN public contract

> **Narrowed by [R4](#r4--2026-07-19-four-criteria-reconciled-with-the-implementation).** The
> semantics are frozen; the parameter types are not. `ObserveSection` takes `Binder` and
> `WithSectionDefaultFunc` takes `Observed`, so "no downstream adapter changes" does not hold.
> Measured port: a rename across 89 declarations plus twelve genuine call sites.

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

> **Completed by [R5](#r5--2026-07-19-the-watchable-seam-d11-names-is-built).** The `Watchable`
> seam this decision names was not built with the rest; watching reached through `Backend` into
> a concrete unexported type instead. It now exists as `WatchableBackend`.

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

### D17 — Comment ownership on delete is `yamldoc`'s, and is a dependency requirement

> **Amended 2026-07-19.** These rules were specified here and are now implemented and tested in
> `yamldoc`, since they are questions about YAML documents rather than about configuration.
> They remain recorded as a **requirement this module places on its dependency** — if a future
> substrate change broke them, the fidelity guarantee in D5 would break with it.

With formatting and ordering out of scope, **"comments stay grouped with the right keys" is
the entire fidelity guarantee** — and deletion is where encoders misbehave.

| Rule | Provided by the parser? |
|---|---|
| Head comment directly above a key → removed with it | free |
| Head comment after a blank line = section comment → survives | **implemented in `yamldoc`** |
| Inline comment on the key's own line → removed with it | free |
| Trailing comment after the last key → hoisted to the preceding sibling | **implemented in `yamldoc`** |
| No bleed onto the following key's head comment | free |

Two of the five needed implementing rather than inheriting. Distinguishing "directly above"
from "after a blank line" is invisible to the parse tree — both attach identically — so
`yamldoc` retains the source's blank-line positions to decide. The trailing case is worse: the
parser attaches such a comment to the *last entry* of the block, so a naive removal of an
unrelated key destroys a comment describing the whole section.

Where ownership is genuinely ambiguous, `yamldoc` errs towards **survival**: orphaning a
comment is untidy and visible, destroying one loses the author's work silently.

**Corollary for creates:** appending a key to a mapping makes it absorb any trailing comment
block as its head comment. Sometimes ideal, sometimes theft — same positional analysis,
handled together.

**Also `yamldoc`'s, and equally load-bearing:** removing the last entry of a nested mapping
must emit `key: {}` rather than the substrate's column-0 `{}`, which does not parse. See D6b
for why emptiness is a value here.

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

### D19 — Documentation is a deliverable, not a follow-up

The Diátaxis site under `docs/` is part of this module's product. It MUST be **revised** for
the new design — not appended to — and revision is part of the definition of done for each
work item, not a subsequent task.

**The reasoning developed here belongs in `explanation/`, not only in this spec.** A spec is a
decision record for maintainers; explanation pages are how users understand *why* the module
behaves as it does. The single-owner argument, what it eliminates, and the measured
limitations must be readable without opening a spec.

**Pages that change, and why:**

| Page | Change |
|---|---|
| `explanation/why-a-wrapper.md` | Currently reads as a catalogue of viper traps the wrapper papers over. Several no longer apply (the `Sub` trap, the write path); the argument becomes single-ownership and layer-correct writes. Substantial rewrite. |
| `explanation/precedence-and-merge.md` | Merge model gains **documents as layers** (D6a), provenance (D7) and shadowing queries. The "which files contributed?" section is superseded by real provenance. |
| `explanation/hot-reload-safety.md` | The reload model changes wholesale: own writes construct snapshots directly, the watcher handles only foreign changes, notification is exactly-once (D4, D9). |
| **NEW** `explanation/the-store.md` | Why a single owner; what fragility it removes; snapshots and consistency (D1–D4, D18). |
| **NEW** `explanation/provenance.md` | Where a value came from, what shadows it, and why merge-eager designs cannot answer this (D7). |
| **NEW** `how-to/write-config.md` | The write surface: `Put`/`Remove`, routing, dry run, conflicts (D13–D16). |
| `how-to/load-and-merge.md`, `how-to/hot-reload.md`, `how-to/typed-sections.md`, `how-to/validate-config.md`, `how-to/bind-cli-flags.md`, `how-to/test-with-mocks.md`, `getting-started.md`, `index.md` | Construction changes (a Store is created and a Container binds to it); removed APIs; new capabilities. |

**Documented limits are mandatory, not optional.** Each of these MUST appear in user-facing
docs, not merely in this spec, because each is a sharp edge a user can hit:

- D5's fidelity guarantee **and** what it does not cover; the refusal of multi-line flow
  collections with interior comments
- D6a's multi-document overlay semantics, stated as **this module's choice**, not YAML's
- D6b's empty-container-is-a-value rule
- D16's map-valued `Put` sharp edge, with the anchor/comment loss spelled out
- D18's caveat that the **default read path does not carry the multi-read guarantee**
- D3's in-process-only serialisation, and that cross-process races rely on conflict detection

**Migration documentation is required**, covering the removed APIs, the `Catalog.WriteTo`-style
port (D16), and what downstream consumers must change — with the explicit statement that the
typed-section surface does not change.

### D20 — Tests are written in advance; unit and BDD both mandatory

Implementation is test-first. A behaviour without a failing test that describes it is not
ready to be built.

**Unit tests** cover pure logic: the document layer (parse, locate, mutate, re-emit), comment
ownership (D17), merge and precedence, routing (D13), provenance (D7), empty containers (D6b),
validation (D15).

**Godog scenarios** cover behaviour that spans components and time — where a Gherkin scenario
genuinely earns its keep over a unit test:

- write → snapshot → notification, end to end (D4, D9)
- reload lifecycle: fail-closed rejection, last-known-good retention, `OnReloadError` (D9)
- exactly-once notification for a multi-file, multi-document `Apply`
- concurrent `Apply` calls, conflict detection, partial-failure restoration (D3, D14)
- watcher behaviour across filesystems, including the polling fallback and loud failure (D8)
- observer propagation, including derived views and the frozen typed-section contract (D10)

**This module has no godog today**; `go-tool-base` and `keyrx` do. Follow the house
convention: `features/<area>/*.feature`, tagged, with the feature docstring naming the spec
section it covers. Use the same godog version as the sibling modules.

**Every numbered acceptance criterion in this spec MUST map to at least one test or scenario**,
and the mapping must be traceable — a reviewer should be able to confirm coverage without
inference.

**Adversarial fixtures are mandatory, not corpus-only.** The corpus validated the common path
and missed two corruption cases: multi-line flow collections with interior comments, and
deleting the last key of a nested mapping. Both were found only by constructing the case
deliberately. Real-world fixtures prove it works; adversarial fixtures find where it breaks,
and both are required.

**The spike's fixtures are the starting suite.** They already encode every measured failure
mode — comment-ownership rules, type-aware scalar dispatch, flow style, multi-document,
empty containers — and must be carried into the module's tests rather than rewritten.

**Run under `-race`.** Concurrency is load-bearing in D3, D9 and D14.

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
   survive a write unmangled. **Every character a reader can see survives verbatim.**
   Invisible and bidirectional-control characters are instead emitted as escapes — losslessly,
   and deliberately: see [R3](#r3--2026-07-19-invisible-and-bidirectional-characters-are-escaped-by-design).
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
7. Provenance answers, for any key, which source supplied the effective value and which
   others define it: `Origin` names the winner, `Shadowed` lists every layer defining it, and
   `Explain` renders the chain. A caller wanting file-only provenance filters `Shadowed` on
   `Kind == SourceFile`. Amended by [R4](#r4--2026-07-19-four-criteria-reconciled-with-the-implementation).
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
17. Flag bindings survive a reload, and a layer added at runtime through `Store.AddLayer`
    survives one too. Amended by [R4](#r4--2026-07-19-four-criteria-reconciled-with-the-implementation).
18. *(Withdrawn — see [R4](#r4--2026-07-19-four-criteria-reconciled-with-the-implementation).
    A derived view is a read scope, not an observable.)*
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

24. The typed-section *semantics* are unchanged (D10) and every consumer ports mechanically:
    verified by compiling the real `config_adapter.go` files against this module and by a
    migration document covering the parameter-type change. Amended by
    [R4](#r4--2026-07-19-four-criteria-reconciled-with-the-implementation).

**Delivery**

24a. Every acceptance criterion above maps to at least one unit test or godog scenario, and
    the mapping is traceable without inference (D20).
24b. The test suite includes both the real-config corpus **and** adversarial fixtures for each
    measured failure mode, and passes under `-race` (D20).
24c. Every Diátaxis page listed in D19 is revised, the new pages exist, and each documented
    limit appears in user-facing docs rather than only in this spec (D19).
24d. Migration documentation covers the removed APIs and the consumer-side port, and states
    explicitly that the typed-section surface is unchanged (D19).
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
- **A consumer-facing diagnostics command.** `View.Explain` already answers "what is
  this value, where did it come from, and what else defines it" in one line:

  ```
  host = flag-host (from flag:--host); also defined in /app.yaml, env:APP_HOST
  ```

  That is the first question of every configuration debugging session, and today it is
  answered with grep. Surfacing it as a command in the toolkit's CLI framework — something
  of the shape `<tool> config explain db.host`, plus a whole-config variant listing every
  key with its winning source — would put it in front of the people who need it rather than
  leaving it as an API only library authors find.

  Deferred rather than dismissed: the data is already there and the library work is done,
  so this is a presentation decision belonging to whichever CLI adopts it first. Worth
  revisiting once a consumer is wired up and there is a real command surface to hang it on.

## Open questions

*None. O1 resolved as D18; the flow-style verification is complete and folded into D5.*

## Rejected alternatives

**Adding a writer beside the existing container.** The previous design. Rejected for the
fragility it required: a commit lease, fingerprint checks, feedback-loop breaking and index
invalidation, all consequences of three components sharing the filesystem with no owner.

**Replacing viper.** ~~Considered at length this session and rejected~~ — **reversed by
[R1](#r1--2026-07-19-viper-is-removed-outright-superseding-d6); viper was removed.** The
original reasoning, kept because the first two clauses still hold: no Go library exceeds ~42%
of a greenfield requirement set, none combines provenance with layer-correct writes, and the
replacement is an estimated 8,000–12,000 LOC. D6 makes it a contained future change rather
than a prerequisite.

The estimate was the error. It priced replacing viper *whole*, but D6 had already made moving
merge, precedence, provenance, watching and writing into the Store a prerequisite. The residue
was 781 LOC over two libraries already imported directly. See R1.

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

> **Note (2026-07-19).** The findings about the YAML substrate — scalar type dispatch, comment
> ownership, the empty-mapping emission defect, flow-style corruption, multi-document handling
> — now belong to `yamldoc` and are recorded in its spec and encoded in its tests. They are
> retained here because they justify decisions in *this* document (D5, D6a, D6b, D17) and
> because a reader deciding whether the dependency is sound should not have to leave to find
> the evidence.

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

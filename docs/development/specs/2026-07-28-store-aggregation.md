---
title: Store aggregation — a Store as a backend of another Store
date: 2026-07-28
author: matt.cockayne
status: implemented
approved: 2026-07-28
implemented: 2026-07-28
---

# Store aggregation

Composing stores, so one configuration can carry another's layers as its own —
with precedence and provenance unbroken across the join, and writes that reach
any layer in the composed graph.

Third and largest of three related specs, and the one to implement last. It
depends on [write-target options](2026-07-28-write-target-options.md), which is
how a promotion is expressed, and on [backend key
filtering](2026-07-28-backend-key-filtering.md), which is how a nested store is
bounded.

## Problem

Two stores end up being passed around together.

[keryx](https://gitlab.com/phpboyscout/keryx) and krites both hit it: a
**project-level** store for the project's own configuration, and a **global**
store for the user's CLI-wide settings. Every function that needs either ends up
taking both, every constructor threads both, and the precedence between them —
which is a real, ordered relationship — exists only as a convention in the
reading code, differently applied each time somebody needs it.

Two things would fall out of composing them instead:

- **One store travels through the code.** The project store carries the global
  one as a layer beneath it, so the call sites take one argument and the
  precedence is declared once, where the store is built.
- **Promotion becomes an ordinary write.** Moving a setting from the project
  config up into the global CLI config is currently a manual read-from-one,
  write-to-the-other. With the global store's layers reachable from the project
  store, it is `Set(path, value, To("<global file>"))` — the same write
  mechanism, the same conflict detection, the same plan.

It does not work today. `Store` implements none of `Backend`:

| `Backend` requires | `Store` has |
|---|---|
| `ID() string` | — |
| `Load(ctx, below []Layer) ([]Layer, error)` | `Reload(ctx) error`, layers held internally |
| `Capabilities() Capabilities` | — |
| *(`WatchableBackend`)* `Watch(ctx, interval, onChange)` | `Watch(ctx, ...WatchOption)` — different signature |

## The write-path constraint, and why it is small

The write path finds a backend by matching `Source.Name` against `Backend.ID()`:
`prepare` groups a plan's operations by `op.Target.Name` and calls
`backendByID` (`store.go:724`, `store.go:1051`), whose error says so outright —
*"a writable backend's ID() must equal the Source.Name of the layers its Load
returns"*.

Every backend today satisfies that trivially, returning one layer or several
sharing a name. An aggregate contributes layers under **other** names, by design
(D2), so it breaks the assumption.

The fix is not a new interface. `loadAll` already returns `[]backendLayers`,
pairing each backend with the layers it produced (`store.go:71`, `store.go:515`).
The store can build a **layer-name → backend map** at load time and have
`prepare` consult that instead of walking `ID()`s. Synthesised sources for empty
writable backends map to their backend the same way.

So the core change is one lookup, replacing a linear scan that was already doing
the wrong thing for any backend contributing more than one distinctly-named
layer. No adapter changes, no new optional interface, no contract change.

## Decisions

### D1 — An explicit adapter, not `Store` implementing `Backend`

```go
func Nested(s *Store, id string, opts ...NestedOption) Backend

// NestedPromotable makes the nested store's writable layers valid targets for
// an explicitly named write, without making them candidates for routing (D3a).
func NestedPromotable(*nestedConfig)
```

Rather than adding the methods to `Store` itself. A `Store` has no natural ID —
it is built from options, not from a named source — so one must be supplied, and
a method cannot take an argument. `Store.Watch` already exists with an
incompatible signature. And making every `Store` implicitly a `Backend` gives
every store a contract it mostly cannot honour.

It lives **in the core package**, not a sibling module, because it needs
`prepare`, `Pending` composition and the layer map — all unexported. This is the
one composition that cannot be an adapter.

### D2 — The inner store's layers pass through, as a contiguous block

**Resolves O1.**

The aggregate contributes the inner store's layers as its own, each keeping its
own `Source` — so `Explain` on a value from the global config says
`~/.krites/config.yaml`, not `nested:global`.

They occupy a **contiguous block** at the position the aggregate was declared:

```
config.NewStore(ctx,
    config.WithBackend(config.Nested(global, "global")),  // ─┐ block
    config.WithFiles(fsys, ".krites.yaml"),               //  │
    config.WithEnv("KRITES"),                             //  │
)

resolved order (lowest → highest):
    global: /etc/krites/defaults.yaml    ─┐
    global: ~/.krites/config.yaml         ├─ the nested block, inner order kept
    .krites.yaml
    env KRITES_*
```

Inner layers keep their relative order among themselves; the block sits where
the user put it. **Ordering is entirely user-driven** — declared by the order of
`WithBackend`/`WithFiles` in each store, never inferred by this module.

An earlier draft of this spec framed the choice as *flatten to one layer
(predictable precedence, lost provenance)* versus *pass through (intact
provenance, interleaved and unpredictable precedence)*. That was a false
dichotomy: interleaving is not a property of passing layers through, it is what
would happen if the layers were merged by some rule other than position.
Splicing a contiguous block has neither problem, and flattening was rejected
once that was clear — it discards provenance to solve a problem that does not
exist.

### D3 — Writes cascade through the composed order, exactly as reads resolve

Routing does not change and does not need to know a nested store is involved.
`findTarget` walks writable targets in reverse precedence looking for one that
already defines the path; with the block spliced in, inner layers are simply
part of that walk. A key defined only in the global config routes to the global
config's file, through the aggregate, without a special case.

The routing rule is *edit where it already lives, create where it will be seen*:
`findTarget` walks the writable targets backwards for the first that **already
defines the path**, and falls back to the highest-precedence writable target only
when nothing does.

That second clause is the one that matters here, and it is why D3a exists.

Mechanically the aggregate implements `WritableBackend` by delegating: its
`Prepare` hands each edit to the inner backend that owns the targeted layer, and
returns a `Pending` composing the inner `Pending`s. `Verify`, `Commit`,
`Rollback` and `Discard` fan out in declared order. The two-phase protocol
composes without modification — which is the payoff for `Pending` being an
interface rather than a struct.

### D3a — A writable nested layer is **pinnable but not routable**

The consequence D3's routing rule has for composition, and the reason
`NestedWritable` as first drafted was a footgun.

`findTarget` prefers a writable layer that already defines the key. So if
`theme` is defined in the nested global config and the nested layers route like
any other, then:

```go
store.Apply(ctx, config.Set("theme", "dark"))   // in a project store
```

…walks past the project file, finds `theme` in `~/.krites/config.yaml`, and
**writes there** — silently editing the value every other project inherits. That
is demotion by default, and it is the opposite of what a project-scoped edit
means.

keryx already found this and already solved it, by making the inherited global
subtrees non-writable in-memory readers so that *"a write can never route into
either"* (`openProjectStore`, spec 0042 D2). The forking behaviour that gives —
a project edit creating a project-owned file rather than mutating the shared one
— is the behaviour to preserve, not to regress.

But read-only nested layers cannot be promoted *into*, which is half the point
of the exercise. So a writable nested layer gets a third state:

| | routed to by default | reachable via `To()` |
|---|---|---|
| nested, read-only *(default)* | no | no |
| **nested, promotable** | **no** | **yes** |
| ordinary writable layer | yes | yes |

An ordinary write therefore always forks into the project's own layers, and
reaching the global config requires naming it. Explicit, and impossible to do by
accident:

```go
// forks into the project file, whether or not the global config defines it
store.Apply(ctx, config.Set("theme", "dark"))

// promotes — named, so it cannot happen by accident
store.Apply(ctx, config.Set("theme", "dark",
    config.To("~/.krites/config.yaml")))
```

A promotion usually wants the project's own copy gone too, or the promoted value
is immediately shadowed by it — which `Operation.ShadowedBy` will correctly
report. Both halves are one batch:

```go
store.Apply(ctx,
    config.Set("theme", "dark", config.To("~/.krites/config.yaml")),
    config.Remove("theme"),
)
```

**Routability is tracked by the store, not by a new `Source` field.** A file
inside the nested store genuinely *is* writable and routable — within its own
store. It is only non-routable *as seen from the outer store*, so it is a
property of the composition rather than of the source. The store already builds
a layer-name → backend map at load (see the constraint section); it records
routability alongside, and `route` receives the routable subset for
`findTarget` while `matchTarget` keeps the full writable set.

No public struct changes, no new obligation on backend authors, and a `Source`
does not mean different things depending on which store is looking at it.

### D4 — The composed graph is a tree: a store may appear at most once

Stricter than refusing cycles. Any attempt to nest a store already reachable in
the graph is refused at construction, whether or not it would close a cycle.

That rules out the diamond — one global store nested into two project stores
which are themselves composed together. A diamond is not a cycle and resolves
perfectly well for reads, so this is a deliberate narrowing rather than a
consequence. Three things pay for it:

- **Enforcement is identity alone**, walked once at construction. No reachability
  analysis, no memoisation, no way for the check itself to be subtly wrong.
- **A store appearing twice would contribute its layers twice**, at two different
  precedences. Every value in it would then be shadowed by a copy of itself, and
  `Explain` would name a layer that appears more than once in the order — which
  is exactly the kind of thing this module exists not to do.
- **A shared store is still expressible.** Nest it once, at the depth where both
  consumers can see it. The tree rule pushes the sharing into the shape of the
  graph, where it is visible, instead of into a repeated node.

Refused at construction rather than at load because this is always a programming
error, never a runtime condition, and the construction site is where the mistake
was made. `ErrCyclicStore` names both stores and the path between them.

### D4a — A shared *name* is fine; an identical `Source` is refused

Two cases that look alike and are not. The distinction is whether the layers are
distinguishable at all.

**A shared name is legal.** Two layers may carry the same `Source.Name` while
differing in `Kind` — a remote backend and a file that happen to agree on a
name. Layering resolves them per key exactly as it would any other pair, and for
`To(name)` the rule is **the highest-precedence layer of that name wins**: the
one whose value the caller can actually read. `Plan` names the resolved target
before anything is written, so a caller who has surprised themselves sees it
rather than discovering it later.

That rule is [write-target options](2026-07-28-write-target-options.md) D8, and
it was a **fix**: `matchTarget` walked the writable targets forward while
`findTarget` walked them backward, so a pinned name used to resolve to the
lowest-precedence copy — the one the others hide.

**An identical `Source` is refused, at composition, with `ErrDuplicateLayer`.**
Two stores in the graph loading the same path produce two layers whose `Source`
structs are equal in every field. Those are not ambiguous, they are
*indistinguishable*, and the routing index cannot hold both:

- `indexLayers` builds `map[Source]map[string]any` (`plan.go:359`), so the
  second layer's values **silently overwrite** the first's.
- `shadowedAbove` compares `src == target`, so it cannot tell them apart.
- `sensitiveSources` has the same `map[Source]bool` shape.

The observable consequence is a **wrong plan**: if the lower copy defines a key
the upper does not, shadow detection never sees it, and `Plan` reports a write as
effective when it is not. That is the failure class this module exists to
prevent, so it is refused rather than tolerated.

Refusing at composition rather than fixing the index is deliberate. Keying the
index by layer position would repair the collapse internally, but
`Operation.Target` is a `Source` in the public API — so a caller would still have
no way to name one of two identical layers, and the ambiguity would simply move
somewhere less visible. And a user who has composed two stores over the same file
has almost certainly not meant to: the same file resolved twice at two
precedences is a configuration mistake, and saying so loudly at construction is
kinder than resolving it silently and correctly.

The tree rule (D4) already removes the systematic source of duplicates — a store
appearing twice — so what this catches is two *different* stores pointed at one
file. Narrow, and always worth hearing about.

### D5 — Depth is unbounded, for reads and for writes

Infinite nesting is supported and documented as *not recommended*. Reads recurse
naturally. Writes cascade through each nested store's own layers in its declared
order, at every level.

**Resolves the write half of the previous O2.** The earlier draft proposed
refusing writes below the first level to keep conflict scopes and rollback
boundaries tractable. That is a real cost but it is not a reason to refuse: each
level's `Pending` already carries its own `Verify` and `Rollback`, so a deep
graph composes the same protocol repeatedly rather than needing a new one. What
it genuinely costs is atomicity, which D7 declares honestly, and error legibility,
which D6 addresses.

### D6 — An error names the layer, and the path to it

A conflict two levels down is reported against a file the caller may never have
heard of. So an error from a nested write names the full chain —
`global → ~/.krites/config.yaml` — rather than only the leaf.

This matters more as depth grows, and it is the concrete cost of D5 that
documentation must be honest about: *you can nest arbitrarily, and the deeper you
go the further an error is from the code that caused it.*

### D7 — Capabilities, and what composition cannot promise

- **`Sensitive` stays per layer**, which passthrough gets for free and flattening
  could not: an inner Vault layer keeps its own flag, so the leak guard has
  exactly the precision it has in a flat store. This is a substantive argument
  for D2 beyond provenance.
- **`AtomicMultiKey` is false**, always. A write spanning the project file and
  the global file spans two backends in two stores; nothing can make that
  indivisible. Declared rather than pretended.
- **`NativeWatch`** is true if any inner backend natively watches.
- **`PreservesComments`** is per layer, unchanged — a nested YAML file still
  round-trips its comments.

### D8 — Watching bridges the two signatures

The aggregate implements `WatchableBackend.Watch(ctx, interval, onChange)` by
running the inner store's own `Watch` and calling `onChange` when it fires. The
inner store already decides whether a reload changed anything before notifying,
so the outer store is not woken for no-op reloads.

This also covers the inner store being mutated directly — by `AddLayer`, or by
its own `Apply` from code that still holds it — which would otherwise leave the
outer store silently stale.

### D9 — Filtering is how a nested store is bounded

Exposing part of a global store is [backend key
filtering](2026-07-28-backend-key-filtering.md) applied to the aggregate:

```go
config.WithBackend(config.Filtered(
    config.Nested(global, "global"),
    config.Allow("theme.**", "editor.**")))
```

No aggregation-specific filtering surface. This is why the two were split, and
it is the shape the motivating case asked for — the project store reading a
*filtered* view of the CLI config.

### D10 — A broken inner store propagates its error and stays usable

A `Store` can be constructed alongside `ErrInvalidConfig` and remain usable —
deliberately, because a config tool must be able to open a broken config to fix
it. The aggregate does the same: it contributes whatever resolved and returns the
error alongside those layers.

Refusing would hide every key that parsed fine behind one that did not.
Swallowing is the silent degradation this module refuses everywhere else.
`fs.ErrNotExist` keeps its contract: an absent inner source contributes nothing
and is not an error.

### D11 — The aggregate does **not** observe the inner store's `Apply`

**Resolves O3.** A composed store does not hear about a write made directly to
the inner store it wraps. That is a stated limit, not an oversight, and the
reasoning is worth keeping because the question looks alarming until the write
path is read.

**What the watch bridge does not cover.** D8 bridges `Watch`, which catches
*foreign* change — another process editing the file. It cannot catch the inner
store's own `Apply`, because a store deliberately keeps its own writes off that
path: *"Apply builds the next snapshot from what it just wrote and notifies
directly, so a write cannot come back round through the watcher"*
(`store.go:1063`). The inner store therefore never calls `onChange` for its own
write, and the aggregate never hears.

**What that costs, precisely.** Between an inner `Apply` and the next outer
`Reload` or `Apply`:

- The outer store's snapshot is **stale** for the inner layers.
- `Plan` output can be **wrong in its reporting** — `ShadowedBy` and `Creates`
  are computed from the stale index, so a dry run may say a write will take
  effect when the inner store now shadows it.
- The outer store's **observers do not fire**.

**What it does not cost, which is the part that decides this.** `rebuild`
re-reads every backend the write did not touch, on every `Apply`
(`store.go:878`) — including the aggregate, which resolves the inner store
afresh. That happens **before** `validateChange` and `verifyAll`, so:

- the snapshot the outer store publishes after any write is **correct**, not
  stale;
- the schema judges fresh data;
- conflict detection compares fresh fingerprints.

So staleness is a **read-side reporting problem with a bounded window**, not a
correctness problem, and **any write through the outer store heals it as a side
effect**. D3a narrows it further: nested layers are not routing candidates, so
stale nested values cannot misdirect a write in the first place — only the
`ShadowedBy` and `Creates` fields of the report about it.

**Why not do it anyway.** Two reasons beyond cost.

The cascade guard does not obviously compose. `insideObserver` is per-store and
keyed by goroutine (`notify.go:164`), so a notification chain crossing a store
boundary is outside what `ErrWriteFromObserver` was built to catch. Whether a
cross-store loop is genuinely reachable would need proving rather than
assuming — and needing to prove that is itself a cost.

And the shape of the feature argues against the case. Aggregation exists so that
**one store travels through the code**. Holding the inner store separately *and*
writing to it directly is the state this replaces; a caller doing both has not
adopted the composition yet. The window is a migration artefact.

**The escape hatch already exists**: `Reload` on the outer store. The
documentation must say so plainly, in these words — *a store composed over
another does not see writes made directly to the inner store; reload if you hold
both.*

**Revisit if** a real case appears of a long-lived process that must hold both —
a studio server where a global-scope write travels a different code path. Even
then the first answer is to route that write through the composed store with
`To()`, which is what D3a exists for.

### D11a — When the outer store *is* watching, an inner write is picked up on the next tick

The partial answer that makes D11's limit survivable, and it needs no new API.

`publish` stamps every snapshot with a monotonic counter (`s.version.Add(1)`,
`store.go:583`), and `Snapshot.Version() uint64` is already public. So the
aggregate records the inner store's version at `Load` and compares it on each
watch tick, alongside the delegated inner `Watch` that D8 already runs:

```
on each tick:
    if inner.Snapshot().Version() != versionAtLoad {
        onChange()
    }
```

The outer store then reloads through its **ordinary** path: the aggregate
re-`Load`s, `sameConfiguration` decides whether anything moved, observers fire
only if it did.

**Why this is not a smaller cascading observer.** Nothing registers a callback
across the store boundary. The notification travels the outer store's *own*
watch path, on its own goroutine, exactly as a file change would — so the
per-store, goroutine-keyed `insideObserver` guard is never asked to reason about
a chain that crosses stores. That was the strongest argument against the
observer in D11, and this approach does not attract it.

Cost is an atomic load and a `uint64` comparison per tick per nested store.

**This is a partial answer and the documentation must say so.** Three limits,
none of which should be discovered rather than read:

- **It only works while the outer store is watching.** A store that is not
  watching stays exactly as D11 describes. That is proportionate — a store that
  is not watching has already accepted that it does not track change — but it is
  not "solved".
- **Latency is the poll interval.** Same as any watch, and worth saying because
  "picked up automatically" reads as "immediately".
- **The version bumps on every reload, changed or not**, because `publish` runs
  on both paths. So a bump does not imply a configuration change, and a tick can
  cost a wasted outer reload that resolves to "nothing moved" and notifies
  nobody. Wasteful, never wrong.

**No solution here is complete**, and the honest framing — which the how-to and
the explanation page must both carry — is that a store composed over another
sees an inner write *soon* when watching and *on next reload* when not, rather
than that the problem has been made to go away. An edge case with a narrow,
documented window is a different thing from a solved one, and writing it up as
the latter is how someone ends up depending on it.

`Plan`-side detection — surfacing the same version comparison where staleness is
observable as *wrongness*, in `ShadowedBy` and `Creates` — is deliberately left
out. It would mean deciding whether a dry run against a moved inner store is an
error or a warning, which is a real API question for a case nobody has hit.
Recorded as a follow-on, not an omission.

## Public API

```go
type NestedOption func(*nestedConfig)

func Nested(s *Store, id string, opts ...NestedOption) Backend

// NestedPromotable makes the nested store's writable layers valid targets for
// an explicitly named write, without making them candidates for routing (D3a).
func NestedPromotable(*nestedConfig)

var ErrCyclicStore = errors.New("config: store is already present in this graph")
var ErrDuplicateLayer = errors.New("config: two layers in the composed store are indistinguishable")
```

No `SourceKind` for the aggregate: it contributes no layer of its own, only the
inner store's, each carrying the kind it already had. `id` names the aggregate
for error messages and for the D6 chain, not for a layer.

## Testing strategy

- **`backendconformance` against an aggregate**, read-only and writable. The
  gate, as for every backend. This is the first backend whose layers are
  themselves resolved, so an assumption baked into the suite surfaces here — and
  the fix belongs in the core, as `Suite.BoundedKeySpace` did for
  `config-keychain`.
- **Precedence**: the nested block sits contiguously at its declared position;
  an outer layer declared above it outranks every inner layer, one declared
  below is outranked by all of them. Watched to fail by reordering.
- **Provenance**: `Explain` on a value from an inner layer names that layer, not
  the aggregate. This is the whole reason for D2 and must be able to fail.
- **Promotion, and the absence of demotion** (D3a). Three assertions, and the
  middle one is the point:
  1. `Set(…, To(innerLayerName))` on a promotable nested store lands in the
     inner file, verified by reading that file rather than the composed view.
  2. **An unpinned `Set` of a key the nested store already defines forks into
     the outer store's own layer** — asserted through `Plan` so the target is
     named, and watched to fail by making nested layers routable, which is
     precisely the regression this decision prevents.
  3. `To(innerLayerName)` against a nested store *without* `NestedPromotable`
     returns `ErrInvalidTarget`.
- **Depth**: a three-level graph resolves, and a write to the deepest layer
  reaches it (D5).
- **Tree enforcement** (D4): direct (`a` in `a`), indirect (`a → b → a`), and
  the diamond (`a` nested into both `b` and `c`, both composed into `d`) — all
  three refused with `ErrCyclicStore`.
- **Shared names versus identical sources** (D4a), which must be asserted as two
  separate cases or the distinction rots:
  1. Two layers sharing a `Name` but differing in `Kind` compose, and
     `To(name)` selects the highest-precedence one — asserted through `Plan`, so
     the test names the target rather than inferring it from a written file.
  2. Two layers with an **equal** `Source` are refused at composition with
     `ErrDuplicateLayer`. Watched to fail by allowing them and asserting the
     wrong-plan symptom directly: a key defined only in the lower copy is missed
     by `shadowedAbove`, and the plan claims a write is effective when it is
     not.
- **`Sensitive` precision**: a nested store containing Vault refuses a write of a
  Vault-defined key into a plain outer layer, and *permits* a write of an
  unrelated key to the same outer layer. The second half is what proves D7 kept
  per-layer precision rather than smearing sensitivity across the block.
- **Conflict**: an out-of-band change to an inner source is detected through the
  aggregate, and the error names the chain (D6).
- **Staleness is bounded** (D11): after a direct `Apply` on the inner store, the
  outer store reads stale — and a subsequent outer `Apply` publishes a snapshot
  containing *both* writes. The second half is what proves `rebuild`'s re-read
  is doing the work the decision rests on, so it is watched to fail by carrying
  the aggregate's layers over instead of re-reading them.
- **The version tick fires** (D11a): a watching outer store observes a change
  after a direct inner `Apply`, and does **not** fire when the inner store
  reloads without changing anything — the second half proving
  `sameConfiguration` still filters, so the tick costs a reload rather than a
  spurious notification.

## Migration & compatibility

Additive for consumers. Internally it changes `prepare`'s backend lookup from an
`ID()` scan to a layer-name map — a behaviour fix in its own right, since the
scan was already wrong for any backend contributing distinctly-named layers, but
one no existing backend can observe.

A minor version.

## Resolved

**2026-07-28 — O3, observing the inner store's `Apply`.** No cascading observer
(D11), but the watch bridge compares the inner store's snapshot version on each
tick (D11a), so a watching store picks the write up without any cross-store
callback. Investigated rather than assumed: `rebuild` re-reads untouched
backends on every `Apply`, before validate and verify, so the residual cost is
stale *reporting* in a window any outer write closes — not stale writes.
Accepted as **partial**, with the limits documented rather than smoothed over.

**2026-07-28 — O1 (second round), tree or DAG.** A tree: a store may appear at
most once (D4). Refusing the diamond is deliberate — it resolves fine for reads,
but a store appearing twice contributes its layers twice at two precedences,
shadowing itself.

**2026-07-28 — O2 (second round), duplicate layer names.** Split in two once
implementation showed the cases differ (D4a). A shared *name* is legal and
resolved by precedence, highest wins. An **identical `Source`** is refused at
composition with `ErrDuplicateLayer`, because such layers are indistinguishable
to `indexLayers` and `shadowedAbove` and silently produce a wrong plan — the
first recorded resolution assumed precedence could separate them, and it cannot.

**2026-07-28 — O1, flatten or pass through.** Pass through, as a contiguous
block (D2). The original framing was a false dichotomy: interleaved precedence
is not a consequence of passing layers through, and once the block is spliced at
its declared position, provenance and predictable ordering are both available.
Flattening was rejected.

**2026-07-28 — O2, write depth.** Unbounded (D5). The `Pending` protocol
composes, so depth costs atomicity and error legibility rather than correctness.

**2026-07-28 — O4, broken inner store.** Propagate and stay usable (D10).

## Revisions

### R1 — 2026-07-28: the conformance suite carried two assumptions this broke

Running `backendconformance` against an aggregate found both, and both were
fixed in the core rather than worked around in the adapter — the precedent
`Suite.BoundedKeySpace` set for `config-keychain`.

**Provenance no longer has to name the backend's ID.** The suite required a
layer's `Source.Name` to equal `Backend.ID()`, which was the rule while routing
scanned IDs for a target's name. It is not the rule now, and a backend whose
layers are resolved elsewhere necessarily breaks it. The assertion instead
requires provenance to name a layer the backend actually contributed.

**Writable no longer implies routable.** The suite's write cases relied on
routing to find the backend, which a pin-only backend must never let it do.
`Suite.PinOnlyTargets` declares the exception and the cases name their target.

### R2 — 2026-07-28: an aggregate needs its own conflict trap

D3 said the aggregate delegates conflict detection to the inner backends,
because they know their own rules. That is true and insufficient.

Each inner backend compares against the version *it* captured at the **inner**
store's load. If the inner store reloaded after the outer store did — which is
the ordinary way it notices a foreign change — every inner check passes, and the
outer store's write lands on top of a change it never saw. The lost update
`ErrConflict` exists to prevent, invisible from inside the inner backends.

So the aggregate applies the family's version-at-Load rule one level up,
comparing the inner store's snapshot version against the one recorded when this
store loaded it. Found by the conformance suite, not by reasoning.

### R3 — 2026-07-28: an aggregate must undo its own partial commit

`commitAll` rolls back the pendings that already succeeded and discards the
rest, so a pending failing midway is never asked to roll back. Correct for a
backend whose `Commit` is all-or-nothing — every backend before this one — and
wrong for an aggregate, whose `Commit` is several in sequence. A batch spanning
two inner backends where the second failed left the first one's write in place.

The aggregate now rolls back its own committed inner writes before returning.
Found by asking why `Rollback` had no coverage.

### R4 — 2026-07-28: a promotion leaves the inner Store object stale

Not anticipated, and the mirror of D11. A promotion writes through to the inner
store's **backend**, which is what makes it durable, but a caller still holding
the inner `Store` has its previous snapshot until it reloads.

Left as a documented limit for D11's reasons: reloading the inner store from
inside `Commit` would notify its observers on the outer store's schedule and can
fail outright inside one. Worth revisiting if it bites.

## Open questions

- **O4 — Should an ordinary backend be able to declare itself pin-only?** D3a's
  mechanism is not nesting-specific: "writable, but only when named" is a
  coherent thing for a system-wide `/etc/app.yaml` to want too. Nothing needs it
  yet, so nothing is exposed — but the internal representation should not
  preclude it, and this is worth revisiting the first time a backend asks.
- **O5 — Is `NestedPromotable` the right name?** It names the intent rather than
  the mechanism, which is usually right, but "promotion" is domain language
  borrowed from the motivating case. `NestedPinnable` names the mechanism and
  ties to `To`. Weak preference for the former.

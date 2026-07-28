---
title: Store aggregation — a Store as a backend of another Store
date: 2026-07-28
author: matt.cockayne
status: draft
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

That is what makes promotion an ordinary write:

```go
// where it would go by default — the project file, because that is the
// highest-precedence writable layer for a new key
store.Apply(ctx, config.Set("theme", "dark"))

// promoted to the user's global config, named explicitly
store.Apply(ctx, config.Set("theme", "dark",
    config.To("~/.krites/config.yaml")))
```

The second form is [write-target options](2026-07-28-write-target-options.md)
doing its job. That spec's O2 asked which real cases justify pinning; **this is
one**, and it should be its worked example.

Mechanically the aggregate implements `WritableBackend` by delegating: its
`Prepare` hands each edit to the inner backend that owns the targeted layer, and
returns a `Pending` composing the inner `Pending`s. `Verify`, `Commit`,
`Rollback` and `Discard` fan out in declared order. The two-phase protocol
composes without modification — which is the payoff for `Pending` being an
interface rather than a struct.

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

### D4a — Two layers may share a name; precedence resolves it

Legal, and not an error. Two stores in the graph loading the same path produce
two layers with the same `Source.Name`, and that is fine: layering already
resolves it per key, the higher-precedence one winning exactly as it would for
any other pair of layers.

For `To(name)` addressing the rule is **first match in precedence order wins** —
if a name does not match the first candidate it will not match the second, so
there is nothing to disambiguate between. `Plan` names the resolved target
before anything is written, so a caller who has surprised themselves can see it
rather than discover it afterwards.

The tree rule (D4) already removes the systematic source of duplicates, so what
remains is two stores genuinely pointed at the same file — which is either
deliberate or a bug in the caller's own composition, and in both cases
precedence is the honest answer rather than a refusal.

**Which end "first" means is settled in [write-target
options](2026-07-28-write-target-options.md) D8**, and it is not currently what
the code does.

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

## Public API

```go
type NestedOption func(*nestedConfig)

func Nested(s *Store, id string, opts ...NestedOption) Backend

var ErrCyclicStore = errors.New("config: store is already present in this graph")
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
- **Promotion**: `Set(…, To(innerLayerName))` lands in the inner file, verified
  by reading that file, not the composed view.
- **Depth**: a three-level graph resolves, and a write to the deepest layer
  reaches it (D5).
- **Tree enforcement** (D4): direct (`a` in `a`), indirect (`a → b → a`), and
  the diamond (`a` nested into both `b` and `c`, both composed into `d`) — all
  three refused with `ErrCyclicStore`.
- **Duplicate names** (D4a): two stores over the same path compose, resolve by
  precedence, and `To(name)` selects the end D8 specifies — asserted through
  `Plan` rather than by writing, so the test names the target rather than
  inferring it from a file.
- **`Sensitive` precision**: a nested store containing Vault refuses a write of a
  Vault-defined key into a plain outer layer, and *permits* a write of an
  unrelated key to the same outer layer. The second half is what proves D7 kept
  per-layer precision rather than smearing sensitivity across the block.
- **Conflict**: an out-of-band change to an inner source is detected through the
  aggregate, and the error names the chain (D6).

## Migration & compatibility

Additive for consumers. Internally it changes `prepare`'s backend lookup from an
`ID()` scan to a layer-name map — a behaviour fix in its own right, since the
scan was already wrong for any backend contributing distinctly-named layers, but
one no existing backend can observe.

A minor version.

## Resolved

**2026-07-28 — O1 (second round), tree or DAG.** A tree: a store may appear at
most once (D4). Refusing the diamond is deliberate — it resolves fine for reads,
but a store appearing twice contributes its layers twice at two precedences,
shadowing itself.

**2026-07-28 — O2 (second round), duplicate layer names.** Legal; precedence
resolves them and first match wins (D4a). No `ErrDuplicateLayer`.

**2026-07-28 — O1, flatten or pass through.** Pass through, as a contiguous
block (D2). The original framing was a false dichotomy: interleaved precedence
is not a consequence of passing layers through, and once the block is spliced at
its declared position, provenance and predictable ordering are both available.
Flattening was rejected.

**2026-07-28 — O2, write depth.** Unbounded (D5). The `Pending` protocol
composes, so depth costs atomicity and error legibility rather than correctness.

**2026-07-28 — O4, broken inner store.** Propagate and stay usable (D10).

## Open questions

- **O3 — Does the aggregate need to observe the inner store's `Apply`?** D8
  covers foreign change via `Watch`. But code that still holds the inner store
  and calls `Apply` on it directly bypasses the watcher entirely — `Apply`
  notifies its own observers without going through the watch path
  (`store.go:1063`). The outer store would not hear about it. Probably wants the
  aggregate registered as an observer of the inner store, which is a different
  mechanism from D8 and worth being explicit about.

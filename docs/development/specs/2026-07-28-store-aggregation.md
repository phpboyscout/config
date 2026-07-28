---
title: Store aggregation — a Store as a backend of another Store
date: 2026-07-28
author: matt.cockayne
status: draft
---

# Store aggregation

Composing stores, so a resolved configuration can be a layer in another
configuration.

Third and largest of three related specs, and the one that should be implemented
last. It depends on [write-target
options](2026-07-28-write-target-options.md) for addressing and on [backend key
filtering](2026-07-28-backend-key-filtering.md) for bounding what a nested store
exposes.

## Problem

A `Store` resolves layers into one configuration with provenance. A `Backend`
contributes layers to a store. The two shapes are close enough that composing
them looks like it should already work — a shared organisational configuration
as the base of a project configuration, a plugin's settings nested inside a
host's, a team default beneath a personal override.

It does not work today. `Store` implements none of `Backend`:

| `Backend` requires | `Store` has |
|---|---|
| `ID() string` | — |
| `Load(ctx, below []Layer) ([]Layer, error)` | `Reload(ctx) error`, layers held internally |
| `Capabilities() Capabilities` | — |
| *(`WatchableBackend`)* `Watch(ctx, interval, onChange)` | `Watch(ctx, ...WatchOption)` — different signature |

So this needs a real adapter, and the adapter has to answer questions the
`Backend` seam has not had to answer before.

## The constraint everything else follows from

**The write path finds a backend by matching `Source.Name` against
`Backend.ID()`.** `Store.prepare` groups a plan's operations by
`op.Target.Name`, then calls `backendByID` with that string (`store.go:724-758`).
The error when it fails is unusually explicit:

> no backend answers to %q. A writable backend's ID() must equal the Source.Name
> of the layers its Load returns

Every existing backend satisfies this trivially: it returns one layer, or several
sharing a name and distinguished by `Document`. An aggregate is the first backend
that would naturally want to contribute layers with *different* names — the inner
store's own layers, `base.yaml` and `overrides.yaml`, each with its own
provenance.

It cannot. Not without either flattening those layers into one name, or changing
the write path. That fork is D2, and it is the whole spec.

## Decisions

### D1 — An explicit adapter, not `Store` implementing `Backend`

```go
func Nested(s *Store, id string, opts ...NestedOption) Backend
```

Rather than adding `ID`, `Load` and `Capabilities` methods to `Store` itself.

Three reasons. A `Store` has no natural ID — it is constructed from options, not
from a source with a name — so one must be supplied, and a method cannot take an
argument. `Store.Watch` already exists with a different signature, so `Store`
satisfying `WatchableBackend` would require either renaming a public method or
living with two watches. And making every `Store` implicitly a `Backend` means
every `Store` gains a contract it mostly cannot honour.

The adapter also gives the options in D3 and D5 somewhere to live.

### D2 — The inner store contributes **one** layer, flattened *(provisional — see O1)*

The fork. Two coherent designs:

**One flattened layer.** The aggregate resolves the inner store and contributes
its effective values as a single layer named after the aggregate's `ID()`.

- Write routing works with no core change (the constraint above is satisfied).
- The inner store's precedence is atomic relative to the outer — the outer
  store's layers cannot interleave with the inner one's, which is almost
  certainly what "aggregate a store" means.
- **Provenance collapses.** `Explain` says `nested:shared`, not
  `shared/base.yaml`. For a module whose central claim is answering "where did
  this value come from", that is a real loss.

**N layers, passed through.** The aggregate returns the inner store's layers as
its own.

- Provenance survives intact.
- Write routing breaks, per the constraint, unless the core changes.
- Precedence interleaves: an inner layer could outrank an outer one, which makes
  "nest this store beneath mine" mean something much harder to predict.

Choosing flattening, provisionally, because interleaving is the wrong semantics
independently of the routing problem — and because a design whose selling point
is provenance should not adopt one where precedence is unpredictable.

But the provenance loss is severe enough to want a mitigation rather than
acceptance. See D4.

### D3 — Read-only by default; writable is opt-in and shallow

```go
config.Nested(inner, "shared")                        // read-only
config.Nested(inner, "shared", config.NestedWritable) // writes reach the inner store
```

Read-only is the safe default and covers the motivating cases — a shared base, a
team default — where the outer store should not be editing the inner source at
all.

When writable, the aggregate implements `WritableBackend` by routing the edits
*through the inner store's own router*. That is the honest implementation: the
inner store knows its own layers and its own conflict rules, and reimplementing
that in the adapter would be a second router that can disagree with the first.

**Reads recurse to any depth** — a nested store containing a nested store
resolves naturally, and nothing in the read path needs to know how deep it went.

Whether a *write* may route deeper than one level is **open question O2**, and
it is a narrower question than it looks: see the note there.

### D4 — Provenance carries the inner source in the layer, even when flattened

D2's cost is mitigable. The flattened layer is one `Source`, but the aggregate
knows which inner layer each key came from, because the inner store's snapshot
records it.

Proposal: the aggregate's layer names itself, and the adapter exposes a lookup —
`Origin(path) (Source, bool)` on the returned backend, or a richer `Explain`
integration — so a caller who wants the inner detail can get it, while the
routing and precedence model stays flat.

The shape of that is **open question O3**. It may want a new optional interface
on `Backend` (`ProvenanceDeclarer`?) so the store's own `Explain` can consult it,
which would make this useful to any backend that aggregates something.

### D5 — Cycles are detected at construction, and refused

`Nested(a, ...)` added to `a` is infinite recursion at `Load`, terminating in a
stack overflow with a trace that names nothing useful.

Detection: each `Store` carries an identity, and `Nested` walks the inner store's
backend list for an aggregate whose inner store is the outer one. Cheap, done
once, at the point where the mistake was made.

`ErrCyclicStore`, a new sentinel. Refusing at construction rather than at load
matters because a cycle is always a programming error, never a runtime
condition.

### D5a — A broken inner store propagates its error and stays usable

A `Store` can be constructed alongside `ErrInvalidConfig` and remain usable —
deliberately, because a config tool must be able to open a broken config in
order to fix it (`store.go:106`, and the same reasoning in keryx's
`openProjectStore`).

The aggregate does the same: it contributes whatever did resolve, and returns
the error alongside those layers rather than instead of them. The outer store is
already built to hand back a usable store next to an error, so this composes
without a new mechanism.

Refusing was rejected because one unparseable nested file would hide every key
that parsed fine, which is a worse outcome than the broken one. Swallowing was
rejected because it is exactly the silent degradation this module refuses
everywhere else — and because the outer store would have no way to tell a user
why a value they expected is missing.

`fs.ErrNotExist` keeps its existing contract: an absent inner source is not an
error, it is a layer that contributes nothing.

### D6 — The aggregate declares its own `SourceKind`

`SourceKind("nested")`, via the existing `SourceKindDeclarer` optional interface,
so a synthesised target for an empty writable aggregate presents with its own
semantics rather than defaulting to `SourceFile` (`store.go:966`).

### D7 — Capabilities are the inner store's, reduced

- `Sensitive` — **true if any** inner backend is sensitive. Conservative on
  purpose: flattening loses the per-layer distinction, so a false negative here
  is a secret written into a plain layer, and a false positive is a write
  refused that could have been allowed. The asymmetry is not close.
- `AtomicMultiKey` — **false**, always. The inner store's `Apply` may touch
  several inner backends; the outer store cannot make that indivisible with its
  own writes.
- `NativeWatch` — true if any inner backend natively watches.
- `PreservesComments` — false. The flattened layer is values, not a document.

### D8 — Watching bridges the two signatures

The aggregate implements `WatchableBackend.Watch(ctx, interval, onChange)` by
starting the inner store's own `Watch` and calling `onChange` when it fires. The
`interval` is passed to the inner store as its poll interval where that is
meaningful.

The inner store already decides whether a change actually changed anything before
notifying, so the outer store is not woken for reloads that resolve to the same
values.

### D9 — Filtering is how a nested store is bounded

Exposing a subtree of a shared store is [backend key
filtering](2026-07-28-backend-key-filtering.md) applied to the aggregate:

```go
config.WithBackend(config.Filtered(
    config.Nested(shared, "shared"),
    config.Allow("defaults.**")))
```

No aggregation-specific filtering surface. This is the reason the two were split.

## Public API

```go
type NestedOption func(*nestedConfig)

func Nested(s *Store, id string, opts ...NestedOption) Backend
func NestedWritable(*nestedConfig)      // shape provisional

var ErrCyclicStore = errors.New("config: nested store would form a cycle")

const SourceNested = SourceKind("nested")
```

## Testing strategy

- `backendconformance` against an aggregate over a store of in-memory readers,
  read-only and writable. **This is the gate**, as it was for every backend
  adapter — and this is the first backend whose layers are themselves resolved,
  so if the suite has an assumption about that, this is where it surfaces. Fixes
  belong in the core, as `Suite.BoundedKeySpace` did for `config-keychain`.
- Precedence: an outer layer outranks the whole nested store regardless of the
  inner ordering. Watched to fail.
- A write routed into a writable aggregate lands in the inner store's correct
  layer, verified by reading the inner file rather than the outer view.
- Cycle detection at construction, direct (`a` in `a`) and indirect
  (`a` → `b` → `a`).
- `Sensitive` propagation: a nested store containing Vault makes the aggregate
  sensitive, and a write of a Vault-defined key to a plain outer layer is refused
  with `ErrSensitiveLeak`. **This is the assertion most worth watching to fail**,
  because D7's reduction is where it could silently stop being true.
- Conflict: an out-of-band change to an inner source is detected through the
  aggregate.

## Resolved

**2026-07-28 — O2 (reads).** Reads recurse to any depth (D3). The write half
remains open.

**2026-07-28 — O4, a broken inner store.** Propagate the error and stay usable
(D5a), matching the store's own existing behaviour rather than inventing a
second policy for nested sources.

## Migration & compatibility

Additive — new constructor, new sentinel, new source kind. Nothing existing
changes unless O1 resolves towards passing layers through, which would require a
change to the write path's backend lookup and would be a larger, breaking-shaped
piece of work.

## Open questions

- **O1 — Flatten to one layer, or pass N layers through?** D2 chooses flattening
  provisionally. Passing through preserves provenance, which is this module's
  central claim, but needs the write path to let one backend answer to several
  names, and brings interleaved precedence with it. **This decides the shape of
  everything else and should be settled first.**
- **O2 — May a *write* route deeper than one nested level?** Reads at any depth
  are settled (D3). Writes are the open half: a write routed into an aggregate
  is re-routed by the inner store, and if that inner routing selects a further
  aggregate the process repeats. Each hop adds a conflict-detection scope and a
  rollback boundary that the outer store cannot make atomic, and an error two
  levels down has to be reported in terms the caller recognises.
- **O3 — How does inner provenance surface?** D4 wants the flattened layer to
  keep the detail. A method on the returned backend is easy; a new optional
  `Backend` interface that `Explain` consults is more useful and more surface.
- **O5 — Does the inner store's `AddLayer` invalidate the aggregate?** `AddLayer`
  replaces the inner backend list wholesale. The aggregate holds a pointer to the
  store, so it would see the change on next `Load` — but nothing tells the outer
  store to reload. Probably wants the aggregate to observe the inner store, which
  is D8's mechanism doing double duty.

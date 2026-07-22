---
title: The Store
description: Why one component owns all configuration I/O, and what follows from that single rule.
tags: [explanation, architecture, store]
---

# The Store

This module is built on one rule:

> **One component owns config I/O. Everything else is a view over what it produces.**

That component is the `Store`. It loads, parses, merges, records provenance, writes and
watches. Nothing else touches a configuration source — not the read surface, not an
observer, not a typed section. This page explains why that rule is worth the constraint,
and what follows from it.

## The problem it solves

An earlier design kept a container that read files, added a writer that wrote them, and
had a watcher observing them. Three components, one resource, no owner.

It worked on paper. What it needed to keep working was a commit lease, fingerprint checks
across component boundaries, two-phase commit, feedback-loop breaking, provenance-index
invalidation and debounce tuning. Every one of those existed for the same reason: **the
filesystem was shared mutable state with no single owner**, so every interaction between
those three components needed a protocol — and every protocol is somewhere to get it
wrong.

The instructive part is that moving responsibilities *between* the three does not help.
All three still touch the files. The only fix is to remove the sharing.

So: one owner. There is no protocol between a reader and a writer because there is one of
each, and it is the same object.

## What single ownership buys

**Access is serialised internally.** One owner, one lock, no cross-component mechanism.
Concurrent `Apply` calls serialise; a reader never observes a partially applied batch; two
writers cannot both pass a check and then overwrite one another. The previous design's
commit lease attempted this from the outside and had a hole exactly there — both writers
could pass their fingerprint check before either committed.

Be honest about the scope: this is **in-process** serialisation. Another process editing
the same file is not excluded, and the defence against that is conflict detection at
commit — the write is refused, not prevented. Portable cross-process locking needs lock
files and is deliberately out of scope.

**Provenance is consistent by construction.** Because the merge happens in exactly one
place, the record of which layer supplied which key is produced by the same pass that
produces the merged values. There is no second computation to drift. See
[Provenance](provenance.md).

**Watching cannot be silently absent.** The Store owns change detection, so it knows
whether the mechanism it needs actually works on the filesystem it was given, and fails
loudly when it does not. The predecessor called `fsnotify.Add()` regardless of filesystem,
failed on an in-memory one, and swallowed the error at debug level — hot-reload was dead
and nothing said so. An application that believes it will hear about changes and never
will is worse off than one that knows it must restart.

**Backends stay simple.** A backend fetches, parses and normalises. Serialisation,
snapshot construction, provenance assembly and notification stay in the Store for every
backend. That coordination must never become per-backend, or the fragility this design
removes comes back once per implementation.

## Snapshots

The Store's output is a `Snapshot`: the layers that contributed, the values they merge to,
the provenance of each key, and a monotonically increasing version. It is immutable.
Layers are cloned on the way in, composites are copied on the way out, and nothing mutates
one after construction. A reload produces a new snapshot; it does not edit the old one.

Immutability is the point rather than an implementation detail. A caller holding a
snapshot sees a coherent configuration for that snapshot's whole lifetime, so a sequence
of related reads cannot straddle a reload and observe a mixture of old and new state.
Under a mutable design that was a latent flake nobody had noticed; here it cannot happen.

`View` is the typed read surface over one snapshot. A `View` performs no I/O whatsoever —
it resolves values from the snapshot it was built with, so what it returns cannot change
underneath you. Taking one is a pointer copy, not a load, so take them freely.

```go
v := store.View()
port := v.GetInt("server.port")
```

### Where the consistency guarantee applies, and where it does not

This is the limit that matters most in practice, and it is easy to over-read.

A single `Get*` call is atomic — one pointer load, no torn values, ever. But the default
read path resolves against the *latest* snapshot each time, so a *sequence* of separate
calls can straddle a reload:

```go
host := cfg.GetString("db.host") // snapshot N
port := cfg.GetInt("db.port")    // snapshot N+1 — a reload landed in between
```

You have now connected to the new host on the old port. `With` closes that window for a
block of related reads by pinning one snapshot for its duration:

```go
err := store.With(func(v *config.View) error {
    return dial(v.GetString("db.host"), v.GetInt("db.port"))
})
```

It is scoped to a closure rather than handed out as a handle, deliberately. A handle can
be kept indefinitely, which pins parsed configuration in memory and quietly serves values
that grow arbitrarily old.

The exposure is narrower than it first looks, which is why pinning is not required
everywhere. `Unmarshal`, `UnmarshalKey` and `UnmarshalSection` are single operations
against one snapshot and cannot straddle at all — so the entire typed-section pattern is
already immune, and that is the pattern these docs recommend. What remains exposed is code
making several individual `Get*` calls: adapters, CLI wiring, older call sites. `With`
covers exactly those. Requiring pinned reads everywhere would make the straddle
unrepresentable at the cost of changing every call site in every consuming module, which
is disproportionate to the remaining risk.

Observers do not need `With`. An observer is handed the snapshot that *triggered* its
notification, not "the latest one", so it cannot read values from a later change partway
through its own callback and produce a result that never existed as a coherent
configuration.

## Own writes build the next snapshot directly

This is the second decision the design leans on, and it is worth understanding why it is
not merely an optimisation.

Under the previous design a write propagated like this: write the file, fsnotify fires,
debounce, re-read every file, rebuild, swap, notify. A round trip through the filesystem —
gated on a timing heuristic — to learn something the process already knew.

`Store.Apply` instead produces the new snapshot **directly from the content it just
wrote**, then notifies. The watcher's only remaining job is detecting changes the Store did
not make.

That single change eliminates four problems structurally rather than mitigating them:

- **Notification nondeterminism.** An own write notifies exactly once, by construction. No
  coalescing heuristic, and no dependence on how many files the batch touched.
- **The write → reload → react feedback loop.** An own write never re-enters through the
  watcher, so that particular cascade is unrepresentable rather than detected and broken.
- **Debounce sensitivity** for the process's own saves. There is no timing window to tune,
  because there is no timing window.
- **Redundant re-parsing** of files that did not change.

It also removes a whole failure mode: there is no interval during which the configuration
in memory disagrees with the configuration on disk. `Apply` returns the snapshot that
already reflects what it wrote.

One refinement matters here. A write that resolves to the configuration already published
is **not** a logical change and notifies nobody. The file may genuinely have been rewritten
— comments reflowed, a key relocated to the layer that should own it — but observers react
to configuration, not to files. Without this rule, `Apply` and `Reload` disagree about what
counts as a change, and an idempotent observer loops forever on a write that keeps being
announced as news.

## Writing from inside an observer is refused

`Apply` and `Reload` return `ErrWriteFromObserver` when called from within an observer
callback. This deserves its own explanation, because the obvious question is why it is not
simply made to work.

The unrepresentable cascade above is the *watcher* loop. It is real, and it still holds.
But `Apply → notify → observer → Apply → notify` is a loop entirely inside the Store, and
nothing structurally prevented it. A probe measured depth 21, bounded only because the
probe gave up.

Two defects had been masking it, and both are fixed independently. A file's fingerprint
never advanced past load, so the second write to any file was reported as conflicting with
the first — which meant a nested write failed before it could publish, and the test
observing the cascade saw a bounded loop and passed. That alone was a serious defect:
**two consecutive writes to one file could never succeed.** Separately, `Apply` notified
unconditionally while `Reload` did not, so an idempotent observer looped forever on
no-op writes.

With those fixed, a *convergent* observer settles on its own. A non-convergent one — every
reaction producing a genuinely new value — cannot converge under any design, so the only
question is what it gets. Suppressing nested notification leaves other observers
indefinitely stale. Coalescing needs an iteration cap that is inevitably arbitrary. A depth
ceiling converts a crash into an error without making the program correct. None of that is
worth the complexity for a program that is already broken.

So writing while reacting to a write is treated as what it is: a breakdown of the
ownership model, refused at the boundary. The sanctioned pattern is to take the change out
of the observation — record what is needed, return, and write from a worker goroutine, a
queue, or the next tick. Serialising and de-duplicating those deferred writes belongs to
the consumer, which is the only party that knows which of them still matter.

The check is scoped to the goroutine running the callback, and that is not incidental.
Notification runs outside the Store's lock by design, because holding it across an observer
would deadlock the Store. An unrelated goroutine may therefore be writing while observers
execute, and that write is entirely legitimate — a flag or a mutex could not tell it apart
from the observer's own re-entrant call, and refusing it would be a worse defect than the
cascade. Threading a marked `context.Context` through the observer interface was considered
and rejected: an observer passing `context.Background()` would defeat it silently, turning
a guaranteed refusal into a cooperative one.

## Everything is a layer

The last piece of the model is that the Store has one notion of a source. A file, a
document within a multi-document file, an in-memory reader, the environment, the flag set,
a layer added at runtime — all of them are layers, ordered by precedence, and everything
else is expressed in terms of that ordering.

Merge order is the layer order. Provenance names a layer. Write routing walks the layer
order in reverse for the first writable one. Shadowing is a layer outranking another. There
is no separate mechanism for the environment, no special case for defaults, no second rule
for multi-document files.

This is why `AddLayer` is the shape it is. It contributes an in-memory source at the
highest precedence, taking part in precedence, provenance, merging and shadowing exactly
like a file, with nothing to remember about it. Being in memory it has nowhere to persist
to, so routing already skips it and a later write lands in the file beneath and is reported
as shadowed — a property inherited from the layer model rather than special-cased. An
earlier design sketched an invisible "override layer" that every read path, `Unmarshal`,
`Keys` and validation would have to consult separately; making it an ordinary layer is
strictly less machinery and strictly fewer places to forget.

Compiled-in defaults belong at construction with `WithReaders`, where they sit at the
bottom of the order rather than the top.

## What this design does not give you

Stated plainly, because each is a real edge:

- **Cross-process safety.** Serialisation is in-process. Conflict detection is the
  defence, and it detects rather than prevents.
- **Multi-read consistency by default.** Individual reads are always coherent; a sequence
  of them needs `With`, or a typed section.
- **Byte-identical writes.** Comments stay with their keys and the structure is preserved.
  Blank lines, indentation, comment alignment and byte identity are not.
- **Every YAML document.** A multi-line flow collection with interior comments cannot be
  round-tripped safely and is refused at load, naming the location.
- **Cross-backend atomicity.** Today there is one backend, so the question is moot. It will
  not stay moot, and a change set spanning a file and a remote parameter store cannot be
  made atomic.
- **YAML's own multi-document semantics.** This module treats a document as a layer and
  later documents as overriding earlier ones. That is an interpretation it adopts for
  consistency with how it treats files, not something the YAML specification says.

## Related

- [Provenance](provenance.md) — where a value came from and what shadows it
- [Write configuration](../how-to/write-config.md) — planning, routing, conflicts
- [Precedence & merge model](precedence-and-merge.md) — how layers fold together
- [Hot-reload safety](hot-reload-safety.md) — the watcher and foreign changes

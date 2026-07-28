---
title: Compose two stores into one
description: Carry a shared or global configuration as layers of a project configuration, so one store travels through your code — and promote settings between them.
tags: [how-to, backends, composition]
---

# Compose two stores into one

A CLI usually has two configurations: the user's global settings and the current project's.
Passed around separately, every function that needs either takes both, and the precedence
between them exists only as a convention in the reading code — applied slightly differently
each time somebody needs it.

`config.Nested` makes one store a layer of another:

```go
global, _ := config.NewStore(ctx, config.WithFiles(config.OS(), "~/.myapp/config.yaml"))

store, err := config.NewStore(ctx,
    config.WithBackend(config.Nested(global, "global")),  // the block sits here
    config.WithFiles(fsys, ".myapp.yaml"),                // project settings outrank it
    config.WithEnv("MYAPP"),                              // env outranks everything
)
```

One store now travels through your code, and the precedence is declared once, where the
store is built.

## Provenance survives the join

The inner store's layers pass through as a **contiguous block** at the position the nested
backend is declared. Each keeps its own source, so `Explain` and `Origin` answer with the
file the value actually came from:

```go
src, _ := store.View().Origin("theme")
fmt.Println(src.Name)   // ~/.myapp/config.yaml — not "global"
```

Inner layers keep their order among themselves, and the whole block sits where you put it.
Nothing interleaves: a layer declared after the nested backend outranks every layer inside
it, and one declared before is outranked by all of them.

Reads recurse to any depth. Nesting deeply is supported and not recommended — see
[the cost of depth](#the-cost-of-depth).

## Bounding what the inner store exposes

Composition and filtering are separate tools, and they combine:

```go
config.WithBackend(config.Filtered(
    config.Nested(global, "global"),
    config.Allow("theme.**", "editor.**")))
```

See [bounding a backend](filter-a-backend.md).

## Writing: a nested store is read-only by default

An ordinary write never lands in a nested store. That is the safe default and usually what
you want — a shared base should not be edited by a project-scoped save.

To allow promotion, opt in:

```go
config.WithBackend(config.Nested(global, "global", config.NestedPromotable))
```

Even then, a promotable nested layer is **pinnable but never routable**:

| | routed to by default | reachable via `To()` |
|---|---|---|
| nested, read-only *(default)* | no | no |
| nested, promotable | **no** | **yes** |
| an ordinary writable layer | yes | yes |

!!! warning "Why routing must never choose a nested layer"

    Routing prefers a writable layer that **already defines** the key. If nested layers
    routed like any other, an ordinary project-scoped `Set("theme", …)` would walk past
    the project file, find `theme` in the global config, and write **there** — silently
    changing the value every other project inherits.

    Promotion has to be deliberate, so it has to be named.

```go
// forks into the project file, whether or not the global config defines it
store.Apply(ctx, config.Set("theme", "dark"))

// promotes — named, so it cannot happen by accident
store.Apply(ctx,
    config.Set("theme", "dark", config.To("~/.myapp/config.yaml")),
    config.Remove("theme"),   // or the promoted value is shadowed by the local copy
)
```

`store.WritableTargets()` lists everything `To` will accept, including pin-only layers —
which is the only way to discover them, since no unpinned plan will ever name one.

## Composition is a tree

A store may appear **at most once** in a graph. Nesting one already reachable is
`ErrCyclicStore` at construction — including as a diamond, where the same store is reached
through two different intermediates.

That is stricter than refusing cycles, and deliberately so: a store appearing twice would
contribute its layers twice at two precedences, so every value in it would be shadowed by a
copy of itself.

Composing two stores over **the same file**, both able to write to it, is
`ErrDuplicateLayer` for a related reason — the two layers would be identical in every
field, so nothing downstream could tell them apart and a plan could report a write as
effective when it is not.

## What composition cannot promise

Read these before relying on a composed store; none is a bug, and each is cheaper to know
now than to discover.

- **A write spanning both stores is not atomic.** `AtomicMultiKey` is false on an
  aggregate, always. Nothing can make a write to a project file and a global file
  indivisible.
- **The outer store does not see writes made directly to the inner store** until it next
  reloads. When it is watching, that happens on the next poll tick; when it is not, call
  `Reload`. This is the mirror of the point below, and both come from the two stores having
  separate snapshots.
- **The inner store does not see a promotion** until *it* reloads. A promotion writes
  through to the inner store's backend — that is what makes it durable — but the inner
  `Store` object still holds its previous snapshot. If you kept a reference to it, call
  `inner.Reload(ctx)`.
- **`Sensitive` is conservative.** An aggregate reports itself sensitive if *any* inner
  backend does. A false negative would be a secret written into a plain layer; a false
  positive is a write refused that could have been allowed.

### The cost of depth

Depth is unbounded and each level adds distance between an error and the code that caused
it. Errors name the whole chain rather than only the leaf — `global → ~/.myapp/config.yaml:
source changed since it was read` — which is what keeps that cost payable, but a conflict
three levels down is still a conflict three levels down.

## Related

- [Bound what a backend contributes](filter-a-backend.md) — how to expose only part of a nested store
- [Write configuration](write-config.md) — routing, naming a target, and what shadowing means
- [Backends and capabilities](../explanation/backends.md) — what a backend declares

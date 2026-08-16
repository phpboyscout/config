---
title: Explanation
description: Why the module is built this way — the single-owner rule the whole design falls out of, and what follows from it for precedence, provenance, writes, reload and the adapter family.
tags: [explanation]
---

# Explanation

Why, rather than how. These pages are for reading rather than looking things up, and they
are worth reading once you have used the module for something — the reasoning lands better
when you have already met the behaviour.

Much of `config` is deliberately counter-intuitive: it refuses writes from inside observers,
drops snapshots that a newer one superseded, and hands back a usable store alongside an
error. Each of those looks like a bug until you know why it is there. That is what this
section is for.

## Start with the one rule

Almost every design decision falls out of a single sentence.

**[The Store](the-store.md)** — one component owns all configuration I/O. Nothing else
reads, writes or watches a source; everything else is a view over the immutable snapshot it
publishes. Read this first, because the rest of the section is consequences of it.

## What follows from it

| Page | Explains |
|---|---|
| [Precedence & merge model](precedence-and-merge.md) | How a value is resolved when several sources define it, why merging is per key rather than per file, and what happens to lists and maps. |
| [Provenance](provenance.md) | What `Origin`, `Shadowed` and `Explain` can tell you about where a value came from — and, as importantly, what they cannot. |
| [What survives a write](write-fidelity.md) | Why your comments, key order, quoting and anchors are still there afterwards: the file is edited, not regenerated. |
| [Hot-reload safety](hot-reload-safety.md) | Why reloading under a running process is safe — snapshots, fail-closed parsing, and reads that stay coherent across a change. |
| [Backends and capabilities](backends.md) | What a backend is, and why being readable does not make a source writable or watchable. |

## The adapter family

One store, many sources. These four explain how a source that is not a local YAML file joins
the merge as an ordinary layer.

| Page | Explains |
|---|---|
| [The adapter ecosystem](adapters.md) | Every adapter in the family, what each reads and writes, its status and what it costs your dependency graph. |
| [How filesystem adapters work](filesystem-adapters.md) | The `config.FS` contract, and how an embedded filesystem, a remote host and a cloud object store all satisfy the same six methods. |
| [How dynamic backends work](dynamic-backends.md) | What every remote backend shares — watch strategies, conflict detection, sensitive layers — and where the systems legitimately differ. |
| [How the Consul backend works](consul-backend.md) | The reference implementation in detail: its data model, its compare-and-swap writes and its native watch. |
| [Who owns the connection](connection-ownership.md) | The four ways an adapter gets its client, why injection stays the default, and why sharing one credential resolution is always deliberate. |

## Elsewhere

- **[Tutorials](../tutorials/index.md)** — learn the behaviour first; this section reads
  better afterwards.
- **[How-to guides](../how-to/index.md)** — the task-shaped route.
- **[Reference](../reference/index.md)** — the exact rules, when you need the edge cases
  rather than the reasoning.
- **[Feature specifications](https://gitlab.com/phpboyscout/go/config/-/wikis/specs/home)** —
  the decision records these pages summarise, with the alternatives that were rejected.

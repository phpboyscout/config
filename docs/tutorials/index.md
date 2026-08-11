---
title: Tutorials
description: Learn config by building something that runs — a YAML file on disk, then the full precedence chain, then the layers that are not files at all.
tags: [tutorials]
---

# Tutorials

A tutorial teaches by having you build something that works. Each one below runs top to
bottom, and you run the program after every step, so you can see what changed and why.

Work through them in order if you are new here. If you already have a store and a specific
job to do, the [how-to guides](../how-to/index.md) are the shorter route.

## Start here

| Tutorial | What you build | What you need |
|---|---|---|
| [Getting started](getting-started.md) | A program that loads a YAML file, reads typed values, explains where each came from, writes one back with comments intact, and reacts to an edit made outside the process. | Go 1.26.5 or newer. Nothing else. |
| [Layer defaults, a file, the environment and flags](layering.md) | The full precedence chain a real service uses — four layers at once, provenance across all of them, and a write that routing sends to the only layer that can take it. | Go 1.26.5 or newer. Nothing else. |

**Do these two in order.** Between them they introduce every concept the rest of the
documentation assumes: layers, precedence, provenance, routed writes, shadowing and
observers. Neither needs a network or a service.

## Then the layers that are not files

Everything above uses files and the environment. The rest of the family plugs into the same
seam — a filesystem that is not a disk, a store that is another store, a remote key–value
system — and behaves identically once it is in place.

| Tutorial | What you build | What you need |
|---|---|---|
| [Ship defaults inside the binary](embedded-defaults.md) | Defaults as a real YAML file compiled into the binary with `embed.FS` and `config-iofs`, an operator's file over the top, and the routing consequence of a layer that cannot be written to. | Go 1.26.5 or newer. Nothing else. |
| [Compose a project config over a global one](composing-stores.md) | One store carried as a layer of another, a setting promoted deliberately into the shared config, a filter bounding what the inner store contributes, and a runtime override above the lot. | Go 1.26.5 or newer. Nothing else. |
| [Configure a service from Consul](consul.md) | The same store with HashiCorp Consul as an ordinary layer — precedence, provenance, native watch and compare-and-swap writes through a remote key–value system. | Go 1.26.5 or newer, plus a local Consul agent. |

Take them in any order; none depends on the others. **Consul** is the one to read if you
are evaluating a remote backend at all — every backend adapter in the family follows the
shape it establishes, so the tutorial doubles as the map for the other fifteen.

## One page per adapter, once you know the shape

There are twenty-five adapter modules — seven file formats, seven filesystems and eleven remote
backends. They do not get a tutorial each, because once you have done one of the three
above, the rest differ only in details a [how-to guide](../how-to/load-and-merge.md) states
faster than a narrative can. [The adapter ecosystem](../explanation/adapters.md) lists all of
them with what each one reads, writes and costs.

## What a tutorial here will not do

A tutorial teaches the shape of the thing. It deliberately stops short of exhausting it:

- **It does not cover every option.** [Store options, error values, struct tags and the
  environment-variable rules](../reference/index.md) are reference material, looked up when
  you need them rather than read through.
- **It is not the fastest route to a specific task.** If you know you need to bind CLI
  flags or filter a secrets backend, go straight to the [how-to guides](../how-to/index.md).
- **It does not explain why the module is built this way.** That is
  [explanation](../explanation/index.md), and it reads better once you have used the module
  for something.

## After the tutorials

- **[How-to guides](../how-to/index.md)** — one page per task: load and merge, read values,
  write configuration, validate, hot-reload, bind flags — and one for every format,
  filesystem and backend adapter.
- **[Reference](../reference/index.md)** — the exact rules: key syntax, the
  environment-variable mapping, struct tags, every error value, every default, and what
  the module deliberately does not do.
- **[Explanation](../explanation/index.md)** — why a single component owns configuration
  I/O, and what follows from that rule.

---
title: Tutorials
description: Learn config by building something that runs — start with a YAML file on disk, then add a remote backend.
tags: [tutorials]
---

# Tutorials

A tutorial teaches by having you build something that works. Each one below runs top to
bottom, and you run the program after every step, so you can see what changed and why.

Work through them in order if you are new here. If you already have a store and a specific
job to do, the [how-to guides](../how-to/load-and-merge.md) are the shorter route.

## Which tutorial to start with

| Tutorial | What you build | What you need |
|---|---|---|
| [Getting started](getting-started.md) | A program that loads a YAML file, reads typed values, explains where each came from, writes one back with comments intact, and reacts to an edit made outside the process. | Go 1.26.5 or newer. Nothing else. |
| [Configure a service from Consul](consul.md) | The same store, with HashiCorp Consul as an ordinary layer — precedence, provenance, hot-reload and writes all working through a remote key–value system. | Go 1.26.5 or newer, plus a local Consul agent. |

Start with **Getting started**. It needs no external services and it introduces every
concept the rest of the documentation assumes: layers, precedence, provenance, routed
writes and observers.

## What a tutorial here will not do

A tutorial teaches the shape of the thing. It deliberately stops short of exhausting it:

- **It does not cover every option.** [Store options, error values, struct tags and the
  environment-variable rules](../reference/index.md) are reference material, looked up when
  you need them rather than read through.
- **It is not the fastest route to a specific task.** If you know you need to bind CLI
  flags or filter a secrets backend, go straight to the [how-to guides](../how-to/load-and-merge.md).
- **It does not explain why the module is built this way.** That is
  [explanation](../explanation/the-store.md), and it reads better once you have used the
  module for something.

## After the tutorials

- **[How-to guides](../how-to/load-and-merge.md)** — one page per task: load and merge,
  read values, write configuration, validate, hot-reload, bind flags, and one per format,
  filesystem and backend adapter.
- **[Reference](../reference/index.md)** — the exact rules: key syntax, the
  environment-variable mapping, struct tags, every error value, every default, and what
  the module deliberately does not do.
- **[Explanation](../explanation/the-store.md)** — why a single component owns
  configuration I/O, and what follows from that rule.

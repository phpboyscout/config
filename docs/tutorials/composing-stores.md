---
title: Compose a project config over a global one
description: Make one store a layer of another so a project's settings sit over a user's global ones, promote a setting deliberately into the shared layer, bound what the inner store exposes, and push a runtime override above everything.
tags: [tutorials, composition, nested, filtering, promotion, override]
---

# Compose a project config over a global one

A tool that runs in a directory usually has two configurations: the user's own settings,
shared by every project, and this project's settings, checked into the repo. Passing both
around together and remembering which wins is the kind of job that quietly grows bugs.

Instead, make one store a **layer of the other**. By the end of this tutorial you'll have a
project store that carries a global store's layers as its own — with provenance intact
across the join — and you'll have promoted a setting up into the shared config, bounded what
the shared config is allowed to contribute, and pushed a runtime override above the lot.

**Time:** about twenty minutes. **You need:** Go 1.26.5 or newer. Nothing else.

Do [Layer defaults, a file, the environment and flags](layering.md) first if you have not —
this builds directly on precedence and routed writes.

## 1. Create a module and two config files

```bash
mkdir cfgcompose && cd cfgcompose
go mod init cfgcompose
go get gitlab.com/phpboyscout/go/config
```

`global.yaml` — stand-in for `~/.config/demo/config.yaml`:

```yaml
# The user's settings, shared by every project.
editor: vim
theme: dark
telemetry:
  enabled: false
```

`project.yaml` — stand-in for a `.demo.yaml` in the repo:

```yaml
# This project's settings, checked into the repo.
theme: light
build:
  target: wasm
```

They overlap on exactly one key, `theme`, which is the interesting one.

## 2. Nest the global store inside the project store

`main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"

	"gitlab.com/phpboyscout/go/config"
)

func main() {
	ctx := context.Background()
	fsys, err := config.Dir(".")
	if err != nil {
		log.Fatal(err)
	}

	global, err := config.NewStore(ctx, config.WithFiles(fsys, "global.yaml"))
	if err != nil {
		log.Fatal(err)
	}

	project, err := config.NewStore(ctx,
		config.WithBackend(config.Nested(global, "global", config.NestedPromotable)),
		config.WithFiles(fsys, "project.yaml"),
	)
	if err != nil {
		log.Fatal(err)
	}

	v := project.View()
	for _, k := range []string{"editor", "theme", "telemetry.enabled", "build.target"} {
		fmt.Printf("%-18s = %-8v %s\n", k, v.Get(k), v.Explain(k))
	}
}
```

```bash
go run .
```

```
editor             = vim      editor = vim (from global.yaml)
theme              = light    theme = light (from project.yaml); also defined in global.yaml
telemetry.enabled  = false    telemetry.enabled = false (from global.yaml)
build.target       = wasm     build.target = wasm (from project.yaml)
```

**Provenance survives the join.** `editor` is reported as coming from `global.yaml` — the
actual file — not from "the aggregate" or "the nested store". The inner store's layers pass
through as a contiguous block at the position the backend was declared, each keeping its own
source, so nothing is interleaved and nothing is anonymised.

The `"global"` id names the aggregate in error messages. It is not a layer name, because an
aggregate contributes no layer of its own.

## 3. Watch an ordinary write stay local

`editor` is defined only in the global file. Routing's usual rule is *edit the key where it
already lives* — so does a write to it reach into the global config?

```go
	p, _ := project.Plan(config.Set("editor", "helix"))
	for _, op := range p.Operations {
		fmt.Printf("ordinary write: editor -> %s\n", op.Target)
	}
```

```
ordinary write: editor -> project.yaml
```

No. **A nested store is never a routing candidate**, even with `NestedPromotable` passed.
An ordinary project-scoped edit lands in the project's own file, and creates the key there.

That asymmetry is the whole design. If routing could reach inside, a mundane "set my editor
for this project" would walk past the project's file and rewrite the shared config every
other project inherits — the destructive version of a helpful default.

## 4. Promote a setting deliberately

Promotion — moving a setting *up* into the shared config — has to be asked for by name:

```go
	promote := config.Set("theme", "solarized", config.To("global.yaml"))

	p2, err := project.Plan(promote)
	if err != nil {
		log.Fatal(err)
	}
	for _, op := range p2.Operations {
		fmt.Printf("promotion:      theme -> %s (effective: %v)\n", op.Target, op.Effective())
		if !op.Effective() {
			fmt.Printf("                shadowed by %s\n", op.ShadowedBy)
		}
	}

	if _, err := project.Apply(ctx, promote); err != nil {
		log.Fatal(err)
	}
```

```
promotion:      theme -> global.yaml (effective: false)
                shadowed by [project.yaml]
```

`global.yaml` really is updated:

```yaml
# The user's settings, shared by every project.
editor: vim
theme: solarized
telemetry:
  enabled: false
```

But `effective: false` is the honest part. The project file still sets `theme: light`, so
reading `theme` back in *this* project still gives you `light` — the promotion will show up
in every other project, and not in this one until the local override is removed. Tell the
user that, rather than letting them wonder why the setting they just saved did nothing.

**`NestedPromotable` is what made this possible at all.** Without it a nested store is
strictly read-only and even a named write cannot reach in. That is the safe default and the
usual case — a shared organisational base, a team standard nobody should edit by accident.

## 5. Bound what the inner store may contribute

A nested store contributes everything it can see. When that is a shared config carrying
settings this tool has no business reading, wrap it:

```go
		config.WithBackend(config.Filtered(
			config.Nested(global, "global", config.NestedPromotable),
			config.Deny("telemetry.*"),
		)),
```

```
editor             = vim      editor = vim (from global.yaml)
theme              = light    theme = light (from project.yaml); also defined in global.yaml
telemetry.enabled  = <nil>    telemetry.enabled is not set
build.target       = wasm     build.target = wasm (from project.yaml)
```

`telemetry.enabled` is not merely hidden from reads — it **is not set**, as far as this store
is concerned, so `Has` is false and a write would not be accepted for it either.

`Allow` is the other half: with no `Allow`, every key is permitted unless a `Deny` excludes
it; with one, only matching keys get through. **`Deny` beats `Allow`** where they overlap,
so an `Allow` can never re-expose something explicitly denied. `Filtered` wraps any backend,
not just a nested store — the same call bounds a Consul prefix a broad token can read.

## 6. Push a runtime override above everything

Some values are decided while the program is running — a `--set` flag, a value fetched at
startup, a test fixture. `AddLayer` adds one above every layer declared at construction:

```go
	if err := project.AddLayer(ctx, "cli-override", strings.NewReader("theme: high-contrast\n")); err != nil {
		log.Fatal(err)
	}
```

```
theme  = high-contrast  theme = high-contrast (from override:cli-override); also defined in global.yaml, project.yaml
```

An override layer is read-only — there is nowhere to persist it — and it **survives
reloads**, because it is re-read each time rather than merged in once. If its content will
not parse it is refused and withdrawn, leaving the last-known-good configuration live.

One restriction worth knowing before you hit it: calling `AddLayer` from inside an observer
returns [`ErrWriteFromObserver`](../reference/errors.md). Observers see a snapshot; letting
one mutate the store mid-notification is how notification ordering stops being defined.

## What you built

```
override:cli-override   ← AddLayer, runtime, read-only, highest
project.yaml            ← the repo's config, writable, routing's target
global.yaml             ← via Nested + Filtered, promotable by name only
```

| Call | What it does |
|---|---|
| `config.Nested(s, id)` | inner store's layers become layers of the outer one, read-only |
| `config.NestedPromotable` | a **named** write may reach in; routing still may not |
| `config.Filtered(b, ...)` | bounds which keys a backend contributes, and accepts writes for |
| `config.To("name")` | pins one change to a named layer |
| `Store.AddLayer` | a read-only layer above everything, added at runtime |

## Where to go next

- **[Compose stores](../how-to/compose-stores.md)** — the same mechanics as a task guide,
  including cycles and reload semantics.
- **[Filter a backend](../how-to/filter-a-backend.md)** — pattern syntax and what filtering
  does to writes.
- **[Write configuration](../how-to/write-config.md)** — routing and targets in full.
- **[The Store](../explanation/the-store.md)** — why one component owns all configuration
  I/O, which is what makes composition safe.

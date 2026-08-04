---
title: Layer defaults, a file, the environment and flags
description: Build the full precedence chain a real service uses — compiled-in defaults under a config file, under environment variables, under command-line flags — and see which layer won every value.
tags: [tutorials, layers, precedence, provenance, flags, environment]
---

# Layer defaults, a file, the environment and flags

Most services resolve configuration from four places at once: defaults compiled into the
binary, a file an operator edits, environment variables the platform injects, and flags
someone typed. By the end of this tutorial you'll have a program with all four, and you'll
be able to ask it which one supplied any given value — and why.

You'll also write a value back, and watch the store refuse to put it somewhere useless.

**Time:** about twenty minutes. **You need:** Go 1.26.5 or newer. Nothing else — no
services, no network after the first `go get`.

If you have not built a store before, do [Getting started](getting-started.md) first. This
tutorial assumes you have seen `NewStore`, `View` and `Explain` once.

## 1. Create a module

```bash
mkdir cfgdemo && cd cfgdemo
go mod init cfgdemo
go get gitlab.com/phpboyscout/go/config github.com/spf13/pflag
```

`pflag` is only needed for the flags layer in step 5. The other three layers need nothing
beyond the module itself.

## 2. Compile the defaults into the binary

A default is not a config file. It ships inside the binary, so the program starts correctly
on a machine with no configuration at all — and it can never be written to, because there is
nowhere to persist it.

Create `main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"

	"gitlab.com/phpboyscout/go/config"
)

var builtinDefaults = []byte(`
server:
  host: 0.0.0.0
  port: 8080
  timeout: 30
log:
  level: info
`)

func main() {
	store, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{
			Name:    "embedded:defaults.yaml",
			Content: builtinDefaults,
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	v := store.View()
	for _, key := range []string{"server.host", "server.port", "server.timeout", "log.level"} {
		fmt.Printf("%-16s = %-10v %s\n", key, v.Get(key), v.Explain(key))
	}
}
```

```bash
go run .
```

```
server.host      = 0.0.0.0    server.host = 0.0.0.0 (from default:embedded:defaults.yaml)
server.port      = 8080       server.port = 8080 (from default:embedded:defaults.yaml)
server.timeout   = 30         server.timeout = 30 (from default:embedded:defaults.yaml)
log.level        = info       log.level = info (from default:embedded:defaults.yaml)
```

Give the source a name you'd recognise in an error message. `embedded:defaults.yaml` tells
you where to look; `reader1` does not, and provenance is most of what this module is for.

## 3. Put a config file over the top

Create `config.yaml` next to `main.go`. Note the comments — step 7 comes back to them.

```yaml
# Deployment settings for the demo service.
server:
  # The port the operators agreed on.
  port: 9090
log:
  level: debug
```

It deliberately sets only two of the four keys. Add the file to the store, **after** the
defaults:

```go
	fsys, err := config.Dir(".")
	if err != nil {
		log.Fatal(err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{
			Name:    "embedded:defaults.yaml",
			Content: builtinDefaults,
		}),
		config.WithFiles(fsys, "config.yaml"),
	)
```

```bash
go run .
```

```
server.host      = 0.0.0.0    server.host = 0.0.0.0 (from default:embedded:defaults.yaml)
server.port      = 9090       server.port = 9090 (from config.yaml); also defined in default:embedded:defaults.yaml
server.timeout   = 30         server.timeout = 30 (from default:embedded:defaults.yaml)
log.level        = debug      log.level = debug (from config.yaml); also defined in default:embedded:defaults.yaml
```

**Precedence is the order you added the sources — later wins.** There is no ranking table to
learn and no priority number to set. The file outranks the defaults because
`WithFiles` came after `WithReaders`.

Two keys came from the file and two from the defaults, and the merge happened **per key**,
not per file. A file that sets one key does not blank out the rest of the tree.

## 4. Add the environment

```go
		config.WithEnv("CFGDEMO"),
```

```bash
CFGDEMO_SERVER_PORT=7000 go run .
```

```
server.host      = 0.0.0.0    server.host = 0.0.0.0 (from default:embedded:defaults.yaml)
server.port      = 7000       server.port = 7000 (from env:CFGDEMO_SERVER_PORT); also defined in default:embedded:defaults.yaml, config.yaml
server.timeout   = 30         server.timeout = 30 (from default:embedded:defaults.yaml)
log.level        = debug      log.level = debug (from config.yaml); also defined in default:embedded:defaults.yaml
```

`CFGDEMO_SERVER_PORT` became `server.port`: the prefix is stripped, the rest lowercased, and
underscores become dots.

**The prefix is required, and it is a security control rather than a nicety.** Without one,
any variable in the process environment — `PATH`, `HOME`, a secret injected for something
else entirely — would be a candidate configuration key. The full rules, including what
happens when an underscore is ambiguous, are in
[Environment variables](../reference/environment-variables.md).

## 5. Add flags on top

Flags go last, because someone typing a flag has the most immediate intent.

```go
	flags := pflag.NewFlagSet("demo", pflag.ExitOnError)
	flags.Int("server-port", 0, "port to listen on")
	flags.String("log-level", "", "log verbosity")
	if err := flags.Parse(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
```

and, as the last store option:

```go
		config.WithFlags(flags),
```

Add `"os"` and `"github.com/spf13/pflag"` to the imports, then run all four layers at once:

```bash
CFGDEMO_SERVER_PORT=7000 go run . --server-port 6000
```

```
server.host      = 0.0.0.0    server.host = 0.0.0.0 (from default:embedded:defaults.yaml)
server.port      = 6000       server.port = 6000 (from flag:--server-port); also defined in default:embedded:defaults.yaml, config.yaml, env:CFGDEMO_SERVER_PORT
server.timeout   = 30         server.timeout = 30 (from default:embedded:defaults.yaml)
log.level        = debug      log.level = debug (from config.yaml); also defined in default:embedded:defaults.yaml
```

One value, four layers that could have supplied it, and the highest one won. `--server-port`
became `server.port` — dashes become dots, the same way underscores do for the environment.
Use `BindFlag` when a flag's name and its key genuinely differ; see
[Bind CLI flags](../how-to/bind-cli-flags.md).

### A flag you did not type contributes nothing

`server-port` has a declared default of `0`. Run without it:

```bash
go run . --log-level warn
```

```
server.host      = 0.0.0.0    server.host = 0.0.0.0 (from default:embedded:defaults.yaml)
server.port      = 9090       server.port = 9090 (from config.yaml); also defined in default:embedded:defaults.yaml
server.timeout   = 30         server.timeout = 30 (from default:embedded:defaults.yaml)
log.level        = warn       log.level = warn (from flag:--log-level); also defined in default:embedded:defaults.yaml, config.yaml
```

`server.port` is 9090, not 0. **Only flags actually set on the command line join the
layer** — the store asks pflag which flags were visited, not what every flag's value is.
Without that, every unset flag's zero value would sit at the top of the stack and bury every
layer beneath it, which would make the flags layer useless.

That is also why declared flag defaults are the wrong place for configuration defaults. Put
them in the defaults layer, as in step 2, where they can be overridden by anything.

## 6. Ask which layer won, and which lost

`Explain` is the readable form you have been printing. Two lower-level calls give you the
same facts as data:

```go
	if src, ok := store.View().Origin("server.port"); ok {
		fmt.Printf("winner: %s (writable: %v)\n", src, src.Writable)
	}
	for _, s := range store.View().Shadowed("server.port") {
		fmt.Printf("shadowed: %s\n", s)
	}
```

`Origin` names the layer whose value you actually get; `Shadowed` lists the ones that
defined the key and lost. `Writable` on a `Source` is the field the next step turns on.

## 7. Write a value back, and watch routing skip what it cannot write

`server.timeout` exists **only** in the compiled-in defaults — a layer with nowhere to
persist to. Ask where a change to it would go, before making it:

```go
	plan, err := store.Plan(config.Set("server.timeout", 60))
	if err != nil {
		log.Fatal(err)
	}
	for _, op := range plan.Operations {
		fmt.Printf("plan: %s -> %s (effective: %v)\n", op.Change.Path, op.Target, op.Effective())
	}
```

```
plan: server.timeout -> config.yaml (effective: true)
```

Routing walked past the defaults layer, because it is not writable, and picked the highest
layer that is. Apply it:

```go
	if _, err := store.Apply(context.Background(), config.Set("server.timeout", 60)); err != nil {
		log.Fatal(err)
	}
```

`config.yaml` is now:

```yaml
# Deployment settings for the demo service.
server:
  # The port the operators agreed on.
  port: 9090
  timeout: 60
log:
  level: debug
```

Both comments survived, `port` kept its place, and `timeout` was inserted into the section
it belongs to. The file was edited, not regenerated from a parsed tree.

## 8. Write a value that a higher layer still shadows

Reset `config.yaml`'s `timeout` if you like, then write `server.port` while the environment
is setting it too:

```bash
CFGDEMO_SERVER_PORT=7000 go run .
```

```
plan: server.port -> config.yaml (effective: false)
      shadowed by [env:CFGDEMO_SERVER_PORT]
```

The write is **not** refused — `config.yaml` really does get `port: 5555` — but the plan
tells you, before you commit to it, that reading the key back will still give you 7000. That
is `op.Effective()` returning false and `op.ShadowedBy` naming who is winning:

```go
	if !op.Effective() {
		fmt.Printf("      shadowed by %s\n", op.ShadowedBy)
	}
```

This is the case that silently confuses people in other configuration libraries: the setting
was saved, the file is correct, and the running process ignores it. Here you can check
before writing, and tell the user why their change appears to have done nothing.

## What you built

Four layers, one merge, and a value you can trace to the layer that supplied it:

| Layer | Added with | Writable | Typical source |
|---|---|:---:|---|
| Defaults | `WithReaders` | — | compiled into the binary |
| File | `WithFiles` | ✓ | an operator edits it |
| Environment | `WithEnv` | — | the platform injects it |
| Flags | `WithFlags` | — | someone typed it |

Only one of the four is writable, which is why routing has anything to decide.

## Where to go next

- **[Configure a service from Consul](consul.md)** — the same store with a remote layer,
  which behaves exactly like the four above.
- **[Ship defaults inside the binary](embedded-defaults.md)** — the defaults layer as a real
  embedded file, parsed by the same code path as a file on disk.
- **[Load and merge](../how-to/load-and-merge.md)** — every source option, as a reference.
- **[Precedence and merge](../explanation/precedence-and-merge.md)** — why per-key merging
  works the way it does, and what happens to lists and maps.

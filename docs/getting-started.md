# Getting started

By the end of this tutorial you will have a small program that loads a YAML file, reads
typed values from it, explains where each value came from, writes a change back without
disturbing the file's comments, and reacts to an edit made outside the process.

It runs on any machine and needs no external services. Work through it top to bottom —
each step builds on the last, and you run the program after every one.

## Prerequisites

- Go 1.26.5 or newer.

## 1. Create a module

```bash
mkdir cfgdemo && cd cfgdemo
go mod init cfgdemo
go get gitlab.com/phpboyscout/go/config
go get github.com/spf13/afero
```

[afero](https://github.com/spf13/afero) is the filesystem abstraction the module reads
through. You will pass `afero.NewOsFs()` for the real disk; the same code takes an
in-memory filesystem in tests, which is why the dependency is there.

## 2. Write a configuration file

Create `config.yaml`. Give it a comment — you will want to prove later that it survives
a write:

```yaml
# The address the demo server listens on.
server:
  host: localhost
  port: 8080

log:
  level: info
```

## 3. Build a store and read from it

The `Store` is the sole owner of configuration I/O — it loads, merges, watches and
writes, and nothing else touches a source. `NewStore` performs its first load as part of
construction, so there is no "built but not loaded" state for you to remember to handle.

Create `main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/spf13/afero"

	"gitlab.com/phpboyscout/go/config"
)

func main() {
	ctx := context.Background()

	store, err := config.NewStore(ctx,
		config.WithFiles(afero.NewOsFs(), "config.yaml"),
		config.WithEnv("CFGDEMO"),
	)
	if err != nil {
		log.Fatal(err)
	}

	view := store.View()

	fmt.Println("host:", view.GetString("server.host"))
	fmt.Println("port:", view.GetInt("server.port"))
}
```

Run it:

```bash
go run .
# host: localhost
# port: 8080
```

You read through a `View`, not through the store directly. A `View` performs no I/O at
all — it resolves values from one immutable snapshot, so what it returns cannot change
underneath you halfway through a sequence of reads. Taking one is a pointer copy rather
than a load, so take them freely.

## 4. Override from the environment — and see why the prefix matters

You passed `config.WithEnv("CFGDEMO")`. That prefix is **required**, and it is a security
control rather than a naming convention. Try it:

```bash
CFGDEMO_SERVER_PORT=3000 go run .
# host: localhost
# port: 3000
```

Now try the same variable without the prefix:

```bash
SERVER_PORT=3000 go run .
# host: localhost
# port: 8080
```

Nothing happens, and that is the point. Without a prefix, any environment variable
matching a configuration key could silently change your program's behaviour. On a shared
CI runner, a container host or a multi-tenant box, an unrelated process setting
`LOG_LEVEL` or `SERVER_PORT` would reconfigure every tool running there. With a prefix,
unprefixed variables cannot reach configuration at all.

The environment wins over the file here because of where you put it: **precedence is
simply the order you added the sources, and later ones win.** There is no ranking baked
into the module. `WithEnv` after `WithFiles` means the environment overrides the file,
which is what an operator expects an environment variable to do.

## 5. Ask where a value came from

Because the store records provenance while it merges rather than reconstructing it
afterwards, it can answer the question every configuration debugging session starts with.
Add this to the end of `main`:

```go
	fmt.Println(view.Explain("server.port"))
```

Run it both ways:

```bash
go run .
# server.port = 8080 (from config.yaml)

CFGDEMO_SERVER_PORT=3000 go run .
# server.port = 3000 (from env:CFGDEMO_SERVER_PORT); also defined in config.yaml
```

`Explain` renders the whole chain. For programmatic use, `view.Origin(path)` returns the
single layer that supplied the effective value, and `view.Shadowed(path)` returns every
layer that defines it, lowest precedence first. "Which file do I edit?" and "why is my
edit not taking effect?" are the same question asked from two directions, and both need
the full list rather than just the winner.

## 6. Write a change back

Now persist a change. Do it in two stages — plan, then apply — because the plan tells you
where the change will land *before* anything is written.

Register an observer first, so you can watch the change being announced. Replace the body
of `main` after the `store` is built with:

```go
	store.AddObserverFunc(func(cfg config.Observed) error {
		fmt.Println("observer: port is now", cfg.GetInt("server.port"))

		return nil
	})

	plan, err := store.Plan(config.Set("server.port", 9090))
	if err != nil {
		log.Fatal(err)
	}

	fmt.Print(plan)

	snap, err := store.Apply(ctx, config.Set("server.port", 9090))
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("port:", config.NewView(snap).GetInt("server.port"))
```

```bash
go run .
# set server.port → config.yaml
# observer: port is now 9090
# port: 9090
```

!!! note "Run it a second time and the observer line disappears"
    That is correct, not a fault. The file already says `9090`, so the write changes
    nothing about the resolved configuration and observers are not told — they react to
    values, and none moved. Change the number in the code to see it fire again.

Three things just happened that are worth understanding.

**The plan is not an approximation.** `Plan` runs exactly the routing pass `Apply` runs,
so a `--dry-run` built on it cannot drift from the real thing the way a separate preview
implementation would. Producing the plan is the expensive part; executing it is not.

**The change was routed, not merged over the top.** Look at `config.yaml`:

```yaml
# The address the demo server listens on.
server:
  host: localhost
  port: 9090

log:
  level: info
```

The comment is still there, the key order is unchanged, and `log.level` was not
rewritten. The target document is edited in place rather than re-serialised from the
merged view — which is what stops a write from flattening every embedded default, and
every secret that arrived through the environment, into a user's file.

**The snapshot came back from the write itself.** `Apply` builds the next snapshot from
the content it just wrote rather than re-reading the file, so there is no window in which
memory and disk disagree, and no waiting on a watcher. Observers are notified exactly
once for the whole batch.

Now run it again with the environment set:

```bash
CFGDEMO_SERVER_PORT=3000 go run .
# set server.port → config.yaml — shadowed by env:CFGDEMO_SERVER_PORT
# port: 3000
```

The write succeeded — the file really was updated — but the environment still outranks
it, so the resolved value did not move. That is not an error, but a program that cannot
say so leaves its user staring at a setting that refuses to change. Check for it:

```go
	for _, op := range plan.Operations {
		if !op.Effective() {
			// ShadowedBy is a []Source, lowest precedence first; the last is
			// the layer actually winning.
			fmt.Println("warning: written, but still shadowed by",
				op.ShadowedBy[len(op.ShadowedBy)-1])
		}
	}
```

## 7. React to a change made outside the process

Finally, have the program keep running and respond to someone else editing the file.
Here is the complete `main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/spf13/afero"

	"gitlab.com/phpboyscout/go/config"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	store, err := config.NewStore(ctx,
		config.WithFiles(afero.NewOsFs(), "config.yaml"),
		config.WithEnv("CFGDEMO"),
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(store.View().Explain("server.port"))

	store.AddObserverFunc(func(cfg config.Observed) error {
		fmt.Println("observer: port is now", cfg.GetInt("server.port"))

		return nil
	})

	store.OnReloadError(func(err error) {
		fmt.Println("rejected reload:", err)
	})

	stop, err := store.Watch(ctx)
	if err != nil {
		log.Fatal(err)
	}

	defer stop()

	fmt.Println("watching config.yaml — edit it, or press Ctrl-C to stop")

	<-ctx.Done()
}
```

Run it, then edit `config.yaml` in another terminal and change the port:

```bash
go run .
# server.port = 9090 (from config.yaml)
# watching config.yaml — edit it, or press Ctrl-C to stop
# observer: port is now 7777
```

Now break the file deliberately — set the port to `[oops` and save:

```
# rejected reload: config: source could not be parsed: config.yaml: yamldoc: parse: ...
```

Put the port back to a number when you are done — the file is left deliberately broken
here, and the next `go run .` will refuse to load it.

The observer was **not** called, and the store is still serving `7777`. Reloading is
fail-closed: a candidate that will not parse, or that violates a schema if you configured
one, is rejected and the last-known-good configuration is retained. A configuration that
is partly the old values and partly the new is worse than either.

Two things about watching are deliberate and worth carrying with you.

`Watch` returns an error rather than silently doing nothing when it cannot work. An
application that believes it will hear about changes and never will is worse off than one
that knows it must restart, so the failure is loud.

And your own writes never travel this path. `Apply` notifies directly, so a write cannot
come back round through the watcher — which is what makes a write-then-react-then-write
cascade unrepresentable rather than something to be detected and broken. Relatedly,
calling `Apply` from *inside* an observer returns `ErrWriteFromObserver`. If an observer
needs to change configuration, record what it needs, return, and write from a worker
goroutine.

## Where next

You now have the whole shape of the module. The how-to guides go deeper on each part:

- **[Load & merge configuration](how-to/load-and-merge.md)** — multiple files, embedded
  defaults, CLI flags, and the full precedence chain.
- **[Read configuration values](how-to/read-values.md)** — the full read surface: typed
  accessors, the generic `config.Value[T]`, scoped views and struct decoding.
- **[Write configuration](how-to/write-config.md)** — routing, conflicts, batching, and
  the sharp edge of setting a whole map.
- **[Use typed sections](how-to/typed-sections.md)** — decode a subtree onto your own
  struct, and keep it current across reloads with `ObserveSection`.
- **[Validate configuration](how-to/validate-config.md)** — catch bad configuration at
  load time with a `Schema` and `config:` struct tags.
- **[React to changes with hot-reload](how-to/hot-reload.md)** — observers, watchers and
  error handling in more detail.

For why it is built this way, read **[The Store](explanation/the-store.md)**.

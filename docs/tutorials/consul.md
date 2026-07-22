# Configure a service from Consul

By the end of this tutorial you will have a program that loads its configuration from a
running HashiCorp Consul, reads typed values from it, reacts when a value changes in Consul
without restarting, and writes a value back — all through the same `config` store a file uses.

Unlike [Getting started](../getting-started.md), this one needs one external service: a Consul
agent, which you will run locally in development mode. Work through it top to bottom; you run the
program after each step.

## Prerequisites

- Go 1.26.5 or newer.
- The [Consul binary](https://developer.hashicorp.com/consul/install) on your `PATH`.

## 1. Start Consul

In its own terminal, run a throwaway single-node agent that keeps its data in memory:

```bash
consul agent -dev
```

Leave it running. Its HTTP API is now on `127.0.0.1:8500` — the default the SDK uses, so you
will not have to configure an address.

## 2. Seed some configuration

In a second terminal, put a few keys under an `app/` prefix. Consul keys are paths; the adapter
will turn the slashes into a nested tree:

```bash
consul kv put app/server/host localhost
consul kv put app/server/port 8080
consul kv put app/log/level info
```

## 3. Create a module

```bash
mkdir consuldemo && cd consuldemo
go mod init consuldemo
go get gitlab.com/phpboyscout/go/config
go get gitlab.com/phpboyscout/go/config-consul
go get github.com/hashicorp/consul/api
```

You build the Consul client yourself and hand it to the adapter, so every address, token and TLS
decision stays yours — the adapter never reaches for credentials.

## 4. Read from Consul

Create `main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"

	capi "github.com/hashicorp/consul/api"
	"gitlab.com/phpboyscout/go/config"
	configconsul "gitlab.com/phpboyscout/go/config-consul"
)

func main() {
	client, err := capi.NewClient(capi.DefaultConfig()) // 127.0.0.1:8500 by default
	if err != nil {
		log.Fatal(err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithBackend(configconsul.FromClient(client, "app/")),
	)
	if err != nil {
		log.Fatal(err)
	}

	view := store.View()
	fmt.Println("host:", view.GetString("server.host"))
	fmt.Println("port:", view.GetInt("server.port"))

	// Provenance names the source, so you can always answer "where did this come from?".
	if src, ok := view.Origin("server.port"); ok {
		fmt.Println("port came from:", src) // consul:app/
	}
}
```

Run it:

```bash
go run .
```

```
host: localhost
port: 8080
port came from: consul:app/
```

The prefix `app/` was stripped and the remaining path split into a tree, so `app/server/port`
reads as `server.port`. Consul stores bytes, so `"8080"` is a string — `GetInt` coerces it, the
same as it would for an environment variable.

## 5. Merge Consul over defaults

A Consul layer is just a layer: it takes part in precedence and per-key merge like any file. Add
compiled-in defaults *beneath* it, so Consul overrides them key by key. Change `NewStore`:

```go
store, err := config.NewStore(context.Background(),
	config.WithReaders(config.NamedSource{
		Name:    "embedded:defaults.yaml",
		Content: []byte("server:\n  port: 1234\n  scheme: http\n"),
	}),
	config.WithBackend(configconsul.FromClient(client, "app/")), // outranks the defaults
)
```

Run again: `server.port` is still `8080` (Consul wins), but `server.scheme` is `http` — the
default survives, because merging is per-key, not layer-replace.

## 6. React to a change, without restarting

Ask the store to watch. The adapter turns this into a Consul blocking query, so a change reaches
you the moment it happens rather than on a timer. Add, before the program would exit, a watch and
a block:

```go
	store.AddObserverFunc(func(cfg config.Observed) error {
		fmt.Println("reloaded — port is now", cfg.GetInt("server.port"))

		return nil
	})

	stop, err := store.Watch(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	defer stop()

	fmt.Println("watching; change app/server/port in another terminal…")
	select {} // block forever
```

Run it, then in your second terminal:

```bash
consul kv put app/server/port 9090
```

The program prints, within moments and without a restart:

```
reloaded — port is now 9090
```

## 7. Write a value back

The store can write to Consul too. A write becomes one atomic compare-and-swap transaction, and
if Consul moved since the store loaded it, the write is refused with `config.ErrConflict` rather
than clobbering the change. Replace the `select {}` block with:

```go
	if _, err := store.Apply(context.Background(), config.Set("log.level", "debug")); err != nil {
		log.Fatal(err)
	}

	fmt.Println("level is now", store.View().GetString("log.level"))
```

Run it, then confirm the value reached Consul itself:

```bash
consul kv get app/log/level
```

```
debug
```

## 8. Read a document-valued key

Some Consul deployments store a whole JSON document under one key rather than many flat keys. Put
one:

```bash
consul kv put app/db '{"host":"db.internal","port":5432}'
```

By default that value is a single string. To decode it into a subtree, give the backend a value
codec — any `config.Codec`, such as the one from `config-json`:

```bash
go get gitlab.com/phpboyscout/go/config-json
```

```go
import configjson "gitlab.com/phpboyscout/go/config-json"

config.WithBackend(configconsul.FromClient(client, "app/",
	configconsul.WithValueCodec(configjson.Codec{})))
```

Now `store.View().GetString("db.host")` is `"db.internal"` and `GetInt("db.port")` is `5432`.
A key whose value is a bare scalar (like `log/level`) still reads as a string, so a prefix mixing
both styles works.

## Where to go next

- [Read and write Consul](../how-to/consul.md) — the task-focused reference for each of these
  operations
- [How the Consul backend works](../explanation/consul-backend.md) — the data model, the conflict
  model and the watch mechanism, and why they are the way they are
- [React to changes with hot-reload](../how-to/hot-reload.md) — the observer contract in depth

---
title: Read and write Consul
description: Read and write configuration from HashiCorp Consul as an ordinary layer, with a codec for structured values.
tags: [how-to, backends, consul]
---

# Read and write Consul

The core reads and writes files. HashiCorp Consul's key-value store is a remote
backend, provided by a sibling module,
[`config-consul`](https://gitlab.com/phpboyscout/go/config-consul), so a consumer who needs
Consul takes it — and its Consul SDK — and one who does not pays nothing.

```bash
go get gitlab.com/phpboyscout/go/config-consul
```

You build and configure the Consul client — that is where every address, token, TLS and
datacenter decision lives — and hand it in. The adapter takes a **prefix** that scopes and is
stripped from the keys:

```go
import (
	capi "github.com/hashicorp/consul/api"
	"gitlab.com/phpboyscout/go/config"
	configconsul "gitlab.com/phpboyscout/go/config-consul"
)

client, _ := capi.NewClient(capi.DefaultConfig())

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                      // YAML defaults
	config.WithBackend(configconsul.FromClient(client, "app/")),  // Consul outranks them
)
```

A Consul layer takes part in precedence, per-key merge, provenance and hot-reload exactly as a
file does. Keys under the prefix, split on `/`, become the nested tree:

```
app/server/host = "localhost"
app/server/port = "8080"
```

```go
store.View().GetString("server.host") // "localhost"
store.View().GetInt("server.port")    // 8080
```

## When you do *not* need this

If your configuration is static and ships with the deploy, a file is simpler and adds
no runtime dependency on anything being reachable.

Reach for Consul when a value has to change **without a redeploy**, or when several
services must agree on one source of truth. Its watch is a blocking query, so a change
reaches the store without polling.

## Values are strings, or decoded documents

Consul stores bytes, so by default every value is a scalar **string** and the View's typed
accessors coerce it — `GetInt("server.port")` parses `"8080"`, the same as an environment
variable. This is the natural model for the flat-key style, where configuration is spread across
many small keys.

For the other common style, where a key holds a whole JSON or YAML document, pass a value codec —
any [`config.Codec`](https://pkg.go.dev/gitlab.com/phpboyscout/go/config#Codec), the interface the
sibling format adapters already implement:

```go
import configjson "gitlab.com/phpboyscout/go/config-json"

configconsul.FromClient(client, "app/", configconsul.WithValueCodec(configjson.Codec{}))
```

A value that decodes to an object becomes a subtree; a bare scalar stays a string, so a prefix
mixing flat keys and document blobs reads correctly. You inject the one format your Consul holds,
so `config-consul` takes no codec dependency of its own.

## Writing is one atomic transaction

`config-consul` is read **and** write. A `Set` or `Remove` batched through `Apply` becomes a
single Consul transaction of compare-and-swaps, so the whole batch lands or none of it does. Each
operation is guarded by the `ModifyIndex` the key had when the store loaded it, so a change that
landed since — from another writer — is refused with `config.ErrConflict` rather than silently
overwritten. That is the same conflict contract a file write is held to, in Consul's own
primitives.

Writes target flat Consul keys; writing into a value a codec decoded from a blob is not supported
and lands as a sibling flat key.

## Watching is a blocking query

Implementing `WatchableBackend`, the adapter joins hot-reload over a Consul blocking query: it
returns the moment anything under the prefix changes, so foreign-change latency is push, not poll
(`NativeWatch: true`). `WithPollInterval` bounds each query; unset, Consul's own default applies.

```go
store.Watch(ctx) // a change in Consul now reaches your observers
```

## What it costs

| | |
|---|---|
| Modules added | **24** — 15 for the Consul API client, 9 for the `config` graph |

The config graph plus the Consul SDK (`github.com/hashicorp/consul/api` and its client
dependencies) — the honest cost of talking to Consul, asserted by an allowlist test in the module
so an unforeseen transitive addition fails the build. The testcontainers integration suite is
test-only and reaches no consumer.

## Related

- [Write a custom backend](custom-backend.md) — the seam this is built on, walked end to end with a
  Consul-shaped example
- [Backends & capabilities](../explanation/backends.md) — why read, write and watch are separate
  interfaces
- [React to changes with hot-reload](hot-reload.md) — the observer contract Consul's watch feeds

---
title: Read and write Consul
description: Read and write configuration from HashiCorp Consul as an ordinary layer, with a codec for structured values.
tags: [how-to, backends, consul]
---

# Read and write Consul

The core reads and writes files. HashiCorp Consul's key-value store is a remote
backend, provided by a sibling module,
[`config-consul`](https://gitlab.com/phpboyscout/go/config-consul), so a consumer who needs
Consul takes it and its Consul SDK, and one who does not pays nothing.

```bash
go get gitlab.com/phpboyscout/go/config-consul
```

You build and configure the Consul client, which is where every address, token, TLS and
datacenter decision lives, and hand it in. The adapter takes a **prefix** that scopes the read and is
stripped from the keys:

```go
import (
	capi "github.com/hashicorp/consul/api/v2"
	"gitlab.com/phpboyscout/go/config"
	configconsul "gitlab.com/phpboyscout/go/config-consul"
)

client, _ := capi.NewClient(capi.DefaultConfig())

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                     // YAML defaults
	config.WithBackend(configconsul.FromClient(client, "app/")), // Consul outranks them
)
```

A Consul layer takes part in precedence, per-key merge, provenance and hot-reload exactly as a
file does. Keys under the prefix, split on `/`, become the nested tree. A key ending in `/`, which Consul uses
to mark a folder, carries no value and is skipped:

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
reaches the store as soon as Consul answers it, with no poll delay.

## Values are strings, or decoded documents

Consul stores bytes, so by default every value is a scalar **string** and the View's typed
accessors coerce it. `GetInt("server.port")` parses `"8080"`, the same as an environment
variable. This is the natural model for the flat-key style, where configuration is spread across
many small keys.

For the other common style, where a key holds a whole JSON or YAML document, pass a value codec.
It can be any [`config.Codec`](https://pkg.go.dev/gitlab.com/phpboyscout/go/config#Codec), the
interface the sibling format adapters already implement:

```go
import configjson "gitlab.com/phpboyscout/go/config-json"

configconsul.FromClient(client, "app/", configconsul.WithValueCodec(configjson.Codec{}))
```

A value that decodes to an object becomes a subtree, and a bare scalar stays a string, so a prefix
mixing flat keys and document blobs reads correctly. You inject the one format your Consul holds,
so `config-consul` takes no codec dependency of its own.

## Writing is one atomic transaction

`config-consul` is read **and** write. A `Set` or `Remove` batched through `Apply` becomes a
single Consul transaction of compare-and-swaps, so the whole batch lands or none of it does. Each
operation is guarded by the `ModifyIndex` the key had when the store loaded it, so a change that
landed since, from another writer, is refused with `config.ErrConflict` rather than silently
overwritten. That is the same conflict contract a file write is held to, in Consul's own
primitives.

Writes target flat Consul keys; writing into a value a codec decoded from a blob is not supported
and lands as a sibling flat key.

## Watching is a blocking query

The adapter joins hot-reload over a Consul blocking query. The query returns the moment anything
under the prefix changes, so foreign-change latency is push, not poll (`NativeWatch: true`). After
the store's settle window, a value comparison decides whether the change matters.

```go
stop, err := store.Watch(ctx) // a change in Consul now reaches your observers
```

The interval you give `Store.Watch` with `config.WithPollInterval` is not a polling cadence here. It
sets the wait time of each blocking query, which is how long an idle query stays open before it is
re-issued. There is no `WithPollInterval` option on the backend itself, and the store passes its
2-second default if you give none, so an idle watch re-issues the query every two seconds. A change
still returns the query at once. If a quieter watch suits you, pass a longer interval:

```go
stop, err := store.Watch(ctx, config.WithPollInterval(time.Minute))
```

## Getting a client

Building the client yourself is the default and stays recommended, because it is where
every address, token, TLS and datacenter decision lives. Two further rungs exist
for when you would rather not:

```go
// You assembled the config; the adapter builds the client.
b, err := configconsul.FromConfig(cfg, "app/")

// Nothing at all: CONSUL_HTTP_ADDR and friends, as capi.DefaultConfig reads them.
b, err := configconsul.Default("app/")
```

`Default` inherits **Consul's own** documented `127.0.0.1:8500`, not one this
adapter invented. etcd documents no such default for its client, so
[`config-etcd`'s `Default`](etcd.md#getting-a-client) refuses without an endpoint instead.

Neither rung contacts Consul: `capi.NewClient` does no network I/O, so a failure
here is a malformed config and reaching the agent stays deferred to the first
load. `FromConfig` with a nil config returns `ErrNoConfig`. Both rungs return a plain
`config.Backend`, because the Consul client has nothing to close. The why behind the ladder is in
[who owns the connection](../explanation/connection-ownership.md).

### The token is read once, and no rung renews it

`capi.NewClient` resolves the ACL token a single time and copies it onto the
client. Nothing re-reads the environment afterwards, and `CONSUL_HTTP_TOKEN_FILE`
is read at construction too, not per request, even though a token file is exactly
the mechanism you would use to rotate one.

Consul ACL tokens may carry an expiry. Where yours does, a **long-lived process
reloading configuration** fails every `Load` once it lapses, with no path back:
the backend cannot learn its credential expired, and it will not pick up a
rotated token file.

For a command this rarely matters. For a daemon with an expiring token, build the
client yourself and rebuild it when the token rotates, then use `FromClient`. A
token with no expiry set is unaffected.

[`config-vault`](vault.md) has the same shape for the same reason: both SDKs
model the client as *configured with a token* rather than as *configured with a
way to get one*. The AWS, Azure and GCP adapters do not: their credential objects
refresh underneath you.

## What it costs

| | |
|---|---|
| Adds | the Consul API client, on top of the `config` graph |

The config graph plus the Consul SDK (`github.com/hashicorp/consul/api/v2` and its client
dependencies), which is the honest cost of talking to Consul. An allowlist test in the module
makes an unforeseen transitive addition fail the build. The testcontainers integration suite is
test-only and reaches no consumer.

## Related

- [Write a custom backend](custom-backend.md): the seam this is built on, walked end to end with a
  Consul-shaped example
- [Backends & capabilities](../explanation/backends.md): why read, write and watch are separate
  interfaces
- [React to changes with hot-reload](hot-reload.md): the observer contract Consul's watch feeds

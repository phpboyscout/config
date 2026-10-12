---
title: Read and write Consul
description: Read and write configuration from HashiCorp Consul as an ordinary layer, with a codec for structured values.
tags: [how-to, backends, consul]
---

# Read and write Consul

HashiCorp Consul's key-value store becomes a read-write config layer through
[`config-consul`](https://gitlab.com/phpboyscout/go/config-consul), a sibling module, so a consumer
who needs Consul takes it and its Consul SDK, and one who does not pays nothing. You build the
Consul client and hand it in with a **prefix** that scopes the read and is stripped from the keys.

```bash
go get gitlab.com/phpboyscout/go/config-consul
```

```go
import (
	capi "github.com/hashicorp/consul/api/v2"
	"gitlab.com/phpboyscout/go/config"
	configconsul "gitlab.com/phpboyscout/go/config-consul"
)

client, err := capi.NewClient(capi.DefaultConfig())
if err != nil {
	return err
}

store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/app.yaml"),              // YAML defaults
	config.WithBackend(configconsul.FromClient(client, "app/")), // Consul outranks them
)
if err != nil {
	return err
}

port := store.View().GetInt("server.port") // 8080, from app/server/port
```

A Consul layer takes part in precedence, per-key merge, provenance and hot-reload exactly as a
file does.

## Connect to Consul

Building the client yourself is the default and stays recommended, because it is where every
address, token, TLS and datacenter decision lives. Two further rungs exist for when you would
rather not:

| Use | When |
|---|---|
| `FromClient(client, prefix)` | The process outlives its token, you need TLS or a datacenter the environment does not name, or you share a client. |
| `FromConfig(cfg, prefix)` | You assembled a `*capi.Config` and want the adapter to build the client from it. |
| `Default(prefix)` | Nothing to configure: `CONSUL_HTTP_ADDR` and friends, as `capi.DefaultConfig` reads them. |

```go
// You assembled the config; the adapter builds the client.
b, err := configconsul.FromConfig(cfg, "app/")

// Nothing at all: CONSUL_HTTP_ADDR and friends, as capi.DefaultConfig reads them.
b, err := configconsul.Default("app/")
```

`Default` inherits **Consul's own** documented `127.0.0.1:8500`, not one this
adapter invented. etcd documents no such default for its client, so
[`config-etcd`'s `Default`](etcd.md#connect-to-etcd) refuses without an endpoint instead.

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

## Map Consul keys to config keys

Keys under the prefix, split on `/`, become the nested tree. A key ending in `/`, which Consul uses
to mark a folder, carries no value and is skipped:

```
app/server/host = "localhost"        store.View().GetString("server.host") // "localhost"
app/server/port = "8080"        →    store.View().GetInt("server.port")    // 8080
```

### Values are strings, or decoded documents

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

## Write back to Consul

`config-consul` is read **and** write. A `Set` or `Remove` batched through `Apply` becomes a
single Consul transaction of compare-and-swaps, so the whole batch lands or none of it does. Each
operation is guarded by the `ModifyIndex` the key had when the store loaded it, so a change that
landed since, from another writer, is refused with `config.ErrConflict` rather than silently
overwritten. That is the same conflict contract a file write is held to, in Consul's own
primitives.

Writes target flat Consul keys; writing into a value a codec decoded from a blob is not supported
and lands as a sibling flat key. A value with no flat form (a map, a slice, or a lossy number such
as a `float32`) is refused with `config.ErrBackendUnsafe`.

## Watch for changes

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

A failed query is not treated as a change; the watch backs off for a second and re-issues it.

## Test without Consul

The adapter reaches Consul through the three-method `configconsul.KV` interface (`List`, `Watch`,
`Commit`), so a unit test hands `New` a fake and needs no agent:

```go
type fakeKV []configconsul.Pair

func (f fakeKV) List(context.Context, string) ([]configconsul.Pair, uint64, error) {
	return f, 1, nil
}

func (fakeKV) Watch(ctx context.Context, _ string, index uint64, _ time.Duration) (uint64, error) {
	<-ctx.Done() // nothing ever changes
	return index, ctx.Err()
}

func (fakeKV) Commit(context.Context, []configconsul.Op) (bool, error) { return true, nil }

backend := configconsul.New(fakeKV{
	{Key: "app/server/port", Value: []byte("8080"), ModifyIndex: 1},
}, "app/")
```

`Wrap(client)` is the real implementation, and what `FromClient` uses. The module's integration
suite, under `test/integration`, runs against a real Consul under testcontainers and is skipped
unless `INT_TEST_INTEGRATION=1` is set; it needs a Docker daemon. For a whole custom backend built
the same way, see [Write a custom backend](custom-backend.md).

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `config.ErrConflict` | A key under the prefix changed between load and write. | Reload and apply the change again. |
| `config.ErrBackendUnsafe` | The written value is a map, a slice or a lossy number, which a flat key cannot hold. | Write scalars, one key at a time, or a whole document as a string. |
| `configconsul.ErrNoConfig` | `FromConfig` got a nil config. | Pass a config, or use `Default`. |
| `fs.ErrNotExist` | Nothing with a value exists under the prefix. | Check the prefix. The store tolerates it as an empty layer unless it is the first source and you set `RequireFirstSource`. |
| A permission error from Consul (wrapped, no sentinel) | The ACL token expired or lacks `key:read` (or `key:write`) on the prefix. | [Rebuild the client with a fresh token](#the-token-is-read-once-and-no-rung-renews-it), or widen the policy. |

## When you don't need this

If your configuration is static and ships with the deploy, a file is simpler and adds
no runtime dependency on anything being reachable.

Reach for Consul when a value has to change **without a redeploy**, or when several
services must agree on one source of truth. Its watch is a blocking query, so a change
reaches the store as soon as Consul answers it, with no poll delay.

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

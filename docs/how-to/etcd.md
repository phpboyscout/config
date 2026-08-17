---
title: Read and write etcd
description: Make an etcd v3 prefix a config layer with config-etcd — real compare-and-swap writes, transactions that are atomic across keys, and a native watch that misses nothing on start.
tags: [how-to, adapters, backends, etcd]
---

# Read and write etcd

The core reads and writes files. An [etcd](https://etcd.io/) v3 cluster is a remote backend,
provided by a sibling module,
[`config-etcd`](https://gitlab.com/phpboyscout/go/config-etcd), so a consumer who needs etcd
takes it — and its etcd client — and one who does not pays nothing.

```bash
go get gitlab.com/phpboyscout/go/config-etcd
```

You build and configure the client — that is where every endpoint, credential, TLS and
namespace decision lives — and hand it in. The adapter takes a **prefix** that scopes and is
stripped from the keys:

```go
import (
	"gitlab.com/phpboyscout/go/config"
	configetcd "gitlab.com/phpboyscout/go/config-etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

client, err := clientv3.New(clientv3.Config{Endpoints: []string{"localhost:2379"}})
if err != nil {
	return err
}

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                    // YAML defaults
	config.WithBackend(configetcd.FromClient(client, "app/")),  // etcd outranks them
)
```

An etcd layer takes part in precedence, per-key merge, provenance and hot-reload exactly as a
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

If nothing needs to change between deploys, a file is simpler and cannot be
unreachable at start-up.

Reach for etcd when values change at runtime, when you need a **write across several
keys to be atomic**, or when you already run etcd and would rather not add a second
system.

## Values are strings, or decoded documents

etcd stores bytes, so by default every value is a scalar **string** and the View's typed
accessors coerce it — `GetInt("server.port")` parses `"8080"`, the same as an environment
variable.

Where a key holds a whole JSON or YAML document, pass a value codec — any
[`config.Codec`](https://pkg.go.dev/gitlab.com/phpboyscout/go/config#Codec), the interface the
sibling format adapters already implement:

```go
import configjson "gitlab.com/phpboyscout/go/config-json"

configetcd.FromClient(client, "app/", configetcd.WithValueCodec(configjson.Codec{}))
```

A value that decodes to a mapping becomes a subtree at its key's path; anything the codec
rejects stays a scalar string. A prefix mixing flat keys and document blobs therefore reads
correctly, and enabling a codec never turns a scalar key into a load error.

## Writing, and what protects you

Unlike the parameter stores, etcd has genuine compare-and-swap, so writes are supported from
the first release rather than deferred.

```go
_, err := store.Apply(ctx, config.Set("server.port", 9090))
```

**The guard is the revision each key held when the store last loaded**, not when the write was
staged. That is the protection worth understanding: if another client changed the key between
your program reading the configuration and deciding to write, the write is refused with
[`ErrConflict`](../reference/errors.md) rather than silently overwriting them.

```go
if errors.Is(err, config.ErrConflict) {
	// Somebody else moved a key. Reload and re-plan; do not retry blindly.
}
```

### A batch is one transaction

Every key in one `Apply` goes into a single etcd transaction, so it applies **completely or
not at all**. If any key's guard fails, none of the writes land.

That is stronger than most of this family can offer — `AtomicMultiKey` is true — and it means
the Store's partial-write rollback path is never needed here.

### There is no batch-size limit in the adapter

etcd bounds transactions in two ways, and both are **server** settings your client cannot
know: operation count (`--max-txn-ops`, 128 by default) and request size
(`--max-request-bytes`).

This adapter imposes no cap of its own, because a guessed one would be too low on a tuned
cluster and too high on a default one. An oversized batch fails with etcd's own error, naming
which limit it hit:

```
configetcd: committing 200 operation(s): etcdserver: too many operations in txn request
```

Match it with `errors.Is` against
[`rpctypes.ErrTooManyOps`](https://pkg.go.dev/go.etcd.io/etcd/api/v3/v3rpc/rpctypes) or
`ErrRequestTooLarge`. **It is not a conflict**, and treating it as one would send a caller
into a retry loop that can never succeed.

## Hot-reload is a real subscription

etcd's watch is a genuine change feed, so a change reaches your observers by push.

!!! info "An explicit poll interval does nothing here"
    `WithPollInterval` is accepted and **ignored** by this backend. There is nothing to
    pace — no poll, and no billed-request argument for slowing one down. If your store's
    only backend is etcd, setting an interval changes nothing at all.

    It still applies to any *other* backend in the same store that does poll.

The watch starts from the revision captured at load, not from the head, so a change landing
between reading the prefix and the watch attaching is **replayed rather than missed**. Without
that the store could hold stale values with no pending notification and no way to discover it.

The cost is that a revision old enough to have been compacted away cannot be replayed from.
When that happens the adapter treats it as "reload and re-watch from the current revision",
which is the honest recovery — the alternative would leave the layer silently unwatched.

## It is not a secrets store

The layer is **not** marked sensitive, and there is deliberately no option to mark it so.

Kubernetes keeps Secret objects in etcd, so people demonstrably store secrets there. But an
etcd prefix has no audit trail of who read a value, no leasing or rotation, and no separation
between the credential that reads configuration and the one that reads secrets — which is
exactly what [Vault](vault.md), [AWS Secrets Manager](aws-secrets.md) and the other secrets
backends provide. An option that made etcd *look* like a secrets store would make the weaker
choice the easier one.

If you have secrets in etcd and cannot move them, you are not stuck:

- wrap the backend in [`config.Filtered`](filter-a-backend.md), which forwards capabilities, or
- contribute those keys through a backend that does declare itself sensitive.

## Leased keys can vanish under you

etcd keys can carry a lease and disappear when it expires. This adapter reads them like any
other key: the value contributes while it exists, and once the lease lapses the key is simply
absent at the next load.

!!! warning "A leased prefix can lose keys with nobody writing anything"
    That is not a bug, and it is not special-cased — it is what a layer losing a key already
    means, and merge, provenance and shadowing all handle it the same way they handle a file
    being deleted underneath a running store.

    It is worth knowing before it surprises you. If you did not mean the keys to be
    ephemeral, use an unleased prefix.

## Getting a client

Building the client yourself is the default. One further rung exists:

```go
// You assembled the config; the adapter builds the client.
b, err := configetcd.FromConfig(clientv3.Config{Endpoints: []string{"localhost:2379"}}, "app/")
```

### There is deliberately no zero-conf rung

Every other remote adapter here offers a `Default()` over its SDK's own ambient
convention. **etcd has none.** `clientv3` has no `DefaultConfig`, and its only
environment variable is `ETCD_CLIENT_DEBUG` — a debug flag, not an endpoint or a
credential. It has neither an ambient credential chain *nor* endpoint discovery,
so both halves would have to be invented.

Adopting a provider's documented default is not the same act as inventing one, so
`config-etcd` stops at `FromConfig` and `ErrNoEndpoints` says so where you meet
it. This is settled rather than pending — see
[who owns the connection](../explanation/connection-ownership.md).

## What it costs

| | |
|---|---|
| Modules added | **25** — 15 for the etcd client, 10 for the `config` graph |
| Requires | the `config` version named in this module's `go.mod` — `go get` brings it — and etcd **v3** |

The etcd client weighs the same as Consul's and less than half of the Kubernetes `client-go` —
the measurement that decided this adapter was worth building while a ConfigMap adapter was not,
since [a mounted ConfigMap is already a directory of files](filekv.md). An allowlist test in
the module pins the footprint, so a transitive upgrade cannot widen it quietly.

## Related

- [How dynamic backends work](../explanation/dynamic-backends.md) — what every remote backend
  shares, and where they legitimately differ
- [Read & write Consul](consul.md) — the closest sibling, and the reference this follows
- [A directory of files](filekv.md) — the Kubernetes ConfigMap case, which needs no cluster client
- [Filter a backend](filter-a-backend.md) — bounding which keys a backend contributes

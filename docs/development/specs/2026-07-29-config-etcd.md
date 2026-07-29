---
title: config-etcd — etcd as a config backend
date: 2026-07-29
author: matt.cockayne
status: draft
---

# config-etcd

A backend adapter over [etcd](https://etcd.io/) v3, reached through the `Backend`
seam. It cites the [dynamic backend adapters
umbrella](2026-07-21-dynamic-backend-adapters.md) and settles what that umbrella
leaves to each adapter (**D2**).

It is Phase C's only remaining member: `config-k8s` is rejected in the same
revision that records this one, because a ConfigMap already reaches a pod as a
file or an environment variable (umbrella R4).

## Problem

etcd is the reference distributed key-value store — the thing Kubernetes itself
is built on — and the only Phase C system a process cannot already reach through
a layer it has. A Consul user has `config-consul`; an etcd user has nothing.

It is also the closest sibling to Consul the family has, which is the point: the
shape is proven, so this adapter is mostly a question of where etcd's semantics
differ rather than a fresh design.

## Verified facts

Probed against **`go.etcd.io/etcd/client/v3` v3.7.1** on 2026-07-29, not recalled.

**Dependency footprint: 16 linked modules** — `go.etcd.io/etcd/api/v3`,
`client/pkg/v3`, `coreos/go-semver`, `coreos/go-systemd/v22`, `golang/protobuf`,
`grpc-ecosystem/grpc-gateway/v2`, `google.golang.org/grpc`, `protobuf`,
`genproto/googleapis/{api,rpc}`, `go.uber.org/{zap,multierr}`,
`golang.org/x/{net,sys,text}`.

That is **the same weight as Consul's 16**, and less than half of `client-go`'s
38 — which is the measurement that decided Phase C's shape.

**The API this needs:**

| Need | Call |
|---|---|
| Read a prefix | `KV.Get(ctx, prefix, clientv3.WithPrefix())` |
| Per-key version | `KeyValue.ModRevision` (`CreateRevision`, `Version` also present) |
| Store-wide version | `GetResponse.Header.Revision` |
| Compare-and-swap | `Txn.If(clientv3.Compare(clientv3.ModRevision(key), "=", rev)).Then(ops...).Else(...)` |
| Write / delete | `clientv3.OpPut(key, val)`, `clientv3.OpDelete(key)` |
| Native watch | `Watcher.Watch(ctx, prefix, clientv3.WithPrefix()) WatchChan` |

**Integration testing:** `testcontainers-go/modules/etcd` exists and is current
(v0.43.0), so this can be proven against a real etcd in CI the way
`config-consul` is — unlike the cloud adapters, which have no emulator.

## Decisions

### D1 — Module `config-etcd`, package `configetcd`

Sibling module, README-only, docs bundled into the parent site. Family
convention; nothing here argues for an exception.

### D2 — The client is injected, and carries all authentication

```go
func New(kv KV, prefix string, opts ...Option) config.Backend
func FromClient(c *clientv3.Client, prefix string, opts ...Option) config.Backend
```

The consumer builds `*clientv3.Client` — endpoints, TLS, username/password,
per-request timeouts, namespace. This adapter takes none of those inputs, per
umbrella D3, and takes no view on how a cluster is reached.

`New` takes a narrow interface so the unit suite can inject a fake, exactly as
`config-consul` does. `FromClient` wraps a real client into it.

### D3 — Prefix scopes and is stripped; `/` splits into the tree

`prefix` bounds what this backend owns and is removed from the key before
splitting. Keys split on `/` into nested paths, so `app/server/port` under
prefix `app/` becomes `server.port`.

The same rule as `config-consul`, and for the same reason: etcd keys are a flat
byte-string space with `/` as convention, not structure.

### D4 — Values are scalars by default, structured through an injected codec

Umbrella **R1**: a byte-valued store decodes structured values through an
injected `config.Codec`, supplied by the consumer:

```go
configetcd.New(kv, "app/", configetcd.WithValueCodec(configjson.Codec{}))
```

Decode-to-mapping becomes a subtree; anything else falls back to the string
value, so a prefix mixing scalars and JSON blobs works. This adapter takes no
codec dependency of its own.

### D5 — Read **and** write at v0.1.0, with real CAS

Unlike the parameter stores, etcd has genuine compare-and-swap, so the write
path can be safe rather than deferred:

- **At Load**, capture each key's `ModRevision` *and* the response header's
  `Revision`.
- **Verify** compares the current header `Revision` for the prefix against the
  one captured at Load — the cheap early check.
- **Commit** is one `Txn` whose `If` compares every touched key's
  `ModRevision` against its load-time value; `Then` carries the `OpPut`/
  `OpDelete` set. A failed transaction is `ErrConflict`.

This is the version-at-Load trap the whole family turns on (**umbrella D11**),
and etcd expresses it directly. A new key compares against `ModRevision = 0`,
which is how etcd spells "does not exist".

### D6 — `AtomicMultiKey: true`

An etcd transaction is genuinely atomic across every key it touches, so unlike
the per-key stores this backend can promise it. Worth stating because the
Store's rollback path is only exercised for backends that cannot.

**Open question O1** covers etcd's transaction size limits.

### D7 — Native watch, not polling

`Watcher.Watch(ctx, prefix, WithPrefix())` returns a channel; the adapter runs a
goroutine translating it into the `WatchableBackend` callback and cancels on
stop. `NativeWatch: true`.

This is the umbrella's first-category system (**D6**): a real change feed, no
poll interval, no billed-request argument. The consumer's poll interval is
therefore ignored, which the documentation must say plainly — a caller setting
`WithPollInterval` on a store whose only backend is etcd should not believe it
did something.

### D8 — `Sensitive: false`

etcd holds configuration. It *can* hold secrets, and Kubernetes puts Secret
objects there, but a general etcd prefix is not secret-category material and
marking it so would refuse ordinary writes into the file beneath.

A consumer who keeps secrets in etcd wants that layer marked; **open question
O2** covers whether that is an option here or an argument for keeping secrets in
a secrets manager.

### D9 — Testing: fake, conformance, and a real etcd

Three ways, matching the family:

- A hand-written fake `KV` drives the unit suite — CAS, watch channels,
  transient errors.
- **`config/backendconformance` is the gate.** Writable and watchable, so every
  branch of the suite runs.
- **Integration against a real etcd** via `testcontainers-go/modules/etcd`,
  gated on `INT_TEST_INTEGRATION`, in `./test/integration/`. The DIND path is
  proven by `config-consul`.

The conformance suite must be run before the adapter is considered done, and any
assumption it turns out to carry is fixed in the core rather than worked around
here — the precedent from `config-keychain` (`BoundedKeySpace`) and the store
aggregate (`PinOnlyTargets`).

### D10 — Documentation is a deliverable, not a follow-up

A how-to page in the parent site, an entry in the ecosystem matrix, a landing
card. Written against the shipped API, not ahead of it.

## Public API

```go
type KV interface {
    Get(ctx context.Context, prefix string) ([]Pair, int64, error)
    Txn(ctx context.Context, cmps []Cmp, ops []Op) (bool, error)
    Watch(ctx context.Context, prefix string, onChange func()) (stop func(), err error)
}

type Pair struct {
    Key         string
    Value       []byte
    ModRevision int64
}

func New(kv KV, prefix string, opts ...Option) config.Backend
func FromClient(c *clientv3.Client, prefix string, opts ...Option) config.Backend
func Wrap(c *clientv3.Client) KV

func WithValueCodec(codec config.Codec) Option

const SourceKind = config.SourceKind("etcd")
```

## Testing strategy

Unit against the fake; `backendconformance` as the gate; integration against a
real etcd container. Falsification discipline throughout — every assertion
mutated and watched to fail, with compile errors and no-ops not counted.

## Migration & compatibility

A new module. Nothing existing changes.

## Open questions

- **O1 — What is the transaction size ceiling, and what happens at it?** etcd
  bounds request size (`--max-request-bytes`, 1.5 MiB by default) and
  transaction operation count. A `Store.Apply` touching more keys than fit must
  fail clearly rather than be silently truncated. Needs probing against a real
  server, and the answer may want a documented cap like Consul's 64-op limit.
- **O2 — Should a consumer be able to declare an etcd prefix sensitive?** D8
  says no by default. An option would be cheap, but it invites keeping secrets
  in a store with no audit trail or leasing, which the secrets managers exist
  for. Leaning: leave it out and say why.
- **O3 — Lease-bound keys.** etcd keys can carry a lease and vanish on expiry.
  A configuration layer whose keys disappear is a coherent thing to want
  (ephemeral service registration) and a confusing thing to debug. Probably
  out of scope for v0.1.0, but the read path should not break on one.
- **O4 — Does the watch need `WithRev` to avoid a gap?** Between Load and the
  watch starting, a change can be missed. etcd supports watching from a known
  revision, which closes it exactly. Likely a D7 amendment once measured.

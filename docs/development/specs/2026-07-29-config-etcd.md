---
title: config-etcd — etcd as a config backend
date: 2026-07-29
author: matt.cockayne
status: approved
approved: 2026-07-29
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

### D5a — A transaction too large fails with etcd's own error, uncapped

etcd bounds both request size (`--max-request-bytes`) and transaction operation
count (`--max-txn-ops`), and both are **server** settings a client cannot know.

It reports them distinguishably, which settles what to do: `rpctypes` exports
`ErrTooManyOps` (*"too many operations in txn request"*) and `ErrRequestTooLarge`
(*"request is too large"*), both matchable with `errors.Is`. Verified against
`go.etcd.io/etcd/api/v3` v3.7.1.

So this adapter imposes **no cap of its own**. A batch that exceeds the server's
limit fails with a wrapped error naming which limit it hit, rather than being
silently truncated or refused against a hardcoded number that does not match the
cluster. A guessed client-side cap would be wrong in both directions: too low on
a tuned cluster, too high on a default one.

### D6 — `AtomicMultiKey: true`

An etcd transaction is genuinely atomic across every key it touches, so unlike
the per-key stores this backend can promise it. Worth stating because the
Store's rollback path is only exercised for backends that cannot.

**D5a** covers what happens when a batch exceeds the server's transaction
limits.

### D7 — Native watch, not polling

`Watcher.Watch(ctx, prefix, WithPrefix())` returns a channel; the adapter runs a
goroutine translating it into the `WatchableBackend` callback and cancels on
stop. `NativeWatch: true`.

This is the umbrella's first-category system (**D6**): a real change feed, no
poll interval, no billed-request argument. The consumer's poll interval is
therefore ignored, which the documentation must say plainly — a caller setting
`WithPollInterval` on a store whose only backend is etcd should not believe it
did something.

**The watch starts from the revision captured at Load**, via `clientv3.WithRev`.
Without it there is a gap between reading the prefix and the watch attaching, in
which a change is silently missed — the store would then hold stale values with
no pending notification and no way to know. etcd can replay from a known
revision, so the gap closes exactly rather than being narrowed.

The cost is that a revision compacted away (`ErrCompacted`) makes the watch
unstartable from that point; the adapter treats that as "reload and re-watch
from the new revision", which is the honest recovery.

### D8 — `Sensitive: false`, with no option to change it

etcd holds configuration, so the layer is not secret-category material and
marking it so would refuse ordinary writes into the file beneath.

There is deliberately **no `WithSensitive()`**, and the reason is worth stating
rather than leaving as an omission. Kubernetes does keep Secret objects in etcd,
so people demonstrably store secrets there — but an etcd prefix has no audit
trail of who read a value, no leasing or rotation, and no separation between the
credential that reads configuration and the one that reads secrets. Those are
precisely what the Phase B adapters exist to provide. Offering a flag that makes
etcd *look* like a secrets store would make the weaker choice the easier one.

A consumer who has secrets in etcd and cannot move them is not blocked: they can
wrap this backend in [`config.Filtered`](2026-07-28-backend-key-filtering.md) —
which forwards `Capabilities` — or contribute the secret keys through a backend
that does declare itself sensitive. Neither is this adapter pretending.

### D8a — A lease-bound key is read like any other

etcd keys can carry a lease and disappear when it expires. They are read
normally: the value contributes while it exists, and when the lease lapses the
key is simply absent at the next load.

That is not a special case, because it is what a layer losing a key already
means — a file deleted beneath a running Store behaves identically, and the
merge, provenance and shadowing rules all already handle it. Detecting leases to
exclude them would cost a per-key check and would surprise anyone deliberately
using ephemeral keys, which is a legitimate thing to do with etcd.

The consequence is documented rather than engineered around: **a configuration
layer over a leased prefix can lose keys without anyone writing anything**, and
a consumer who finds that confusing wants an unleased prefix.

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

## Resolved (2026-07-29)

- **O1, transaction size ceiling.** No client-side cap; surface etcd's own
  `ErrTooManyOps` / `ErrRequestTooLarge` (D5a). The limits are server settings, and
  the server reports them distinguishably, so a guessed cap would be wrong in
  both directions.
- **O2, a sensitive option.** No, and the spec says why (D8): etcd has no audit
  trail, leasing or credential separation, and a flag making it look like a
  secrets store would make the weaker choice the easier one.
- **O3, lease-bound keys.** Read like any other key (D8a). A vanishing key is
  what a layer losing a key already means, and every merge rule handles it.
- **O4, the watch gap.** Watch from the revision captured at Load using
  `WithRev` (D7), which closes the gap exactly rather than narrowing it.

## Open questions

None outstanding. Every question raised at drafting is settled above; anything
implementation turns up is recorded as a dated revision rather than a silent
edit.

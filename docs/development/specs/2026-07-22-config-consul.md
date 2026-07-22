---
title: config-consul — the HashiCorp Consul KV backend adapter
date: 2026-07-22
author: matt.cockayne
status: implemented
approved: 2026-07-22
---

# config-consul

The first backend adapter, and the one the [dynamic backend adapters umbrella
spec](2026-07-21-dynamic-backend-adapters.md) is validated by: the
[custom-backend guide](../../how-to/custom-backend.md) already models a
Consul-shaped remote store end to end, so `config-consul` proves the umbrella's
conventions against the system the umbrella was written with in mind. Per the
umbrella's **D2**, no adapter is built without its own approved spec; this is
that spec for Consul. It cites the umbrella and settles the nine things the
umbrella left to each adapter.

## Problem

A tool that configures from HashiCorp Consul's key-value store cannot use this
module today: the core and its file adapters read files, and Consul is a remote,
strongly-consistent, path-keyed namespace fetched at runtime, with a native
change feed and compare-and-swap writes. The seam to reach it exists and is
proven (umbrella D-preamble; the custom-backend guide compiles a Consul-shaped
backend against a fake). What is missing is the shipped, first-class module —
`config-consul` — and the per-system decisions the umbrella cannot make: how a
Consul key maps to a config tree, what a value's type is when Consul stores only
bytes, how the conflict trap is satisfied with `ModifyIndex`, and how the whole
thing is tested against a real Consul without making every merge request need
Docker.

Consul is the right first adapter for the reason the umbrella names: it is
read **and** write **and** natively watchable, so it exercises every part of the
backend contract — including the parts a read-only parameter store would leave
unproven — and it is the shape the guide and the `backendconformance` suite were
already written against.

## Decisions

### D1 — Module `config-consul`, package `configconsul`

A bare, vendor-neutral name (umbrella D1): Consul is one system under one vendor,
so there is no cloud to qualify. The module is
`gitlab.com/phpboyscout/go/config-consul`, package `configconsul`, matching the
`config-<x>` / `configx` convention every file adapter uses. The repo carries a
README only; this spec lives centrally here (umbrella D2). It requires `config`
**v0.6.0+** — the release that shipped `backendconformance` and the `Sensitive`
enforcement this adapter's family depends on.

### D2 — Data model: a prefix scopes the store; keys are paths that nest

Consul KV is a flat namespace of `/`-separated keys holding opaque byte values.
The adapter takes a required **prefix** (umbrella D8), the same scoping control
`WithEnv` uses: it bounds the read to one namespace, strips the prefix, splits
the remaining key on `/`, and nests the segments into the layer's tree. A worked
example — prefix `app/`, with Consul holding:

```
app/server/host   = "localhost"
app/server/port   = "8080"
app/features/beta  = "true"
```

becomes the layer

```yaml
server:
  host: localhost
  port: "8080"
features:
  beta: "true"
```

Provenance names the full remote key (`consul:app/server/port`), so a value is
always traceable to where it lives (umbrella D8). The flat-to-nested step is the
few lines each flat file adapter already writes; there is still no shared core
helper for it (umbrella D8 / non-YAML R3). Consul "directory" markers — keys
ending in `/` with an empty value — are skipped, since they carry no leaf.

### D3 — Values are scalar strings by default; structured values decode through an injected codec

Consul stores bytes, not typed values, and its data comes in two common shapes.
The **flat-key** shape spreads configuration across many small keys
(`app/server/port = "8080"`); the **blob** shape stores a whole document under one
key (`app/config = '{"server":{"port":8080}}'`). The adapter reads both.

By default — the flat-key shape — each value is a UTF-8 **string leaf**, and the
View's typed accessors coerce as they do for every flat source:
`GetInt("server.port")` parses `"8080"`, exactly as for an environment variable or
a dotenv value. This is the honest model for a byte store and needs no
configuration.

For the blob shape, the constructor accepts an optional **value codec** — a
`config.Codec`, the very interface `config-json` and `config-toml` already
implement — that the consumer injects with `WithValueCodec`. When set, each Consul
value is passed to the codec: a value that decodes to a **mapping** (an object
document) is grafted into the tree as the subtree at that key's path; a value the
codec **rejects** — a bare scalar like `"8080"`, or bytes that are not a document —
falls back to a scalar string leaf. So a prefix that *mixes* flat scalar keys and
object-blob keys reads correctly under one codec, and a prefix with no blobs needs
no codec at all. That fallback is the edge case that keeps mixed content safe:
enabling a codec never turns a scalar key into a load error, it just leaves it a
string.

Reusing `config.Codec` is deliberate on three counts. The decoding machinery
already exists, so nothing new is invented. The **consumer** injects the one
format their Consul actually holds, so `config-consul` takes no codec dependency of
its own (a JSON-blob user wires `configjson.Codec{}`; `config-consul` never imports
`config-json`). And the same byte-value question faces every backend over a
byte-valued store — etcd, the parameter stores — so this is **established as the
family convention** (umbrella **R1**): a backend over a byte-valued store decodes
structured values through an injected `config.Codec`, defaulting to scalar strings.
No new core surface is added for it — the seam is the one that already ships.

### D4 — Capability: read, write, and native watch; not sensitive

`config-consul` implements `Backend`, `WritableBackend` and `WatchableBackend`
(umbrella D4 — capability by the type system, never a flag). Its `Capabilities`:

| Field | Value | Why |
|---|---|---|
| `PreservesComments` | `false` | a KV store has nowhere to put a comment |
| `AtomicMultiKey` | `true` | a Consul transaction commits the whole set indivisibly (D8) |
| `NativeWatch` | `true` | Consul blocking queries are a real subscription, not polling (D7) |
| `Sensitive` | `false` | Consul KV is general configuration, not a secrets store |

`Sensitive` is `false` deliberately. Consul KV is where application configuration
lives; secret material belongs in Vault, which is the `Sensitive: true` member of
this family (umbrella D5/D7). A consumer who stores secrets in plain Consul KV has
already made that choice upstream; the adapter does not second-guess it by
claiming a sensitivity Consul does not enforce. This keeps the `ErrSensitiveLeak`
guard meaningful — reserved for backends that genuinely hold secrets.

### D5 — Full read+write+watch at v0.1.0

The adapter ships complete rather than read-first (a supported path, umbrella D7 /
custom-backend guide, but not the one chosen here). Consul's compare-and-swap on
`ModifyIndex` gives clean conflict detection, the native watch is a standard
blocking query, and the custom-backend guide already models the write path — so
the cost of the write half is low and the value is high. Read-first would defer
exactly the parts (`Verify`/`Commit`, the conflict trap) that most need proving
for the family, and this adapter is the family's validation.

### D6 — The constructor injects a narrow client; auth stays with the consumer

The adapter defines its own narrow interface and never takes `*capi.Client`
auth, address, token, TLS or datacenter (umbrella D3). The consumer builds a
configured `*capi.Client` — that is where every credential decision lives — and
hands it in:

```go
// KV is the slice of Consul this adapter uses. A fake satisfying it drives the
// whole unit suite (D11); the real client is adapted by Wrap.
type KV interface {
	// List returns every pair under prefix and the index identifying that read.
	List(ctx context.Context, prefix string) (pairs []Pair, index uint64, err error)
	// Watch blocks until prefix changes past index or wait elapses, returning
	// the new index. A timeout returns the same index and no error.
	Watch(ctx context.Context, prefix string, index uint64, wait time.Duration) (uint64, error)
	// Commit applies ops as one atomic compare-and-swap transaction. ok is false
	// (with no error) when the CAS failed because the store moved.
	Commit(ctx context.Context, ops []Op) (ok bool, err error)
}

type Pair struct {
	Key         string // full Consul key, prefix included
	Value       []byte
	ModifyIndex uint64
}

// Op is one write in a commit: a set or a delete, guarded by the ModifyIndex the
// key had at Load (zero to require the key be absent — a create).
type Op struct {
	Key    string
	Value  []byte // ignored when Delete
	Index  uint64
	Delete bool
}
```

`Wrap(client *capi.Client) KV` adapts the real SDK — `List` → `client.KV().List`,
`Watch` → a blocking `List` with `QueryOptions{WaitIndex, WaitTime}`, `Commit` →
`client.KV().Txn` with `KVCAS`/`KVDeleteCAS` verbs. The common path is one call,
`FromClient(client, prefix, opts...)` = `New(Wrap(client), prefix, opts...)`, so a
consumer writes `configconsul.FromClient(client, "app/")` and never sees the narrow
interface unless testing. Both constructors take variadic `Option`s; the only one
in v0.1.0 is `WithValueCodec(config.Codec)` (D3).

### D7 — Watch is a Consul blocking query

`Watch` implements `WatchableBackend` over a Consul blocking query: `List` under
the prefix with `WaitIndex` set to the index last seen and `WaitTime` bounding
the block. Consul returns as soon as anything under the prefix changes past that
index, or when `WaitTime` elapses (returning the same index — no change, loop).
`onChange` fires on every return that advances the index. `NativeWatch: true`,
because latency is push, not poll. The `interval` argument from `WithPollInterval`
maps to `WaitTime` — the only knob a blocking query takes — with a sane default
if unset. The stop function is `sync.Once`-guarded and ends the query goroutine
when called or when the context is done (custom-backend guide).

### D8 — Conflict is `ModifyIndex` CAS, captured at Load

The trap the whole family turns on (umbrella D11): the conflict version is taken
at **Load**, not at **Prepare**. At Load the adapter records the prefix's
`LastIndex` and each pair's `ModifyIndex`. `Prepare` copies the edits and the
recorded indices; it does not re-read. `Verify` re-lists and compares the prefix
`LastIndex` — a cheap early check. `Commit` is the real guard: a Consul
transaction of `KVCAS`(key, value, `Index` = the value at Load) for sets and
`KVDeleteCAS`(key, `Index`) for removes; a new key uses `Index: 0`, which Consul
treats as create-if-absent. If any key moved since Load the whole transaction
fails atomically, and the adapter returns `config.ErrConflict`. This is the
`remoteBackend` guide's model in Consul's own primitives — CAS at commit, index
from Load — and it is what makes `backendconformance`'s conflict subtest pass
honestly rather than by luck.

### D9 — Writes are one atomic transaction

A `config.Apply` batch of sets and removes under this backend becomes a single
`KV.Txn`, so the whole batch lands indivisibly or not at all — which is why
`AtomicMultiKey` is `true`. Consul limits a transaction to 64 operations; a batch
larger than that is rejected with a clear error rather than silently split, since
splitting would break the atomicity the capability promises. (A batch that large
against a config store is pathological; the limit is documented, not worked
around.)

### D10 — Dependency footprint: the Consul SDK, stated plainly

`config-consul` depends on `github.com/hashicorp/consul/api`, which is the
largest thing in its graph and the honest cost of talking to Consul (umbrella
D9). Measured at `consul/api v1.34.4`, it pulls sixteen external modules:

```
github.com/hashicorp/consul/api
github.com/hashicorp/go-cleanhttp      github.com/hashicorp/go-rootcerts
github.com/hashicorp/go-hclog          github.com/hashicorp/go-immutable-radix
github.com/hashicorp/go-metrics        github.com/hashicorp/golang-lru
github.com/hashicorp/go-multierror     github.com/hashicorp/errwrap
github.com/hashicorp/serf              github.com/armon/go-metrics
github.com/go-viper/mapstructure/v2    github.com/fatih/color
github.com/mattn/go-colorable          github.com/mattn/go-isatty
golang.org/x/exp                       golang.org/x/sys
```

A `depfootprint_test.go` allowlist asserts exactly this set plus the `config`
graph, so a dependency creeping in — or the SDK growing one — is a failing test,
not a surprise in a consumer's `go.sum`. The testcontainers dependencies (D11)
are **test-only** and never reach the shipped package, so the allowlist, scoped
to `go list -deps .`, does not include them.

### D11 — Testing: a fake for units, testcontainers for the real thing

Three layers, from the umbrella's D10 and the shared `backendconformance` suite:

1. **A hand-written fake `KV`** — in-memory, CAS-aware, blocking-query-capable —
   drives the unit suite: reads, per-key merge, provenance, writes, the conflict
   case, and watch. No Docker, no account, IDE-runnable, the primary suite.
2. **The shared `backendconformance` suite** (`config/backendconformance`, shipped
   in v0.6.0), run against a `configconsul` backend over that fake. This is what
   proves the version-at-Load trap is not reintroduced.
3. **testcontainers integration** — the same behaviours against a *real* Consul,
   started by `github.com/testcontainers/testcontainers-go/modules/consul`
   (`consul.Run(ctx, "hashicorp/consul:1.15")` → `ApiEndpoint`), whose endpoint
   builds a real `*capi.Client` passed through `Wrap`. These live under
   `./test/integration/` (the go-test component's default `integration_paths`) and
   are **env-gated** on `INT_TEST_INTEGRATION` — the component's convention, set to
   `1` only in the DIND job — so `if os.Getenv("INT_TEST_INTEGRATION") != "1" {
   t.Skip() }` keeps them compiled and IDE-discoverable but out of the ordinary
   unit job.

### D12 — Integration runs in CI on the go-test component's DIND job

The env-gated integration tests (D11) run in CI through the **`go-test`
component's opt-in DIND job, added in phpboyscout/cicd v0.26.0** — not a
hand-rolled `docker:dind` job. `config-consul`'s `.gitlab-ci.yml` sets, on its
`go-test@v0.26.0` include:

```yaml
inputs:
  enable_integration: true                       # adds the go-test-integration job
  integration_paths: "./test/integration/..."    # the testcontainers suite (also the default)
```

The component's job supplies the rest: a `docker:dind` service, `DOCKER_HOST`,
`FF_NETWORK_PER_BUILD` and `TESTCONTAINERS_RYUK_DISABLED=true` (the ephemeral
daemon makes Ryuk redundant), and `INT_TEST_INTEGRATION=1` in the job's
environment — the gate D11's tests read. `integration_timeout` defaults to `10m`
(image pull + container startup) and `dind_image` to `docker:27-dind`; both are
left at their defaults.

This is a deliberate infrastructure opt-in: the runners are self-hosted and the
DIND job **requires a privileged runner** (the phpboyscout runner already is).
Enabling it is the point of moving to v0.26.0. The ordinary merge gate stays
Docker-free and fast — the fake and `backendconformance` suites carry it — and the
DIND job runs the real-Consul proof.

## Rejected alternatives

**Take `*capi.Client` directly in the constructor.** Simpler to call, but it
couples the adapter to the SDK type, makes the unit suite need a real or heavily
mocked client, and invites the adapter to reach for auth/address configuration
that is the consumer's. The narrow `KV` interface with a `Wrap` adapter keeps
units fake-driven and auth consumer-owned (umbrella D3), and `FromClient` keeps
the common path a single call anyway.

**Prefix-`LastIndex`-only conflict detection (no per-key CAS).** Comparing the
prefix index at `Verify` and then doing plain `KVSet`s would detect most
conflicts, but leaves a window between `Verify` and `Commit` where a change can
land and be overwritten. Consul gives per-key CAS inside a transaction for free,
so the commit itself is the guard (D8) — matching what the reference
`remoteBackend` does with its versioned `Put`. The weaker model is rejected for a
race the stronger one closes at no extra cost.

**Auto-sniff each value's format with no opt-in.** Tempting, because Consul values
are often JSON blobs — try to parse everything and use whatever comes out.
Rejected: it guesses a format the store does not declare, and the ambiguity is
real (`"123"` and `"true"` are valid JSON scalars, a malformed blob becomes a load
error for a key the consumer may not even use). The injected value codec (D3) is
explicit — the consumer names the format their store holds — while still handling
mixed content through the decode-or-string fallback, so it gets the capability
without the guessing.

**A bespoke value-decoder interface on the adapter.** `config-consul` could define
its own `ValueDecoder` type. Rejected: `config.Codec` already *is* that interface,
implemented by every format adapter, so a new one would fragment the seam and make
`config-json`'s decoder unusable here. Reuse the codec (D3, umbrella R1).

**Poll instead of blocking-query watch.** Consul has a real change feed; polling
it would be slower and busier for no benefit. Blocking queries are the native
mechanism and `NativeWatch: true` says so (umbrella D6).

## Public API

The module's exported surface:

- `func New(kv KV, prefix string, opts ...Option) config.Backend` — the injection
  seam; `kv` is a fake in tests, a `Wrap`ped client in production.
- `func FromClient(client *capi.Client, prefix string, opts ...Option) config.Backend`
  — the convenience path, `New(Wrap(client), prefix, opts...)`.
- `func Wrap(client *capi.Client) KV` — adapts the real Consul SDK client to the
  narrow interface.
- `func WithValueCodec(codec config.Codec) Option` — decode structured values
  through `codec` (D3); omitted, values are scalar strings.
- `type KV interface { … }`, `type Pair struct { … }`, `type Op struct { … }`,
  `type Option func(…)` — the narrow client seam (D6) and the option type.

The returned backend satisfies `config.WritableBackend` and
`config.WatchableBackend`; the Store discovers that by type assertion, so no
capability flag is exported (umbrella D4). No change to the `config` core is
required — v0.6.0 already carries everything this adapter needs.

## Testing strategy

Per D11: a fake-`KV` unit suite (reads, merge, provenance, writes, conflict,
watch); a `backendconformance.Run` against a `configconsul` backend over the fake,
including the `Control` that mutates the fake out of band for the conflict and
watch subtests; testcontainers integration under `./test/integration/` gated on
`INT_TEST_INTEGRATION`, run in CI on the v0.26.0 DIND job (D12); and a
`depfootprint_test.go` allowlist (D10). What would falsely pass: a conflict test
whose fake never advances its index — the `backendconformance` suite guards that
by requiring the `Control.Mutate` to move the version, and it was itself watched
to fail with a no-op mutation.

## Migration & compatibility

Purely additive for consumers: add the module and a `WithBackend(configconsul.
FromClient(client, prefix))` call, exactly as for a file adapter. No `config` core
change, no breaking change anywhere. The module ships at v0.1.0 as a full
read+write+watch backend, so there is no later read→write promotion to
communicate (unlike the read-first file adapters).

## Resolved (2026-07-22)

1. **Structured-value decoding.** Support **both** shapes: scalar strings by
   default, and an injected `config.Codec` for object blobs, with a
   decode-or-string fallback for mixed content (D3). Rather than an
   adapter-specific mechanism, this is established as the **family convention**
   (umbrella R1) so etcd and the parameter stores inherit it. Not deferred — the
   pattern is worth setting now, on the first adapter.
2. **The go-test v0.26.0 DIND inputs.** `enable_integration: true` adds the
   `docker:dind` job; `integration_paths: "./test/integration/..."` points it at
   the testcontainers suite; the gate is the component's `INT_TEST_INTEGRATION`
   env var, and `DOCKER_HOST`/Ryuk-disable are wired by the job. `integration_timeout`
   (`10m`) and `dind_image` (`docker:27-dind`) stay at their defaults (D12).
3. **Consul namespaces / Enterprise.** Namespace-agnostic: the consumer sets
   namespace/datacenter on the `*capi.Client` they build, and the adapter uses the
   client it is given (D6). No constructor parameter.
4. **Default `WaitTime`.** Pass Consul's own default through (no `WaitTime` set)
   rather than invent one; `WithPollInterval` still overrides it (D7).

All open questions resolved.

## Implementation phases

**Phase 0 — this spec.** Approve it, resolving the open questions above.

**Phase 1 — the module, read path.** Scaffold `config-consul` (README-only, no
microsite; umbrella D2 / adapter docs model), the narrow `KV` interface, `Wrap`,
`New`/`FromClient`, `Load` with prefix scoping and flat-to-nested (D2), the
scalar-string value model (D3), `Capabilities` (D4). Fake-`KV` read tests +
provenance. `depfootprint` allowlist (D10).

**Phase 2 — write and watch.** `Prepare`/`Verify`/`Commit` over the CAS
transaction (D8, D9) and `Watch` over the blocking query (D7). Run
`backendconformance` against the backend over the fake — the gate that proves the
conflict trap. Fake-`KV` write, conflict and watch tests.

**Phase 3 — real-Consul integration.** testcontainers suite (D11), env-gated, and
the v0.26.0 DIND CI job (D12). Then publish v0.1.0: the module `main`, the
`config` docs page (`how-to/consul.md`) bundled into the parent site, and the
landing card — the same rollout every adapter takes.

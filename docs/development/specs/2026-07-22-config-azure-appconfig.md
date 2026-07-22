---
title: config-azure-appconfig — the Azure App Configuration backend adapter
date: 2026-07-22
author: matt.cockayne
status: draft
issue: phpboyscout/go/config#4
---

# config-azure-appconfig

The third backend adapter, and the first of the cloud parameter-store trio the
[dynamic backend adapters umbrella spec](2026-07-21-dynamic-backend-adapters.md)
groups after Consul (umbrella Phase A). Per the umbrella's **D2**, no adapter is
built without its own approved spec; this is that spec for Azure App
Configuration. It cites the umbrella and settles the nine things the umbrella
left to each adapter, and it follows the shape the reference
[config-consul](2026-07-22-config-consul.md) adapter established — a narrow
injected client, a prefix→nested-tree data model, byte values decoded through an
injected `config.Codec` (umbrella **R1**), and the conflict version captured at
Load — diverging only where App Configuration's semantics force it.

Three forces make it diverge, and this spec is mostly about them:

- **A label dimension Consul has no equivalent for.** A setting's identity is
  `(key, label)`, not `key` alone. The prefix scopes the key space; the label is
  a second axis the adapter must take a position on.
- **No transaction.** App Configuration has no multi-key atomic write. Each
  setting is written on its own, guarded by its own ETag. `AtomicMultiKey` is
  therefore `false`, and a multi-key batch is a sequence of per-key
  compare-and-swaps, not one indivisible commit — the opposite of Consul's D9.
- **No change feed and no emulator.** There is nothing to subscribe to (watch is
  polled, not native — umbrella D6), and there is no local App Configuration to
  test against (Azurite is Blob/Queue/Table storage, not App Config, and there is
  no testcontainers module), so the integration story is real-service-only and
  env-gated, not the testcontainers-in-DIND story Consul uses.

## Problem

A tool that configures from Azure App Configuration cannot use this module
today: the core and its file adapters read files, and App Configuration is a
remote, cloud-hosted key-value store of `(key, label)` settings, reached over
HTTPS with an Azure credential, with ETag optimistic concurrency and no native
change feed. The seam to reach it exists and is proven (umbrella preamble; the
[custom-backend guide](../../how-to/custom-backend.md) compiles a remote backend
against a fake, and `config-consul` shipped it for real). What is missing is the
first-class module — `config-azure-appconfig` — and the per-system decisions the
umbrella cannot make: how a setting's `(key, label)` maps to a config tree, how
the label dimension is scoped, what a value's type is when a setting stores a
string, how the conflict trap is satisfied with an ETag rather than Consul's
`ModifyIndex`, what "atomic" means when there is no transaction, and how the
whole thing is tested against a real App Configuration store when there is no
emulator to stand one up locally.

App Configuration is a general application-configuration store, not a secrets
store — Azure Key Vault is the `Sensitive: true` member of this family (umbrella
D5/D7). So this adapter is read **and** write, like Consul, and unlike the
read-only secrets managers that follow it.

## Decisions

### D1 — Module `config-azure-appconfig`, package `configazureappconfig`

A **cloud-qualified** name (umbrella D1, OQ5 resolved): the same
parameter-store purpose exists under three vendors, and the service name is what
people search for, so the cloud is carried in the name —
`config-azure-appconfig`, alongside `config-aws-ssm` and `config-gcp-parameter`.
The module is `gitlab.com/phpboyscout/go/config-azure-appconfig`, package
`configazureappconfig`, matching the `config-<x>` / `configx` convention every
adapter uses. The repo carries a README only; this spec lives centrally here
(umbrella D2). It requires `config` **v0.6.0+** — the release that shipped
`backendconformance` and the `Sensitive` enforcement this adapter's family
depends on.

### D2 — Data model: a key-filter prefix scopes the store; keys are paths that nest; label is a second axis

App Configuration is a flat namespace of **settings**, each a
`(key, label, value, content-type, ETag)` tuple. Keys conventionally use `/` or
`:` as a hierarchy separator (`app/server/port`, `app:server:port`); the value
is a string; the label is an optional second identity dimension (`dev`, `prod`,
a release stamp), with a **no-label** default (the store's null label, `nil` in
the SDK).

The adapter takes a required **prefix** (umbrella D8), the same scoping control
`WithEnv` and `config-consul` D2 use. It bounds the read to one namespace via
App Configuration's server-side **key filter** (`prefix*`), strips the prefix,
splits the remaining key on `/`, and nests the segments into the layer's tree.
A worked example — prefix `app/`, label `prod`, with the store holding these
settings:

```
key=app/server/host   label=prod   value="localhost"
key=app/server/port   label=prod   value="8080"
key=app/features/beta  label=prod   value="true"
```

becomes the layer

```yaml
server:
  host: localhost
  port: "8080"
features:
  beta: "true"
```

Provenance names the full remote identity — key **and** label
(`azure-appconfig:app/server/port@prod`) — so a value is always traceable to the
exact setting it came from, label included (umbrella D8). The flat-to-nested step
is the few lines each flat adapter writes; there is still no shared core helper
for it (umbrella D8 / non-YAML R3).

The **label axis is what distinguishes this adapter from Consul**, and the exact
scoping control it deserves is left open (Open questions §1): whether the label
is a constructor argument beside the prefix, an `Option`, defaulted to the
no-label setting, and whether a read pins one label or composes several. This
decision records only that the data model *has* the axis and that provenance
records it; the control surface is the human's call.

The default key separator is `/`, matching Consul; a store that uses `:` is a
recognised convention App Configuration supports, and whether the separator is
configurable is a minor open question (§5).

### D3 — Values are scalar strings by default; structured values decode through an injected codec

App Configuration stores a string value plus a **content type**, and its data
comes in the same two shapes Consul's does. The **flat-key** shape spreads
configuration across many settings (`app/server/port = "8080"`); the **blob**
shape stores a whole JSON document under one key
(`app/config = '{"server":{"port":8080}}'`, with content type
`application/json`). The adapter reads both.

By default — the flat-key shape — each value is a UTF-8 **string leaf**, and the
View's typed accessors coerce as they do for every flat source:
`GetInt("server.port")` parses `"8080"`, exactly as for Consul, an environment
variable or a dotenv value.

For the blob shape, the constructor accepts an optional **value codec** — a
`config.Codec`, the interface `config-json` and `config-toml` already implement —
injected with `WithValueCodec`. When set, each value is passed to the codec: a
value that decodes to a **mapping** is grafted into the tree as the subtree at
that key's path; a value the codec **rejects** — a bare scalar, or bytes that are
not a document — falls back to a scalar string leaf. This is the exact model
established for Consul (D3 there) and set as the **family convention** by umbrella
**R1**: a backend over a byte-valued store decodes structured values through an
injected `config.Codec`, defaulting to scalar strings, taking no codec dependency
of its own (a JSON-blob user wires `configjson.Codec{}`; this module never imports
`config-json`).

App Configuration adds one thing Consul does not have: the setting **already
declares a content type**. Whether the adapter should *honour that content type
automatically* — decode a setting whose content type is `application/json`
without the consumer opting in — or keep the Consul model where the consumer
injects one codec explicitly, is a genuine design fork left open (Open questions
§3). This decision records only the default (scalar strings) and the
explicit-codec floor; the content-type-driven behaviour is the human's call.

### D4 — Capability: read, write, and polled watch; not sensitive

`config-azure-appconfig` implements `Backend`, `WritableBackend` and
`WatchableBackend` (umbrella D4 — capability by the type system, never a flag).
Its `Capabilities`:

| Field | Value | Why |
|---|---|---|
| `PreservesComments` | `false` | a setting store has nowhere to put a comment |
| `AtomicMultiKey` | **`false`** | App Configuration has no transaction; each setting is written on its own ETag (D9) |
| `NativeWatch` | **`false`** | there is no change feed; watch is polling (D7) |
| `Sensitive` | `false` | App Configuration is general configuration; secrets belong in Key Vault |

Two of these invert Consul's answers, and both are the store's doing, not a
choice: **`AtomicMultiKey: false`** because there is no multi-key transaction
(D9), and **`NativeWatch: false`** because there is no subscription primitive
(D7). Declaring them honestly is the whole point of the `Capabilities` split
(backend.go) — a consumer that needs cross-key atomicity learns it here rather
than by hitting a torn write.

`Sensitive` is `false` for the same reason as Consul: App Configuration is where
application configuration lives, and Azure's secrets home is Key Vault, the
`Sensitive: true` member of this family (umbrella D5/D7). A consumer who stores a
secret in a plain App Configuration setting has made that choice upstream — a
Key Vault **reference** setting is the sanctioned pattern, and it is a pointer,
not the secret — so the adapter does not claim a sensitivity the store does not
enforce, keeping the `ErrSensitiveLeak` guard meaningful (Open questions §4 notes
Key Vault references and feature flags as content-type edge cases).

### D5 — Full read + write + polled watch at v0.1.0

The adapter ships complete rather than read-first (a supported path, umbrella
D7 / custom-backend guide, but not the one chosen here), matching Consul D5. App
Configuration's ETag optimistic concurrency gives clean per-key conflict
detection (D8), writing configuration back to a parameter store is legitimate and
common (umbrella D7 — this is not a secrets store), and the polled watch is a
small, well-understood loop (D7). Read-first would defer exactly the parts
(`Verify`/`Commit`, the conflict trap) the shared `backendconformance` suite most
needs to exercise. The one caveat the write half carries is **non-atomicity**
(D9) — a real divergence from Consul that the write decision below owns
explicitly rather than papering over.

### D6 — The constructor injects a narrow client; auth stays with the consumer

The adapter defines its own narrow interface and never takes the
`*azappconfig.Client` type, an endpoint, a connection string, a
`TokenCredential`, or any `azidentity` configuration (umbrella D3). The consumer
builds a configured client — `azappconfig.NewClient(endpoint, cred, nil)` with a
credential from `azidentity` (a managed identity, a service principal, the Azure
CLI), or `azappconfig.NewClientFromConnectionString(cs, nil)` — that is where
every credential decision lives, and hands it in:

```go
// Store is the slice of App Configuration this adapter uses. A fake satisfying
// it drives the whole unit suite (D11); the real client is adapted by Wrap.
type Store interface {
	// List returns every setting whose key matches keyFilter and whose label is
	// label, walking the SDK pager to completion, each with the ETag identifying
	// its state at this read.
	List(ctx context.Context, keyFilter, label string) ([]Setting, error)

	// Get fetches one setting, or reports changed=false when it is unchanged
	// since sinceETag (the App Configuration conditional-GET / 304 path). Used by
	// the sentinel-key watch (D7).
	Get(ctx context.Context, key, label, sinceETag string) (s Setting, changed bool, err error)

	// Set writes value at (key, label). etag empty means create-if-absent; a
	// non-empty etag means overwrite only if the store's ETag still matches
	// (If-Match). ok is false, with a nil error, when the ETag no longer matches —
	// the setting moved since Load.
	Set(ctx context.Context, key, label, value, contentType, etag string) (ok bool, err error)

	// Delete removes (key, label) only if the store's ETag still matches. ok is
	// false, nil error, when it moved.
	Delete(ctx context.Context, key, label, etag string) (ok bool, err error)
}

// Setting is one App Configuration setting: its identity (key, label), its
// string value, its declared content type, and the ETag the write and watch
// paths compare against.
type Setting struct {
	Key         string
	Label       string
	Value       string
	ContentType string
	ETag        string // opaque; azcore.ETag rendered to string
}
```

`Wrap(client *azappconfig.Client) Store` adapts the real SDK: `List` →
`client.NewListSettingsPager(azappconfig.SettingSelector{KeyFilter: &f,
LabelFilter: &l}, nil)` walked with `pager.More()`/`pager.NextPage(ctx)`; `Get` →
`client.GetSetting(ctx, key, &azappconfig.GetSettingOptions{Label, OnlyIfChanged:
etag})` mapping a 304 to `changed=false`; `Set` → `client.AddSetting` when etag
is empty, else `client.SetSetting(..., &azappconfig.SetSettingOptions{Label,
OnlyIfUnchanged: etag})`; `Delete` → `client.DeleteSetting(...,
&azappconfig.DeleteSettingOptions{Label, OnlyIfUnchanged: etag})`. The common
path is one call, `FromClient(client, prefix, opts...)` =
`New(Wrap(client), prefix, opts...)`, so a consumer writes
`configazureappconfig.FromClient(client, "app/")` and never sees the narrow
interface unless testing. Both constructors take variadic `Option`s; the option
set is D3's `WithValueCodec` plus whatever the label control (§1) and watch
control resolve to.

### D7 — Watch is polling, over a sentinel key by default

App Configuration has **no change feed** — nothing to subscribe to (umbrella D6).
`Watch` therefore polls, and `NativeWatch` is `false`. Two poll strategies are
possible, and the store's own idiom picks the first:

- **Sentinel key (the "refresh" pattern, chosen default).** The consumer
  designates a sentinel setting — conventionally a key like `app/sentinel` bumped
  by whatever process changes the configuration — and `Watch` issues a
  **conditional `GetSetting`** on it each interval, passing the ETag last seen
  (`OnlyIfChanged`). App Configuration returns **304 Not Modified** while the
  sentinel is unchanged (one cheap request, no body) and the new setting when it
  moves — at which point `onChange` fires and the Store re-reads, re-merges and
  decides. This is Microsoft's own recommended refresh mechanism and it keeps the
  steady-state cost to a single conditional GET per interval regardless of how
  many settings the prefix holds.

- **Conditional re-list (fallback).** Re-list the whole prefix each interval and
  fire when any setting's ETag changed. Correct without a sentinel, but it costs a
  full listing per interval and cannot use a single ETag to short-circuit, so it
  is heavier. It is the right answer only when no sentinel key can be established.

The latency is the poll interval, not push — stated plainly, as umbrella D6
requires. The `interval` argument (`Watch`'s parameter, from the Store's
`WithPollInterval`) is the poll period, with a sane default if zero. The stop
function is `sync.Once`-guarded and ends the poll goroutine when called or when
the context is done, exactly as `config-consul`'s watch is. Whether to ship
`Watch` at all, versus omitting `WatchableBackend` and letting the consumer poll
the Store (umbrella D6 offers both), and which strategy is the default, are noted
open (Open questions §5) — this decision records the mechanism and its latency.

### D8 — Conflict is ETag If-Match, captured at Load

The trap the whole family turns on (umbrella D11): the conflict version is taken
at **Load**, not at **Prepare**. App Configuration gives every setting an
**ETag**, and its write operations take an `If-Match` precondition
(`OnlyIfUnchanged` on `SetSetting`/`DeleteSetting`) that fails with **412
Precondition Failed** when the stored ETag has moved. This maps cleanly onto the
conflict trap, with the ETag playing the role Consul's `ModifyIndex` plays:

- At **Load**, the adapter records each setting's ETag (keyed by `(key, label)`).
- `Prepare` copies the edits and the recorded ETags; it does **not** re-read.
- `Verify` is a cheap early check (see below).
- `Commit` is the real guard: each `Set` is conditioned on the ETag the setting
  had at Load (`If-Match`), and a create (a key absent at Load) is an
  `AddSetting`, which App Configuration accepts only if the key does not already
  exist (an implicit `If-None-Match: *`). A 412 on any operation is surfaced as
  `config.ErrConflict`.

This is the `remoteBackend` guide's model — version from Load, compared at commit
— in App Configuration's own primitive, and it is what makes
`backendconformance`'s conflict subtest pass honestly rather than by luck.

Two granularity questions this decision deliberately leaves to §2:

1. **There is no prefix-level version.** Consul's `Verify` compares one prefix
   `LastIndex`; App Configuration has no equivalent single fingerprint for a
   listing. `Verify` can only re-list and compare per-key ETags (or check the
   sentinel), which is closer to the real commit-time check than Consul's cheap
   early guard. Whether `Verify` does a full re-list, checks a sentinel ETag, or
   is a near-no-op that defers entirely to the per-key CAS at `Commit` is open.
2. **Per-key vs per-listing.** Because the conflict is per-setting ETag, a batch
   touching ten settings can have nine unchanged and one moved. This is the
   direct consequence of no transaction (D9) and is where partial failure lives.

### D9 — Writes are per-key compare-and-swap; there is no transaction

This is the sharpest divergence from Consul. App Configuration has **no multi-key
atomic operation** — no `Txn`, no batch. A `config.Apply` batch of sets and
removes under this backend is therefore a **sequence of per-setting
compare-and-swaps** (`SetSetting`/`AddSetting`/`DeleteSetting`, each `If-Match`),
applied in order. `AtomicMultiKey` is `false` (D4) and this is why.

The consequence is **partial failure**: if the third of five settings 412s
because it moved since Load, the first two have already been written and the last
two have not. The adapter's `Commit` stops at the first conflict, returns
`config.ErrConflict`, and **`Rollback` best-effort restores the settings it did
manage to write** — the same `priorState` mechanism `config-consul`'s write.go
uses (restore prior values / delete created keys, each guarded by the *current*
ETag so rollback does not clobber a concurrent change), but here it is genuinely
best-effort because the store gave no atomicity to lean on. A rollback that
cannot fully apply returns a named error (`errRollback`, as Consul's does), and
the batch is left in the partially-reverted state the store permits — surfaced,
never hidden.

The reason this is acceptable rather than a blocker: a configuration write batch
is small and usually single-key, the per-key CAS still prevents *overwriting* a
concurrent change (the value that matters), and the alternative — refusing writes
entirely — throws away legitimate, common single-key configuration updates for a
multi-key atomicity guarantee the store simply does not offer. The honesty is in
`AtomicMultiKey: false` and this decision, not in pretending. (Snapshots export a
consistent point-in-time copy but are not a write transaction, so they do not
change this.)

### D10 — Dependency footprint: the Azure App Configuration SDK, stated plainly

`config-azure-appconfig` depends on
`github.com/Azure/azure-sdk-for-go/sdk/data/azappconfig`, the honest cost of
talking to App Configuration (umbrella D9). It is a **modular** SDK — the adapter
pulls only the App Configuration data-plane package and its `azcore` runtime, not
the whole Azure SDK, and **not `azidentity`**, which is the consumer's dependency
for building the credential (D6). Measured at `azappconfig v1.2.0`, the shipped
graph is strikingly small — **five modules including itself**:

```
github.com/Azure/azure-sdk-for-go/sdk/data/azappconfig   v1.2.0
github.com/Azure/azure-sdk-for-go/sdk/azcore             v1.18.0
github.com/Azure/azure-sdk-for-go/sdk/internal           v1.11.1
golang.org/x/net                                         v0.39.0
golang.org/x/text                                        v0.24.0
```

Materially lighter than Consul's sixteen-module graph. A `depfootprint_test.go`
allowlist asserts exactly this set plus the `config` graph, so a dependency
creeping in — or the SDK growing one — is a failing test, not a surprise in a
consumer's `go.sum`. `azidentity` must **not** appear in it: if it does, the
adapter has taken on credential configuration it was supposed to leave to the
consumer (D6), so the allowlist doubles as a guard on that boundary.

### D11 — Testing: a fake for units, real-service-only for integration

Three layers, from the umbrella's D10 and the shared `backendconformance` suite —
but the third diverges from Consul because **App Configuration has no local
emulator**:

1. **A hand-written fake `Store`** — in-memory, ETag-aware (each write mints a new
   ETag; `If-Match` mismatches return `ok=false`; conditional `Get` returns
   `changed=false` on a matching ETag), label-aware — drives the unit suite:
   reads, per-key merge, provenance (key **and** label), writes, the conflict
   case, partial-batch failure and rollback (D9), and the sentinel-key watch. No
   cloud, no account, IDE-runnable, the primary suite.

2. **The shared `backendconformance` suite** (`config/backendconformance`, shipped
   in v0.6.0), run against a `configazureappconfig` backend over that fake, with
   the `Control` that mutates the fake out of band for the conflict and watch
   subtests. This proves the version-at-Load trap is not reintroduced — the same
   guarantee it gives Consul.

3. **Real-service integration, env-gated — and NOT in a DIND job.** This is the
   finding that changes the testing decision for this adapter:
   **Azure App Configuration has no local emulator.** Azurite emulates Blob, Queue
   and Table storage — **not App Configuration** — and there is **no
   `testcontainers-go` module** for App Configuration (verified: the
   `testcontainers-go/modules/azureappconfig` path resolves to no versions, while
   `.../modules/azurite` does exist). There is therefore **nothing to run in a
   Docker-in-Docker job**. Integration tests must hit a **real** App Configuration
   store, reached with a connection string or endpoint + credential supplied from
   the environment, and are **env-gated** on `INT_TEST_INTEGRATION` — kept
   compiled and IDE-discoverable (`if os.Getenv("INT_TEST_INTEGRATION") != "1" {
   t.Skip() }`) but out of the ordinary unit job, exactly as the toolkit
   convention requires — while **not** being wired into the go-test DIND job
   `config-consul` uses (D12 there). See D12.

### D12 — Integration CI: no DIND; a credentialed, opt-in job or local-only

Because there is no container to start (D11), the go-test component's DIND job —
`config-consul`'s D12 mechanism — **does not apply**. There is nothing for a
`docker:dind` service to host. The integration tests instead need a **real App
Configuration store and a credential**, which means either a dedicated, manually
or scheduled CI job that reads an App Configuration connection string from a
protected CI variable (and is skipped on forks and untrusted pipelines, since it
touches a real Azure resource and costs money), or they run **locally only**
against a developer's own store, with the fake + `backendconformance` suites
carrying the ordinary merge gate. The exact CI shape — a protected scheduled job
vs local-only — is an infrastructure decision noted open (Open questions §6),
because unlike Consul's self-contained DIND job it depends on provisioning and
paying for a cloud resource and on how secrets reach CI. What is **not** open:
the ordinary merge gate stays cloud-free and fast (fake + conformance), and the
integration suite never runs without an explicit credential and gate.

## Rejected alternatives

**Take `*azappconfig.Client` directly in the constructor.** Simpler to call, but
it couples the adapter to the SDK type, makes the unit suite need a real or
heavily mocked Azure client, and invites the adapter to reach for endpoint /
credential / connection-string configuration that is the consumer's (umbrella
D3). The narrow `Store` interface with a `Wrap` adapter keeps units fake-driven
and auth consumer-owned, and `FromClient` keeps the common path a single call —
the exact reasoning `config-consul` settled.

**Claim `AtomicMultiKey: true` and split a batch under the hood.** App
Configuration has no transaction, so "atomic" could only be faked by applying
settings one at a time and hoping. Rejected: it would promise an indivisibility
the store cannot deliver and hide the partial-failure window a consumer must be
able to reason about. The capability is declared `false` and the partial-failure
behaviour is specified (D9), which is the honest answer the `Capabilities` split
exists to allow.

**Snapshots as a write transaction.** App Configuration *snapshots* export a
consistent, immutable point-in-time copy of a filtered set of settings. Tempting
as an atomicity primitive, but a snapshot is a **read** construct — a frozen view
for consistent retrieval — not a multi-key write, and it cannot make a batch of
edits land indivisibly. Rejected as a mismatch for the write path; it may later
earn a role in consistent *reads* (Open questions §5), but not here.

**Auto-sniff each value's format with no opt-in.** As with Consul, tempting
because values are often JSON, but it guesses a format and turns a malformed blob
into a load error for a key the consumer may not use. The injected value codec
(D3) is explicit. Note the twist unique to this store: App Configuration settings
*carry a declared content type*, so "honour the content type" is **not** blind
sniffing — it is reading a label the store itself provides. That makes it a
legitimate design fork rather than a rejected one, and it is surfaced as an open
question (§3), not decided here.

**Poll by re-listing the whole prefix every interval.** Correct, but heavier than
it needs to be: it costs a full listing per tick and cannot short-circuit on a
single ETag. The sentinel-key conditional GET (D7) is the store's own refresh
idiom and reduces the steady state to one conditional request. Full re-list is
kept only as the fallback when no sentinel can be established.

**Depend on `azidentity` and offer `New(endpoint, cred)`.** Would let a consumer
skip building the client. Rejected on umbrella D3 grounds and measured in D10: it
pulls a credential library and its transitive graph into every consumer, couples
the adapter to one auth story, and makes the unit suite reach for real
credentials. The client — and therefore the credential — is the consumer's.

## Public API

The module's exported surface (subject to the label-control open question §1,
which may add one option or constructor parameter):

- `func New(store Store, prefix string, opts ...Option) config.Backend` — the
  injection seam; `store` is a fake in tests, a `Wrap`-ped client in production.
- `func FromClient(client *azappconfig.Client, prefix string, opts ...Option) config.Backend`
  — the convenience path, `New(Wrap(client), prefix, opts...)`.
- `func Wrap(client *azappconfig.Client) Store` — adapts the real App
  Configuration SDK client to the narrow interface.
- `func WithValueCodec(codec config.Codec) Option` — decode structured values
  through `codec` (D3); omitted, values are scalar strings.
- `type Store interface { … }`, `type Setting struct { … }`,
  `type Option func(…)` — the narrow client seam (D6) and the option type.

The returned backend satisfies `config.WritableBackend` and
`config.WatchableBackend`; the Store discovers that by type assertion, so no
capability flag is exported (umbrella D4). **No change to the `config` core is
required** — v0.6.0 already carries `backendconformance` and the `Sensitive`
enforcement this adapter needs, and this adapter declares `Sensitive: false` so
it does not exercise the leak guard.

The label control (§1), any content-type option (§3), and any watch/sentinel
option (§5) will add to this surface once resolved; they are called out rather
than invented here.

## Testing strategy

Per D11: a fake-`Store` unit suite (reads, merge, provenance including label,
writes, conflict, **partial-batch failure and best-effort rollback**, the
sentinel watch); a `backendconformance.Run` against a `configazureappconfig`
backend over the fake, including the `Control` that mutates the fake out of band
for the conflict and watch subtests; real-service integration under
`./test/integration/`, env-gated on `INT_TEST_INTEGRATION`, run **only** with a
credentialed store from the environment and **not** in a DIND job (D12); and a
`depfootprint_test.go` allowlist (D10, which also guards that `azidentity` never
enters the graph).

What would falsely pass, and how it is guarded:

- **A conflict test whose fake never changes an ETag** — a `Set` that mints the
  same ETag every time would let a stale write "succeed". The fake must mint a
  fresh ETag on every write, and the `backendconformance` conflict subtest
  requires the `Control.Mutate` to move the version; watch that subtest fail with
  a no-op mutation before trusting it.
- **A partial-failure test that only ever writes one key** — the non-atomic batch
  path (D9) is only exercised by a *multi-key* batch where a *middle* op 412s;
  a single-key test would leave the partial-write-and-rollback code unproven.
  The suite must include a batch whose second-of-three op conflicts and assert
  both the `ErrConflict` and the rollback of the first.
- **A watch test that re-lists instead of using the sentinel** — would pass while
  proving the wrong mechanism. The sentinel test must assert the conditional GET
  path fires exactly on a sentinel ETag change and *not* on an unrelated
  setting's change (or state plainly that the chosen semantics are re-list, per
  §5).

## Migration & compatibility

Purely additive for consumers: add the module and a
`WithBackend(configazureappconfig.FromClient(client, prefix))` call, exactly as
for a file or Consul backend. No `config` core change, no breaking change
anywhere. The module ships at v0.1.0 as a read + write + polled-watch backend, so
there is no later read→write promotion to communicate. The one behaviour a
consumer must understand up front — and which the README states — is
`AtomicMultiKey: false` (D9): a multi-key write is not indivisible on this store,
because the store offers no transaction.

## Open questions

Raised for the human to resolve before this spec moves to `approved`. These are
the decision-bearing forks the store's semantics create; the decisions above
record the mechanism and defaults, these choose the policy.

1. **The label dimension — the big one.** App Configuration's identity is
   `(key, label)`, an axis Consul lacks entirely (D2). What is the control?
   Options, not resolved here:
   - **A constructor argument beside the prefix** — `New(store, prefix, label,
     opts...)` — making label as first-class as prefix.
   - **An `Option`** — `WithLabel("prod")` — defaulting to the no-label setting
     (the store's null label) when omitted.
   - **Read scope vs write pin.** Does the label scope only the *read* (list one
     label), only the *write* (pin new settings to a label), or both? Does a read
     compose several labels (e.g. no-label base overlaid by `prod`), which is a
     common App Configuration usage but multiplies the listing and the merge?
   - **The default.** No-label only? All labels flattened (and if so, how is the
     `(key, label)` collision between two labels resolved in one tree)?
   This choice shapes the constructor signature, provenance, the write path and
   the data model, so it is the first thing to settle.

2. **Conflict / CAS granularity.** D8 establishes ETag `If-Match` as the
   conflict primitive, captured at Load — confirmed to work. Open: (a) what does
   `Verify` do, given there is **no prefix-level version** to compare (full
   re-list? a sentinel ETag check? a near-no-op deferring to per-key CAS at
   Commit)? and (b) is the per-key granularity (with the partial-failure window of
   D9) acceptable, or should a batch larger than one key be refused rather than
   applied non-atomically?

3. **Content type vs explicit codec (D3).** App Configuration settings carry a
   declared **content type**. Should the adapter **honour it automatically** —
   decode a `application/json` setting through a codec without the consumer opting
   in per value — or keep the Consul model where the consumer injects one
   `WithValueCodec` explicitly and it applies to every value? Automatic honouring
   is not blind sniffing (the store declares the type), but it means the adapter
   must decide *which* codec a content type maps to (does it import `config-json`,
   contradicting D3's "no codec dependency of its own"? or take a
   `map[contentType]config.Codec`?). Present both; do not choose.

4. **Feature flags and Key Vault references.** App Configuration stores two
   special setting kinds by content type: **feature flags**
   (`application/vnd.microsoft.appconfig.ff+json`, key prefix
   `.appconfig.featureflag/`) and **Key Vault references**
   (`...keyvaultref+json`, whose value is a *pointer* to a secret, not the
   secret). The umbrella put feature-flag systems **out of scope** for this family
   (OQ7). So: does this adapter **filter these out** of a prefix read (skip any
   setting whose content type is a feature flag or Key Vault reference), **pass
   them through** as opaque string values, or **refuse** a prefix that contains
   them? Feature flags almost certainly should be ignored (umbrella OQ7); Key
   Vault references are murkier — passing the reference string through is harmless
   (it is not the secret), but a consumer might expect resolution the adapter will
   not do.

5. **Watch: ship it, and how (D7).** (a) Ship `WatchableBackend` at all, or omit
   it and let the consumer poll the Store (umbrella D6 permits either for a
   pollable store)? (b) If shipped, is the default the **sentinel-key** conditional
   GET (needing the consumer to name a sentinel key — a new option,
   `WithSentinelKey`?) or **conditional re-list** (no configuration, heavier)? (c)
   The default poll interval when `interval` is zero. (d) Minor: is the key
   **separator** configurable (`/` vs `:`), and should **snapshots** back a
   consistent read? These are lower-stakes than §1–§4.

6. **Integration CI shape (D12).** Given there is no emulator and no DIND path
   (D11), do the env-gated integration tests run in a **protected, scheduled CI
   job** against a real App Configuration store (connection string in a protected
   variable, skipped on forks), or **local-only** against a developer's store with
   CI carrying just the fake + conformance suites? This is an infrastructure and
   cost decision, not a code one, but it needs an owner.

## Implementation phases

Gated on D2 of the umbrella: this spec is `approved`, with the open questions
above resolved, **before** any code.

**Phase 0 — this spec.** Approve it, resolving the open questions — especially the
label control (§1), the content-type fork (§3), and the feature-flag policy (§4),
each of which shapes the public API.

**Phase 1 — the module, read path.** Scaffold `config-azure-appconfig`
(README-only, no microsite; umbrella D2), the narrow `Store` interface, `Wrap`,
`New`/`FromClient`, `Load` with key-filter prefix scoping and flat-to-nested (D2)
including the resolved label control (§1), the scalar-string value model with
`WithValueCodec` (D3), `Capabilities` (D4). Fake-`Store` read tests + provenance
(key and label). `depfootprint` allowlist (D10), asserting `azidentity` is
absent.

**Phase 2 — write and watch.** `Prepare`/`Verify`/`Commit`/`Rollback` over the
per-key ETag compare-and-swap (D8) with the non-atomic partial-failure and
best-effort rollback path (D9), and `Watch` over the resolved poll strategy (D7).
Run `backendconformance` against the backend over the fake — the gate that proves
the conflict trap. Fake-`Store` write, conflict, partial-batch-failure and watch
tests.

**Phase 3 — real-service integration.** The env-gated integration suite under
`./test/integration/` (D11), run against a real App Configuration store per the
resolved CI shape (§6 / D12) — **not** a DIND job. Then publish v0.1.0: the module
`main`, the `config` docs page (`how-to/azure-appconfig.md`) bundled into the
parent site, and the landing card — the same rollout every adapter takes.

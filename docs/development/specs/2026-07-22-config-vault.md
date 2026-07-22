---
title: config-vault — the HashiCorp Vault KV v2 secrets backend adapter
date: 2026-07-22
author: matt.cockayne
status: approved
approved: 2026-07-22
---

# config-vault

The first **secrets** backend adapter, and the one that turns the
`Sensitive` machinery from a guarded invariant into a shipped capability. The
[dynamic backend adapters umbrella spec](2026-07-21-dynamic-backend-adapters.md)
opens Phase B with the four secrets managers, and its **D2** forbids building any
of them without an approved spec of its own; this is that spec for Vault. It
cites the umbrella and settles the nine things the umbrella leaves to each
adapter.

Vault leads Phase B for the same reason Consul led Phase A: it is the
vendor-neutral reference the family's conventions were written against. Where
`config-consul` proved the umbrella's *write* path, `config-vault` proves its
*secrets* path — a statically `Sensitive: true`, read-only backend, the exact
shape [R2](2026-07-21-dynamic-backend-adapters.md) taught `backendconformance` to
expect and that no adapter has yet exercised.

## Problem

A tool that reads its database password, API tokens or TLS material from
HashiCorp Vault cannot use this module today. It can read files, and now Consul
and three parameter stores, but the moment a value is a genuine secret two
things change that no existing adapter has had to handle.

The first is the **leak**. Secrets backends are read-only (umbrella D7), so a
`Set` on a key Vault provides cannot route to the Vault layer — it falls through
to the next writable layer, typically `config.yaml`. Without a guard, a consumer
calling `Set("db.password", …)` writes a Vault secret into a plaintext file on
disk. The core already refuses this (`ErrSensitiveLeak`, umbrella D5), and
`backendconformance` already requires a sensitive read-only backend to *trigger*
that refusal (umbrella R2) — but both were built ahead of a real consumer.
`config-vault` is the consumer.

The second is that **Vault is not shaped like the stores already adapted**. It is
not a flat namespace of scalar strings (Consul) nor one document per name (GCP
Parameter Manager). A Vault KV v2 secret is a *path* holding a **map of fields**,
each field holding arbitrary JSON, and a path can simultaneously be a secret and
a directory containing further secrets. That produces a merge — and a collision —
that no other backend adapter faces.

None of this can be inferred from documentation, so the decisions below are
grounded in a real Vault (`hashicorp/vault:1.20`, driven through
`vault/api` v1.23.0) probed on 2026-07-22. Three of the findings contradict the
obvious assumption, and each is called out where it lands.

## Decisions

### D1 — Module `config-vault`, package `configvault`, requiring `config` v0.7.0+

A bare, vendor-neutral name (umbrella D1): Vault is one system under one vendor,
with no cloud to qualify. The module is `gitlab.com/phpboyscout/go/config-vault`,
package `configvault`, matching the `config-<x>` / `configx` convention every
adapter uses. The repo carries a README only; this spec lives centrally here
(umbrella D2).

It requires `config` **v0.7.0+** — a *higher* floor than the v0.6.0 the Phase A
adapters take, and deliberately so. v0.7.0 is the release that shipped the
`backendconformance` branch requiring a **sensitive read-only** backend to refuse
the routed-beneath write with `ErrSensitiveLeak` (umbrella R2). Against v0.6.0
this adapter's conformance run would assert the *old* contract — that a read-only
backend silently routes beneath — and fail for being correct. The floor is the
suite, not the feature.

### D2 — The SDK is `github.com/hashicorp/vault/api`; `vault-client-go` is rejected

Two official Go clients exist and the choice is not a matter of taste. Measured
2026-07-22 from `proxy.golang.org`:

| Client | Latest | Released | Status |
|---|---|---|---|
| `github.com/hashicorp/vault/api` | **v1.23.0** | **2026-03-23** | active, post-1.0 |
| `github.com/hashicorp/vault-client-go` | v0.4.3 | **2023-12-18** | pre-1.0, ~2.5 years stale |

`vault/api` is the maintained, stable, post-1.0 client, and it carries the typed
`KVv2` helper this adapter is built on. `vault-client-go` — the newer generated
client — has had no release in two and a half years and has never reached 1.0;
adopting it would put a stale, unversioned dependency at the centre of a secrets
integration. `vault/api` it is.

### D3 — Data model: KV v2 only, with two scope modes

**KV v2 only.** The versioned KV engine is Vault's default and recommended
secrets engine. KV v1 is legacy and, as probed, differs in two ways that would
each fork the code path: its list endpoint is the bare mount (`kv1`) rather than
`<mount>/metadata`, and its `KVSecret.VersionMetadata` is **`nil`** — no version,
so nothing to carry as a change marker or provenance detail. Supporting it would
double the path logic for an engine Vault itself steers people off. Demand-driven
later, per the family's standing "no speculative surface" discipline
(non-YAML format adapters R3).

**Two scope modes**, mirroring the precedent `config-gcp-parameter` set for a
store whose natural unit is a document rather than a key:

**Single-secret mode (the default).** `New(kv, path)` reads **one** secret; its
fields become the layer's tree. This is the overwhelmingly common Vault usage —
one secret holds one application's configuration — and it costs exactly one API
call and one `read` capability in the consumer's policy. Given `secret/app`:

```json
{ "name": "checkout", "debug": false, "replicas": 3,
  "db": { "host": "db.internal", "port": 5432 } }
```

the layer is:

```yaml
name: checkout
debug: false
replicas: 3
db:
  host: db.internal
  port: 5432
```

**Prefix mode (opt-in).** `NewPrefix(kv, prefix)` walks a path prefix
recursively: list the directory, `Get` each secret, and nest each secret's fields
under its path segments with the prefix stripped (umbrella D8). Given prefix
`app/` over `app` → `{name: checkout}`, `app/db` → `{user: admin, password: s3cr3t}`
and `app/cache` → `{url: "redis://cache:6379"}`:

```yaml
name: checkout
db:
  user: admin
  password: s3cr3t
cache:
  url: redis://cache:6379
```

Prefix mode is opt-in rather than the default because its cost is real and
Vault-specific: there is no recursive list, so a walk is **N+1 round trips** (one
list per directory, one `Get` per secret), and it requires the `list` capability
on every directory — which least-privilege Vault policies routinely withhold.
A consumer who wants it asks for it.

**A path is both a secret and a directory.** Probed: `LIST secret/metadata`
returns `["app", "app/", "other/"]` — `app` is listed *twice*, once as a secret
and once as a directory. So in prefix mode a secret's own fields and its child
secrets merge into the **same tree node**. That merge is the source of D5.

### D4 — Values arrive structured; `json.Number` is normalised; no value codec

Probed against real Vault, a field's value round-trips with its JSON structure
**intact** — nested maps stay maps, arrays stay slices, `bool` stays `bool`,
`null` stays nil, at arbitrary depth. Vault is a *structured* store.

This is a genuine divergence from the family convention, and it is worth being
explicit rather than silently inconsistent. Umbrella **R1** has byte-valued
backends decode structured values through an **injected `config.Codec`**
(`WithValueCodec`), because Consul, etcd and the parameter stores hand back
opaque bytes that only the consumer knows the format of. Vault does not: it
stores and returns JSON natively, already decoded by the SDK. There is nothing
to decode, so **`config-vault` ships no `WithValueCodec` option**. R1 is not
contradicted — it governs byte-valued stores, and Vault is not one. The adapter's
README and how-to say so plainly, because a reader coming from `config-consul`
will look for the option.

One conversion *is* required, and it is easy to miss. The `vault/api` client
decodes JSON with `UseNumber`, so **every number arrives as `json.Number`** — at
every depth, including inside nested maps and slices:

```
t.intv         json.Number   42
t.floatv       json.Number   1.5
t.nested.a     json.Number   7
t.listval[0]   json.Number   1
```

Left alone, `Get("replicas")` would hand a consumer a `json.Number` — a `string`
kind — where every other backend yields an `int`. So the adapter **walks the
decoded value recursively** and converts each `json.Number` to `int64` where it
parses as an integer and `float64` otherwise, leaving `string`, `bool` and `nil`
untouched. Without this the adapter is subtly wrong in a way no shallow test
catches.

### D5 — A field/child collision is refused — loudly, and prominently documented

In prefix mode a secret's fields and its child secrets occupy one node (D3), so
they can collide: secret `app` holds a field `db`, **and** secret `app/db`
exists. Both claim `db`. There is no correct silent answer — one of them is a
value the operator can see in Vault and would never expect to vanish.

**The adapter refuses the Load**, with a named error identifying the mount, the
path and the colliding segment. This matches the family: `config-xml` refuses an
attribute/element name collision (non-YAML format adapters D21) and `config-hcl`
refuses an attribute/block-type collision, both for the same reason — an
ambiguous merge is a defect in the source, and guessing hides it.

**This decision carries a documentation obligation, not just an implementation
one.** A refused Load is a hard failure at startup, and an operator who hits it
must be able to understand it immediately rather than read it as a bug in the
adapter. So the collision rule is documented **prominently and by worked
example** in both places a consumer looks:

1. the module's **`README.md`**, in its own section — not a footnote — showing the
   colliding shape, the error, and the two ways to resolve it (rename the field,
   or move the child secret); and
2. the **config site's Diátaxis docs** — the `how-to/vault.md` page carries the
   same worked example, and the shared
   [dynamic backends explanation](../../explanation/dynamic-backends.md) gains
   the *why* (ambiguous merges are refused across this family, not guessed).

Single-secret mode cannot collide — one secret, one flat map of fields — which is
a further reason it is the default.

### D6 — Soft-deleted and destroyed secret versions are skipped, not read as empty

The third probed surprise, and the one most likely to ship as a bug. A
soft-deleted KV v2 secret does **not** return `ErrSecretNotFound`. It returns:

```
err == nil     Data == nil     VersionMetadata.DeletionTime = <non-zero>
```

and it **still appears in directory listings**. So a naive prefix walk finds the
key, `Get`s it, sees no error, and inserts an **empty node** into the tree for a
secret that has been deleted. A destroyed version behaves the same way, flagged
by `VersionMetadata.Destroyed`.

The adapter therefore treats a secret as absent when its `Data` is empty or its
version is marked deleted or destroyed: it is skipped in prefix mode, and in
single-secret mode the layer is empty exactly as an absent source is (the
`absent_source_tolerated` contract). `api.ErrSecretNotFound` is a real sentinel
and is matched with `errors.Is` for the genuinely-absent case.

### D7 — Capability: read-only, poll-watchable, and statically `Sensitive: true`

| Capability | Value | Why |
|---|---|---|
| `Sensitive` | **`true`** | It is a secrets manager. Statically true — see below |
| Writable | *not implemented* | Secrets are read-only by default (umbrella D7); write is Phase D (D11) |
| `NativeWatch` | `false` | Vault's `api` client exposes no change feed; watch polls (D10) |
| `AtomicMultiKey` | `false` | No writes at v0.1.0 |
| `PreservesComments` | `false` | Not a file format |

`Sensitive` is **statically `true`**, and that is a deliberate contrast with
`config-aws-ssm`. SSM reports `Sensitive` **dynamically** — true only when a
`SecureString` is actually loaded — because it is a *mixed* store where most
parameters are ordinary configuration, and because at the time it was built a
statically-sensitive read-only backend could not pass `backendconformance`
(config-aws-ssm R1). Vault is not mixed: every value in it is a secret. And the
conformance gap that forced SSM's hand is closed — umbrella R2 taught the suite
that a sensitive read-only backend must refuse the routed-beneath write with
`ErrSensitiveLeak`. `config-vault` is the first adapter to exercise that branch,
which is precisely why it leads Phase B.

### D8 — The constructor injects a narrow client; four entry points

Per umbrella D3 the adapter defines its own minimal interface and never
constructs or authenticates a client:

```go
// KV is the narrow Vault surface this adapter needs.
type KV interface {
	// Get reads the latest version of one secret. It returns a nil Secret
	// (no error) when the secret is absent, deleted or destroyed (D6).
	Get(ctx context.Context, path string) (*Secret, error)
	// List returns the immediate children of a directory. Child directories
	// carry a trailing "/". An absent directory returns nil, nil.
	List(ctx context.Context, path string) ([]string, error)
}

// Secret is one version of one Vault secret.
type Secret struct {
	Data    map[string]any // fields, JSON-structured (D4)
	Version int            // KV v2 version; provenance and change marker (D11)
}
```

`List` is only called in prefix mode, so a single-secret consumer can supply a
`KV` whose `List` is never reached. Four constructors, matching the naming
`config-gcp-parameter` established for a two-mode adapter:

- `New(kv, path, opts…)` / `NewPrefix(kv, prefix, opts…)` — the injection seam.
- `FromClient(client, mount, path, opts…)` / `FromClientPrefix(client, mount, prefix, opts…)`
  — the convenience path over a real `*vaultapi.Client`.

`Wrap(client *vaultapi.Client, mount string) KV` adapts the SDK. The mount is a
`Wrap` parameter rather than baked into paths because KV v2 splits its endpoints:
reads go through `client.KVv2(mount).Get(path)` while listing goes to
`<mount>/metadata/<path>` via `Logical().ListWithContext` — a split the adapter's
own logic should never have to know about.

### D9 — Authentication *and token lifetime* stay with the consumer

The adapter never authenticates (umbrella D3) — no auth method, no address, no
token handling, no credential of any kind in its API. The consumer builds and
authenticates a `*vaultapi.Client` and hands it over; the adapter uses the client
it is given. This is what makes the adapter testable against a fake (D8), keeps
every Vault auth method usable without the adapter knowing any of them, and keeps
credentials out of a configuration library's surface area.

For Vault this needs saying more firmly than for the other adapters, because
Vault tokens **expire**. A long-running process holding a client whose token has
lapsed will see reloads — and poll-watch cycles (D10) — start failing, and that
is not a bug in this adapter. Token acquisition and renewal are the consumer's,
using the SDK's own `LifetimeWatcher`, Vault Agent, or whatever their platform
provides. The adapter's contribution is to fail *legibly*: a failed reload
surfaces the underlying Vault error rather than swallowing it, so an expired
token is diagnosable from the error alone.

**This carries a documentation obligation** on the same footing as D5's, because
"how do I authenticate?" is the first question every consumer of this module will
have, and the honest answer — "you already did, before you called us" — is
unsatisfying unless it is shown. The README and `how-to/vault.md` must therefore
carry **runnable worked examples** for the common paths, each ending in the same
one-line handover to `FromClient`:

- **token** (the dev/CI path, and what the integration suite uses);
- **AppRole** (the common service-to-service path);
- **Kubernetes** auth (the common in-cluster path);

plus an explicit **token renewal** example using `LifetimeWatcher`, stating that
the adapter does not do this for you and what breaks if nobody does. The point of
three examples rather than one is to make the pattern — *any* authenticated
client works — obvious by induction rather than by assertion.

Namespaces (Vault Enterprise) are likewise the consumer's: they are set on the
`*vaultapi.Client`, so the adapter is namespace-agnostic and takes no parameter
for it, exactly as `config-consul` is (config-consul, Resolved 3).

### D10 — Watch is polling, at a 60-second default

`vault/api` exposes no change feed — Vault's event system is not part of this
client's surface — so watch is polling (umbrella D6), the same shape as the three
parameter stores. `config-vault` implements `WatchableBackend` with
`NativeWatch: false`.

The default interval is **60 seconds**, matching the parameter stores, and for a
sharper reason here: every poll is an authenticated read that lands in Vault's
**audit log**. A 2-second cadence would flood an audit stream that exists to be
readable. `WithPollInterval(d)` overrides it. The loop backs off on transient
errors rather than hammering a Vault that is sealed or unreachable, the same
`watchBackoff` behaviour `config-consul` uses. In prefix mode a poll costs the
same N+1 walk a Load does, which the how-to states plainly so the cost is chosen
knowingly.

The Store coalesces and settles the resulting foreign change, so the polling
backend inherits debouncing for free.

### D11 — The version is provenance and change marker; CAS is real but out of scope

Every read captures `KVSecret.VersionMetadata.Version`. It serves two purposes at
v0.1.0: it is the **change marker** the poll loop compares to detect a foreign
change (D10), and it appears in **provenance**, so a value can always be traced
to the exact secret path and version it came from.

It is worth recording — because it will be the first question Phase D asks — that
Vault offers a **genuine compare-and-swap**: `Put` with
`api.WithCheckAndSet(version)` fails a stale write. Probed, a stale CAS returns a
`*api.ResponseError` with **`StatusCode: 400`** and the message
`"check-and-set parameter did not match the current version"`. Note the shape:
unlike Azure App Configuration's clean `412 Precondition Failed`, Vault signals
the conflict with a **400 plus a message match**, which is a mushier thing to
depend on and would need care. So the conflict trap *is* satisfiable here — but
umbrella D7 keeps secrets read-only until a consumer justifies writing, and this
spec does not justify it. Write support is a **Phase D** follow-on with its own
dated revision, not a v0.1.0 feature.

### D12 — Dependency footprint: the Vault SDK, 16 modules

Measured, not estimated — `go list -deps` over a package importing
`vault/api` v1.23.0, 2026-07-22:

```
cenkalti/backoff/v4              hashicorp/go-secure-stdlib/parseutil
go-jose/go-jose/v4               hashicorp/go-secure-stdlib/strutil
hashicorp/errwrap                hashicorp/go-sockaddr
hashicorp/go-cleanhttp           hashicorp/hcl
hashicorp/go-multierror          hashicorp/vault/api
hashicorp/go-retryablehttp       mitchellh/mapstructure
hashicorp/go-rootcerts           ryanuber/go-glob
golang.org/x/net                 golang.org/x/text
golang.org/x/time
```

Seventeen external modules — the same order as `config-consul`'s sixteen, and far
below `config-gcp-gcs`'s 54. With the nine the `config` graph brings, a consumer
inherits **26** in total. A `depfootprint_test.go` allowlist pins it exactly
(umbrella D9), so an SDK bump that drags in new modules fails the build rather
than quietly widening every consumer's graph.

### D13 — Testing: a fake `KV` for units, real Vault for integration

Per umbrella D10. The injected seam (D8) means the unit suite needs no Vault: a
fake `KV` drives single-secret reads, prefix walks, the collision refusal (D5),
deleted-secret skipping (D6), `json.Number` normalisation (D4), provenance and
the poll watch.

The gate that matters is **`backendconformance.Run`** against a `configvault`
backend over the fake. For a sensitive read-only backend that suite asserts the
`read_only_skipped_by_routing` branch added by umbrella R2 — that a write to a
key this layer defines is refused with `ErrSensitiveLeak` rather than routed into
the plain layer beneath. That assertion is the entire security claim of Phase B,
and this is the first adapter to make it.

Integration runs against a **real Vault** via testcontainers
(`testcontainers-go/modules/vault` v0.43.0, verified working against
`hashicorp/vault:1.20` on 2026-07-22), under `./test/integration/`, gated on
`INT_TEST_INTEGRATION`. It covers what a fake cannot: the real list/get path
split, a genuinely soft-deleted secret, real `json.Number` decoding, and a real
version bump reaching a watcher. testcontainers stays test-only and out of the
depfootprint allowlist, which scopes to `go list -deps .`.

### D14 — Integration runs in CI on the go-test component's DIND job

`.gitlab-ci.yml` sets `enable_integration: true` on the `go-test` component
(phpboyscout/cicd v0.26.0) with `integration_paths: "./test/integration/..."`.
The job runs `docker:dind` on the privileged runner and sets the component's
`INT_TEST_INTEGRATION` gate; `DOCKER_HOST`, `FF_NETWORK_PER_BUILD` and
Ryuk-disable are wired by the component. This is the path `config-consul` proved
end-to-end (its MR !2 pipeline ran the DIND job green), so it is adopted rather
than re-derived.

### D15 — Documentation is a deliverable of each phase, not a follow-on pass

Every phase in this spec ships its documentation **with** its code, and a phase
is not done until both are. This is a deliberate departure from how the last two
families were run, and the reason for the departure matters.

The param-store trio and the six filesystem adapters were built by **parallel
agents**, so config-site docs were held back into a single centralised pass —
three agents editing `explanation/adapters.md`, the homepage and the shared
framing at once would have conflicted, and docs written ahead of a shipped API
drift from it (the `configjson.Codec{}` episode, where four merged pages
documented a symbol that was not exported, is the standing example). That
sequencing decision was about **write contention between agents**, not about docs
being second-class.

`config-vault` is built **solo**, so the contention does not exist and the
deferral buys nothing. Deferring instead costs something: the auth examples (D9)
and the collision worked example (D5) are the two things most likely to be
written thinly if they are written last, and they are precisely the two a
consumer meets first. Both are load-bearing enough to be decisions in this spec.

The documentation deliverables, in full:

| Where | What | Phase |
|---|---|---|
| module `README.md` | what it is, footprint (D12), auth examples (D9), the collision section (D5), the read-only/`Sensitive` consequence | 1–2 |
| `how-to/vault.md` | the task-shaped guide: authenticate, single-secret, prefix mode, watch cadence, the collision worked example | 3 |
| `explanation/dynamic-backends.md` | extend the shared family page — why ambiguous merges are refused; why a secrets layer refuses writes beneath it | 3 |
| `explanation/adapters.md` + `docs/index.md` | ecosystem matrix row and homepage pill | 3 |
| `landing` card | the subcard under the config card | 3 |

Reference documentation stays on **pkg.go.dev** — the family convention, no
hand-maintained API dump (as settled for `config-consul`). Whether Vault also
earns a Tutorial is deferred to the docs work itself: Consul got one because it
was the family's first backend and the tutorial taught the *whole model*; Vault's
distinct content is auth and the collision rule, which are how-to shaped. If the
auth examples prove to want a guided run, it gets one.

### D16 — Implementation is test-first, and BDD is assessed rather than assumed

**Test-first is binding, not aspirational.** Each contract in this spec becomes a
failing test before the code that satisfies it, and — the part that actually
catches defects — **every new assertion is watched to fail** before it is made to
pass. This is the practice that found the real bugs in this family: the
`backendconformance` seed-mutation bug that had silently turned a watch assertion
into a no-op, and the `Verify`-against-`Load`-fingerprint trap the whole store
design turns on. A green test that has never been seen red proves nothing.

The three probed surprises (D4, D5, D6) are the specific places where a test can
pass while the adapter is wrong, so each gets an assertion written against the
**real** shape, listed in the Testing strategy below.

**BDD suitability assessment** — required by the project's BDD guidance, which
asks every spec to decide this up front rather than leave it to the implementer:

*In the adapter: no Gherkin.* `config-vault` is pure library logic behind a
narrow injected interface — no CLI, no multi-step user workflow, no service
lifecycle. Its contracts are matrix-shaped (value types, path shapes, absent and
deleted secrets) which table-driven unit tests express directly and a scenario
would express with more wiring and less precision. Crucially, its
*wired-together* behaviour is already covered by an executable contract:
`backendconformance.Run`, which is the family's conformance suite and does the
job a feature file would, in a form every adapter shares. No adapter in this
family has a `features/` directory, and this spec does not add the first.

*In the core: one scenario, and it closes a real gap.* Verified 2026-07-22, the
core's `features/store/` covers write routing — "a write targets the layer that
already defines the key" — but **no scenario anywhere covers the sensitive-leak
refusal**. That behaviour is only in `sensitive_test.go`. It is the most
security-critical routing rule in the design and the entire premise of Phase B,
and it is exactly the shape BDD is for: user-facing, Given/When/Then, spanning
layers. So it gets a scenario in the core's existing suite, where the behaviour
and the step definitions already live:

```gherkin
@store @writes @sensitive
Scenario: A secret is never written into the plain layer beneath it
  Given a config file "/app.yaml" containing:
    """
    db:
      password: from-file
    """
  And a sensitive read-only backend providing "db.password"
  And a store reading both
  When I set "db.password" to "rotated"
  Then the write is refused as a sensitive leak
  And "/app.yaml" still contains "from-file"
```

This is a **`config` core change, so it is a separate MR** from the adapter and
is not gated on it — the gap exists today, independent of Vault. It is recorded
here because this spec is what surfaced it.

## Revisions

### R1 (2026-07-22) — the version is the change marker only, not part of provenance (corrects D11)

D11 said the captured version "appears in **provenance**, so a value can always
be traced to the exact secret path *and version* it came from". Building Phase 1
showed the second half cannot hold, for a structural reason worth recording
rather than quietly dropping.

A layer's provenance is its `config.Source`, whose fields are `Kind`, `Name`,
`Document` and `Writable` — there is nowhere to put a version. The only candidate
is `Name`, and `Name` is spoken for: the core requires a backend's `ID()` to
equal the `Source.Name` of the layers it returns, because that is how the Store
finds the backend again when routing a write. `ID()` must therefore be the *same
string on every Load*, while the version changes every time anyone writes the
secret. Folding the version into `Name` would break routing the moment a secret
was rotated.

So: provenance names the **mount and secret path**, which is what a reader needs
to go and look at the value, and the version remains what D11's other half
already made it — the marker the poll watch compares to notice a foreign change.
The decision is unchanged in substance; the claim about provenance was wrong and
is corrected here. `TestLoad_IDIsStableAcrossLoads` guards it: a version bump
must not change a layer's identity.

## Rejected alternatives

**Use `vault-client-go`.** The newer, generated official client. Rejected on
measured evidence (D2): v0.4.3, last released 2023-12-18, still pre-1.0. A
secrets integration is the last place to accept a stale, unversioned dependency.

**Support KV v1 alongside v2.** Cheap-looking, since a read-only adapter never
needs v1's missing CAS. Rejected (D3): it forks both the list path and the get
path for a legacy engine, and `VersionMetadata: nil` means no change marker for
the poll loop and no version for provenance — so v1 would be a second, quietly
weaker code path. Added on demand, not on spec.

**Offer `WithValueCodec` for symmetry with `config-consul`.** Rejected (D4):
Vault returns already-decoded JSON, so the option would have nothing to do. An
inert option that exists only for symmetry is worse than its absence — it implies
a capability gap that is not there.

**Auto-resolve a field/child collision (child wins, or field wins).** Never fails
a Load, which is superficially attractive for a startup-path dependency.
Rejected (D5): both variants silently discard a value the operator can see in
Vault, and for a secrets store "the password silently wasn't the one you set" is
the worst possible failure mode. Refuse and document.

**Treat an empty `Data` as an empty secret.** The naive reading of a deleted
secret. Rejected (D6) on probed behaviour: it inserts phantom empty nodes for
secrets that no longer exist, and would make a deletion look like a
restructuring of the config tree.

**Prefix mode as the default, for uniformity with `config-consul`.** Rejected
(D3): it makes the common case (one secret, one read) pay an N+1 walk and demand
a `list` capability the consumer's policy may not grant. Uniformity with Consul
is not worth a worse default for Vault's actual usage.

**Poll aggressively (the 2-second default the watcher uses for files).**
Rejected (D10): every poll is an audited, authenticated read. 60 seconds is the
honest cadence for a secrets store; consumers who need fresher values opt in.

## Public API

The exported surface for v0.1.0 (read + poll-watch):

- `func New(kv KV, path string, opts ...Option) config.Backend` — single-secret
  mode (D3), the default shape.
- `func NewPrefix(kv KV, prefix string, opts ...Option) config.Backend` —
  prefix-walk mode (D3).
- `func FromClient(client *vaultapi.Client, mount, path string, opts ...Option) config.Backend`
  — `New(Wrap(client, mount), path, opts...)`.
- `func FromClientPrefix(client *vaultapi.Client, mount, prefix string, opts ...Option) config.Backend`
  — the prefix equivalent.
- `func Wrap(client *vaultapi.Client, mount string) KV` — adapts the real SDK
  client to the narrow interface (D8).
- `func WithPollInterval(d time.Duration) Option` — the watch interval (D10);
  default 60s.
- `type KV interface { … }`, `type Secret struct { … }`, `type Option func(…)` —
  the injection seam (D8).
- A named error for the collision refusal (D5), matchable with `errors.Is`.

The returned backend satisfies `config.WatchableBackend` but **not**
`config.WritableBackend`; the Store discovers that by type assertion. No `config`
core change is required — v0.7.0 already carries everything this adapter needs.

## Testing strategy

Per D13, written test-first per D16: a fake-`KV` unit suite;
`backendconformance.Run` over the fake as the gate (the sensitive read-only
branch, umbrella R2); a testcontainers suite against real Vault under
`./test/integration/`, gated on `INT_TEST_INTEGRATION` and run on the DIND job
(D14); and a `depfootprint_test.go` allowlist (D12). No Gherkin in the adapter,
and one new scenario in the core's existing suite — the assessment and its
reasoning are D16.

What would falsely pass, and is therefore watched to fail explicitly:

- A collision test whose fake never actually produces the colliding shape — the
  refusal must be observed firing, not assumed.
- A deleted-secret test using `ErrSecretNotFound` instead of the real
  `nil error, nil Data` shape (D6) — it would pass while the adapter is wrong
  about real Vault, which is exactly the bug the probe found. The integration
  suite deletes a real secret to close this.
- A `json.Number` test asserting on the *string* form — it passes whether or not
  the conversion happened. Assert the Go **type** a consumer receives.
- The `ErrSensitiveLeak` conformance assertion against a backend that reports
  `Sensitive: false` — it would pass by routing beneath. `Capabilities()` is
  asserted directly as well.

## Migration & compatibility

Purely additive. A consumer adds the module and a
`WithBackend(configvault.FromClient(client, "secret", "app"))` call. No `config`
core change and no breaking change anywhere.

One behaviour is new *to consumers* even though it is not new to the core, and
the how-to must lead with it: once a Vault layer is in the store, `Set` on a key
Vault provides is **refused** with `ErrSensitiveLeak` rather than written to the
file beneath. That is the D5 guard working as designed, and it is the first time
any shipped adapter makes it fire. A consumer who wants such a key writable must
not source it from Vault.

The module ships at v0.1.0 read-only; a later write capability would be a
documented minor-version promotion with a mandatory release note (non-YAML format
adapters D18) and its own dated revision here (D11).

## Resolved (2026-07-22)

1. **Scope shape.** **Both modes, single-secret as the default** (D3) — mirroring
   `config-gcp-parameter`. Single-secret is one call and one policy grant and
   matches how Vault is actually used; prefix walking is opt-in because it costs
   N+1 round trips and a `list` capability least-privilege policies often deny.
2. **KV v1.** **Not supported at v0.1.0** (D3). v2 is the default and recommended
   engine; v1 forks both the list and get paths and has no version metadata to
   carry. Added on demand.
3. **Field/child collision.** **Refused with an error** (D5), matching
   `config-xml` D21 and `config-hcl`. Explicitly extended, at review, into a
   **documentation obligation**: the rule is documented prominently and by worked
   example in the module README *and* in the config site's Diátaxis docs
   (`how-to/vault.md` plus the shared dynamic-backends explanation), so a consumer
   meets it before a refused Load does.
4. **Authentication.** **Injected client, no auth in the adapter** (D9, umbrella
   D3) — confirmed at review as the deliberate default, not an omission. Extended,
   at review, with the same documentation obligation as the collision rule: the
   README and how-to carry runnable **token, AppRole and Kubernetes** examples
   plus a `LifetimeWatcher` **token-renewal** example, because token expiry
   degrades a long-running consumer in a way that is not the adapter's bug but
   will be read as one.
5. **Documentation status.** **First-class: each phase ships its own docs** (D15).
   The param-store and filesystem families deferred site docs to a centralised
   pass to stop parallel agents colliding on shared files; `config-vault` is built
   solo, so that reason does not apply and the deferral would only risk the auth
   and collision examples being written thinly and last.
6. **Test approach.** **Test-first, with every assertion watched to fail** (D16).
   BDD assessed per the project's guidance rather than assumed: **no Gherkin in
   the adapter** (pure library logic; `backendconformance` already is the
   executable wired-together contract), but **one new scenario in the `config`
   core** covering the sensitive-leak refusal, which is verifiably uncovered by
   any existing scenario and is Phase B's whole security premise. That is a
   separate core MR, not gated on this adapter.

All open questions resolved.

## Implementation phases

Each phase ships code **and** its documentation (D15), test-first (D16).

**Phase 0 — this spec.** Approve it, resolving the questions above.

**Phase 1 — the module, single-secret read.** Scaffold `config-vault`
(README-only, no microsite; umbrella D2), the narrow `KV` interface, `Wrap`,
`New`/`FromClient`, `Load` for one secret, `json.Number` normalisation (D4),
deleted-secret handling (D6), `Capabilities` with static `Sensitive: true` (D7),
provenance (D11). Fake-`KV` tests + the `depfootprint` allowlist (D12).
**Docs:** README with the footprint and the **auth examples** (D9).

**Phase 2 — prefix mode and watch.** `NewPrefix`/`FromClientPrefix`, the
recursive walk with prefix stripping and field/child merging, the **collision
refusal** (D5), and the polled `Watch` (D10). Run `backendconformance` against
the backend over the fake — the gate that proves the `ErrSensitiveLeak` contract
(D13). **Docs:** the README's collision section, by worked example (D5).

**Phase 3 — real-Vault integration, docs and release.** testcontainers suite
(D13), env-gated, on the DIND CI job (D14). **Docs:** `how-to/vault.md`, the
`explanation/dynamic-backends.md` extension, the `explanation/adapters.md` matrix
row, the homepage pill and the landing card (D15). Then publish v0.1.0 — the
rollout every adapter takes.

**Separate, ungated — the core BDD gap.** The sensitive-leak scenario (D16) lands
in the `config` core on its own MR. It is not part of the adapter's phases and
does not block them; it is recorded here because this spec is what surfaced it.

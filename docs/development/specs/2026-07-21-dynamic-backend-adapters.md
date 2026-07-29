---
title: Dynamic backend adapters — remote configuration systems as sibling modules
date: 2026-07-21
author: matt.cockayne
status: approved
approved: 2026-07-21
revised: 2026-07-27
issue: phpboyscout/go/config#4
---

# Dynamic backend adapters

## Problem

The module and its adapters read and write **files**. But a large share of modern configuration
does not live in a file: it is fetched at runtime from a remote system — HashiCorp Consul or
Vault, a cloud parameter store (AWS SSM, Azure App Configuration, GCP Parameter Manager), a cloud
secrets manager, an etcd cluster, a Kubernetes ConfigMap. A tool that can only read files cannot
adopt this module in those environments, exactly the "we only do X" objection the non-YAML format
work removed for file formats.

The seam for this already exists and is proven. `WithBackend` takes anything satisfying `Backend`,
with `WritableBackend` and `WatchableBackend` as opt-in capabilities (store-architecture D11), and
[Write a custom backend](../../how-to/custom-backend.md) walks a **Consul-shaped** remote backend
end to end — read, native watch, and a compare-and-swap write with the conflict detection the
whole design turns on. That how-to's code is compiled and tested against a fake remote. So the
question is not *can* the module talk to these systems — it demonstrably can — but which ones we
ship as first-class sibling modules, and under what shared conventions.

This spec is the **umbrella**. It establishes the family of `config-<system>` backend adapters and
the decisions that cut across all of them. It deliberately does **not** specify any single adapter:
these systems differ far more than file formats do — a codec is `Decode(bytes)`, but a backend
carries authentication, network failure, consistency semantics, a watch mechanism, atomicity
limits, and a genuine "should a config tool even write this?" question for secrets. That
per-system granularity cannot live in a shared document. So the headline rule of this spec is that
**each adapter gets its own approved spec before it is built** (D2).

## Decisions

### D1 — Backend adapters are sibling `config-<system>` modules, one per system

Every remote system ships as its own module, depended on only by consumers who use it — the same
decision, for the same reason, as the format adapters (non-YAML format adapters D1, D9). The
reason is sharper here: each adapter carries its system's **SDK**, and the cloud SDKs are large. A
consumer configuring from AWS SSM must not acquire the Azure or GCP SDK to do it. One system per
module keeps each consumer's graph to the one integration they use.

**Naming (OQ5, resolved): cloud-qualified.** A cloud service carries its cloud —
`config-aws-ssm`, `config-aws-secrets`, `config-azure-appconfig`, `config-azure-keyvault`,
`config-gcp-parameter`, `config-gcp-secret` — because the same purpose exists under three vendors
and the service name is what people search for. A vendor-neutral system stays bare —
`config-consul`, `config-vault`, `config-etcd`, `config-k8s`.

### D2 — Every adapter has its own approved spec before implementation

This is the load-bearing rule. This umbrella spec settles what is common; **no `config-<system>`
adapter is implemented until it has its own `status: approved` spec** — kept **centrally, here in
the config repo** under `docs/development/specs/` alongside this umbrella (OQ6, resolved), so the
family's specs stay discoverable and cross-linkable and this one can point at each. The adapter's
own repo carries only its README, as the format adapters do. Each per-adapter spec cites this one
and settles the decisions this one cannot:

1. **Data model** — how the system's keys, paths, namespaces, versions and value types map onto a
   config layer's nested tree, with worked examples.
2. **Capability** — read-only, or read+write; watchable natively, by polling, or not; sensitive or
   not — and *why*, decided against the interface split (D4) and `Sensitive` (D5).
3. **Client injection** — the narrow interface the adapter defines (D3) and which SDK client
   satisfies it.
4. **Authentication & configuration model** — what the consumer supplies, what the adapter never
   touches (D3).
5. **Watch mechanism** — the native change signal, or the polling strategy, and its latency (D6).
6. **Error & consistency semantics** — eventual consistency, throttling, retries, partial reads,
   what a transient failure does to a Load or a reload.
7. **Write semantics** — if writable, the atomicity and compare-and-swap model, and how the
   conflict trap (store-architecture) is satisfied; if a secrets store, the justification for
   writing at all (D7).
8. **Dependency footprint** — the exact SDK packages, with the allowlist (D9).
9. **Testing** — the fake-client surface and the env-gated integration tests (D10).

The rationale is the whole reason for spec-driven development, amplified: the expensive part of a
remote adapter is *understanding the system's semantics*, not the Go. A shared template gets the
Go right and the semantics wrong. The per-adapter spec is where the semantics are pinned, reviewed
and recorded before a line is written — and where the "should we write secrets" kind of question
is answered by a human, once, on the record.

### D3 — The constructor injects a configured client; the adapter owns no credentials

An adapter's constructor takes an already-configured client, wrapped behind a **narrow interface
the adapter defines** — never the SDK client type directly, and never authentication, region,
tenant, endpoint or profile configuration. That is the pattern the custom-backend guide already
uses (`remoteStore` with `Fetch`/`Put`), and it earns three things at once:

- **The adapter is credential-agnostic.** Every cloud does auth differently and the consumer
  already configures it; the adapter has no business re-deciding it.
- **The adapter is testable without the cloud.** A fake satisfying the narrow interface drives
  every unit test (D10), so the suite needs no network and no account.
- **The dependency is honest.** The SDK is required by the consumer who constructs the client, and
  the narrow interface keeps the adapter's own surface small.

### D4 — Capability is answered by the type system, never a flag

An adapter implements `WritableBackend` and `WatchableBackend` only when the system genuinely
supports writing and change-notification (store-architecture D11, backends explanation). A backend
that cannot persist does not implement `Prepare`; one that cannot notice change does not implement
`Watch`. Routing, `Plan` and the watch set then follow the type system rather than a runtime flag
that can disagree with the code.

### D5 — `Sensitive` becomes load-bearing, and the core enforces it

`Capabilities.Sensitive` was forward-declared for the file work and consumed by nothing (non-YAML
format adapters D8). The secrets backends are the moment it becomes real. A backend over Vault,
AWS Secrets Manager, Azure Key Vault or GCP Secret Manager declares `Sensitive: true`, and the
rule it guards — **a value sourced from a Sensitive backend must never be written into a layer
that is not** — becomes a **checked invariant in the core write path** (OQ2, resolved: enforce).

The leak this prevents is specific and made *more* likely, not less, by D7. Because secrets
backends are read-only, a write to a key a Sensitive layer defines cannot route to that layer —
so routing falls through to the next writable layer, which is typically a plain file. A consumer
calling `Set("db.password", …)` on a key Vault provides would, without a guard, have that value
written into `config.yaml`. So the enforcement is at routing: **a write that would land in a
non-Sensitive layer while a Sensitive layer defines the key is refused**, with a named error,
rather than leaking the value into a non-secret store. This is a core write-path change (Public
API), and it is what makes read-only secrets backends safe to combine with writable files.

### D6 — Watch is native where the system offers it, polled where it does not

The systems split cleanly:

- **Native change signal** — Consul blocking queries, etcd watch, Kubernetes informers. These
  implement `WatchableBackend` with push-based notification and `NativeWatch: true`.
- **No change feed** — the cloud parameter and secrets stores (SSM, App Configuration, Parameter
  Manager, Secrets Manager, Key Vault, Secret Manager). These have nothing to subscribe to, so
  they either implement `Watch` by polling they own, or omit it and let the consumer poll via the
  Store — the per-adapter spec decides, stating the latency either way (D2.5).

The Store's existing hybrid watch already coalesces and settles foreign changes, so a polling
backend inherits that for free.

### D7 — Write is per-system and demand-driven; writing secrets is refused by default

Reading is most of the value, and read-only is a first-class outcome (non-YAML format adapters
D10). Writing configuration back to a KV or parameter store — Consul, etcd, SSM parameters — is
legitimate and common, and gets write support where a consumer needs it. Writing **secrets** back
from a configuration-*consuming* tool is a different matter: secrets are provisioned by a separate,
audited process, and a config library writing to Vault or a secrets manager is a surprising and
risky capability. So a secrets backend ships **read-only by default**; write support requires a
per-adapter spec that justifies the need and specifies the guard. This is the same "read-only is
honest" reasoning as the file side, with a security edge.

### D8 — Keys are paths; a prefix scopes the backend and maps to the nested tree

These stores are flat, path-keyed namespaces (`/app/server/port`, `app/server/port`). An adapter
takes a **prefix** — a required scoping control the way `WithEnv`'s prefix is (non-YAML format
adapters D15 / env backend): it bounds what the backend reads to one namespace, strips the prefix,
and nests the remaining path segments into the layer's tree. Provenance names the full remote key,
so a value can always be traced to where it lives. The flat-to-nested step is the same few lines
each flat file adapter writes; there is still no shared core helper for it (R3 withdrew D15).

### D9 — The SDK is the honest cost, stated per adapter

Unlike the file adapters — several of them zero-dependency — a backend adapter carries its system's
client library, and that is the largest thing in its graph. This is stated plainly, per adapter,
in an allowlist `depfootprint` test and the module's README, so the cost is visible up front.
Where an SDK is modular (AWS SDK for Go v2 ships per-service packages; the Azure and GCP SDKs
similarly), the adapter pulls only the one service it uses, not the whole SDK.

### D10 — Testing is a fake client plus env-gated integration tests

The injected-client seam (D3) makes the unit suite need no cloud: a fake satisfying the narrow
interface drives reads, writes, conflicts and watch. Integration tests that hit the real service
are **gated behind an environment variable** rather than a build tag, so they stay compiled and
IDE-discoverable but do not run in ordinary CI — the toolkit's standing convention for
external-dependency tests. Each adapter's spec names its fake surface and its integration gate.

### D11 — A shared backend-conformance suite asserts the contract every backend must meet

The codec `conformance` suite does not apply here — there is no file and no codec. But there **is**
a contract every remote backend must meet, and one trap every *writable* one must avoid: the
conflict fingerprint, or version, must be captured at **Load**, not at **Prepare** — the exact D3
trap of the file seam, in a remote costume (the custom-backend guide's `remoteBackend` gets this
right, comparing the version recorded at Load). So this spec **builds** a
`config/backendconformance` suite (OQ3, resolved: yes), stdlib `testing` only (as the codec one is, D17 there), that an
adapter runs against its backend to assert: it participates as an ordinary layer with per-key
merge and provenance; an absent or empty source is tolerated; for a `WritableBackend`, a write
round-trips and a change landing between Load and Commit is refused with `ErrConflict`; for a
`WatchableBackend`, a foreign change reaches observers. Making it shared makes the version-at-Load
trap impossible once rather than risked per adapter — the same value the codec conformance suite
delivered.

## Rejected alternatives

**One `config-remote` module with every backend.** Simpler to publish. Rejected on the same
dependency ground as one `config-formats` module, only worse: it would pull *every* cloud's SDK
into every consumer's graph. See D1.

**Managing authentication inside the adapter.** Would let a consumer write
`config-ssm.New(region, profile)`. Rejected: it couples the adapter to one cloud's auth model,
duplicates configuration the consumer already does, and makes the suite need real credentials. The
client is injected (D3).

**A single generic "any key-value store" adapter.** One `Backend` over an interface with `Get`,
`Set`, `Watch`, satisfied by a Consul, etcd or Redis client. Rejected: the systems' semantics —
consistency, atomicity, watch, secrecy, versioning — differ too much for a lowest-common-denominator
to serve any of them well, and the genuinely generic case is already covered by the custom-backend
guide, which is where a consumer with an unlisted store should start.

**Skipping the per-adapter specs and working from this umbrella alone.** Rejected as the central
point of the spec: a shared document that tried to specify Consul's blocking queries, SSM's
throttling, Vault's leases and a ConfigMap's informer in one place would be wrong about all of
them. See D2.

## Public API

The backend seam — `Backend`, `WritableBackend`, `WatchableBackend`, `Capabilities`, `Layer`,
`Source`, `Pending`, `Change`, `NewWatcher` — already exists and is sufficient for an adapter,
verified by the custom-backend guide building a full remote backend against only the public API.
Two core additions were confirmed in review:

- **`Sensitive` enforcement (D5, OQ2 resolved: enforce).** A write-path change refusing a write
  that would land in a non-Sensitive layer while a Sensitive layer defines the key, with a new
  named error sentinel. This is a behaviour change to routing/`Plan`, and a per-adapter spec is
  not needed for it — it is core, specified by D5 and implemented as a precursor to the first
  secrets backend.
- **`config/backendconformance` (D11, OQ3 resolved: build).** One new package, stdlib `testing`
  only, mirroring `config/conformance`.

## Testing strategy

Per adapter: a fake-client unit suite (D3, D10); env-gated integration tests against the real
service (D10); an allowlist `depfootprint` test (D9); and, if built, a run of the shared
`backendconformance` suite (D11). The umbrella itself is verified by the adapters that cite it
passing that shared contract.

## Migration & compatibility

Additive for consumers: a consumer adds a module and a `WithBackend` call, exactly as for a format
adapter. Two core additions land (Public API): the `Sensitive` enforcement (D5) is a write-path
behaviour change — it can only *newly refuse* a write that would have leaked a secret, so no
existing correct usage breaks — and `config/backendconformance` (D11) is a new test-only package.

## Open questions

All resolved in review, **2026-07-21**.

1. ~~**Scope and order.**~~ **Resolved: the proposed list stands** — Phase A (Consul, then AWS
   SSM / Azure App Configuration / GCP Parameter Manager), Phase B (the four secrets managers),
   Phase C (etcd, Kubernetes). Ten adapters; nothing trimmed or added.
2. ~~**`Sensitive` enforcement.**~~ **Resolved: enforce in the core** (D5). Because secrets
   backends are read-only (D7), a write to a secret-provided key routes *down* to the next
   writable layer — a plain file — so without a guard it would leak the secret there. The core
   refuses such a write. See D5, Public API.
3. ~~**Backend conformance suite.**~~ **Resolved: build `config/backendconformance` now** (D11).
4. ~~**Secrets writing.**~~ **Resolved: read-only by default** (D7); write needs a per-adapter
   spec that justifies it and specifies the guard.
5. ~~**Naming.**~~ **Resolved: cloud-qualified** — `config-aws-ssm`, `config-azure-appconfig`,
   `config-gcp-parameter`, and bare for vendor-neutral systems (`config-consul`, `config-vault`,
   `config-etcd`, `config-k8s`). See D1.
6. ~~**Where the per-adapter specs live.**~~ **Resolved: centrally, in the config repo** alongside
   this umbrella; the adapter repo carries only its README. See D2.
7. ~~**Feature-flag systems.**~~ **Resolved: out of scope.** Feature flags are a distinct concern
   (flags, targeting, rollout), not general configuration retrieval. If pursued, they get their
   own umbrella spec — not this one.

## Implementation phases

**Phase 0 — this umbrella spec.** Approved 2026-07-21; open questions resolved above.

**Phase 1 — the core precursors, in the config repo.** Two changes the adapters depend on, each
its own MR: the **`config/backendconformance`** suite (D11), and the **`Sensitive` write-path
enforcement** with its error sentinel (D5). Both are additive; the conformance suite is proven
against the custom-backend guide's `remoteBackend` (a known-good writable backend), the way the
codec suite was proven against the trivial in-repo codec.

Every adapter phase below is gated on D2: **the named adapter's own approved spec — written here,
centrally — precedes its implementation.** The grouping is a suggested order, not a licence to
skip a spec.

**Phase A — the reference and the parameter stores.** `config-consul` first, because the how-to
already models it, so it validates this umbrella the way `config-json` validated the codec seam.
Then `config-aws-ssm`, `config-azure-appconfig`, `config-gcp-parameter` — the symmetric
parameter-store trio.

**Phase B — the secrets managers.** `config-vault`, `config-aws-secrets`, `config-azure-keyvault`,
`config-gcp-secret`. The `Sensitive` family (D5), read-only by default (D7).

**Phase C — cloud-native key-value.** `config-etcd` and `config-k8s`. Native watch (D6), the
second typically read-only. *(`config-k8s` was subsequently rejected — see R4.)*

**Phase D — revisit.** Write support for any read-only secrets backend a consumer needs, and
anything the earlier adapters showed this umbrella got wrong — corrected here by dated revision,
never silently. Feature-flag systems are out of scope (OQ7) and, if ever pursued, get their own
umbrella.

## Revisions

### R1 (2026-07-22) — structured values decode through an injected `config.Codec` (amends D8)

D8 said keys are paths and a prefix maps flat path segments into the nested tree — implicitly
treating every value as a scalar. The first adapter, [config-consul](2026-07-22-config-consul.md)
(D3), surfaced the other common shape of a byte-valued store: a single key holding a whole JSON or
YAML document, not a scalar. Rather than let each adapter invent its own value parser, the family
convention is set here:

**A backend over a byte-valued store may accept an injected `config.Codec` that decodes structured
values — a value decoding to a mapping becomes a subtree, anything else stays a scalar string —
defaulting to scalar strings when no codec is given.**

This reuses the existing codec seam (no new core surface — the same reason D15 was withdrawn for
the flat file adapters, non-YAML R3), keeps the format choice with the consumer so the adapter
takes no codec dependency of its own, and applies uniformly to etcd, the parameter stores and any
other byte-valued backend. Each adapter's own spec states whether it offers the option and which
formats it is tested against; config-consul D3 is the worked example.

### R2 (2026-07-22) — `backendconformance` expects a sensitive read-only backend to refuse the routed-beneath write (refines D11 against D5)

D11 built a shared conformance suite; its read-only check asserts that a write to a key the
backend defines routes down to the writable layer beneath and is reported shadowed. Building the
first sensitive-capable adapter, [config-aws-ssm](2026-07-22-config-aws-ssm.md), surfaced that this
is **wrong for a sensitive backend**: the D5 leak guard *correctly* refuses that write with
`ErrSensitiveLeak`, because routing a key a sensitive layer owns into a plain file is exactly the
leak D5 exists to stop. So "routes beneath" and "sensitive" are mutually exclusive by design.

This is not a niche case — **every Phase-B secrets manager is a *statically* sensitive read-only
backend** (Vault, AWS Secrets Manager, Azure Key Vault, GCP Secret Manager), so all of them would
fail the suite as first written. The suite is therefore refined: for a backend whose
`Capabilities().Sensitive` is true, the read-only check asserts the routed-beneath write is
**refused with `ErrSensitiveLeak`**, not routed; for a non-sensitive read-only backend it asserts
routing-beneath as before. This closes the D5/D11 interaction once, in the shared suite, so no
Phase-B secrets backend has to rediscover it. (config-aws-ssm dodged it another way — dynamic
sensitivity, its R1 — but the statically-sensitive backends cannot, which is why this belongs in
the suite.)

### R3 (2026-07-27) — the "no change feed" category is wrong; there is a third kind of feed (corrects D6)

D6 splits the systems in two: those with a **native change signal** (Consul blocking queries, etcd
watch, Kubernetes informers) and those with **no change feed**, naming *"the cloud parameter and
secrets stores (SSM, App Configuration, Parameter Manager, Secrets Manager, Key Vault, Secret
Manager)"* and concluding these *"have nothing to subscribe to"*.

**That is factually wrong for five of the six systems it names.** It was caught while specifying
[config-gcp-secret](2026-07-27-config-gcp-secret.md) (its D11), which found that GCP Secret Manager
carries a Pub/Sub notification mechanism on the secret resource itself. Rather than correct only the
system that happened to be caught, the claim was checked against the rest of the list:

| System | Feed | Established |
|---|---|---|
| GCP Secret Manager | `Secret.Topics` — up to 10 Pub/Sub topics, published to on control-plane operations on the secret *or its versions* | **measured** on `secretmanagerpb.Secret` v1.21.0 |
| AWS Secrets Manager | native `Secret Label Updated` event on EventBridge, fired when `AWSCURRENT` moves — **enabled by default** for all secrets, on the default event bus | **documented** (AWS) |
| Azure Key Vault | Event Grid, `Microsoft.KeyVault.*` — new version available, near expiry, expired; *"you must first subscribe to the event on your key vault"* | **documented** (Microsoft) |
| AWS SSM Parameter Store | EventBridge `aws.ssm` / `Parameter Store Change`, operations `Create`/`Update`/`Delete`/`LabelParameterVersion`, *"emitted on a best effort basis"* | **documented** (AWS) |
| Azure App Configuration | Event Grid, `Microsoft.AppConfiguration.KeyValueModified` / `KeyValueDeleted`, carrying the key, label and etag | **documented** (Microsoft) |
| GCP Parameter Manager | **not re-checked.** [config-gcp-parameter](2026-07-22-config-gcp-parameter.md) D7 asserts it exposes no first-class change topic; that claim stands here **unverified** rather than being restated as fact | — |

So the binary split is the error, not merely the GCP entry. **D6 is corrected to three categories:**

1. **Native change signal** — the change arrives through the same client and connection the reads
   use, with no other infrastructure. Consul blocking queries, etcd watch, Kubernetes informers.
   These implement `WatchableBackend` with `NativeWatch: true`. Unchanged.
2. **Out-of-band feed** — the system genuinely publishes changes, but to a *separate messaging
   service* the consumer must provision and subscribe to: an EventBridge rule and target, an Event
   Grid subscription, a Pub/Sub topic and subscription with its own IAM. The five systems above.
3. **No change feed** — nothing to subscribe to by any route. GCP Parameter Manager is the only
   candidate on the current list, and it is unverified.

**The default for category 2 remains polling, and that is a decision rather than an oversight.** An
out-of-band feed is not a drop-in substitute for a native watch, for three reasons that hold across
all five systems. The subscription is **infrastructure the consumer owns** — a topic, a rule, a
subscription, IAM to read it — so a backend that required one would be dictating how the consumer's
account is provisioned, which umbrella D3 places entirely outside the adapter. It brings a **second
client library** with its own delivery semantics — acknowledgement, redelivery, ordering,
dead-lettering — none of which the `Backend` contract has a place for. And the feed is a
notification, not a value: every event still costs a read to act on, so it changes *latency*, not
work.

What changes is what the specs may claim. **No adapter spec may say its system "has no change
feed" without checking** — for five of six, that sentence would be false. A spec choosing to poll
should say the system has an out-of-band feed and that polling was chosen anyway, with the reasons
above. `config-gcp-secret` D11 is the worked example, and it also records the measurement that
weakens the usual dependency objection: adding `cloud.google.com/go/pubsub/v2` beside
`cloud.google.com/go/secretmanager` costs **four** modules, not a second SDK, because the two share
the Google client stack. The argument for polling is ownership and contract, not footprint.

An opt-in event-driven watch remains a legitimate follow-on for any category-2 system, per adapter,
each with its own spec (D2), and each most likely in its **own module** so consumers who poll do not
carry the messaging client. None is in scope for the adapters specified so far.

### R4 (2026-07-29) — `config-k8s` is rejected; Phase C is etcd alone (amends the phasing)

Phase C listed `config-etcd` and `config-k8s` together, inheriting "native watch" as the
justification for both. That was never argued, and it does not survive being argued.

**A ConfigMap already reaches a pod through two layers this module has.** Mounted as a volume, a
ConfigMap key holding a document *is a file* — `WithFiles` reads it, and the kubelet rewrites the
mount in place when the ConfigMap changes, so the existing file watch gives hot reload with no new
code. Consumed with `envFrom`, it is environment variables — `WithEnv` reads it. An adapter that
talks to the API server would be a third route to data the process can already see.

**The cost is the heaviest in the toolkit.** `k8s.io/client-go` links **38 modules** (measured
2026-07-29), against `config-gcp-secret`'s 39 — the adapter whose weight the family already
singles out as a warning. And unlike a mount, the API path needs RBAC `get`/`watch` on
configmaps and a service account, so it is strictly more privilege for strictly less reach: it
only works in-cluster.

By contrast `go.etcd.io/etcd/client/v3` links **16** — the same as Consul. The two systems were
grouped by "cloud-native" framing, not by cost or by need.

Three cases the mount does not cover were weighed and rejected as insufficient:

1. **A `subPath` mount never updates** — a genuine Kubernetes trap, but a documentation problem,
   not one an adapter fixes.
2. **A ConfigMap you have not mounted** — another namespace, or chosen at runtime. Real, narrow,
   and anyone doing it already carries `client-go`.
3. **A ConfigMap of many scalar keys mounts as many single-value files**, which no adapter here
   reads. This one is real — and it is not a Kubernetes problem. The identical shape is how
   Docker and Podman secrets (`/run/secrets/*`) and systemd `LoadCredential`
   (`$CREDENTIALS_DIRECTORY/*`) present. It wants a small zero-dependency backend over
   `config.FS`, not a Kubernetes API client. Specified as
   [config-filekv](2026-07-29-config-filekv.md).

So Phase C is **`config-etcd` alone**. `config-k8s` is not deferred, it is **rejected** — recorded
here rather than dropped from the list, so the question is not re-litigated from scratch.

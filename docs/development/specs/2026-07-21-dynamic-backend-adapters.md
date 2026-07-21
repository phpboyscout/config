---
title: Dynamic backend adapters — remote configuration systems as sibling modules
date: 2026-07-21
author: matt.cockayne
status: draft
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

### D2 — Every adapter has its own approved spec before implementation

This is the load-bearing rule. This umbrella spec settles what is common; **no `config-<system>`
adapter is implemented until it has its own `status: approved` spec** under
`docs/development/specs/` in its own repository (or here, if we keep specs central — see OQ6),
citing this one and settling the decisions this one cannot:

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

### D5 — `Sensitive` becomes load-bearing here

`Capabilities.Sensitive` was forward-declared for the file work and consumed by nothing (non-YAML
format adapters D8: "it becomes load-bearing the moment anything dumps or exports resolved
configuration"). The secrets backends are that moment. A backend over Vault, AWS Secrets Manager,
Azure Key Vault or GCP Secret Manager declares `Sensitive: true`, and the rule it guards — **a
value sourced from a Sensitive backend must never be written into a layer that is not** — moves
from a comment to a checked invariant. Whether the *core* enforces that now, or it stays a declared
property each adapter honours, is OQ2.

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
right, comparing the version recorded at Load). So this spec proposes a
`config/backendconformance` suite, stdlib `testing` only (as the codec one is, D17 there), that an
adapter runs against its backend to assert: it participates as an ordinary layer with per-key
merge and provenance; an absent or empty source is tolerated; for a `WritableBackend`, a write
round-trips and a change landing between Load and Commit is refused with `ErrConflict`; for a
`WatchableBackend`, a foreign change reaches observers. Whether to build this now or let each
adapter test the contract itself is OQ3 — but the version-at-Load trap is a strong argument for
making it shared, so the mistake is made impossible once rather than risked per adapter.

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

**Net-new exported names in the core: none expected.** The backend seam — `Backend`,
`WritableBackend`, `WatchableBackend`, `Capabilities`, `Layer`, `Source`, `Pending`, `Change`,
`NewWatcher` — already exists and is sufficient, verified by the custom-backend guide building a
full remote backend against only the public API. Two possible additions, each its own decision:

- If D5's `Sensitive` invariant is enforced in the core (OQ2), that is a behaviour change to the
  write path, specified there.
- If D11's `config/backendconformance` suite is built (OQ3), that is one new package, stdlib
  `testing` only, mirroring `config/conformance`.

## Testing strategy

Per adapter: a fake-client unit suite (D3, D10); env-gated integration tests against the real
service (D10); an allowlist `depfootprint` test (D9); and, if built, a run of the shared
`backendconformance` suite (D11). The umbrella itself is verified by the adapters that cite it
passing that shared contract.

## Migration & compatibility

Purely additive. No consumer change; no core change unless OQ2 or OQ3 resolves to one. A consumer
adds a module and a `WithBackend` call, exactly as for a format adapter.

## Open questions

1. **Scope and order.** The proposed set is Consul (the reference the how-to already models),
   then the parameter-store trio (AWS SSM, Azure App Configuration, GCP Parameter Manager), then
   the secrets managers (Vault, AWS Secrets Manager, Azure Key Vault, GCP Secret Manager), then
   the cloud-native KV pair (etcd, Kubernetes ConfigMaps/Secrets). Is that the right list, and is
   anything in it not worth doing — or missing?
2. **`Sensitive` enforcement.** Does the core enforce "a Sensitive value never lands in a
   non-Sensitive layer" now (a write-path change), or does it stay a declared property each
   secrets adapter honours until something exports resolved configuration?
3. **Backend conformance suite.** Build `config/backendconformance` now (D11), or let each adapter
   assert the contract itself?
4. **Secrets writing.** Confirm the default: secrets backends ship read-only unless their spec
   justifies write (D7).
5. **Naming.** `config-ssm` vs `config-aws-ssm` vs `config-aws-parameters`; `config-k8s` vs
   `config-kubernetes`. Settle one convention for the whole family before the first adapter.
6. **Where the per-adapter specs live.** In each adapter's own repo, or centrally here alongside
   this umbrella? Central keeps them discoverable and cross-linkable; per-repo keeps a module
   self-contained.
7. **Feature-flag systems (LaunchDarkly, Unleash, ConfigCat, …).** In scope as backend adapters,
   or explicitly out — a distinct concern (flags, not general configuration) deserving its own
   umbrella if pursued at all?

## Implementation phases

**Phase 0 — this umbrella spec, approved.** Nothing is built against it until its open questions
are resolved and it is `approved`.

Every phase below is gated on D2: **the named adapter's own spec is written and approved before it
is implemented.** The grouping is a suggested order, not a licence to skip a spec.

**Phase A — the reference and the parameter stores.** `config-consul` first, because the how-to
already models it, so it validates this umbrella the way `config-json` validated the codec seam.
Then AWS SSM, Azure App Configuration, GCP Parameter Manager — the symmetric parameter-store trio.

**Phase B — the secrets managers.** Vault, AWS Secrets Manager, Azure Key Vault, GCP Secret
Manager. The `Sensitive` family (D5), read-only by default (D7).

**Phase C — cloud-native key-value.** etcd and Kubernetes ConfigMaps/Secrets. Native watch (D6),
the second of them typically read-only.

**Phase D — revisit.** Feature-flag systems if OQ7 says so, write support for any read-only
secrets backend a consumer needs, and anything the earlier adapters showed this umbrella got wrong
— corrected here by dated revision, never silently.

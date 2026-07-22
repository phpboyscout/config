---
title: config-gcp-parameter — the GCP Parameter Manager backend adapter
date: 2026-07-22
author: matt.cockayne
status: draft
---

# config-gcp-parameter

The third of the parameter-store trio the [dynamic backend adapters umbrella
spec](2026-07-21-dynamic-backend-adapters.md) names for Phase A (after
`config-consul`, alongside `config-aws-ssm` and `config-azure-appconfig`). Per
the umbrella's **D2**, no adapter is built without its own approved spec; this is
that spec for GCP Parameter Manager. It cites the umbrella and settles the nine
things the umbrella leaves to each adapter — but its **first and most important
finding is that the service is real, GA, and the right one**, because for GCP the
configuration story is the least settled of the three clouds and that question
had to be answered before anything else.

The reference for shape is [config-consul](2026-07-22-config-consul.md): a narrow
injected client interface with a `Wrap(*sdkClient)` adapter, `New`/`FromClient`
constructors, the conflict fingerprint captured at Load, values-as-bytes with the
family's injected-`config.Codec` convention (umbrella **R1**), a fake-client unit
suite and env-gated integration. Where Parameter Manager's semantics diverge from
Consul's — and they diverge more than the other two parameter stores do — this
spec says why.

## Problem

A tool that configures from GCP cannot use this module today: the core and its
file adapters read files, and a GCP-native workload keeps its configuration in a
managed service, not a file on disk. The umbrella settled that we ship a GCP
parameter-store adapter (Phase A); it could not settle **which** GCP service, and
that is not a trivial question. GCP's configuration-service history is littered:

- **Runtime Configurator** (`google.cloud.runtimeconfig`) was GCP's old
  key-value configuration service. It is **deprecated / shut down** and must not
  be the target.
- **Secret Manager** is GCP's secrets service. It is the `Sensitive: true` member
  of this family and belongs to **Phase B** (`config-gcp-secret`), not here.
  Configuration is not secrets (umbrella D5/D7).
- **Parameter Manager** is the new, purpose-built service for *workload
  parameters* — application configuration as versioned key-value data. It is an
  extension of Secret Manager (same product area, `parametermanager.googleapis.com`)
  but a distinct service with its own resources and SDK.

So before deciding how a parameter maps to a config tree, this spec had to verify
that Parameter Manager is Generally Available, is genuinely the configuration
service (not the secrets one), and has a supported Go SDK. It is, it is, and it
does (D2). What remains genuinely unsettled — and is left to the human as open
questions rather than guessed — is the **data-model shape** the service's design
pushes toward, how the declared **format field** interacts with the family's
value-codec convention, how the **conflict trap** is satisfied in a store that has
*no* compare-and-swap primitive, and whether **Secret Manager references** reopen
the `Sensitive` question. These are decision-bearing and are the point of the
per-adapter spec (umbrella D2).

## Decisions

### D1 — Module `config-gcp-parameter`, package `configgcpparameter`

Cloud-qualified, per umbrella **D1**: a cloud service carries its cloud, because
the same purpose (a parameter store) exists under AWS, Azure and GCP and the
service name is what people search for. The module is
`gitlab.com/phpboyscout/go/config-gcp-parameter`, package `configgcpparameter`,
matching the `config-<x>` / `configx` convention. The repo carries a README only;
this spec lives centrally here (umbrella D2). It requires `config` **v0.6.0+** —
the release that shipped `backendconformance` and the `Sensitive` enforcement the
family depends on.

### D2 — Service viability: Parameter Manager is GA, is the right service, and has a Go SDK

This is the headline finding and the reason the spec can proceed.

- **GA.** Parameter Manager is Generally Available (announced in the Secret
  Manager release notes; it is an extension of Secret Manager). The Go SDK is
  published at **`cloud.google.com/go/parametermanager` v1.0.0** — a `v1` major,
  which in the Google Cloud Go client convention signals a stable, GA service, not
  a preview. Client package `cloud.google.com/go/parametermanager/apiv1`, protobuf
  types `cloud.google.com/go/parametermanager/apiv1/parametermanagerpb`.
- **The right service.** It is purpose-built for *workload parameters*
  (application configuration), explicitly **not** the deprecated Runtime
  Configurator and **not** Secret Manager. Secret material stays in Secret Manager
  and reaches Parameter Manager only by reference (D8); this adapter is the
  configuration one, `Sensitive: false` by default (D5).
- **The SDK exists and is idiomatic.** `parametermanager.NewClient(ctx, opts...)`
  returns a `*Client` with `GetParameter`, `GetParameterVersion`,
  `ListParameters`, `ListParameterVersions`, `CreateParameter`,
  `CreateParameterVersion`, `RenderParameterVersion`, and the delete/update
  variants — verified by `go doc` against v1.0.0.

Had any of these three failed — not GA, wrong service, no Go SDK — the correct
outcome would have been to **defer the adapter** and record why, exactly as the
umbrella's D2 intends. None failed, so the adapter is viable; the remaining
uncertainty is about *semantics*, not *existence*, and is carried in the open
questions.

### D3 — Data model: a parameter is a versioned document, not a flat KV prefix

This is where Parameter Manager diverges hardest from Consul, and the divergence
is forced by the service's resource design, so it is stated plainly rather than
bent to match Consul.

Parameter Manager resources are hierarchical named objects, not a flat key
namespace:

```
projects/{project}/locations/{location}/parameters/{parameter}/versions/{version}
```

A **parameter** is a container with metadata (`Format`, `Labels`, `KmsKey`, IAM
policy). A **parameter version** is immutable and holds the actual value as a
`Payload.Data []byte` (up to 1 MiB). Every write creates a **new version** (D9);
versions can be individually `Disabled`.

Consul's model — a flat prefix of many small scalar keys nesting into a tree
(umbrella D8) — does **not** fit this. A Parameter Manager parameter is a
heavyweight resource (its own IAM, format, versions, CMEK); it is designed to hold
a **whole configuration document**, typically JSON or YAML, not a single scalar
leaf. The natural, idiomatic unit is therefore:

> **The adapter targets one named parameter. Its current version's payload is the
> layer's source document, decoded into the layer's nested tree.**

Worked example — parameter `app-config` in `global`, holding a version whose
payload is:

```yaml
server:
  host: localhost
  port: 8080
features:
  beta: true
```

becomes exactly that layer. Provenance names the parameter version resource
(`gcpparameter:projects/p/locations/global/parameters/app-config/versions/3`), so
a value is traceable to the version it came from.

**"Current version" resolution.** Unlike Secret Manager, a *parameter* version has
**no `latest` alias**. The adapter resolves the current value by
`ListParameterVersions` and choosing the most-recently-created **enabled**
version (skipping `Disabled` ones), recording that version's resource name as the
conflict fingerprint (D9). A consumer that wants a pinned version names it
explicitly.

The multi-parameter alternative — treat many parameters under a location as flat
keys with a name prefix, mirroring Consul — is possible but fights the resource
design and costs one `GetParameterVersion` RPC per parameter. It is surfaced as an
open question (OQ7), not adopted here, because the single-document model is the one
the service is built for.

### D4 — Decoding the payload: the declared format, or the injected codec (proposed; mechanism is OQ2)

Consul stores opaque bytes and cannot tell you a value's format, so `config-consul`
requires the consumer to inject a `config.Codec` (umbrella R1, config-consul D3).
Parameter Manager is **different in a way that matters**: a parameter carries an
immutable **`Format`** field, set at creation, with verified enum values:

```
PARAMETER_FORMAT_UNSPECIFIED = 0
UNFORMATTED                  = 1   // plain text / custom
YAML                         = 2
JSON                         = 3
```

So the store *declares* whether its payload is JSON, YAML, or unstructured. This
reopens umbrella R1's assumption that the consumer must name the format:

- **A `YAML`/`JSON` parameter** should decode into a subtree; an `UNFORMATTED`
  one is a scalar string leaf. The service already knows which.
- But the adapter still must not take a codec dependency of its own (umbrella R1,
  D1): it does not import `config-json` or embed a YAML parser gratuitously.

The **proposed** decision, pending OQ2: the adapter reads `Parameter.Format`, and
for a structured parameter decodes the payload through **an injected
`config.Codec` the consumer supplies** — the format field selects *whether* to
decode and validates *which* codec is expected, but the decoding machinery is
still the injected `config.Codec`, keeping the family's no-own-codec-dependency
rule (R1). An `UNFORMATTED` parameter, or a structured parameter with no matching
codec injected, is a scalar string leaf (the same decode-or-string fallback
config-consul uses). Whether the format field should instead drive **automatic**
decoding (the adapter selecting a bundled codec by format, taking the dependency)
is the sharp call left to OQ2 — it is a genuine departure from R1 and the human's
to make.

### D5 — Capability: read-only and not sensitive at v0.1.0

`config-gcp-parameter` implements `Backend` (read) and, per D7, a polling
`WatchableBackend`. It does **not** implement `WritableBackend` at v0.1.0 (umbrella
D4 — capability by the type system, never a flag; umbrella D7 — read-only is a
first-class outcome). Its `Capabilities`:

| Field | Value | Why |
|---|---|---|
| `PreservesComments` | `false` | a parameter payload is data, not a formatted document the adapter round-trips |
| `AtomicMultiKey` | `true` | a single parameter version is written whole and indivisibly (when write lands — D9) |
| `NativeWatch` | `false` | Parameter Manager has no change feed; watch is polling (D7) |
| `Sensitive` | `false` | Parameter Manager is configuration; secrets live in Secret Manager (`config-gcp-secret`, Phase B) |

`Sensitive` is `false` **only because the read path returns the raw, unrendered
payload** (D8). A parameter payload *can* embed Secret Manager references, and
rendering them resolves real secret values — which would make the resolved layer
sensitive. The adapter does not render by default, so no secret ever enters a
layer, and `Sensitive` stays honestly `false`. If rendering is ever offered
(OQ5), that variant must declare `Sensitive: true`.

Read-first is the right default here, unlike Consul: Consul had clean CAS and the
guide already modelled its write path, so shipping write was cheap and proven.
Parameter Manager has **no compare-and-swap primitive** (D9), so its write story
carries an unresolved conflict-semantics question — exactly the kind of thing that
should not be rushed into v0.1.0. Reading a configuration document is the whole of
the value for a config-*consuming* tool; write is deferred behind its own decision
(OQ3, phase 3).

### D6 — The constructor injects a narrow client; auth, project, location and endpoint stay with the consumer

The adapter defines its own narrow interface and never takes credentials, project
selection, regional-endpoint configuration or scopes (umbrella D3). The consumer
builds a configured `*parametermanager.Client` — where every auth decision and the
choice of **regional vs global endpoint** lives (a `ClientOption`, D8) — and hands
it in along with the resource coordinates:

```go
// PM is the slice of Parameter Manager this adapter uses. A fake satisfying it
// drives the whole unit suite (D11); the real client is adapted by Wrap.
type PM interface {
	// Get returns the parameter's declared format and the current (latest
	// enabled) version: its resource name and raw payload bytes. A parameter or
	// version that does not exist returns fs.ErrNotExist.
	Get(ctx context.Context, parameter string) (Parameter, error)

	// Create writes payload as a new immutable version of the parameter and
	// returns the new version's resource name. versionID is the caller-chosen id
	// for the new version. (Write path only — D9.)
	Create(ctx context.Context, parameter, versionID string, payload []byte) (versionName string, err error)
}

// Parameter is a resolved read: the format the parameter declares and its
// current version.
type Parameter struct {
	Format      Format // Unformatted, YAML, or JSON (D4)
	VersionName string // full version resource name — the conflict fingerprint (D9)
	Payload     []byte
}

// Format mirrors the service's immutable ParameterFormat, so the adapter never
// exposes the raw protobuf enum.
type Format int

const (
	Unformatted Format = iota
	YAML
	JSON
)
```

`Wrap(client *parametermanager.Client, project, location string) PM` adapts the
real SDK: `Get` → `GetParameter` + `ListParameterVersions` (pick latest enabled) +
`GetParameterVersion`; `Create` → `CreateParameterVersion`. `project` and
`location` compose the resource path
(`projects/{project}/locations/{location}/parameters/{parameter}`); `location` is
`global` by default and any region the consumer's client endpoint targets
otherwise (D8). The common path is `FromClient(client, project, location,
parameter, opts...)` = `New(Wrap(client, project, location), parameter,
opts...)`, so a consumer writes `configgcpparameter.FromClient(client, "my-proj",
"global", "app-config")` and never sees the narrow interface unless testing.

### D7 — Watch is a poll, and it says so

Parameter Manager has no native change feed (no blocking query, no informer). Per
umbrella **D6**, a store with no change signal either polls in a `Watch` it owns,
or omits `Watch` and lets the Store poll via reload. This adapter **implements
`Watch` by polling** on the `interval` from `WithPollInterval`: each tick it
re-resolves the current version name (a `ListParameterVersions`, cheap — no
payload fetch) and calls `onChange` when the current version name differs from the
one last seen. `NativeWatch: false`, and the latency is the poll interval — stated
plainly, not disguised as push. The Store's hybrid watch coalesces and settles the
resulting reload for free (umbrella D6). A Pub/Sub notification path is **out of
scope**: Parameter Manager exposes no first-class change topic, and wiring one
would move change-detection outside the Store, which owns it.

### D8 — Error & consistency semantics

- **Absent source.** A parameter, or a parameter with no enabled version, that
  does not exist maps to `fs.ErrNotExist`, so the Store decides whether a missing
  source is fatal — exactly as for a file or a Consul prefix (umbrella D2.6).
- **gRPC errors.** The SDK returns `google.golang.org/grpc/status` errors. The
  adapter maps `codes.NotFound` → `fs.ErrNotExist`, and returns other codes
  (`PermissionDenied`, `Unavailable`, `ResourceExhausted`) wrapped with context so
  a caller sees which parameter failed and why. Transient-failure retry/backoff is
  the SDK's (`gax` call options) — the adapter does not re-implement it.
- **Consistency.** Parameter Manager reads are strongly consistent for a named
  version, but "which version is latest" is a list that can race a concurrent
  writer; the adapter records the resolved version name at Load and treats a
  change to it as a foreign change (D7) or a conflict (D9).
- **Regional vs global.** The `location` segment of the resource path selects
  global (`global`, the default), multi-regional, or a specific region. A regional
  parameter additionally requires the consumer's client to target the matching
  **regional endpoint** (a `ClientOption`). The adapter takes the `location`
  string (D6) and trusts the injected client's endpoint to match it — it does not
  configure endpoints (umbrella D3). Whether the adapter should validate that the
  two agree, or accept the location as purely the consumer's responsibility, is
  OQ4.

### D9 — Write semantics and the conflict trap without a CAS primitive (deferred to phase 3; OQ3)

This is the sharpest semantic problem and the reason write is not in v0.1.0 (D5).

Parameter Manager versions are **immutable**, and there is **no compare-and-swap,
no `etag`, and no generation number** on either a `Parameter` or a
`ParameterVersion` — verified against v1.0.0. The `RequestId` field on
`CreateParameterVersion` is **idempotency for retries** (dedupe a re-sent
request), **not** a conflict guard. So Consul's clean `ModifyIndex` CAS has no
equivalent, and the family's version-at-Load trap (umbrella D11) has no native
mechanism to lean on. The options, laid out for the human (OQ3), not chosen:

1. **Version-id-as-guard (create-if-absent).** Derive the new version's id
   deterministically from the version seen at Load (e.g. a monotonic successor).
   `CreateParameterVersion` fails with `AlreadyExists` if a concurrent writer
   already created that id — a genuine create-if-absent guard, the closest thing
   to CAS the service offers. Cost: it dictates the version-id scheme, and version
   ids have format constraints.
2. **Read-before-write check.** At `Verify`/`Commit`, re-resolve the current
   version name and refuse with `config.ErrConflict` if it differs from the one
   recorded at Load, then create a new version. Simple, but leaves a
   TOCTOU window between the check and the create (no atomic guard closes it).
3. **Last-write-wins.** Always create a new version; never refuse. Honest for an
   append-only, immutable-version store, but silently loses a concurrent writer's
   change and **fails `backendconformance`'s conflict subtest** (umbrella D11),
   which requires a change landing between Load and Commit to be refused.

Because option 3 cannot pass the shared conformance suite and options 1–2 each
carry a real trade-off, **write is deferred** to a phase gated on the human
resolving OQ3. When it lands, a write replaces the whole parameter document as one
new version (`AtomicMultiKey: true`, D5) — the adapter does not do partial-key
edits, because the unit is the document (D3).

### D10 — Dependency footprint: the GCP client stack, the largest in the family

`config-gcp-parameter` depends on `cloud.google.com/go/parametermanager` and,
transitively, the full Google Cloud Go client stack — gRPC, the API transport,
genproto, protobuf, OAuth2/auth, and OpenTelemetry. This is **the largest graph of
any adapter so far** — substantially heavier than Consul's sixteen modules — and
it is the honest cost of a first-party cloud SDK (umbrella D9). The distinct
external module groups the client package pulls, verified by `go list -deps`
against v1.0.0:

```
cloud.google.com/go/parametermanager   cloud.google.com/go/auth
cloud.google.com/go/compute/metadata   cloud.google.com/go/iam
github.com/googleapis/gax-go/v2         github.com/googleapis/enterprise-certificate-proxy
github.com/google/s2a-go                github.com/cespare/xxhash/v2
github.com/felixge/httpsnoop            github.com/go-logr/logr
github.com/go-logr/stdr                 golang.org/x/crypto
golang.org/x/net                        golang.org/x/oauth2
golang.org/x/sync                       golang.org/x/sys
golang.org/x/text                       golang.org/x/time
google.golang.org/api                   google.golang.org/genproto
google.golang.org/grpc                  google.golang.org/protobuf
go.opentelemetry.io/otel                go.opentelemetry.io/contrib
go.opentelemetry.io/auto/sdk
```

A `depfootprint_test.go` allowlist asserts this set (plus the `config` graph), so
a dependency creeping in — or the SDK growing one — is a failing test, not a
surprise in a consumer's `go.sum`. Because the set is large and the SDK bumps its
own transitive graph often, the allowlist is expected to need periodic updates;
that is the visible, reviewed cost the umbrella's D9 intends, not a reason to hide
it. The exact pinned set is captured at implementation time against the SDK version
then current.

### D11 — Testing: a fake for units, and **real-service-only, env-gated** integration

Parameter Manager has **no emulator and no testcontainers module** — verified: the
Testcontainers GCloud module covers Bigtable, Datastore, Firestore, Spanner and
Pub/Sub, and Parameter Manager is not among them, nor does Google ship a standalone
Parameter Manager emulator. This is a material finding, because it means the
Consul model's DIND-plus-testcontainers job (config-consul D11/D12) **does not
apply**. The three layers become:

1. **A hand-written fake `PM`** — in-memory, format-aware, version-tracking —
   drives the unit suite: reads, format-driven decode (D4), provenance, the
   absent-parameter case, and watch (poll detecting a new version name). No cloud,
   no account, IDE-runnable, the primary suite.
2. **The shared `backendconformance` suite** (`config/backendconformance`, shipped
   in v0.6.0), run against a `configgcpparameter` backend over the fake — for the
   read+watch subset at v0.1.0, and (once OQ3 is resolved and write ships) the
   conflict subtest, which is the gate on whichever conflict option D9 adopts.
3. **Real-service integration**, env-gated on `INT_TEST_INTEGRATION`, hitting a
   **real GCP project** with a real `*parametermanager.Client` and
   application-default credentials. Because there is no container, these tests
   need a project, credentials and cleanup of created parameters; they are **not**
   run in ordinary CI and **not** on a DIND job — they run only where GCP
   credentials are present (a dedicated pipeline or a developer's machine). The
   env gate keeps them compiled and IDE-discoverable but out of the merge gate,
   the same convention as Consul's, but the CI wiring is deliberately different: no
   `enable_integration` DIND job, because there is nothing to containerise.

## Rejected alternatives

**Target Runtime Configurator or Secret Manager.** Runtime Configurator is
deprecated; Secret Manager is the secrets service (Phase B, `config-gcp-secret`,
`Sensitive: true`). Parameter Manager is the configuration service (D2). Picking
either would be wrong on service identity.

**Model the store as a flat prefix of many parameters (Consul-shaped).** Treat
every parameter under a location as a scalar key with a name prefix, mirroring
`config-consul`. Rejected as the default: a Parameter Manager parameter is a
heavyweight versioned resource built to hold a whole document, listing gives only
metadata so each leaf costs an extra `GetParameterVersion` RPC, and it throws away
the `Format` field that makes the single-document model clean. Kept alive as OQ7
because a consumer with genuinely many tiny parameters might want it, but it is not
the shape the service is designed for (D3).

**Require an injected codec, ignoring the format field (pure R1).** Do exactly
what Consul does — make the consumer name the format via `WithValueCodec`,
ignoring `Parameter.Format`. Rejected as under-using the service: the store
*declares* the format, so at minimum the adapter should use that declaration to
decide *whether* to decode. How far to take it — down to auto-selecting a bundled
codec — is OQ2, but blindly ignoring the field would be a worse adapter than the
service allows.

**Render Secret Manager references by default.** Call `RenderParameterVersion` so
`__REF__(//secretmanager.googleapis.com/...)` placeholders resolve to real secret
values on read. Rejected as the default: it pulls secret material into a
`Sensitive: false` layer, defeating the whole point of the `Sensitive` guard
(umbrella D5). Rendering, if offered at all, is opt-in and forces `Sensitive:
true` (OQ5).

**Ship write in v0.1.0 like Consul did.** Rejected: Consul had native CAS and a
proven write path; Parameter Manager has no CAS primitive at all (D9), so its
write story carries an unresolved conflict-semantics decision (OQ3) that must not
be rushed. Read-only is a first-class outcome (umbrella D7); write follows its own
decision in phase 3.

**Take `*parametermanager.Client` directly in the constructor.** Simpler to call,
but couples the adapter to the SDK type, makes the unit suite need a real or
heavily mocked GCP client, and invites the adapter to reach for project, endpoint
and credential configuration that is the consumer's. The narrow `PM` interface
with a `Wrap` adapter keeps units fake-driven and configuration consumer-owned
(umbrella D3), and `FromClient` keeps the common path a single call.

**Poll via a Pub/Sub subscription.** Parameter Manager exposes no first-class
change topic, and building change-detection outside the Store would break the
Store's ownership of I/O. Polling inside `Watch` (D7) keeps change-detection where
it belongs.

## Public API

The module's proposed exported surface (v0.1.0, read + poll-watch):

- `func New(pm PM, parameter string, opts ...Option) config.Backend` — the
  injection seam; `pm` is a fake in tests, a `Wrap`ped client in production.
- `func FromClient(client *parametermanager.Client, project, location, parameter string, opts ...Option) config.Backend`
  — the convenience path, `New(Wrap(client, project, location), parameter,
  opts...)`.
- `func Wrap(client *parametermanager.Client, project, location string) PM` —
  adapts the real Parameter Manager SDK client to the narrow interface.
- `func WithValueCodec(codec config.Codec) Option` — decode a structured
  parameter's payload through `codec`, selected by the parameter's declared
  `Format` (D4); omitted, structured payloads that have no codec fall back to a
  scalar string.
- `func WithPollInterval(d time.Duration) Option` — the watch poll interval (D7).
- `type PM interface { … }`, `type Parameter struct { … }`, `type Format int`
  (with `Unformatted`, `YAML`, `JSON`), `type Option func(…)` — the narrow client
  seam (D6) and the option type.

The returned backend satisfies `config.WatchableBackend` (poll); it does **not**
satisfy `config.WritableBackend` at v0.1.0 (D5). The Store discovers capability by
type assertion, so no capability flag is exported (umbrella D4). No change to the
`config` core is required — v0.6.0 already carries everything this adapter needs.
When write ships (phase 3, OQ3), it adds `Prepare`/`Verify`/`Commit` and the
`Create` half of `PM`; those are additive.

## Testing strategy

Per D11: a fake-`PM` unit suite (reads, format-driven decode, the
`UNFORMATTED`/no-codec fallback, provenance, absent-parameter → `fs.ErrNotExist`,
and poll-watch detecting a new version name); a `backendconformance.Run` against a
`configgcpparameter` backend over the fake, for the read+watch subset at v0.1.0;
real-service integration under `./test/integration/` env-gated on
`INT_TEST_INTEGRATION`, run only where GCP credentials exist (no DIND job — D11);
and a `depfootprint_test.go` allowlist (D10). What would falsely pass: a
poll-watch test whose fake never advances its current-version name — the watch
subtest must drive the fake to publish a new version and be watched to fail when it
does not; and, once write ships, a conflict test whose fake has no
create-if-absent semantics would pass vacuously under the last-write-wins option
(D9.3), so the conformance conflict subtest must be run against whichever guard
OQ3 selects and watched to fail without it.

## Migration & compatibility

Purely additive for consumers: add the module and a
`WithBackend(configgcpparameter.FromClient(client, project, location, parameter))`
call, exactly as for a file adapter. No `config` core change, no breaking change.
The module ships at v0.1.0 as **read + poll-watch**; a later minor promotes it to
read+write once OQ3 is resolved — a read→write promotion to communicate in the
README and landing card, the same shape the read-first file adapters took.

## Open questions

These are decision-bearing and are the human's to resolve **before** this spec
moves to `approved`. They are deliberately not answered here (umbrella D2).

1. **Service viability — confirm the verdict.** D2 finds Parameter Manager GA,
   correct, and Go-supported (SDK v1.0.0). Confirm this is acceptable as the GCP
   Phase A target, and that Secret Manager is left to Phase B. *(If, against the
   finding, the human judges the service too new to depend on, the outcome is to
   defer — D2 is written to make that a clean choice.)*

2. **Format field vs value-codec (the sharpest design call).** The parameter
   declares its `Format` (JSON/YAML/UNFORMATTED). Two models: **(a)** the D4
   proposal — the format field selects *whether* to decode, but decoding still
   goes through a consumer-injected `config.Codec`, preserving umbrella R1's
   no-own-codec-dependency rule; or **(b)** the adapter *auto-decodes* using the
   declared format with a **bundled** JSON/YAML decoder, taking that dependency so
   the consumer supplies nothing. (b) is more convenient and uses the service's
   own signal fully, but is a real departure from R1 and gives the adapter a codec
   dependency. Which?

3. **Conflict semantics without CAS (gates write).** Given immutable versions and
   no compare-and-swap (D9): adopt **(1)** version-id-as-guard (create-if-absent
   via `AlreadyExists`), **(2)** read-before-write check with a TOCTOU window, or
   **(3)** last-write-wins (which cannot pass `backendconformance`)? This gates
   whether — and how — write ships in phase 3.

4. **Global vs regional, and endpoint validation.** The adapter takes `project`
   and `location`; a regional parameter also needs the consumer's client to target
   the matching regional endpoint (D8). Should the adapter validate that the
   injected client's endpoint agrees with `location`, or accept `location` as
   purely the consumer's responsibility (consistent with umbrella D3's
   "adapter touches no endpoint config")?

5. **Secret Manager references and `Sensitive` (D5).** A payload may embed
   `__REF__(//secretmanager.googleapis.com/...)` references that
   `RenderParameterVersion` resolves to real secret values. v0.1.0 reads the raw,
   unrendered payload, so `Sensitive: false` is honest. Should a rendering variant
   ever be offered? If so it must declare `Sensitive: true` (umbrella D5) — a
   different capability profile, and arguably closer to `config-gcp-secret`'s
   remit. Offer it, or leave rendering entirely to Secret Manager's adapter?

6. **Watch: poll interval default and cost.** D7 polls `ListParameterVersions`
   each tick. What is the default `WithPollInterval` (Parameter Manager has no
   free blocking query, so every tick is a billed API call), and is polling the
   right choice versus omitting `Watch` and letting the consumer reload on their
   own schedule?

7. **Data-model shape — single document vs multi-parameter prefix.** D3 adopts the
   single-named-parameter document model. Should the adapter *also* offer a
   multi-parameter, name-prefix mode (Consul-shaped) for consumers with many small
   parameters, accepting the extra RPC-per-leaf cost — or is the single-document
   model the only one worth shipping?

## Implementation phases

Every phase is gated on this spec reaching `status: approved`, which requires the
open questions above resolved (umbrella D2).

**Phase 0 — this spec.** Resolve the open questions with the human; in particular
OQ1 (viability sign-off), OQ2 (format vs codec), OQ3 (conflict semantics), and
OQ5 (secret rendering / `Sensitive`).

**Phase 1 — the module, read path.** Scaffold `config-gcp-parameter` (README-only,
no microsite; umbrella D2 / adapter docs model), the narrow `PM` interface, `Wrap`,
`New`/`FromClient`, `Load` resolving the current version and decoding per the OQ2
decision (D3, D4), `Capabilities` (D5), error mapping (D8). Fake-`PM` read tests +
provenance. `depfootprint` allowlist (D10).

**Phase 2 — poll-watch and conformance.** `Watch` by polling the current-version
name (D7). Run `backendconformance` against the backend over the fake for the
read+watch subset (D11). Then publish v0.1.0: the module `main`, the `config` docs
page (`how-to/gcp-parameter.md`) bundled into the parent site, and the landing card
— the rollout every adapter takes.

**Phase 3 — write (conditional on OQ3).** Once the conflict decision is made,
implement `Prepare`/`Verify`/`Commit` creating a new parameter version under the
chosen guard (D9), the `Create` half of `PM`, and run `backendconformance`'s
conflict subtest against it. Promote to a read+write minor release.

**Real-service integration** rides alongside phases 1–3, env-gated, run only where
GCP credentials exist (D11) — never a DIND job, because there is no emulator to
containerise.

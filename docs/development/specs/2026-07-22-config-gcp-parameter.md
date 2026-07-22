---
title: config-gcp-parameter — the GCP Parameter Manager backend adapter
date: 2026-07-22
author: matt.cockayne
status: approved
approved: 2026-07-22
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
does (D2). The genuinely decision-bearing questions the service raised — the
**data-model shape**, how the declared **format field** interacts with the family's
value-codec convention, how a **conflict trap** could work in a store with *no*
compare-and-swap primitive, and whether **Secret Manager references** reopen the
`Sensitive` question — were put to the human and resolved (see [Resolved](#resolved-2026-07-22));
the decisions below record the outcomes.

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

### D3 — Data model: single-document (default) and prefix mode (opt-in)

> **Amended 2026-07-22 (R-resolution).** This decision originally adopted the
> single-document model only and left the multi-parameter, prefix-scanning shape
> as an open question (then OQ7). The human resolved OQ7 by **supporting both**:
> single-document is the default, prefix mode is opt-in via its own constructor
> (D6). The body below is amended to describe both; the reasoning for the
> single-document default is unchanged.

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

A Parameter Manager parameter is a heavyweight resource (its own IAM, format,
versions, CMEK); it is designed to hold a **whole configuration document**,
typically JSON or YAML, not a single scalar leaf. So the natural, idiomatic unit
is a whole parameter, and the adapter supports two ways to read one or many:

**(a) Single-document mode (default).** The adapter targets **one named
parameter**; its current version's payload is the whole layer's source document,
decoded into the nested tree (via an injected codec — D4). Worked example —
parameter `app-config` in `global`, holding a version whose payload is:

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

**(b) Prefix mode (opt-in).** For consumers who keep many small parameters rather
than one document, the adapter scans every parameter whose id begins with a given
**prefix** under the location, and nests each one as a leaf — the Consul-shaped
model (umbrella D8), adapted to Parameter Manager. Each matching parameter's id,
with the prefix stripped and any `-`/`/` separators split into path segments,
becomes a key path; its current version's payload is that leaf's value (a scalar
string by default, decoded through the injected codec if one is given — D4).
Example — prefix `app-`, with parameters `app-server-host`, `app-server-port`,
`app-features-beta` — nests into the same `server`/`features` tree as (a).
Provenance names each parameter's version resource. Prefix mode costs one
`GetParameterVersion` RPC per matching parameter (a `ListParameters` filtered by
id prefix, then a payload fetch each), which is the honest price of the fan-out and
the reason single-document is the default.

Both modes feed the **same read/merge pipeline**: each produces one `config.Layer`
whose `Values` tree is merged, provenance-tracked and hot-reloaded like any file
layer. The mode is chosen by the constructor (D6), not a runtime flag.

**"Current version" resolution.** Unlike Secret Manager, a *parameter* version has
**no `latest` alias**. The adapter resolves the current value by
`ListParameterVersions` and choosing the most-recently-created **enabled**
version (skipping `Disabled` ones), recording that version's resource name as the
**change fingerprint** the watch poll compares against (D7). A consumer that wants
a pinned version names it explicitly.

### D4 — Decoding the payload: an explicitly injected codec, exactly like Consul (R1)

> **Resolved 2026-07-22.** This decision originally *proposed* letting the
> parameter's declared `Format` field select whether to decode, and left auto-
> decoding as OQ2. The human resolved OQ2 **against** using the format field to
> pick a codec: decoding is driven by an **explicitly injected `config.Codec`
> only** (umbrella R1), consistent with `config-consul`. The body below records
> the resolved rule; the format field is surfaced but not used to choose a codec.

Consul stores opaque bytes and cannot tell you a value's format, so `config-consul`
requires the consumer to inject a `config.Codec` (umbrella R1, config-consul D3).
Parameter Manager *does* carry an immutable **`Format`** field, set at creation,
with verified enum values:

```
PARAMETER_FORMAT_UNSPECIFIED = 0
UNFORMATTED                  = 1   // plain text / custom
YAML                         = 2
JSON                         = 3
```

So the store *declares* whether its payload is JSON, YAML, or unstructured — but
the adapter deliberately **does not use that declaration to pick a decoder**. Two
reasons, both from the family conventions:

- **No own codec dependency (umbrella R1, D1).** Auto-decoding by format would
  force the adapter to bundle a JSON and a YAML parser so it had something to
  select — precisely the dependency R1 keeps out of the adapter. The consumer,
  who already depends on the format adapter for their data, injects the one
  `config.Codec` their parameter holds.
- **Uniformity with Consul.** The value-decoding seam is identical across the
  family: `WithValueCodec(codec)` (D6), with the same decode-or-string fallback —
  a payload the codec rejects stays a scalar string, so enabling a codec never
  turns a value into a load error. A JSON-document consumer wires
  `configjson.Codec{}`; `config-gcp-parameter` never imports `config-json`.

The rule therefore matches Consul's D3 exactly:

- **Single-document mode (D3a):** without a codec, the whole payload is a single
  scalar string leaf; with a codec, a payload decoding to a mapping becomes the
  layer's subtree, and anything the codec rejects falls back to a string.
- **Prefix mode (D3b):** each parameter's payload is a scalar string leaf by
  default; a codec, if injected, decodes each leaf the same way (a blob-per-
  parameter store), falling back to a string per leaf.

The `Format` field **may be surfaced** — exposed on the narrow interface's
`Parameter.Format` (D6) for diagnostics and provenance — but it never selects a
codec. Whether a future version uses it as a *validation* signal (warn when a
`JSON` parameter is read with no codec) is a possible enhancement, not a v0.1.0
behaviour.

### D5 — Capability: read-only and not sensitive

> **Resolved 2026-07-22.** The human confirmed the module is **read-only** — not
> "read-only at v0.1.0 pending a write phase". Parameter Manager has no compare-
> and-swap (D9), so the write path cannot honour the family's conflict trap;
> read-only is a first-class outcome (umbrella D7). A future investigation into
> GCP write support is tracked separately (D9, "Follow-on tasks").

`config-gcp-parameter` implements `Backend` (read) and, per D7, a polling
`WatchableBackend`. It does **not** implement `WritableBackend` (umbrella D4 —
capability by the type system, never a flag; umbrella D7 — read-only is a
first-class outcome). Its `Capabilities`:

| Field | Value | Why |
|---|---|---|
| `PreservesComments` | `false` | a parameter payload is data, not a formatted document the adapter round-trips |
| `AtomicMultiKey` | `false` | the module does not write, so it makes no multi-key atomicity promise |
| `NativeWatch` | `false` | Parameter Manager has no change feed; watch is polling (D7) |
| `Sensitive` | `false` | Parameter Manager is configuration; secrets live in Secret Manager (`config-gcp-secret`, Phase B) |

`Sensitive` is `false` **only because the read path returns the raw, unrendered
payload** (D8). A parameter payload *can* embed Secret Manager references, and
rendering them resolves real secret values — which would make the resolved layer
sensitive. The adapter does not render, so no secret ever enters a layer, and
`Sensitive` stays honestly `false`. A rendering variant (which would declare
`Sensitive: true`) is deferred to a tracked follow-on task (D9).

Read-only is the right and final shape here, unlike Consul: Consul had clean CAS
and the guide already modelled its write path, so shipping write was cheap and
proven. Parameter Manager has **no compare-and-swap primitive** (D9), so a writable
adapter could not satisfy `backendconformance`'s conflict subtest honestly.
Reading a configuration document is the whole of the value for a config-*consuming*
tool.

### D6 — The constructor injects a narrow client; auth, project, location and endpoint stay with the consumer

The adapter defines its own narrow interface and never takes credentials, project
selection, regional-endpoint configuration or scopes (umbrella D3). The consumer
builds a configured `*parametermanager.Client` — where every auth decision and the
choice of **regional vs global endpoint** lives (a `ClientOption`, D8) — and hands
it in along with the resource coordinates:

```go
// PM is the slice of Parameter Manager this adapter uses, behind an interface it
// owns so a fake drives the unit suite (D11) and the real client is adapted by
// Wrap. Read-only (D5): there is no write method.
type PM interface {
	// Get returns one parameter's declared format and current (latest enabled)
	// version — its resource name and raw payload bytes. A parameter or version
	// that does not exist returns fs.ErrNotExist. Drives single-document mode (D3a).
	Get(ctx context.Context, parameter string) (Parameter, error)

	// List returns every parameter whose id begins with prefix, each resolved to
	// its current version — id, format, version name and raw payload. An empty
	// result is not an error. Drives prefix mode (D3b); it is the fan-out whose
	// per-parameter payload fetch is the honest cost of that mode (D3).
	List(ctx context.Context, prefix string) ([]Parameter, error)
}

// Parameter is one resolved read: the parameter's id, the format it declares
// (surfaced but not used to pick a codec — D4), and its current version.
type Parameter struct {
	ID          string // parameter id (the last path segment), for prefix-mode nesting
	Format      Format // Unformatted, YAML, or JSON — informational (D4)
	VersionName string // full version resource name — the watch change fingerprint (D7)
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
`GetParameterVersion`; `List` → `ListParameters` (filtered by id prefix) + the same
per-parameter version resolution. `project` and `location` compose the resource
path (`projects/{project}/locations/{location}/parameters/{parameter}`); `location`
is `global` by default and any region the consumer's client endpoint targets
otherwise (D8).

Two constructor pairs select the data-model mode (D3), one narrow-seam and one
convenience each:

- **Single-document (default):** `New(pm, parameter, opts...)` and
  `FromClient(client, project, location, parameter, opts...)` =
  `New(Wrap(client, project, location), parameter, opts...)`. A consumer writes
  `configgcpparameter.FromClient(client, "my-proj", "global", "app-config")`.
- **Prefix mode (opt-in):** `NewPrefix(pm, prefix, opts...)` and
  `FromClientPrefix(client, project, location, prefix, opts...)`. A consumer writes
  `configgcpparameter.FromClientPrefix(client, "my-proj", "global", "app-")`.

The narrow `PM` is only seen when testing; the `FromClient*` pair is the common
path. Both constructors take the same variadic `Option`s (`WithValueCodec`,
`WithPollInterval`).

### D7 — Watch is a poll, and it says so

Parameter Manager has no native change feed (no blocking query, no informer). Per
umbrella **D6**, a store with no change signal either polls in a `Watch` it owns,
or omits `Watch` and lets the Store poll via reload. This adapter **implements
`Watch` by polling**. Each tick it re-resolves the change fingerprint — in
single-document mode the one parameter's current version name, in prefix mode the
set of `(parameter id → version name)` pairs — without fetching payloads (a
`ListParameterVersions`, or a `ListParameters`, is enough), and calls `onChange`
when the fingerprint differs from the one last seen (a new version, an added or
removed parameter). `NativeWatch: false`, and the latency is the poll interval —
stated plainly, not disguised as push.

The interval comes from `WithPollInterval`; its **default is 60 seconds** when
unset. The default is deliberately conservative because, unlike a Consul blocking
query, every tick is a **billed API call** and there is no free long-poll — 60s
keeps a hot-reloading consumer current within a minute without generating steady
per-second request cost, and a consumer who needs faster reaction sets a shorter
interval explicitly. The Store's hybrid watch coalesces and settles the resulting
reload for free (umbrella D6). A Pub/Sub notification path is **out of scope**:
Parameter Manager exposes no first-class change topic, and wiring one would move
change-detection outside the Store, which owns it.

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
- **Regional vs global (OQ4 resolved 2026-07-22).** The `location` segment of the
  resource path selects global (`global`, the default), multi-regional, or a
  specific region. A regional parameter additionally requires the consumer's
  client to target the matching **regional endpoint** (a `ClientOption`). The
  adapter takes the `location` string (D6) and **trusts the injected client's
  endpoint to match it** — it does **not** validate the agreement and does **not**
  configure endpoints (umbrella D3: the adapter re-decides no client
  configuration). A mismatched endpoint surfaces naturally as a `NotFound` or
  transport error from the SDK, mapped like any other read failure above; forcing
  the adapter to police the consumer's endpoint choice would duplicate a decision
  D3 places entirely with the consumer.

### D9 — No write path: Parameter Manager has no compare-and-swap (resolved 2026-07-22)

> **Resolved 2026-07-22.** The human resolved write scope (OQ3): **v0.1.0 is
> read-only, and no write path is specified here.** The investigation of GCP write
> support is a tracked follow-on task (below), tied to the umbrella's "Phase D —
> revisit", not a phase of this adapter.

This is the sharpest semantic problem, and the reason there is no write path.

Parameter Manager versions are **immutable**, and there is **no compare-and-swap,
no `etag`, and no generation number** on either a `Parameter` or a
`ParameterVersion` — verified against v1.0.0. The `RequestId` field on
`CreateParameterVersion` is **idempotency for retries** (dedupe a re-sent
request), **not** a conflict guard. So Consul's clean `ModifyIndex` CAS has no
equivalent, and the family's version-at-Load conflict trap (umbrella D11) has no
native mechanism to lean on. A writable adapter would have to pick among three
unsatisfying options — none adopted, all recorded for the follow-on:

1. **Version-id-as-guard (create-if-absent).** Derive the new version's id
   deterministically from the version seen at Load (e.g. a monotonic successor).
   `CreateParameterVersion` fails with `AlreadyExists` if a concurrent writer
   already created that id — a genuine create-if-absent guard, the closest thing
   to CAS the service offers. Cost: it dictates the version-id scheme, and version
   ids have format constraints.
2. **Read-before-write check.** At `Verify`/`Commit`, re-resolve the current
   version name and refuse with `config.ErrConflict` if it differs from the one
   recorded at Load, then create a new version. Simple, but leaves a TOCTOU window
   between the check and the create (no atomic guard closes it).
3. **Last-write-wins.** Always create a new version; never refuse. Honest for an
   append-only, immutable-version store, but silently loses a concurrent writer's
   change and **fails `backendconformance`'s conflict subtest** (umbrella D11),
   which requires a change landing between Load and Commit to be refused.

Because option 3 cannot pass the shared conformance suite and options 1–2 each
carry a real trade-off, shipping write now would either lie about consistency or
bolt on a bespoke conflict model the family has not agreed. Read-only is the honest
outcome (umbrella D7); the module implements no `Prepare` and is not a
`WritableBackend` (D5).

**Follow-on tasks (tracked; tied to umbrella "Phase D — revisit").** These are
recorded here so they are not lost, but are explicitly **out of scope** for this
spec and each needs its own decision before implementation:

- **GCP write support.** Decide between the version-id-as-guard and last-write-wins
  models above, and — crucially — settle **how `backendconformance`'s conflict
  subtest should treat a writable backend that has no true CAS** (does the suite
  gain a "best-effort conflict" tier, or does a non-CAS store simply not qualify as
  writable under the family contract?). That question is bigger than this adapter;
  it is a family-level call the umbrella should record. Until it is answered, GCP
  write stays unbuilt.
- **Secret Manager rendering variant.** A read variant that calls
  `RenderParameterVersion` to resolve `__REF__(//secretmanager...)` references,
  returning the rendered payload and declaring `Sensitive: true` (umbrella D5).
  Deferred (see D5/D8); it is arguably closer to `config-gcp-secret`'s remit and
  should be weighed against that adapter rather than bolted onto this one.

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

1. **A hand-written fake `PM`** — in-memory, format-aware, version-tracking,
   implementing both `Get` and `List` — drives the unit suite: single-document and
   prefix-mode reads (D3), codec decode and the decode-or-string fallback (D4),
   provenance, the absent-parameter case, and watch (poll detecting a new version
   name or a changed parameter set). No cloud, no account, IDE-runnable, the
   primary suite.
2. **The shared `backendconformance` suite** (`config/backendconformance`, shipped
   in v0.6.0), run against a `configgcpparameter` backend over the fake — the
   **read+watch subset** only, since the backend is not a `WritableBackend` (D5);
   the conflict subtest does not apply. It asserts the backend participates as an
   ordinary layer with per-key merge and provenance, tolerates an absent source,
   and reports a foreign change to observers.
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

**Prefix mode *only*, or single-document *only*.** An earlier draft made
single-document the sole model and left the prefix scan as an open question.
Resolved (2026-07-22) by supporting **both** (D3): single-document is the default —
it is the shape the service is designed for and costs one read — and prefix mode is
an opt-in constructor for consumers who keep many small parameters, at the honest
cost of one payload fetch per matching parameter. Neither alone was rejected; the
rejected option was *forcing* a consumer into whichever single model did not fit
their store.

**Auto-decode using the `Format` field (adapter bundles JSON/YAML codecs).** Read
`Parameter.Format` and select a built-in decoder, so the consumer supplies no
codec. Tempting, because the store *declares* its format — but rejected
(2026-07-22, OQ2): it forces the adapter to bundle format parsers, taking exactly
the codec dependency umbrella R1 keeps out of an adapter, and it fragments the
family's decoding seam. `config-gcp-parameter` uses the same explicit
`WithValueCodec` injection as `config-consul` (D4); the `Format` field is surfaced
for diagnostics but never picks a codec.

**Render Secret Manager references by default.** Call `RenderParameterVersion` so
`__REF__(//secretmanager.googleapis.com/...)` placeholders resolve to real secret
values on read. Rejected as the default: it pulls secret material into a
`Sensitive: false` layer, defeating the whole point of the `Sensitive` guard
(umbrella D5). Rendering is not offered in v0.1.0; if ever built it is opt-in and
forces `Sensitive: true`, and is a tracked follow-on weighed against
`config-gcp-secret` (D9).

**Ship write like Consul did.** Rejected: Consul had native CAS and a proven write
path; Parameter Manager has no CAS primitive at all (D9), so a writable adapter
would either lie about consistency or invent a bespoke conflict model the family
has not agreed. Read-only is a first-class outcome (umbrella D7); GCP write is a
tracked follow-on tied to the umbrella's Phase D (D9), not a phase of this adapter.

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

The module's exported surface (v0.1.0, read + poll-watch, both data-model modes):

- `func New(pm PM, parameter string, opts ...Option) config.Backend` —
  single-document injection seam (D3a); `pm` is a fake in tests, a `Wrap`ped client
  in production.
- `func NewPrefix(pm PM, prefix string, opts ...Option) config.Backend` — prefix-
  mode injection seam (D3b), scanning every parameter under `prefix`.
- `func FromClient(client *parametermanager.Client, project, location, parameter string, opts ...Option) config.Backend`
  — single-document convenience path, `New(Wrap(client, project, location),
  parameter, opts...)`.
- `func FromClientPrefix(client *parametermanager.Client, project, location, prefix string, opts ...Option) config.Backend`
  — prefix-mode convenience path, `NewPrefix(Wrap(client, project, location),
  prefix, opts...)`.
- `func Wrap(client *parametermanager.Client, project, location string) PM` —
  adapts the real Parameter Manager SDK client to the narrow interface.
- `func WithValueCodec(codec config.Codec) Option` — decode a parameter's payload
  through `codec` (a value decoding to a mapping becomes a subtree; anything the
  codec rejects stays a scalar string — D4); omitted, payloads are scalar strings.
- `func WithPollInterval(d time.Duration) Option` — the watch poll interval (D7);
  omitted, the default is 60s.
- `type PM interface { … }`, `type Parameter struct { … }`, `type Format int`
  (with `Unformatted`, `YAML`, `JSON`), `type Option func(…)` — the narrow client
  seam (D6) and the option type.

The returned backend satisfies `config.WatchableBackend` (poll); it does **not**
satisfy `config.WritableBackend` (D5, D9). The Store discovers capability by type
assertion, so no capability flag is exported (umbrella D4). No change to the
`config` core is required — v0.6.0 already carries everything this adapter needs.
There is no planned write surface; GCP write, if ever built, is a tracked follow-on
(D9) that would be additive.

## Testing strategy

Per D11: a fake-`PM` unit suite (single-document and prefix-mode reads, codec
decode with the decode-or-string fallback, provenance, absent-parameter →
`fs.ErrNotExist`, and poll-watch detecting a new version name or a changed
parameter set); a `backendconformance.Run` against a `configgcpparameter` backend
over the fake for the read+watch subset (the conflict subtest does not apply — the
backend is not writable, D5); real-service integration under `./test/integration/`
env-gated on `INT_TEST_INTEGRATION`, run only where GCP credentials exist (no DIND
job — D11); and a `depfootprint_test.go` allowlist (D10). What would falsely pass:
a poll-watch test whose fake never advances its current-version fingerprint — the
watch subtest must drive the fake to publish a new version (single-document) or add
a parameter (prefix mode) and be watched to fail when it does not; and a codec test
whose fixture is already a scalar would not exercise the mapping-to-subtree path, so
the decode test must feed a real document payload and assert a nested subtree, not
just a non-error.

## Migration & compatibility

Purely additive for consumers: add the module and a
`WithBackend(configgcpparameter.FromClient(client, project, location, parameter))`
call — or `FromClientPrefix(...)` for prefix mode — exactly as for a file adapter.
No `config` core change, no breaking change. The module ships at v0.1.0 as **read +
poll-watch**, and that is its intended shape: unlike the read-first *file* adapters,
there is **no planned read→write promotion**, because Parameter Manager has no CAS
to write against safely (D9). If GCP write is ever built (a tracked follow-on),
that is a separate, additive capability decided on its own merits, not a promotion
this module's consumers should expect.

## Resolved (2026-07-22)

The open questions were resolved with the human, and the decisions above amended in
place (never renumbered, per the specs convention). No open question remains.

1. **Service viability.** **Confirmed — proceed.** Parameter Manager is GA, is the
   right service (not Runtime Configurator, not Secret Manager), and has a Go SDK
   (`cloud.google.com/go/parametermanager` v1.0.0). It is the GCP Phase A target;
   Secret Manager stays in Phase B (`config-gcp-secret`). See D2.

2. **Format field vs value-codec.** **Explicit injected codec only** (umbrella
   R1), exactly as `config-consul`. The `Format` field is surfaced for diagnostics
   but is **not** used to pick a codec; the adapter takes no codec dependency of
   its own and does not auto-decode. See D4 (amended) and the flipped rejected
   alternative.

3. **Conflict semantics / write.** **v0.1.0 is read-only; no write path is
   specified.** Parameter Manager has no compare-and-swap (immutable versions, no
   etag/generation), so the family conflict trap cannot be honoured; read-only is
   first-class (umbrella D7). GCP write — and the family-level question of how
   `backendconformance` should treat a non-CAS writable backend — is a **tracked
   follow-on** tied to the umbrella's "Phase D — revisit". See D5, D9.

4. **Global vs regional / endpoint validation.** The adapter takes a `location`;
   the consumer's client must target the matching endpoint. **The adapter does not
   validate the agreement and configures no endpoint** (umbrella D3); a mismatch
   surfaces as a `NotFound`/transport error. See D8.

5. **Secret Manager references and `Sensitive`.** **v0.1.0 returns the raw,
   unrendered payload and never calls `RenderParameterVersion`, so `Sensitive:
   false` is honest.** A rendering variant (which would be `Sensitive: true`) is a
   tracked follow-on, weighed against `config-gcp-secret` rather than bolted on
   here. See D5, D9.

6. **Watch poll default and cost.** **Poll for a new latest version;
   `NativeWatch: false`; default interval 60s, overridable by `WithPollInterval`.**
   60s is conservative because each tick is a billed API call (no free long-poll).
   See D7.

7. **Data-model shape.** **Support both.** Single-document is the default
   (`New`/`FromClient`); prefix mode is opt-in (`NewPrefix`/`FromClientPrefix`),
   scanning parameters under a prefix as Consul-style leaves, at one payload fetch
   per parameter. Both feed the same read/merge pipeline. See D3 (amended), D6.

## Implementation phases

This spec is `approved` (open questions resolved above). Implementation is
test-first, phase by phase.

**Phase 0 — this spec.** Approved 2026-07-22; open questions resolved above.

**Phase 1 — the module, single-document read path.** Scaffold
`config-gcp-parameter` (README-only, no microsite; umbrella D2 / adapter docs
model), the narrow `PM` interface, `Wrap`, `New`/`FromClient`, `Load` resolving the
current version and decoding through the injected codec (D3a, D4), `Capabilities`
(D5), gRPC error mapping (D8). Fake-`PM` read tests + provenance. `depfootprint`
allowlist (D10).

**Phase 2 — prefix mode.** `NewPrefix`/`FromClientPrefix` and the `List` half of
`PM`, scanning parameters under a prefix and nesting each as a leaf (D3b). Fake-`PM`
prefix-mode read tests, including mixed scalar/codec leaves and the extra-RPC
fan-out.

**Phase 3 — poll-watch and conformance.** `Watch` by polling the change
fingerprint (single-document version name, prefix-mode parameter set), 60s default
(D7). Run `backendconformance` against the backend over the fake for the read+watch
subset (D11). Then publish v0.1.0: the module `main`, the `config` docs page
(`how-to/gcp-parameter.md`) bundled into the parent site, and the landing card — the
rollout every adapter takes.

**Real-service integration** rides alongside phases 1–3, env-gated on
`INT_TEST_INTEGRATION`, run only where GCP credentials exist (D11) — never a DIND
job, because there is no emulator to containerise.

**Follow-on (out of scope, tracked — umbrella Phase D).** GCP write support and its
non-CAS conflict question, and a Secret-Manager-rendering `Sensitive: true` read
variant (D9). Each needs its own decision before any implementation.

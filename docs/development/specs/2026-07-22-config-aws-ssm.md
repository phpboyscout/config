---
title: config-aws-ssm — the AWS Systems Manager Parameter Store backend adapter
date: 2026-07-22
author: matt.cockayne
status: draft
---

# config-aws-ssm

The second Phase-A backend adapter and the first of the cloud parameter-store
trio (umbrella **Phase A**). Per the [dynamic backend adapters umbrella
spec](2026-07-21-dynamic-backend-adapters.md) **D2**, no adapter is built
without its own approved spec; this is that spec for AWS Systems Manager
Parameter Store. It cites the umbrella and settles the nine things the umbrella
left to each adapter — and, because Parameter Store differs from Consul in the
two ways the umbrella most cares about (no native change feed, umbrella D6; no
compare-and-swap write, umbrella D11), it surfaces the decisions those
differences force as **open questions for the human**, not resolved defaults.

`config-consul` is the reference this mirrors: the same narrow-injected-client
shape, the same prefix→nested-tree data model, the same value-codec seam
(umbrella R1), the same three-layer testing story. Read that spec alongside this
one; this document calls out only where SSM's semantics make the two diverge,
and those divergences are the substance.

## Problem

A tool that configures from AWS Systems Manager Parameter Store cannot use this
module today. Parameter Store is a remote, eventually-consistent, path-keyed
namespace fetched at runtime over the AWS SDK, holding `String`, `StringList`
and KMS-encrypted `SecureString` values. The seam to reach it exists and is
proven (umbrella preamble; the custom-backend guide; `config-consul` shipped
against it). What is missing is the shipped module — `config-aws-ssm` — and the
per-system decisions the umbrella cannot make.

Two of those decisions are genuinely hard, and they are the reason this adapter
earns a separate, carefully-argued spec rather than a copy of Consul's:

1. **Parameter Store has no compare-and-swap write.** `PutParameter` overwrites
   unconditionally (verified below); there is no "put if version == N". The
   version-at-Load conflict trap that `backendconformance` enforces (umbrella
   D11) — the trap Consul satisfies with `KVCAS` — has no native primitive to
   rest on here. Whether, and how, this adapter writes at all is the headline
   question.
2. **`SecureString` yields decrypted secret material.** Reading a `SecureString`
   with `WithDecryption` returns plaintext. That collides directly with the
   `Sensitive` capability and its `ErrSensitiveLeak` guard (umbrella D5) — but
   Parameter Store also holds ordinary non-secret configuration, so the layer is
   not simply "a secrets store". How SecureString is treated is the second
   question.

Neither is the adapter's to answer unilaterally. Both are recorded below with
what was **verified against the SDK**, the options, and the consequence for the
shared conformance contract — and left open for the human (umbrella D2, points 2
and 7).

## Verified against the SDK

Everything in this spec about the SSM API was checked against
`github.com/aws/aws-sdk-go-v2/service/ssm` **v1.73.0** (SDK core
`github.com/aws/aws-sdk-go-v2` **v1.43.0**, `github.com/aws/smithy-go`
**v1.27.3**), probed in a throwaway module — never against the config repo's
`go.mod`. The load-bearing facts:

- **Read** is `Client.GetParametersByPath(ctx, *GetParametersByPathInput)`.
  Input: `Path *string` (required — the hierarchy), `Recursive *bool` (all levels
  under the path), `WithDecryption *bool` (decrypt `SecureString` values),
  `MaxResults *int32` (**cap 10 per page**), `NextToken *string`. Output:
  `Parameters []types.Parameter`, `NextToken *string`. Pagination is via
  `NewGetParametersByPathPaginator`.
- **Value carrier** is `types.Parameter`: `Name *string`, `Value *string`,
  `Type types.ParameterType` (`String` | `StringList` | `SecureString`),
  `Version int64`, `LastModifiedDate *time.Time`, `ARN *string`. **A `StringList`
  comes back as a comma-separated string in `Value` with no spaces** (SDK doc).
  Every type — including `SecureString` after decryption — is delivered as a
  plain `*string`.
- **Write** is `Client.PutParameter(ctx, *PutParameterInput)`. Input: `Name`,
  `Value`, `Type`, `Overwrite *bool` (**default `false`**), `KeyId *string` (KMS
  key for `SecureString`), `Tier`, `AllowedPattern`. Output:
  `PutParameterOutput{ Version int64, Tier }`. **There is no conditional/version
  parameter.** `Overwrite=false` fails with `types.ParameterAlreadyExists` if the
  name exists (a race-free *create-if-absent*); `Overwrite=true` clobbers the
  current value regardless of its version. That is the whole of the write
  concurrency control SSM offers.
- **Delete** is `Client.DeleteParameter` (single) or `Client.DeleteParameters`
  (batch, `Names []string`) — the batch returns `DeletedParameters` **and**
  `InvalidParameters`, i.e. it is **partial-success, not atomic**. There is no
  multi-key transaction anywhere in the API.
- **History** is `Client.GetParameterHistory` — a version log, not a
  conditional-write primitive; it cannot make a write atomic against a version.
- **Errors**: `types.ThrottlingException`, `types.TooManyUpdates`,
  `types.ParameterNotFound`, `types.ParameterAlreadyExists`,
  `types.ParameterVersionNotFound`. Throttling is a first-class, expected
  condition — Parameter Store has real per-account/per-Region request-rate
  limits.
- **No change feed.** Nothing in the SSM client subscribes to change. (Change can
  be delivered out-of-band via EventBridge, which is **out of scope** — it is a
  separate service, separate SDK, separate infrastructure the consumer would have
  to wire.) Watch, if offered, is polling.

## Decisions

### D1 — Module `config-aws-ssm`, package `configawsssm`

A **cloud-qualified** name (umbrella D1, OQ5): the same parameter-store purpose
exists under three vendors, and the service name is what people search for, so
the cloud is carried in the name. The module is
`gitlab.com/phpboyscout/go/config-aws-ssm`, package `configawsssm`, matching the
`config-<x>` / `configx` convention. The repo carries a README only; this spec
lives centrally here (umbrella D2). It requires `config` **v0.6.0+** — the
release that shipped `backendconformance` and the `Sensitive` enforcement this
family depends on.

### D2 — Data model: a path prefix scopes; parameter names are paths that nest

Parameter Store is a flat namespace of `/`-separated names holding string
values. The adapter takes a required **prefix** (umbrella D8), the same scoping
control `WithEnv` and `config-consul` use: it bounds the read to one hierarchy
with `GetParametersByPath(Path: prefix, Recursive: true)`, strips the prefix,
splits the remaining name on `/`, and nests the segments into the layer's tree.
A worked example — prefix `/app`, with Parameter Store holding:

```
/app/server/host   (String)     = "localhost"
/app/server/port   (String)     = "8080"
/app/features/beta (String)     = "true"
```

becomes the layer

```yaml
server:
  host: localhost
  port: "8080"
features:
  beta: "true"
```

Provenance names the full parameter name (`aws-ssm:/app/server/port`), so a value
is always traceable to where it lives (umbrella D8). The flat-to-nested step is
the same few lines every flat adapter writes; there is still no shared core
helper for it (umbrella D8 / non-YAML R3).

SSM names begin with a leading `/` (`/app/...`); the prefix is normalised the way
`config-consul` normalises its trailing slash, so `/app` and `/app/` scope
identically and the leading `/` is handled once. Read is paginated (D8): the
adapter drains `NewGetParametersByPathPaginator` (10 per page) into one layer.

### D3 — Values carry no format hint; structured values decode through an injected codec (umbrella R1)

**Confirmed against the SDK:** an SSM parameter carries no content-type or format
metadata — `types.Parameter.Value` is a bare `*string` for every `Type`. So, as
for Consul (config-consul D3), the family convention applies unchanged (umbrella
**R1**): each value is a UTF-8 **scalar string leaf** by default, and the View's
typed accessors coerce (`GetInt("server.port")` parses `"8080"`); structured
decoding is **purely opt-in** via `WithValueCodec(config.Codec)`, where a value
that decodes to a mapping becomes a subtree and anything the codec rejects falls
back to a scalar string. The consumer injects the one format their store holds,
so `config-aws-ssm` takes no codec dependency of its own. This is settled by the
family convention and is **not** an open question; what *is* open is how the
`StringList` type interacts with it (Open Question 4).

### D4 — Capability: read and poll-watch are certain; write and Sensitive are open

`config-aws-ssm` implements `Backend` (read) unconditionally. The remaining
capability cells are what the open questions decide, so the matrix is stated with
its contested rows marked:

| Field | Value | Why |
|---|---|---|
| `PreservesComments` | `false` | a parameter store has nowhere to put a comment |
| `AtomicMultiKey` | **`false`** | **verified**: `PutParameter` is single-key and `DeleteParameters` is partial-success — SSM has no multi-key transaction, unlike Consul's `Txn` |
| `NativeWatch` | `false` | SSM has no change feed; watch is polling (D7) |
| `Sensitive` | **open** | depends on the SecureString decision (Open Question 3) |

`AtomicMultiKey: false` is the sharpest *settled* divergence from Consul: a
batch of edits under this backend cannot land indivisibly, because the API has no
transaction. Whatever the write decision (Open Question 1), the capability must
tell the truth about that — a batch is a sequence of independent `PutParameter`
calls, and a failure partway leaves earlier writes applied.

### D5 — The constructor injects a narrow client; auth stays with the consumer

The adapter defines its own narrow interface and never takes region, profile,
credentials, endpoint or KMS-key configuration (umbrella D3). The consumer builds
a configured `*ssm.Client` — that is where every credential decision lives,
typically `cfg, _ := config.LoadDefaultConfig(ctx); client := ssm.NewFromConfig(cfg)`
— and hands it in. The proposed narrow seam, mirroring `config-consul`'s `KV`:

```go
// ParamStore is the slice of SSM this adapter uses. A fake satisfying it drives
// the whole unit suite (D11); the real client is adapted by Wrap.
type ParamStore interface {
	// GetByPath returns every parameter under prefix (recursive, paginated,
	// drained) and, per D8, whatever version marker the conflict model needs.
	GetByPath(ctx context.Context, prefix string, decrypt bool) ([]Param, error)

	// Put writes one parameter. create true maps to Overwrite=false — a race-free
	// create that fails if the name exists; create false maps to Overwrite=true.
	Put(ctx context.Context, p Param, create bool) error

	// Delete removes one parameter name.
	Delete(ctx context.Context, name string) error
}

type Param struct {
	Name    string          // full SSM name, prefix included
	Value   string
	Type    string          // "String" | "StringList" | "SecureString"
	Version int64           // monotonic; captured at Load for the conflict check
}
```

`Wrap(client *ssm.Client) ParamStore` adapts the real SDK — `GetByPath` drains
`NewGetParametersByPathPaginator`, `Put` calls `PutParameter` (mapping `create`
to `Overwrite`), `Delete` calls `DeleteParameter`. The common path is
`FromClient(client, prefix, opts...)` = `New(Wrap(client), prefix, opts...)`, so
a consumer writes `configawsssm.FromClient(client, "/app")`. The exact method set
of `ParamStore` depends on the write and watch decisions (Open Questions 1, 5) —
a read-only adapter needs only `GetByPath`.

### D6 — Authentication & configuration model

The consumer supplies a fully-configured `*ssm.Client`; the adapter touches
**none** of: region, credential provider (static, SSO, IMDS, env, profile),
endpoint/URL override (including LocalStack), retry policy, or the KMS key used
to decrypt `SecureString`. All of that is the consumer's `aws.Config`. This keeps
the adapter credential-agnostic and lets the unit suite run with no AWS account
(umbrella D3, D10). The only SSM-shaped choice the adapter itself makes is
whether to pass `WithDecryption` (tied to Open Question 3).

### D7 — Watch is polling, or omitted; latency and throttling stated

SSM has no native change signal (verified), so `NativeWatch` is `false`
(umbrella D6). Two shapes are available, and the adapter picks one in the open
questions:

- **Poll** — implement `WatchableBackend` by calling `GetParametersByPath` on an
  interval and firing `onChange` when any parameter's `Version` (or the set of
  names) has moved. The Store's hybrid watch already coalesces and settles the
  resulting foreign change, so a polling backend inherits debouncing for free.
- **Omit** — don't implement `Watch`; let the consumer poll via the Store.

Polling has a real cost SSM makes concrete: `GetParametersByPath` is
rate-limited, and a short interval across many prefixes will draw
`ThrottlingException`. So a poll implementation must have a **conservative
default interval** and back off on throttle, and the default is a decision for
the human (Open Question 5), not a number invented here. The `interval` argument
from the Store (`WithPollInterval`) overrides whatever default is chosen.

### D8 — Error & consistency semantics

- **Eventual consistency.** A `PutParameter` is not guaranteed visible to an
  immediately-following `GetParametersByPath`. This weakens any read-check-based
  conflict scheme further (Open Question 1) and is stated plainly to the consumer.
- **Throttling** (`ThrottlingException`, `TooManyUpdates`) is expected under load,
  not exceptional. The adapter relies on the SDK's built-in adaptive retry (the
  consumer's `aws.Config` owns the retry policy, D6) and surfaces a persistent
  throttle as a load/reload error the Store already tolerates for a transient
  source failure.
- **Partial reads.** Pagination is drained fully before a layer is built; a
  mid-drain error fails the Load rather than contributing a truncated layer, so a
  half-read prefix never silently drops keys.
- **Absent prefix.** A path with no parameters returns an empty set, which the
  adapter reports as `fs.ErrNotExist` (config-consul's model), letting the Store
  decide whether a missing source is fatal.

### D9 — Write semantics and the conflict trap — THE HEADLINE (see Open Question 1)

This is where SSM and Consul part hardest, and the decision is the human's
(Open Question 1). The **verified** facts that constrain it:

- The conflict version *can* be captured at Load: `types.Parameter.Version` is a
  monotonic `int64` per name, recorded at Load exactly as Consul records
  `ModifyIndex` (umbrella D11).
- But **there is no write primitive that consumes that version.** `PutParameter`
  has no "if-version" parameter. The only concurrency control is `Overwrite`:
  `false` = create-if-absent (race-free, via `ParameterAlreadyExists`); `true` =
  unconditional clobber. So a create of a *new* key can be made conflict-safe;
  an **update** of an existing key cannot be made atomic against its Load version.
- There is no multi-key transaction, so `AtomicMultiKey` is `false` regardless
  (D4).

The consequence for the shared contract is specific: `backendconformance`'s
conflict subtest (umbrella D11) asserts that a change landing between Load and
Commit is refused with `ErrConflict`. For updates, SSM can only approximate this
with a **read-check-then-write** (re-`Get` the version at Verify/Commit, compare
to the Load version, then `PutParameter`) — which has an irreducible TOCTOU race
between the check and the put, made worse by eventual consistency (D8). So the
adapter cannot honour that subtest the way Consul does; whether it (a) does the
best-effort read-check and documents the residual race, (b) accepts last-write-
wins and documents it, or (c) ships **read-only**, is Open Question 1. Until it is
answered, the write half of `ParamStore` (D5), `WritableBackend`, and the
`backendconformance` write/conflict run are all conditional.

### D10 — Dependency footprint: the AWS SDK v2, stated plainly

`config-aws-ssm` depends on the modular AWS SDK for Go v2, pulling **only the SSM
service** — not the whole SDK (umbrella D9). **Measured** two ways, because the
adapter's own graph and the consumer's client-build graph differ sharply:

- **The shipped adapter package** (`go list -deps .`, importing
  `service/ssm`, `service/ssm/types`, `aws`) pulls **5 modules**:

  ```
  github.com/aws/aws-sdk-go-v2
  github.com/aws/aws-sdk-go-v2/internal/configsources
  github.com/aws/aws-sdk-go-v2/internal/endpoints/v2
  github.com/aws/aws-sdk-go-v2/service/ssm
  github.com/aws/smithy-go
  ```

- **The consumer** who also builds the client with `config.LoadDefaultConfig`
  additionally pulls `aws-sdk-go-v2/config`, `credentials`, `feature/ec2/imds`,
  `internal/v4a`, `service/internal/accept-encoding`,
  `service/internal/presigned-url`, `service/signin`, `service/sso`,
  `service/ssooidc`, `service/sts` — **16 modules total**. That larger graph is
  the consumer's, incurred by their credential loading (D6), not the adapter's.

A `depfootprint_test.go` allowlist asserts the adapter's own 5-module set (scoped
to `go list -deps .`, so it excludes both the consumer's client-build modules and
the test-only testcontainers/LocalStack deps), so an unexpected dependency — or
the SDK growing one — is a failing test, not a surprise in a consumer's `go.sum`.
Versions above are what was verified (SSM v1.73.0, SDK core v1.43.0, smithy-go
v1.27.3); the README states them.

### D11 — Testing: a fake for units, LocalStack testcontainers for the real thing

Three layers, from the umbrella's D10 and the shared `backendconformance` suite,
identical in shape to config-consul D11:

1. **A hand-written fake `ParamStore`** — in-memory, version-tracking — drives the
   unit suite: reads, per-key merge, provenance, `StringList`/`SecureString`
   handling (per Open Questions 3, 4), and, if writable, writes and the
   conflict/create cases. No AWS, no account, IDE-runnable, the primary suite.
2. **The shared `backendconformance` suite** (`config/backendconformance`,
   v0.6.0) over a `configawsssm` backend on that fake — the read contract always;
   the write/conflict subtests **only if** Open Question 1 lands on a writable
   posture, and the conflict subtest's expectations are whatever that decision can
   honestly meet (D9).
3. **LocalStack testcontainers integration** — **verified available**:
   `github.com/testcontainers/testcontainers-go/modules/localstack` **v0.43.0**
   (`localstack.Run(ctx, "localstack/localstack:3")` → `PortEndpoint` on 4566),
   whose endpoint is set as the `aws.Config` `BaseEndpoint` (the consumer-side
   override, D6) to build a real `*ssm.Client` passed through `Wrap`. SSM is a
   core LocalStack service, so reads, writes and (for SecureString) KMS-backed
   decryption exercise the same behaviours against a real SSM API surface. These
   live under `./test/integration/` and are **env-gated on `INT_TEST_INTEGRATION`**
   (config-consul D11), kept compiled and IDE-discoverable but out of the ordinary
   unit job.

### D12 — Integration runs in CI on the go-test component's DIND job

As config-consul D12: the env-gated integration tests run through the **`go-test`
component's opt-in DIND job (phpboyscout/cicd v0.26.0)** — `enable_integration:
true`, `integration_paths: "./test/integration/..."` — which supplies the
`docker:dind` service, `DOCKER_HOST`, `FF_NETWORK_PER_BUILD`,
`TESTCONTAINERS_RYUK_DISABLED=true` and `INT_TEST_INTEGRATION=1`. The ordinary
merge gate stays Docker-free — the fake and `backendconformance` suites carry it
— and the DIND job runs the real-SSM (LocalStack) proof. `integration_timeout`
(`10m`) and `dind_image` (`docker:27-dind`) stay at their defaults.

## Rejected alternatives

**Take `*ssm.Client` directly in the constructor.** Simpler to call, but couples
the adapter to the SDK type, makes the unit suite need a mocked client, and
invites the adapter to reach for region/credential/KMS configuration that is the
consumer's. The narrow `ParamStore` with a `Wrap` adapter keeps units fake-driven
and auth consumer-owned (umbrella D3); `FromClient` keeps the common path one
call (D5).

**Claim `AtomicMultiKey: true` and split large batches.** Rejected on a verified
fact: SSM has no transaction and `DeleteParameters` is explicitly
partial-success. Claiming atomicity the API cannot deliver is exactly the "claim
one thing and do another" the capability split exists to prevent (backend.go).
The capability tells the truth: `false` (D4).

**Manage AWS auth/region inside the adapter** (`configawsssm.New(region, profile)`).
Rejected per umbrella D3 and its own rejected-alternatives: it duplicates the
consumer's `aws.Config`, couples the adapter to one credential model, and makes
the suite need real credentials.

**Use EventBridge for native watch.** Parameter Store change events *can* be
delivered via EventBridge, which would make `NativeWatch: true` defensible.
Rejected as out of scope: it is a separate service with its own SDK, its own IAM,
and infrastructure (a rule, a target, a queue) the consumer must provision — far
beyond "inject a client". Polling with a stated interval (D7) is the honest
default; EventBridge is a future revision if a consumer needs push latency.

**Auto-sniff each value's format with no opt-in.** Rejected for the same reasons
as config-consul: SSM declares no format, `"123"`/`"true"` are ambiguous, and a
malformed blob would become a load error for a key the consumer may not use. The
injected value codec (D3, umbrella R1) is explicit.

**A bespoke value-decoder interface on the adapter.** Rejected: `config.Codec`
already is that interface (umbrella R1); a new one fragments the seam.

## Public API

The proposed exported surface (final shape depends on the write/watch open
questions):

- `func New(store ParamStore, prefix string, opts ...Option) config.Backend` —
  the injection seam; `store` is a fake in tests, a `Wrap`ped client in
  production.
- `func FromClient(client *ssm.Client, prefix string, opts ...Option) config.Backend`
  — the convenience path, `New(Wrap(client), prefix, opts...)`.
- `func Wrap(client *ssm.Client) ParamStore` — adapts the real SSM SDK client to
  the narrow interface.
- `func WithValueCodec(codec config.Codec) Option` — decode structured values
  through `codec` (D3); omitted, values are scalar strings.
- `func WithPollInterval(d time.Duration) Option` — *if* watch ships (D7); the
  poll cadence, overriding the chosen default.
- `type ParamStore interface { … }`, `type Param struct { … }`,
  `type Option func(…)` — the narrow client seam (D5) and the option type.

Whether the returned backend also satisfies `config.WritableBackend` /
`config.WatchableBackend` is discovered by the Store via type assertion, so no
capability flag is exported (umbrella D4), and it is exactly what Open Questions 1
and 5 decide. No change to the `config` core is required — v0.6.0 already carries
`backendconformance`, `Sensitive` enforcement and everything a backend needs.

## Testing strategy

Per D11: a fake-`ParamStore` unit suite (reads, merge, provenance, value-type
handling; writes/conflict *if* writable); a `backendconformance.Run` over the
fake (read contract always; write/conflict per Open Question 1, with the
conflict subtest's expectation matched to what SSM can honestly deliver, D9);
LocalStack testcontainers integration under `./test/integration/`, env-gated,
run on the v0.26.0 DIND job (D12); and a `depfootprint_test.go` allowlist (D10).
What would **falsely pass**: (a) a conflict test whose fake's `Put` silently
enforces a version guard the real `PutParameter` does not — the fake must model
SSM's *actual* clobber-or-create-if-absent semantics, or the suite would prove a
CAS the service cannot do; (b) an atomicity assumption in a multi-edit test — the
fake must apply edits one-by-one and leave earlier writes on a mid-batch failure,
mirroring the real partial-failure (D4). Both are traps the fake's fidelity, not
its convenience, must close.

## Migration & compatibility

Purely additive for consumers: add the module and a `WithBackend(configawsssm.
FromClient(client, prefix))` call. No `config` core change. If the adapter ships
read-only first (Open Question 1/2) and later gains write, that is a documented
read→write promotion (the file-adapter model), communicated in the README and a
dated revision here — not a silent capability change.

## Open questions

These are the human's to answer (umbrella D2); they are **not** resolved here.
Each states what was verified so the decision is made on facts, not guesses.

1. **Conflict / CAS — the headline.** *Verified:* `PutParameter` has no
   version/conditional parameter; the only controls are `Overwrite=false`
   (race-free create-if-absent) and `Overwrite=true` (unconditional clobber);
   `types.Parameter.Version` exists and can be captured at Load; there is no
   multi-key transaction; reads are eventually consistent. So the version-at-Load
   trap that `backendconformance` enforces (umbrella D11) has **no atomic write
   to rest on for updates**. The options, with their costs:
   - **(a) Read-only.** Ship `Backend` only; no `WritableBackend`. Honest, safe,
     defers nothing that later cannot be added. `backendconformance`'s
     write/conflict subtests do not apply. This is the umbrella's own default
     posture for a store where write is not clearly needed (umbrella D7).
   - **(b) Read-check-then-write, best effort.** At Commit, re-`Get` each key's
     version, refuse with `ErrConflict` if it moved from Load, else
     `PutParameter(Overwrite=true)`. Detects the common conflict but has an
     irreducible TOCTOU race (and eventual-consistency slack) between check and
     put — so it can *reduce* lost updates, not prevent them. `backendconformance`'s
     conflict subtest would need its expectation framed as best-effort, or the
     adapter would fail an honest run. New-key creates *can* be race-free via
     `Overwrite=false`.
   - **(c) Last-write-wins, documented.** `PutParameter(Overwrite=true)`
     unconditionally; no conflict detection; the README states it. Simplest,
     least safe.
   Which of (a)/(b)/(c)? And if writable, is the residual race acceptable, and
   how is the `backendconformance` conflict subtest reconciled with it (D9)?

2. **Write at all?** Even if a safe-enough write model exists (Open Question 1),
   is writing configuration *back* to Parameter Store a capability this adapter
   should ship in v0.1.0, or is read-first the right first release (umbrella D7,
   the file-adapter default)? Parameter Store is often provisioned by Terraform/
   CloudFormation/a pipeline, and a config-consuming tool writing to it may be
   as surprising as writing to a secrets store.

3. **`SecureString` + `Sensitive` (umbrella D5).** *Verified:* reading a
   `SecureString` with `WithDecryption=true` returns plaintext secret material in
   `Value`; the parameter's `Type` distinguishes it. Options:
   - **Refuse / skip** `SecureString` parameters (read only `String`/`StringList`),
     so no secret ever enters a layer — `Sensitive: false`, and the store is
     treated as plain configuration only.
   - **Read them decrypted and declare `Sensitive: true`** for the whole layer, so
     the core's `ErrSensitiveLeak` guard prevents a decrypted secret being written
     into a plain file (umbrella D5). But then *non-secret* keys in the same prefix
     are also marked sensitive, over-restricting them.
   - **Read them un-decrypted** (`WithDecryption=false`), so `Value` is the
     KMS-ciphertext blob — useless as configuration but leaks nothing.
   And the **mixed-prefix** case: a prefix holding both `String` and
   `SecureString` — is `Sensitive` a per-layer flag (so one secret taints the
   whole prefix) or must SecureString be split to its own backend/prefix? This is
   the umbrella D5 decision made concrete for SSM.

4. **`StringList` representation.** *Verified:* a `StringList` arrives as a
   comma-separated string in `Value` (no spaces after commas). Should the adapter
   (a) split it into a `[]string` leaf, so `Get` returns a real list; or (b) leave
   it the raw comma-joined string and let the consumer split? Splitting is more
   useful but bakes in a comma delimiter and loses round-trip fidelity for a value
   that legitimately contains a comma; leaving it raw is honest to what SSM stores.

5. **Watch: poll or omit, and the default interval.** *Verified:* no native change
   feed; polling `GetParametersByPath` is the only in-SDK option, and it is
   rate-limited (throttling is expected under load, D8). Does v0.1.0 implement
   `WatchableBackend` by polling, or omit `Watch` and let the consumer poll via
   the Store (umbrella D6)? If it polls, what conservative **default interval**
   balances staleness against throttle risk across many prefixes/accounts — and
   the backoff-on-throttle behaviour? No number is invented here.

## Implementation phases

Gated on this spec reaching `status: approved` with the open questions resolved.

**Phase 0 — this spec.** Resolve the open questions above with the human, then
approve.

**Phase 1 — the module, read path.** Scaffold `config-aws-ssm` (README-only, no
microsite; umbrella D2), the narrow `ParamStore` interface, `Wrap`,
`New`/`FromClient`, `Load` with prefix scoping, pagination and flat-to-nested
(D2), the scalar-string value model with `WithValueCodec` (D3), value-type
handling per Open Questions 3–4, and `Capabilities` (D4, with the resolved
`Sensitive`). Fake-`ParamStore` read tests + provenance; run
`backendconformance`'s read contract; `depfootprint` allowlist (D10).

**Phase 2 — write and/or watch, per the resolutions.** Only what Open Questions
1, 2 and 5 approve: `Prepare`/`Verify`/`Commit` over the chosen write model (D9)
and/or `Watch` by polling (D7). Run the applicable `backendconformance` subtests
— the gate that proves the conflict posture is what the spec says it is. Fake
write/conflict and/or poll tests.

**Phase 3 — real-SSM integration.** LocalStack testcontainers suite (D11),
env-gated, and the v0.26.0 DIND CI job (D12). Then publish v0.1.0: the module
`main`, the `config` docs page (`how-to/aws-ssm.md`) bundled into the parent
site, and the landing card — the same rollout every adapter takes.

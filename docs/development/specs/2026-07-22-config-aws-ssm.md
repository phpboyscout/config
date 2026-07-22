---
title: config-aws-ssm — the AWS Systems Manager Parameter Store backend adapter
date: 2026-07-22
author: matt.cockayne
status: approved
approved: 2026-07-22
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
differences forced. Those questions have now been **answered by the human**
(2026-07-22); see the **Resolved** section. In short: **v0.1.0 ships read-only**
— SSM has no compare-and-swap, so the version-at-Load conflict trap cannot be
honoured and read-only is first-class (umbrella D7) — with **poll-based watch**,
`SecureString` read decrypted under a **`Sensitive: true`** layer, and structured
values via an **explicit `WithValueCodec`**. Write support is deferred to a
tracked follow-on (umbrella Phase D).

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

Neither was the adapter's to answer unilaterally. Both were recorded with what
was **verified against the SDK**, the options, and the consequence for the shared
conformance contract, and **decided by the human** (umbrella D2, points 2 and 7):
v0.1.0 is **read-only** (question 1), and `SecureString` is **read decrypted into
a `Sensitive: true` layer** (question 2). The full reasoning is in the **Resolved**
section; the decisions below are amended to match.

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
so `config-aws-ssm` takes no codec dependency of its own. There is **no
auto-decode**: without a codec every value stays a scalar string. Since v0.1.0 is
read-only (D4), `WithValueCodec` applies on the read path only.

Two SSM value **types** need explicit handling before the codec sees them (both
**verified** against the SDK, **resolved** 2026-07-22):

- **`StringList`** arrives as a comma-separated string with no spaces after commas.
  It becomes a **`[]string` leaf** — comma-split — so `Get` returns a real list
  rather than one joined string.
- **`SecureString`** is read **decrypted** (`WithDecryption`, D6) so its plaintext
  is usable as configuration, and its presence makes the whole layer
  `Sensitive: true` (D4) so the core's D5 guard stops that plaintext leaking into
  a plain writable layer beneath.

The value codec is offered only for `String` values (a `StringList` is already a
list; a decrypted `SecureString` is a secret scalar), keeping the
"decode-or-string" fallback unambiguous.

### D4 — Capability: read and poll-watch; not writable; Sensitive

**Resolved 2026-07-22.** `config-aws-ssm` implements `Backend` (read) and
`WatchableBackend` (poll, D7). It does **not** implement `WritableBackend` in
v0.1.0 — SSM has no compare-and-swap, so the version-at-Load conflict trap
(umbrella D11) cannot be honoured, and read-only is a first-class outcome
(umbrella D7). It therefore has no `Prepare`; the Store's routing skips this layer
as a write target (backend.go — capability by the type system, umbrella D4). Its
`Capabilities`:

| Field | Value | Why |
|---|---|---|
| `PreservesComments` | `false` | a parameter store has nowhere to put a comment |
| `AtomicMultiKey` | `false` | **verified**: `PutParameter` is single-key and `DeleteParameters` is partial-success — SSM has no multi-key transaction, unlike Consul's `Txn`. Moot while read-only, but stated honestly for the future write task (D9) |
| `NativeWatch` | `false` | SSM has no change feed; watch is polling (D7) |
| `Sensitive` | **`true`** | the layer reads `SecureString` decrypted (D3, D6); marking it `Sensitive` engages the core's D5 guard so a decrypted secret cannot be written into a plain layer beneath |

`Sensitive: true` is a deliberate divergence from Consul (which is `false`).
Consul KV is general configuration; a Parameter Store prefix may hold
`SecureString` secret material, and once this adapter decrypts it (D3) the layer
genuinely carries secrets — so it declares so, and the guard is meaningful. A
mixed plain/secure prefix is fine: the whole layer is `Sensitive`, which
over-restricts the plain keys in it (they too cannot be routed a write onto a
non-sensitive layer) — an acceptable, safe-by-default cost, and moot in v0.1.0
where nothing under this backend is writable anyway.

### D5 — The constructor injects a narrow client; auth stays with the consumer

The adapter defines its own narrow interface and never takes region, profile,
credentials, endpoint or KMS-key configuration (umbrella D3). The consumer builds
a configured `*ssm.Client` — that is where every credential decision lives,
typically `cfg, _ := config.LoadDefaultConfig(ctx); client := ssm.NewFromConfig(cfg)`
— and hands it in. The proposed narrow seam, mirroring `config-consul`'s `KV`:

**Resolved 2026-07-22.** Because v0.1.0 is read-only (D4) and watch is polling
(D7), the seam is **read-only** — a single method that both `Load` and the poll
loop use. It mirrors `config-consul`'s `KV`, minus the write verbs:

```go
// ParamStore is the slice of SSM this adapter uses. A fake satisfying it drives
// the whole unit suite (D11); the real client is adapted by Wrap.
type ParamStore interface {
	// GetByPath returns every parameter under prefix (recursive, paginated,
	// drained). decrypt requests SecureString plaintext (D3, D6). Each Param
	// carries the Version the poll loop compares to detect change (D7).
	GetByPath(ctx context.Context, prefix string, decrypt bool) ([]Param, error)
}

type Param struct {
	Name    string          // full SSM name, prefix included
	Value   string
	Type    string          // "String" | "StringList" | "SecureString"
	Version int64           // monotonic per name; the poll loop's change marker (D7)
}
```

`Wrap(client *ssm.Client) ParamStore` adapts the real SDK — `GetByPath` drains
`NewGetParametersByPathPaginator` with `WithDecryption`. The common path is
`FromClient(client, prefix, opts...)` = `New(Wrap(client), prefix, opts...)`, so
a consumer writes `configawsssm.FromClient(client, "/app")`. The write verbs
(`Put`/`Delete`, mapping to `PutParameter`/`DeleteParameter`) are deliberately
absent; they are part of the deferred write task (D9, Resolved note 2), which will
widen this interface when it lands.

### D6 — Authentication & configuration model

The consumer supplies a fully-configured `*ssm.Client`; the adapter touches
**none** of: region, credential provider (static, SSO, IMDS, env, profile),
endpoint/URL override (including LocalStack), retry policy, or the KMS key used
to decrypt `SecureString`. All of that is the consumer's `aws.Config`. This keeps
the adapter credential-agnostic and lets the unit suite run with no AWS account
(umbrella D3, D10). The one SSM-shaped choice the adapter itself makes is to pass
`WithDecryption` (resolved: yes, D3), so `SecureString` values arrive as plaintext
into the `Sensitive` layer (D4).

### D7 — Watch is polling, or omitted; latency and throttling stated

SSM has no native change signal (verified), so `NativeWatch` is `false`
(umbrella D6). **Resolved 2026-07-22: poll.** `config-aws-ssm` implements
`WatchableBackend` by calling `GetParametersByPath` on an interval and firing
`onChange` when the set of names, or any parameter's `Version`, has moved from the
snapshot recorded at Load. The `Version` is a monotonic per-name marker (verified,
D5), so the compare is exact — no value diffing. The Store's hybrid watch already
coalesces and settles the resulting foreign change, so the polling backend
inherits debouncing for free, exactly as the umbrella promises for a polled
backend (umbrella D6).

Polling has a real cost SSM makes concrete: `GetParametersByPath` is rate-limited,
and a short interval across many prefixes draws `ThrottlingException`. So the
default interval is **conservative: 60 seconds**. The reasoning: parameter-store
configuration changes infrequently (it is provisioned by a pipeline, not edited
in a hot loop), one recursive `GetParametersByPath` per prefix per minute is
negligible against SSM's per-account rate limits even across many prefixes, and
config staleness of up to a minute is acceptable for the kind of value a parameter
store holds. The poll loop **backs off on throttle** — on `ThrottlingException` it
waits a bounded interval before retrying rather than hammering, the same
busy-loop-avoidance as config-consul's `watchBackoff`. `WithPollInterval(d)`
overrides the 60s default (a consumer who needs fresher config and knows their
rate budget can shorten it; one with many prefixes can lengthen it), mapping to
the Store's `interval` argument.

### D8 — Error & consistency semantics

- **Eventual consistency.** A `PutParameter` is not guaranteed visible to an
  immediately-following `GetParametersByPath`. It is stated plainly to the
  consumer, and it is one more reason the deferred write task (D9) cannot lean on
  a read-check for conflict detection.
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

### D9 — Write semantics and the conflict trap — deferred; v0.1.0 is read-only

**Resolved 2026-07-22: v0.1.0 is read-only, and write is a tracked follow-on**
(Resolved note 2, tied to umbrella Phase D). This is where SSM and Consul part
hardest. The **verified** facts that forced the decision:

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
between the check and the put, made worse by eventual consistency (D8). SSM
cannot honour that subtest the way Consul does. Given that, and that read is most
of the value (umbrella D7), **the human chose read-only for v0.1.0**: no
`Prepare`, no `WritableBackend`, and `backendconformance` runs its **read contract
only** (D11). The layer participates as an ordinary read-only source and routing
skips it as a write target (D4).

The write question is **not closed, it is deferred** to a tracked follow-on
(Resolved note 2). That task must answer two things together: whether SSM write is
**best-effort read-check** (detects most conflicts, documents the residual race)
or **documented last-write-wins**; and **how `backendconformance`'s conflict
subtest should treat a writable backend that has no true CAS** — since the suite's
current assertion presumes a store that can refuse a mid-flight change atomically.
It is tied to the umbrella's **Phase D — revisit** (write support for a backend
that shipped read-only, corrected by dated revision). Landing it widens
`ParamStore` (D5) with the write verbs and flips this decision by a numbered
revision here, never silently.

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
   unit suite: reads, per-key merge, provenance, `StringList` (`[]string` leaf) and
   `SecureString` (decrypted, `Sensitive` layer) handling (D3), the absent-prefix
   case, and the poll watch (a version bump between polls fires `onChange`, D7). No
   AWS, no account, IDE-runnable, the primary suite.
2. **The shared `backendconformance` suite** (`config/backendconformance`,
   v0.6.0) over a `configawsssm` backend on that fake — the **read contract only**,
   since v0.1.0 is read-only (D4, D9): it participates as an ordinary layer with
   per-key merge and provenance, and an absent source is tolerated. The
   write/conflict subtests do not apply and are not run.
3. **LocalStack testcontainers integration** — **verified available**:
   `github.com/testcontainers/testcontainers-go/modules/localstack` **v0.43.0**
   (`localstack.Run(ctx, "localstack/localstack:3")` → `PortEndpoint` on 4566),
   whose endpoint is set as the `aws.Config` `BaseEndpoint` (the consumer-side
   override, D6) to build a real `*ssm.Client` passed through `Wrap`. SSM is a
   core LocalStack service, so real reads — including a `SecureString` seeded and
   read back decrypted (KMS-backed) — exercise the same behaviours against a real
   SSM API surface. These live under `./test/integration/` and are **env-gated on
   `INT_TEST_INTEGRATION`** (config-consul D11), kept compiled and IDE-discoverable
   but out of the ordinary unit job.

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

The exported surface for v0.1.0 (read + poll-watch):

- `func New(store ParamStore, prefix string, opts ...Option) config.Backend` —
  the injection seam; `store` is a fake in tests, a `Wrap`ped client in
  production.
- `func FromClient(client *ssm.Client, prefix string, opts ...Option) config.Backend`
  — the convenience path, `New(Wrap(client), prefix, opts...)`.
- `func Wrap(client *ssm.Client) ParamStore` — adapts the real SSM SDK client to
  the narrow interface.
- `func WithValueCodec(codec config.Codec) Option` — decode `String` values
  through `codec` (D3); omitted, values are scalar strings.
- `func WithPollInterval(d time.Duration) Option` — the watch poll cadence (D7),
  overriding the 60s default.
- `type ParamStore interface { … }`, `type Param struct { … }`,
  `type Option func(…)` — the narrow client seam (D5) and the option type.

The returned backend satisfies `config.WatchableBackend` (poll, D7) but **not**
`config.WritableBackend` (D4, D9); the Store discovers that by type assertion, so
no capability flag is exported (umbrella D4). The deferred write task (D9) will
add `Put`/`Delete` to `ParamStore` and make the backend satisfy
`WritableBackend`. No change to the `config` core is required — v0.6.0 already
carries `backendconformance`, `Sensitive` enforcement and everything a read-only,
watchable, sensitive backend needs.

## Testing strategy

Per D11: a fake-`ParamStore` unit suite (reads, merge, provenance, `StringList`
and `SecureString` handling, absent prefix, poll watch); a `backendconformance.Run`
over the fake, its **read contract only** since v0.1.0 is read-only (D4, D9);
LocalStack testcontainers integration under `./test/integration/`, env-gated,
run on the v0.26.0 DIND job (D12); and a `depfootprint_test.go` allowlist (D10).
What would **falsely pass**: (a) a `SecureString` test where the fake returns
plaintext but the backend never marks the layer `Sensitive` — the test must
assert `Capabilities().Sensitive == true` *and* exercise the core's
`ErrSensitiveLeak` guard against a plain writable layer beneath, or a leak of
decrypted secrets would pass unnoticed (D3, D4); (b) a poll-watch test whose fake
never advances a `Version` — `onChange` would never be provoked, so the watch
would look correct while detecting nothing; the fake must mutate a version out of
band and the test must watch `onChange` actually fire (D7); (c) a `StringList`
test asserting the joined string rather than the `[]string` leaf, which would
enshrine the wrong representation (D3). Each is a trap the fake's fidelity, not
its convenience, must close — and per the module's testing rule, each assertion is
watched to fail before it is trusted.

## Migration & compatibility

Purely additive for consumers: add the module and a `WithBackend(configawsssm.
FromClient(client, prefix))` call. No `config` core change. The adapter ships
read-only (D4, D9); when the deferred write task lands, that is a documented
read→write promotion (the file-adapter model), communicated in the README and a
dated numbered revision here — not a silent capability change. One consumer-facing
note worth stating: because the layer is `Sensitive` (D4), the core's D5 guard
will **refuse** a write to a key this backend defines that would otherwise land in
a plain writable layer beneath, returning `ErrSensitiveLeak`. That is the intended
protection, but it is a behaviour a consumer combining SSM with a writable file
should expect.

## Resolved (2026-07-22)

All open questions were answered by the human on 2026-07-22, and the decisions
above were amended in place to match (no decision renumbered). Each records what
was verified, so the choice rests on facts.

1. **Write scope — read-only for v0.1.0** (amends D4, D9). *Verified:*
   `PutParameter` has no version/conditional parameter — the only controls are
   `Overwrite=false` (race-free create-if-absent) and `Overwrite=true`
   (unconditional clobber); `types.Parameter.Version` can be captured at Load but
   no write consumes it; there is no multi-key transaction; reads are eventually
   consistent. So the version-at-Load conflict trap `backendconformance` enforces
   (umbrella D11) has no atomic write to rest on for updates, and it **cannot be
   honoured**. Rather than ship a best-effort or last-write-wins write with a
   hidden race, **v0.1.0 is read-only** — a first-class outcome (umbrella D7). The
   backend implements `Backend` (and `WatchableBackend`, note 6) but **not**
   `WritableBackend`; it has no `Prepare`; routing skips it as a write target
   (D4). `backendconformance` runs its **read contract only** (D11). `AtomicMultiKey`
   stays `false` (verified — no transaction), moot while read-only but stated
   honestly.

2. **Write support is a tracked follow-on** (documents D9; tied to umbrella
   **Phase D — revisit**). Write is deferred, **not abandoned**. A future task
   must decide, together: whether SSM write is **best-effort read-check-then-write**
   (re-`Get` the version at Commit, refuse with `ErrConflict` if it moved, then
   `PutParameter(Overwrite=true)`; detects most conflicts but has an irreducible
   TOCTOU race worsened by eventual consistency) or **documented last-write-wins**;
   *and* how `backendconformance`'s conflict subtest should treat a **writable
   backend with no true CAS** (its current assertion presumes a store that can
   refuse a mid-flight change atomically — a non-CAS writable backend needs a
   defined, weaker expectation). New-key creates *can* be made race-free via
   `Overwrite=false`, so a partial write capability is conceivable. Landing this
   flips D9/D4 by a dated numbered revision here and widens `ParamStore` (D5) with
   `Put`/`Delete`. Recorded so the question is not lost.

3. **`SecureString` — read decrypted, layer `Sensitive: true`** (amends D3, D4,
   D6). *Verified:* `WithDecryption=true` returns plaintext in `Value`; `Type`
   distinguishes it. The adapter reads `SecureString` **with** decryption so the
   plaintext is usable configuration, and declares the whole layer
   `Capabilities.Sensitive: true`, so the core's D5 guard refuses any write that
   would leak a decrypted secret into a plain writable layer beneath
   (`ErrSensitiveLeak`). A **mixed** plain/`SecureString` prefix is fine — the
   whole layer is `Sensitive`, which over-restricts the plain keys in it (a safe,
   accepted cost, and moot in read-only v0.1.0). No splitting of secure keys to a
   separate backend is required.

4. **Value format — explicit `WithValueCodec` only** (confirms D3). *Verified:*
   an SSM parameter carries no format hint. Structured decoding is opt-in via
   `WithValueCodec` (umbrella R1), consistent with config-consul; there is **no
   auto-decode**. It applies on the read path (the only path in v0.1.0).

5. **`StringList` — a `[]string` leaf** (amends D3). *Verified:* a `StringList`
   arrives comma-separated with no spaces. The adapter comma-splits it into a real
   `[]string` leaf so `Get` returns a list, rather than leaving it a joined string.

6. **Watch — poll `GetParametersByPath`, 60s default** (amends D7). *Verified:* no
   native change feed; polling is the only in-SDK option and is rate-limited. The
   adapter implements `WatchableBackend` by polling, `NativeWatch: false`, firing
   `onChange` when a name set or `Version` moves from Load. The default interval is
   **60 seconds** — conservative because parameter-store config changes
   infrequently and SSM rate limits are real; one recursive call per prefix per
   minute is negligible and up-to-a-minute staleness is acceptable. The loop backs
   off on `ThrottlingException`; `WithPollInterval` overrides the default.

7. **Testing — as drafted** (confirms D11, D12). Fake `ParamStore` unit suite +
   `backendconformance` **read contract only** (read-only, note 1) + env-gated
   LocalStack integration on the go-test v0.26.0 DIND job.

**No open questions remain.**

## Implementation phases

**Phase 0 — this spec.** Approved 2026-07-22; open questions resolved above.

**Phase 1 — the module, read path.** Scaffold `config-aws-ssm` (README-only, no
microsite; umbrella D2), the narrow read-only `ParamStore` interface, `Wrap`,
`New`/`FromClient`, `Load` with prefix scoping, pagination and flat-to-nested
(D2), the scalar-string value model with `WithValueCodec` and the `StringList`
→ `[]string` / `SecureString`-decrypted handling (D3), and `Capabilities` (D4:
`Sensitive: true`). Fake-`ParamStore` read tests + provenance, including a
`SecureString` + `ErrSensitiveLeak` test; run `backendconformance`'s read
contract; `depfootprint` allowlist (D10).

**Phase 2 — poll watch.** `Watch` over the 60s-default poll loop with
throttle-backoff (D7), `WithPollInterval`. Fake poll tests: a version bump
between polls fires `onChange`; a quiet poll does not.

**Phase 3 — real-SSM integration.** LocalStack testcontainers suite (D11),
env-gated, and the v0.26.0 DIND CI job (D12) — including a seeded `SecureString`
read back decrypted. Then publish v0.1.0: the module `main`, the `config` docs
page (`how-to/aws-ssm.md`) bundled into the parent site, and the landing card —
the same rollout every adapter takes.

**Phase D (future) — write support.** The deferred write task (Resolved note 2),
gated on its own dated revision here answering the non-CAS-conflict question and
`backendconformance`'s treatment of a writable backend without true CAS.

---
title: config-aws-secrets — the AWS Secrets Manager backend adapter
date: 2026-07-25
author: matt.cockayne
status: approved
approved: 2026-07-25
---

# config-aws-secrets

The second secrets manager, after [config-vault](2026-07-22-config-vault.md), and
the one that shows the Phase B family is not uniform. It cites the [dynamic
backend adapters umbrella](2026-07-21-dynamic-backend-adapters.md) and settles
the nine things the umbrella leaves to each adapter (**D2**).

Vault established the shape: read-only, statically `Sensitive`, polled, injected
client. AWS Secrets Manager keeps all of that and diverges in three measured
ways, each of which makes it *better* rather than merely different — and one of
which reverses a Vault decision outright.

## Problem

A tool configured from AWS Secrets Manager cannot use this module today. The
`Sensitive` machinery it needs already exists and is proven — `config-vault`
shipped it — so this adapter is not breaking new ground on safety. What it needs
is its own answer to the per-system questions, and for Secrets Manager those
answers differ from Vault's more than the family's symmetry would suggest.

All decisions below are grounded in measurement against `secretsmanager` v1.44.0
and a real LocalStack 4.0 on 2026-07-25, not inferred from documentation.

## Decisions

### D1 — Module `config-aws-secrets`, package `configawssecrets`, `config` v0.7.0+

Cloud-qualified (umbrella D1): the same purpose exists under three vendors, so
the cloud is part of the name. The module is
`gitlab.com/phpboyscout/go/config-aws-secrets`, package `configawssecrets`, README
only, spec here (umbrella D2).

`config` **v0.7.0+**, for the same reason `config-vault` needs it: v0.7.0 is the
release whose `backendconformance` requires a sensitive read-only backend to
refuse the routed-beneath write with `ErrSensitiveLeak`. Against v0.6.0 this
adapter would be tested against the superseded contract.

### D2 — The SDK is `aws-sdk-go-v2/service/secretsmanager`, and it is the leanest yet

Measured with `go list -deps` over a package importing `secretsmanager` v1.44.0:

```
github.com/aws/aws-sdk-go-v2
github.com/aws/aws-sdk-go-v2/internal/configsources
github.com/aws/aws-sdk-go-v2/internal/endpoints/v2
github.com/aws/aws-sdk-go-v2/service/secretsmanager
github.com/aws/smithy-go
```

**Five modules** — the smallest footprint of any backend adapter in the family
(Vault 17, `config-gcp-parameter` ~25, GCP Secret Manager 31). The AWS SDK's
per-service split is doing exactly what the umbrella's D9 hoped for: a consumer
reading secrets does not acquire the rest of AWS. Pinned exactly by a
`depfootprint` allowlist, counted in both directions.

Note what is *absent*: `aws-sdk-go-v2/config` and `credentials`. Those are how a
consumer builds a client, and the consumer keeps them (D7). They appear in this
module's test and example graph, never its library graph.

### D3 — Data model: names are paths, and a prefix nests — prefix mode is the DEFAULT

Verified against LocalStack: a Secrets Manager name accepts `/`, `.`, `_` and `-`,
so `app/db/password` is an ordinary name. The store has a real hierarchy, and the
Vault/Consul prefix→tree model transfers directly. Given prefix `app/`:

```
app/db/password   = "s3cr3t"          db:    { password: s3cr3t }
app/cache/url     = "redis://…"   →   cache: { url: "redis://…" }
app/region        = "eu-west-2"       region: eu-west-2
```

**Prefix mode is the default here, reversing `config-vault` D3 — deliberately.**
Vault made the tree walk opt-in because it costs one request per directory plus
one per secret, and demands a `list` capability least-privilege policies withhold.
Neither applies to Secrets Manager: `BatchGetSecretValue` accepts the same
name-prefix filter `ListSecrets` does and returns **every matching secret's value
in one call** (verified — one request returned three secrets across two directory
levels). There is no N+1 to avoid, so there is no reason to make the natural model
opt-in.

Single-secret mode still exists, because one secret holding a whole JSON document
is *the* idiomatic AWS shape — an RDS-managed secret is exactly that. It is
`NewSecret`/`FromClientSecret` rather than the default constructor.

This is worth stating plainly rather than smoothing over: **two adapters in the
same family default to opposite modes, because the same abstraction costs
different amounts in each system.** Uniformity of naming, not of behaviour.

### D4 — `SecretString` is a scalar unless a codec is injected

Secrets Manager stores an opaque string, so by default a secret's value is a
**scalar string** at its path, and the typed accessors coerce it — the
`config-consul` model exactly.

Unlike Vault, this adapter **does** offer `WithValueCodec` (umbrella R1), and the
reason is a convention rather than a capability: AWS's own tooling writes JSON
objects into `SecretString` (the console's key/value editor, RDS and
Redshift-managed secrets). A consumer whose secrets are JSON passes
`configjson.Codec{}` and each blob becomes a subtree, with the decode-or-string
fallback covering a prefix that mixes both. Vault needed none of this because it
returns already-structured JSON; here the structure is inside a string, which is
precisely the case R1 exists for.

### D5 — `SecretBinary` is skipped, with the layer still loading

A secret carries **either** `SecretString` **or** `SecretBinary` — verified: a
binary secret returns `SecretString` nil and raw bytes in `SecretBinary`.

Binary secrets are **skipped**: they contribute no key, and the rest of the prefix
loads normally. A certificate or keystore is not configuration a `View` can serve,
and the alternatives are worse — base64-encoding invents a representation the
consumer did not ask for, and refusing the whole Load would let one unrelated
binary secret in a shared prefix break an application's startup. Skipping is
recorded in the adapter's documentation so it is not silent to a reader, even
though it is silent at runtime.

### D6 — A partial read refuses the Load

`BatchGetSecretValue` does not fail when it cannot read one of the secrets it
matched. It returns the successes in `SecretValues` and the failures in a separate
`Errors` slice — `{SecretId, ErrorCode, Message}` — most often for a secret the
caller's IAM policy excludes or whose KMS key it cannot use.

**Any entry in `Errors` fails the Load**, naming the secrets and their error
codes. This is the family's refuse-ambiguity stance (`config-vault` D5,
`config-xml` D21) applied to a new shape: a partial read is a configuration that
is *missing keys it should have*, and for a secrets backend the missing key is a
password. An application that starts with a silently absent credential fails later,
further from the cause, and possibly by falling back to a default that is worse
than not starting.

### D7 — Client injected; credentials, region and endpoint stay with the consumer

Per umbrella D3, the adapter defines its own narrow interface and never builds a
client, resolves credentials, or reads the environment:

```go
// SecretsAPI is the slice of Secrets Manager this adapter uses.
type SecretsAPI interface {
	// GetSecret reads one secret's current value. It returns a nil Secret and
	// a nil error when the secret does not exist.
	GetSecret(ctx context.Context, name string) (*Secret, error)

	// ListSecrets reads every secret under prefix, in as few calls as the
	// service allows. Partial failures are returned, not swallowed (D6).
	ListSecrets(ctx context.Context, prefix string) (secrets []Secret, failures []Failure, err error)
}

type Secret struct {
	Name    string
	Value   string // SecretString; empty when the secret was binary (D5)
	Binary  bool   // the secret held SecretBinary and was skipped
	Version string // VersionId — the poll's change marker (D9)
}

type Failure struct{ Name, Code, Message string }
```

`Wrap(*secretsmanager.Client) SecretsAPI` adapts the real client. IAM — instance
roles, IRSA, SSO, static keys — is entirely the consumer's, which is why every
AWS auth mechanism works without this adapter knowing any of them. Region and
endpoint likewise live on the client, so LocalStack needs no adapter support.

Constructors mirror `config-vault`'s four, with the modes swapped per D3:
`New`/`FromClient` take a prefix; `NewSecret`/`FromClientSecret` take one name.

### D8 — Capability: read-only, statically `Sensitive: true`, polled

| Capability | Value | Why |
|---|---|---|
| `Sensitive` | **`true`**, statically | It is a secrets manager; every value is a secret |
| Writable | *not implemented* | Umbrella D7 — secrets are read-only by default |
| `NativeWatch` | `false` | No change feed; watch polls (D9) |
| `AtomicMultiKey` | `false` | No writes |
| `PreservesComments` | `false` | Not a file format |

Statically sensitive, like Vault and unlike `config-aws-ssm`. SSM is dynamic
because it is a *mixed* store where most parameters are ordinary configuration;
Secrets Manager is not mixed, and a service named "Secrets Manager" declaring
itself conditionally sensitive would be indefensible.

Secrets Manager *can* be written (`PutSecretValue`, verified to move `VersionId`),
so read-only here is policy rather than capability — the same distinction
`config-vault` draws. Write support is a Phase D question with its own spec.

### D9 — Watch polls at 60 seconds, comparing `VersionId`

No change feed, so `WatchableBackend` by polling (umbrella D6). Each secret's
**`VersionId`** is the change marker: verified to change on every
`PutSecretValue`, and a rotation — the thing most likely to change a secret in
this service — is exactly that. A secret appearing or disappearing under the
prefix is also a change, so the comparison is over the whole name→version map,
as `config-vault`'s prefix mode does.

**60 seconds** by default, `WithPollInterval` to override, matching the whole
polled family. The cost here is billing rather than audit noise: Secrets Manager
charges per 10,000 API calls, and `BatchGetSecretValue` makes a poll one request
rather than one per secret, so the default is affordable in a way Vault's prefix
poll is not. Throttling backs off rather than retrying immediately.

### D10 — Staging labels are selectable, and doing so costs the batch read

Secrets Manager labels versions — `AWSCURRENT` is what an application should read,
`AWSPREVIOUS` is the version before it, and during a rotation the difference
matters. `WithVersionStage(stage)` selects one; omitted, the adapter reads
`AWSCURRENT`.

It carries a cost that is easy to miss and must not be discovered during
implementation. Measured on the SDK surface: `BatchGetSecretValueInput` has only
`Filters`, `SecretIdList`, `MaxResults` and `NextToken` — **there is no
`VersionStage` parameter**. Only `GetSecretValueInput` has one. So:

| Mode | Requests for a prefix of *n* secrets |
|---|---|
| default (`AWSCURRENT`) | **one** batch call |
| `WithVersionStage(…)` | one list + **one `GetSecretValue` per secret** |

Selecting a stage therefore reintroduces exactly the N+1 that D3 cites as the
reason prefix mode can be the default. That does not invalidate D3 — the default
path keeps the batch read, and the cost is paid only by a consumer who opts into
a non-current stage — but it does mean the option is not free, and the
documentation says so at the point of use rather than leaving it to be measured
in a bill.

The change marker adapts with it: `VersionId` still identifies what was read
(D9), and the poll compares whatever the configured stage currently resolves to,
so a rotation moving `AWSCURRENT` is a change under the default and moving
`AWSPREVIOUS` is a change under that stage.

### D11 — Testing: a fake, `backendconformance`, and LocalStack in CI

Per umbrella D10 and the discipline `config-vault` settled:

- a fake `SecretsAPI` drives the unit suite — reads, prefix nesting, codec
  decoding, binary skipping, partial-failure refusal, provenance and the poll;
- **`backendconformance.Run`** is the gate, asserting the sensitive read-only
  branch (`ErrSensitiveLeak`) added by umbrella R2;
- **LocalStack** integration under `./test/integration/`, gated on
  `INT_TEST_INTEGRATION`, on the DIND job (`testcontainers-go/modules/localstack`
  v0.43.0, verified working against `localstack/localstack:4.0` on this machine).

The integration suite is **not optional**, and `config-vault` is why: its unit
fake was built to the same assumptions as its spec, so only a real server could
falsify them — and it did, finding that Vault rounds integers above 2^53. The
equivalent risk here is LocalStack's *own* fidelity: it is an emulator, so where
this suite proves a behaviour that matters (the prefix filter's exact matching
semantics, partial-failure shape), the spec says so and the risk is stated rather
than assumed away.

### D12 — Documentation ships with each phase, and the work is test-first

Carried forward from `config-vault` D15 and D16, which earned their place:

**Docs per phase.** `config-vault` deferred nothing and still shipped a doc bug
in v0.1.0 by describing an API a later phase would add. Each phase here ships its
own README section, and the config-site page lands with the release. The
`TestDocLinksResolve` guard that adapter grew is copied into this module, because
the defect it catches was a family pattern rather than a one-off.

**Test-first, assertions watched to fail.** Every contract becomes a failing test
before the code, and each is falsified before being trusted. On `config-vault`
that caught thirteen of fourteen mutations, and the one it missed was reported
rather than quietly counted.

**BDD suitability: no Gherkin in the adapter** — pure library logic behind a
narrow injected interface, whose wired-together contract is already
`backendconformance`. Unchanged from `config-vault` D16, and for the same reasons.

## Rejected alternatives

**Make prefix mode opt-in, for symmetry with `config-vault`.** Rejected (D3):
Vault's reason was N+1 and a `list` grant, and `BatchGetSecretValue` removes both.
Copying the shape while discarding the reason is cargo-culting; the family shares
naming and safety conventions, not costs that do not apply.

**Base64-encode `SecretBinary` into the layer.** Rejected (D5): it invents a
representation nobody asked for, and a `View` serving a base64 blob as a string is
a worse outcome than the key being absent and documented.

**Refuse the Load on a binary secret.** Rejected (D5): one unrelated binary secret
in a shared prefix would break an unrelated application's startup. Refusal is
reserved for genuine *ambiguity* (D6), not for data this layer simply cannot
carry.

**Tolerate partial reads, logging the failures.** Rejected (D6): a config layer
missing a password is not a degraded state a library should paper over, and the
module has no logger of its own to report it through.

**Auto-detect JSON in `SecretString`.** Rejected, as it was for `config-consul`:
`"123"` and `"true"` are valid JSON scalars, so detection guesses at a format the
store does not declare. `WithValueCodec` is explicit (D4).

**Leave staging labels out until someone asks.** The family's usual
no-speculative-surface instinct, and it was the draft's proposal. Rejected in
review (D10): reading `AWSPREVIOUS` during a rotation is a concrete, foreseeable
need rather than a hypothetical one, and the option is small. What it is *not* is
free — it costs the batch read (D10), which is the reason it needed deciding
rather than assuming.

## Public API

- `func New(api SecretsAPI, prefix string, opts ...Option) config.Backend`
- `func NewSecret(api SecretsAPI, name string, opts ...Option) config.Backend`
- `func FromClient(client *secretsmanager.Client, prefix string, opts ...Option) config.Backend`
- `func FromClientSecret(client *secretsmanager.Client, name string, opts ...Option) config.Backend`
- `func Wrap(client *secretsmanager.Client) SecretsAPI`
- `func WithValueCodec(codec config.Codec) Option` (D4)
- `func WithPollInterval(d time.Duration) Option` (D9)
- `func WithVersionStage(stage string) Option` (D10) — default `AWSCURRENT`
- `const DefaultPollInterval = time.Minute`
- `const SourceKind = config.SourceKind("aws-secrets")`
- `var ErrPartialRead` — the D6 refusal, matchable with `errors.Is`
- `type SecretsAPI`, `type Secret`, `type Failure`, `type Option`

Read-only: the backend satisfies `config.WatchableBackend` and **not**
`config.WritableBackend`. No `config` core change is required.

## Testing strategy

Per D11, test-first per D12. What would falsely pass, and is therefore watched to
fail explicitly:

- a partial-failure test whose fake returns no `Errors` — it would pass whether or
  not the refusal exists;
- a prefix test whose fake ignores the prefix, so scoping is never exercised;
- a binary test asserting the key is absent without asserting the *rest of the
  layer still loaded* — skipping and refusing look identical otherwise;
- a `Sensitive` assertion made only through conformance: a backend reporting
  `false` would still satisfy a routing test by routing beneath, so
  `Capabilities()` is asserted directly as well;
- a watch test asserting "fired at least once", which cannot distinguish a
  working change detector from one that signals on every tick.

## Migration & compatibility

Purely additive. No `config` core change, no breaking change. Ships v0.1.0
read-only; a later write capability would be a documented minor-version promotion
(non-YAML format adapters D18) with its own dated revision here.

Consumers should expect the `ErrSensitiveLeak` behaviour `config-vault`
introduced: with a Secrets Manager layer present, `Set` on a key it provides is
refused rather than written to the file beneath.

## Resolved (2026-07-25)

1. **Prefix mode is the default** (D3), reversing `config-vault`. Confirmed in
   review: Vault made its walk opt-in because of N+1 and a `list` grant, and
   `BatchGetSecretValue` removes both. The family shares naming and safety
   conventions, not costs that do not apply to a given system.
2. **`SecretBinary` is skipped and documented** (D5). Refusing would let one
   unrelated binary secret break an application's startup, and base64 would invent
   a representation nobody asked for. Silent at runtime, stated in the docs.
3. **Staging labels ship at v0.1.0** as `WithVersionStage` (D10) — reversing the
   draft's deferral, because reading `AWSPREVIOUS` during a rotation is a concrete
   need. Probing the decision surfaced its cost: `BatchGetSecretValue` has **no**
   `VersionStage` parameter, so selecting a stage falls back to one
   `GetSecretValue` per secret and reintroduces the N+1 the default avoids. The
   default path is unaffected; the option documents its cost at the point of use.

All open questions resolved.

## Implementation phases

Each phase ships code **and** its documentation (D12), test-first.

**Phase 0 — this spec.** Approve it, resolving the questions above.

**Phase 1 — prefix read.** Scaffold, `SecretsAPI`/`Wrap`, `New`/`FromClient`,
`Load` over `BatchGetSecretValue` with prefix nesting, `SecretBinary` skipping
(D5), partial-failure refusal (D6), static `Sensitive` (D8), provenance,
`depfootprint` allowlist (D2). **Docs:** README with the footprint and the IAM
note.

**Phase 2 — single-secret mode, codec, staging and watch.**
`NewSecret`/`FromClientSecret`, `WithValueCodec` (D4), `WithVersionStage` with its
per-secret read path (D10), polled `Watch` (D9). Run `backendconformance` over the fake
— the gate. **Docs:** README's JSON-secret section.

**Phase 3 — LocalStack integration, docs and release.** testcontainers suite on
the DIND job (D11), then `how-to/aws-secrets.md`, the ecosystem matrix, homepage
and landing card, and v0.1.0.

---
title: config-azure-keyvault — the Azure Key Vault secrets backend adapter
date: 2026-07-25
author: matt.cockayne
status: approved
approved: 2026-07-25
---

# config-azure-keyvault

The third secrets manager, after [config-vault](2026-07-22-config-vault.md) and
[config-aws-secrets](2026-07-25-config-aws-secrets.md). It cites the [dynamic
backend adapters umbrella](2026-07-21-dynamic-backend-adapters.md) and settles
the nine things the umbrella leaves to each adapter (**D2**).

It is the first Phase B adapter whose store **cannot express a hierarchy at all**,
and the first this family has built with **no way to run the real service
locally**. Both change what can be specified honestly, and both are stated rather
than worked around.

## Problem

Key Vault is where a great many Azure applications keep their credentials, and
this module cannot read it. The `Sensitive` machinery is proven — two adapters
ship it — so the safety story is not new. What is new is that the two things this
family has leaned on for secrets adapters are both absent here.

**There is no hierarchy.** A Key Vault secret name is `1-127` characters of
`0-9`, `a-z`, `A-Z` and `-` only (authoritative: [Key Vault object
identifiers](https://learn.microsoft.com/en-us/azure/key-vault/general/about-keys-secrets-certificates)).
No dots, no slashes, no underscores. The prefix→tree model every previous backend
adapter uses has nothing to split on, and there is no character available to
reserve as a separator that is not also legal in an ordinary name.

**There is no emulator.** Consul, Vault and AWS were each proven against a real
service in CI, and each time that suite caught something the unit fake could not.
Key Vault has no official emulator, so that check is not available at the same
cost, and the spec says where its evidence is weaker rather than implying parity.

Facts below are labelled by how they were established: **measured** from the SDK
on 2026-07-25, **documented** by Microsoft, or **unverified** — behaviour that
could not be probed and must be confirmed against a real vault before release.

## Decisions

### D1 — Module `config-azure-keyvault`, package `configazurekeyvault`

Cloud-qualified (umbrella D1). README only, spec here (umbrella D2).

It requires `config` **v0.9.2+** rather than the v0.7.0 floor its siblings quote.
That is [config-aws-secrets R2](2026-07-25-config-aws-secrets.md) applied up
front: v0.7.0 is the floor for the *feature*, but building at a bare floor runs an
older `backendconformance` — at v0.7.0 five subtests instead of six — so the
contract goes partly unexercised while everything stays green. Pin the current
release and check the subtest count against a sibling.

### D2 — The SDK is `azsecrets`, and `azidentity` is deliberately absent

**Measured** — `go list -deps` over a package importing
`sdk/security/keyvault/azsecrets` v1.5.0 yields **six** modules: `azsecrets`,
`azcore`, `internal`, and `golang.org/x/{net,text}` plus `internal/errorinfo`
transitively.

`azidentity` is **not** among them, and that is the point. Credentials are the
consumer's (D6), so the module that resolves them stays in the consumer's graph —
the same split `config-azure-appconfig` established. A `depfootprint` allowlist
pins the count in both directions, and includes an explicit assertion that
`azidentity` is absent, because its arrival would mean the adapter had started
authenticating.

### D3 — Data model: flat keys, verbatim — and an opt-in document mode

The naming rule (**documented**) leaves three options, and only one is honest.

A **separator convention** — mapping `-` to `.` so `app-db-password` nests — is
rejected. `-` is legal in ordinary names, and Key Vault offers no second
character to escape with, so `my-service-key` would silently become three levels
of nesting with no way to say you meant it literally. That is the guessing this
family refuses elsewhere (config-vault D5, config-xml D21), and it would be
guessing on every key rather than on a rare collision.

So **a secret's name is a config key, verbatim**:

```
db-password    = "s3cr3t"          store.View().GetString("db-password")
api-key        = "k-123"      →    store.View().GetString("api-key")
```

The store is flat, so the layer is flat. A consumer who wants structure uses the
second mode: `NewSecret(api, name, codec)` reads **one** secret whose value is a
whole document, decoded through a `config.Codec` — the same shape
`config-aws-secrets` and `config-gcp-parameter` offer, and the same reason the
codec is a parameter rather than an option there (a single opaque string has no
tree without one).

Both modes are exported because they answer different questions: "read my vault"
and "read my config document, which happens to live in a vault".

### D4 — Reading a whole vault is N+1, and that is the service, not the adapter

**Measured**, and the most consequential thing about this adapter:
`SecretProperties` — what the list pager returns — has `Attributes`, `ContentType`,
`ID`, `Tags` and `Managed`, and **no `Value`**. Values come only from
`GetSecret`, one call per secret.

So reading a vault of *n* secrets is **one list plus n gets**, unavoidably. There
is no batch equivalent of AWS's `BatchGetSecretValue`. This is the reverse of
`config-aws-secrets`, where the batch read is what made prefix mode the default,
and it is the same shape as `config-vault`'s prefix walk.

Two consequences, both stated in the documentation rather than left to be
discovered in a bill or a latency graph:

- `WithNamePrefix(prefix)` filters **client-side, after the listing**. It cannot
  save the list call — the service has no server-side name filter — but it does
  avoid fetching every secret in a shared vault, which is the cost that scales.
- The watch (D8) pays the same N+1 per poll, which is why its default interval is
  the family's slowest.

The single-secret mode costs one call, and for a vault shared with unrelated
applications it is the cheaper design as well as the tidier one.

### D5 — Skipped: managed secrets, and secrets the service will not return

Three states a listed secret can be in where reading it is not useful, all
**documented**:

**Managed secrets** (`Managed: true`) back a Key Vault *certificate* — creating a
certificate also creates an addressable secret of the same name holding the PFX
or PEM. That is key material, not configuration a `View` can serve, so it is
**skipped**, exactly as `config-aws-secrets` skips a binary secret. The rest of
the vault loads normally: one certificate must not break an unrelated
application's startup.

**Disabled secrets** (`Enabled: false`) cannot be retrieved — Microsoft is
explicit that retrieval is permitted only when `enabled` is true. They are
**skipped at the listing**, before a `GetSecret` that would fail. Skipping rather
than failing is deliberate: an operator disabling a secret has said "do not use
this", and turning that into a startup failure for every application sharing the
vault would punish the wrong party.

**Expired secrets** are the surprise, and are covered separately in D6 because
the service disagrees: it serves them, and the adapter still does not. Key
Vault's other informational attribute, `nbf`, is deliberately *not* a skip — see
R1.

### D6 — An expired secret is skipped, and the layer is "secrets fit to use"

**Documented, and contrary to the obvious assumption:** `exp` and `nbf` are
**informational only** in Key Vault. Microsoft states that a get "works for
not-yet-valid and expired secrets, outside the `nbf`/`exp` window" — reading an
expired secret is explicitly supported, for recovery.

So the service *will* hand back an expired password. The adapter **skips it
anyway**, together with the disabled and managed secrets of D5.

The draft proposed the opposite — serve it, and match the service — on the
grounds that withholding a value the store returns invents policy. That was
rejected in review, and the reasoning holds up better than the proposal did: an
operator who set an expiry said something about that secret, and a configuration
library is exactly the wrong place to quietly override them. "Informational" is a
statement about what the *service* enforces, not about what the operator meant.

Taken together the three skips are one rule rather than three special cases, and
it is the rule worth stating in the documentation: **the layer contains the
secrets that are fit to use.** Not managed key material, not what an operator
disabled, not what they expired.

The consequence must be documented prominently, because it is genuinely
surprising and this adapter is the only one where it can happen: **a key can
disappear from the configuration while the secret still exists and the service
would still serve it.** An application that was working can stop finding a key
because a date passed. The how-to and README therefore say, in as many words,
that a key going missing is worth checking the secret's expiry for — that is a
diagnosis nobody arrives at unaided.

### D7 — Client injected; credentials and vault URL stay with the consumer

Per umbrella D3. The consumer builds an `azsecrets.Client` from a vault URL and
an `azcore.TokenCredential` — managed identity, workload identity, a service
principal, `DefaultAzureCredential` — and hands it over. The adapter defines:

```go
type SecretsAPI interface {
	// Get reads one secret's current value. It returns a nil Secret and a nil
	// error when the secret does not exist.
	Get(ctx context.Context, name string) (*Secret, error)

	// List returns every secret's metadata — not its value, which the service
	// does not include in a listing (D4).
	List(ctx context.Context) ([]Properties, error)
}

type Secret struct {
	Name        string
	Value       string
	Version     string // the poll's change marker (D8)
	ContentType string // a hint the consumer may act on; never auto-decoded
}

type Properties struct {
	Name        string
	Version     string
	Enabled     bool
	Managed     bool
	ContentType string
}
```

`Wrap(*azsecrets.Client) SecretsAPI` adapts the real client. `ID.Name()` and
`ID.Version()` (**measured**) supply the name and version from the identifier the
service returns.

### D8 — Capability: read-only, statically `Sensitive: true`, polled at 5 minutes

| Capability | Value | Why |
|---|---|---|
| `Sensitive` | **`true`**, statically | It is a key vault; every value is a secret |
| Writable | *not implemented* | Umbrella D7 |
| `NativeWatch` | `false` | No change feed; watch polls |
| `AtomicMultiKey` | `false` | No writes |

The change marker is each secret's **version**, which Key Vault mints afresh on
every write, so a rotation registers — as does a secret appearing or disappearing,
since the comparison is over the whole name→version map.

**The default poll interval is five minutes, not the family's sixty seconds.**
That is D4's consequence made concrete: a poll re-walks the vault at one list plus
one get per secret, so a sixty-second cadence over a fifty-secret vault is 51
requests a minute, indefinitely. Five minutes is the honest default for a store
that cannot be polled cheaply; `WithPollInterval` overrides it either way.

### D9 — `ContentType` is surfaced, never acted on

Key Vault carries a free-text `ContentType` (**documented**: a hint, 255
characters, no predefined values). It is exposed on `Secret` so a consumer can
read it, and **never** used to choose a decoder.

This is the family convention, settled when the parameter stores were specified:
format hints are surfaced, never auto-decoded. Deciding to parse a value because
a free-text field happens to read `application/json` is guessing at a format the
store does not enforce, and the failure lands at startup on a value the consumer
may not even use. `WithValueCodec` is explicit.

### D10 — Testing: a fake, `backendconformance`, and real-service integration only

Per umbrella D10, with an honest gap.

The unit suite runs against a fake `SecretsAPI` — flat keys, document mode,
managed and disabled skipping, the N+1 shape, provenance and the poll — and
**`backendconformance.Run`** is the gate, asserting the sensitive read-only
`ErrSensitiveLeak` branch. Both are as strong here as anywhere.

The integration suite is **real-service only**, following
`config-azure-appconfig`. It lives under `./test/integration/`, is gated on an
environment variable *and* the presence of `AZURE_KEYVAULT_URL`, and is skipped —
not failed — when neither is set, so it stays compiled and IDE-discoverable
without a credential.

**`lowkey-vault` was considered and rejected.** It is a third-party Key Vault
emulator and it exists, but nothing in this family has verified its fidelity, and
an integration suite whose emulator is itself unverified would produce green runs
that mean less than they appear to — which is worse than an honestly absent
suite. If someone later validates it against a real vault, that is a revision
worth making.

The consequence must be stated plainly rather than buried: **the runtime
behaviours in D5 and D6 are documented, not observed.** For Vault and AWS the
integration suite caught things the fake could not — Vault rounding integers above
2^53, the exact prefix-matching boundary — and precisely that check is missing
here.

**So v0.1.0 is gated on running the integration suite against a real Key Vault.**
The code can be written, reviewed and merged without one; the tag waits. Every
adapter in this family so far has been proven against the real service before
release, each time catching something the fake could not, and shipping the first
one that was not would quietly lower the bar for the ones after it.

The specific claims to confirm, which the suite asserts directly:

| Claim | Source |
|---|---|
| A disabled secret appears in the listing with `Enabled: false` | D5 |
| A managed (certificate-backed) secret appears with `Managed: true` | D5 |
| An expired secret's get **succeeds** — so skipping it is the adapter's choice, not the service's | D6 |
| A listing returns no values, forcing the per-secret get | D4 |
| `ID.Name()` and `ID.Version()` parse the identifier the service actually returns | D7 |

If any turns out false, it is a dated revision here before release — which is the
point of writing them down as claims rather than as assumptions.

### D11 — Documentation ships with each phase, and the work is test-first

Carried forward (config-vault D15/D16, config-aws-secrets D12), including the
`TestDocLinksResolve` guard — which has now caught the same class of defect in two
successive adapters, so it is copied in as a matter of course.

**Test-first, assertions watched to fail.** Recorded here because the discipline
slipped in `config-aws-secrets` Phase 2, where the code preceded its tests;
falsification covered it, but the ordering is part of the practice and this
adapter holds to it.

**BDD suitability: no Gherkin in the adapter.** Unchanged reasoning — pure library
logic behind a narrow injected interface, whose wired-together contract is
`backendconformance`. The core's own sensitive-leak scenarios landed separately
and cover the guard this adapter relies on.

## Revisions

### R1 (2026-07-25) — `NotBefore` is deliberately not acted on (extends D6)

D6 settled expiry and named the rule the three skips share — the layer holds the
secrets fit to use — but said nothing about `nbf`, the other attribute Key Vault
treats as informational. Building Phase 1 surfaced the omission: the skip rule
checked `exp` and silently ignored `nbf`, which would have been an accident of
implementation rather than a decision, and the next person to read the rule would
reasonably have "fixed" it.

**`nbf` is ignored.** A not-yet-valid secret loads normally.

The two attributes look symmetrical and are not. An expiry is an operator
retiring a credential: the value is finished, and continuing to serve it is the
thing D6 refuses. A start date is a credential being *prepared* — and since this
adapter reads each secret's **current version**, a future `nbf` on that version
does not mean the value is unusable, it means someone set a date. Withholding a
working secret on that basis would produce the disappearing-key failure D6 already
warns about, for no safety gain.

Pinned by `TestLoad_NotYetValidSecretIsKept`, and falsified against a rule that
skips both, so the asymmetry is a recorded decision rather than an untested gap.

## Rejected alternatives

**Map `-` to `.` for nesting.** Rejected (D3): `-` is legal in ordinary names and
Key Vault has no escape character, so every hyphenated name would silently nest.
Guessing on every key is worse than the collision-guessing this family already
refuses.

**Reserve a double separator (`--` → `.`).** Superficially safer, still rejected:
it is a convention the store does not know about, so a name containing `--` for
any other reason still misbehaves, and it makes the module's key names differ from
what an operator sees in the portal.

**Use `lowkey-vault` for CI integration.** Rejected (D10) as an unverified
emulator. A green run against something whose fidelity nobody has checked is
weaker evidence than an acknowledged gap.

**Auto-decode on `ContentType`.** Rejected (D9): a free-text hint is not a
contract, and the family settled this when the parameter stores were specified.

**Serve expired secrets, matching the service.** This was the draft's proposal
and was rejected in review (D6). "Informational" describes what Key Vault
enforces, not what the operator meant by setting a date, and a configuration
library is the wrong place to quietly overrule them. The cost — a key that
disappears while the secret still exists — is paid in documentation instead.

## Public API

- `func New(api SecretsAPI, opts ...Option) config.Backend` — the whole vault as flat keys
- `func NewSecret(api SecretsAPI, name string, codec config.Codec, opts ...Option) config.Backend`
- `func FromClient(client *azsecrets.Client, opts ...Option) config.Backend`
- `func FromClientSecret(client *azsecrets.Client, name string, codec config.Codec, opts ...Option) config.Backend`
- `func Wrap(client *azsecrets.Client) SecretsAPI`
- `func WithNamePrefix(prefix string) Option` (D4) — client-side filter
- `func WithValueCodec(codec config.Codec) Option` (D3) — flat mode, decode blobs
- `func WithPollInterval(d time.Duration) Option` (D8)
- `const DefaultPollInterval = 5 * time.Minute` (D8)
- `const SourceKind = config.SourceKind("azure-keyvault")`
- `type SecretsAPI`, `type Secret`, `type Properties`, `type Option`

Read-only: satisfies `config.WatchableBackend`, not `config.WritableBackend`. No
`config` core change required.

## Testing strategy

Per D10, test-first per D11. What would falsely pass, and is watched to fail:

- a flat-key test whose fake returns names that happen to contain no hyphens —
  it would pass under a separator convention too, so a hyphenated name is
  asserted to stay one key;
- a managed/disabled skip test that only asserts the key is absent, which cannot
  tell skipping from refusing — the rest of the vault is asserted to load;
- an N+1 test asserting only the result: the call counts are asserted, so a
  future batch-shaped refactor cannot silently change the cost;
- a `Sensitive` assertion made only through conformance, which a
  `Sensitive: false` backend would satisfy by routing beneath;
- a watch test asserting "fired at least once", which cannot distinguish working
  change detection from signalling every tick.

## Migration & compatibility

Purely additive; no core change. Ships v0.1.0 read-only. Consumers should expect
the `ErrSensitiveLeak` behaviour the other secrets adapters introduced.

## Resolved (2026-07-25)

1. **Expired secrets are skipped** (D6), reversing the draft's proposal to serve
   them. "Informational" describes what Key Vault enforces, not what an operator
   meant by setting a date, and a configuration library is the wrong place to
   overrule them quietly. The cost — a key that disappears while the secret still
   exists and the service would still serve it — is real, is unique to this
   adapter, and is paid in prominent documentation.
2. **Disabled secrets are skipped** (D5), not treated as a failed read. This
   deliberately differs from `config-aws-secrets` D6, which refuses a partial
   read: there the adapter *could not* read a secret it was meant to, which is an
   access accident; here an operator has said "do not use this", which is an
   instruction. The two look alike and are not.
3. **v0.1.0 waits for real-vault verification** (D10). The code merges without
   one; the tag does not. Every adapter so far was proven against its real
   service before release, and each time that caught something the fake could
   not — releasing the first unproven one would lower the bar for those after it.

All open questions resolved.

## Implementation phases

Each phase ships code **and** its documentation (D11), test-first.

**Phase 0 — this spec.** Approve it, resolving the questions above.

**Phase 1 — flat read.** Scaffold, `SecretsAPI`/`Wrap`, `New`/`FromClient`,
`Load` over list-then-get with managed and disabled skipping (D5),
`WithNamePrefix` (D4), static `Sensitive` (D8), provenance, `depfootprint`
allowlist including the `azidentity`-absent assertion (D2). **Docs:** README with
the footprint, the flat-key model and the N+1 cost.

**Phase 2 — document mode, codec and watch.** `NewSecret`/`FromClientSecret`,
`WithValueCodec`, polled `Watch` at the five-minute default (D8). Run
`backendconformance` — the gate.

**Phase 3 — integration suite and docs.** The real-service suite (D10), skipped
without credentials, asserting each claim in D10's table directly. Then
`how-to/azure-keyvault.md` — leading on the flat-key model, the N+1 cost, and the
"secrets fit to use" rule with its disappearing-key consequence — plus the
ecosystem matrix, homepage and landing card.

**Phase 4 — verification, then release.** Run the integration suite against a
real Key Vault, confirm or correct each claim in D10's table, record any
correction as a dated revision, and only then cut **v0.1.0**. This phase is a
gate, not a formality: it is the check every sibling had and this one has had to
defer.

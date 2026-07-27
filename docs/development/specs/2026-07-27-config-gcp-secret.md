---
title: config-gcp-secret — the GCP Secret Manager backend adapter
date: 2026-07-27
author: matt.cockayne
status: approved
approved: 2026-07-27
---

# config-gcp-secret

The fourth and last of the Phase B secrets managers, after
[config-vault](2026-07-22-config-vault.md),
[config-aws-secrets](2026-07-25-config-aws-secrets.md) and
[config-azure-keyvault](2026-07-25-config-azure-keyvault.md). It cites the
[dynamic backend adapters umbrella](2026-07-21-dynamic-backend-adapters.md) and
settles the nine things the umbrella leaves to each adapter (**D2**).

Its closest sibling is `config-azure-keyvault`: a flat namespace with no
hierarchy to map, and no way to run the real service locally. It inherits both of
those constraints and adds three of its own that no previous adapter has had —
a payload that is **bytes rather than a string**, a **version state machine** the
`latest` alias does not respect, and by a wide margin the **heaviest dependency
graph in the family**.

Facts below are labelled by how they were established: **measured** against
`cloud.google.com/go/secretmanager` **v1.21.0** on 2026-07-27, **documented** by
Google, or **unverified** — behaviour that could not be probed without a real
project and must be confirmed before release.

## Problem

Secret Manager is where GCP-native workloads keep their credentials, and this
module cannot read it. [config-gcp-parameter](2026-07-22-config-gcp-parameter.md)
deliberately stopped short of it: its D2 established that Parameter Manager is
the *configuration* service and that Secret Manager is a distinct Phase B
concern, and its D5 keeps `Sensitive: false` honest precisely by never resolving
a Secret Manager reference. That leaves a gap that only this adapter closes.

The `Sensitive` machinery is proven — three adapters ship it — so the safety
story is not new. What is new is the shape of the store.

**There is no hierarchy, and this time the character set proves it.**
**Measured**, from `CreateSecretRequest.SecretId`: *"A secret ID is a string with
a maximum length of 255 characters and can contain uppercase and lowercase
letters, numerals, and the hyphen (`-`) and underscore (`_`) characters."* No
dots, no slashes. This is a stricter namespace than Key Vault's in one respect
(Key Vault forbids `_`, so it has one fewer candidate separator) and identical in
the respect that matters: every character that could serve as a separator is also
legal in an ordinary name, and the service offers no escape.

**There is no emulator.** **Measured** — `testcontainers-go/modules/gcloud`
v0.43.0 ships exactly six emulator packages: `bigquery`, `bigtable`, `datastore`,
`firestore`, `pubsub` and `spanner`. There is no `secretmanager` package, and
grepping the module for the string `secret` returns nothing. Google ships no
standalone Secret Manager emulator either. This is the same finding
`config-gcp-parameter` D11 recorded for Parameter Manager, re-verified rather
than assumed, and it puts this adapter in `config-azure-keyvault`'s position: the
real-service check that caught a genuine defect in every previous adapter is not
available at CI cost.

**And a payload is bytes.** Key Vault hands back a `string`; AWS splits
`SecretString` from `SecretBinary` so the adapter can tell them apart. Secret
Manager does neither — every payload is `[]byte`, capped at 64 KiB
(**measured**, from `SecretPayload.Data`), with nothing declaring whether those
bytes are text. That is a decision this family has not had to make before (D6).

## Decisions

### D1 — Module `config-gcp-secret`, package `configgcpsecret`, `config` v0.9.2

Cloud-qualified (umbrella D1): the same purpose exists under four vendors, so the
cloud is part of the name — and here it also distinguishes the adapter from its
own sibling `config-gcp-parameter`, which reads a different GCP service in the
same product area. The module is `gitlab.com/phpboyscout/go/config-gcp-secret`,
package `configgcpsecret`, README only, spec here (umbrella D2).

It pins `config` **v0.9.2**, not the v0.7.0 feature floor. v0.7.0 remains the
correct statement of what the adapter *needs* — it is the release whose
`backendconformance` requires a sensitive read-only backend to refuse the
routed-beneath write with `ErrSensitiveLeak` (umbrella R2). But
[config-aws-secrets R2](2026-07-25-config-aws-secrets.md) established that
building at a bare floor is a different mistake: `go.mod` pins what the module is
*tested* against, and an older `backendconformance` runs fewer subtests while
everything stays green — the quietest way for coverage to be wrong.

**Measured** at v0.9.2, `backendconformance` runs eight named subtests:
`participates_as_layer`, `provenance_names_backend`, `absent_source_tolerated`,
`apply_tolerated_beside_absent_source`, `read_only_skipped_by_routing`,
`write_round_trips`, `conflict_detected` and `foreign_change_reaches_observers`.
A read-only backend exercises the subset that applies. The implementation asserts
the count it observes against a sibling rather than trusting a green run.

### D2 — The SDK is `secretmanager/apiv1`, and this is the heaviest adapter in the family

**Measured** — `go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}'` over a
package importing `cloud.google.com/go/secretmanager/apiv1` and its
`secretmanagerpb` types yields **31 external modules** (32 including the probing
module itself). This confirms the earlier estimate rather than correcting it. The
set:

```
cloud.google.com/go/auth                cloud.google.com/go/auth/oauth2adapt
cloud.google.com/go/compute/metadata    cloud.google.com/go/iam
cloud.google.com/go/secretmanager       github.com/cespare/xxhash/v2
github.com/felixge/httpsnoop            github.com/go-logr/logr
github.com/go-logr/stdr                 github.com/google/s2a-go
github.com/googleapis/enterprise-certificate-proxy
github.com/googleapis/gax-go/v2         golang.org/x/crypto
golang.org/x/net                        golang.org/x/oauth2
golang.org/x/sync                       golang.org/x/sys
golang.org/x/text                       golang.org/x/time
google.golang.org/api                   google.golang.org/genproto
google.golang.org/genproto/googleapis/api
google.golang.org/genproto/googleapis/rpc
google.golang.org/grpc                  google.golang.org/protobuf
go.opentelemetry.io/auto/sdk            go.opentelemetry.io/otel
go.opentelemetry.io/otel/metric         go.opentelemetry.io/otel/trace
go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc
go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp
```

Against the siblings, counting the same way — the SDK-attributable library graph,
excluding `config`'s own:

| Adapter | SDK modules | Source |
|---|---|---|
| `config-aws-secrets` | **5** | its D2 |
| `config-azure-keyvault` | **6** | its D2, re-measured here at `azsecrets` v1.5.0 and confirmed |
| `config-vault` | **17** | its D12 |
| **`config-gcp-secret`** | **31** | measured here |

So this adapter costs a consumer roughly **five times** the AWS or Azure secrets
adapter and nearly twice `config-vault`. That is worth stating without softening:
it is the largest graph any module in this family has asked a consumer to take
on, and a reader comparing the four should know it before choosing.

Three things make it defensible rather than merely regrettable. It is **not the
adapter's doing** — it is the irreducible Google Cloud Go client stack (gRPC, the
API transport, genproto, protobuf, the auth stack and OpenTelemetry), and any
first-party GCP integration pays it; `config-gcp-parameter` D10 already accepted
the same bill for the same reason, and recorded it as the largest in the family
at the time. It is **not additive for a GCP consumer** — a workload already
talking to any Google API has substantially all of this resolved, so the marginal
cost of adding this adapter is small even though the absolute figure is large.
And it is **not silently incurred**: a `depfootprint_test.go` allowlist pins the
set in both directions (umbrella D9), so an SDK bump that drags in a new module
is a failing test rather than a surprise in someone's `go.sum`, and the README
leads with the number.

Note what is deliberately **absent**: `google.golang.org/api/option` appears only
because the SDK imports it, and nothing in the adapter's own code constructs a
client or resolves credentials (D9). Unlike `config-azure-keyvault`, there is no
separate credentials module to assert the absence of — GCP's auth stack is inside
`cloud.google.com/go/auth`, which the SDK pulls regardless. The allowlist
therefore cannot make the equivalent "the adapter is not authenticating"
assertion structurally, and that guarantee rests on code review instead. Said
plainly because it is a genuine weakening of a check a sibling has.

### D3 — Data model: flat secret IDs, verbatim — and an opt-in document mode

The naming rule (**measured**, quoted in the Problem) leaves the same three
options `config-azure-keyvault` D3 faced, and the same one is honest.

A **separator convention** — mapping `-` or `_` to `.` so `app-db-password` nests
— is rejected, for a reason that is if anything sharper here than at Key Vault.
Both characters are legal in ordinary secret IDs, and Secret Manager offers no
third character to escape with, so `my-service-key` would silently become four
levels of nesting with no way to say you meant it literally. Worse, GCP's own
conventions push operators towards hyphenated names, so the misfire would be the
common case rather than the rare one. That is guessing on every key, and this
family refuses to guess even on rare collisions (config-vault D5, config-xml
D21).

So **a secret's ID is a config key, verbatim**:

```
db-password    = "s3cr3t"          store.View().GetString("db-password")
api_key        = "k-123"      →    store.View().GetString("api_key")
```

The store is flat, so the layer is flat, and the key a consumer writes is exactly
the name an operator sees in the console — which is the property a separator
convention destroys.

A consumer who wants structure uses the second mode. `NewSecret(api, id, codec)`
reads **one** secret whose payload is a whole document, decoded through a
`config.Codec`. The codec is a **required parameter, not an option**, for the
reason [config-aws-secrets R1](2026-07-25-config-aws-secrets.md) settled: in
single-secret mode the payload is one opaque blob, so without a codec there is
nothing to build a layer from. Expressing a non-optional dependency as an
`Option` only moves the failure from compile time to run time.

In flat mode the codec stays optional (`WithValueCodec`), because there the names
supply the structure and a payload that the codec rejects can honestly fall back
to a scalar string. The asymmetry is deliberate and matches AWS and Azure
exactly.

Both modes are exported because they answer different questions: "read my
project's secrets" and "read my configuration document, which happens to live in
Secret Manager".

### D4 — Reading a project is N+1, and the listing carries neither payload nor version

**Measured**, and the most consequential thing about the read path. `ListSecrets`
returns a `SecretIterator` over `*secretmanagerpb.Secret`, whose fields are
`Name`, `Replication`, `CreateTime`, `Labels`, `Topics`, `Expiration`, `Etag`,
`Rotation`, `VersionAliases`, `Annotations`, `VersionDestroyTtl`,
`CustomerManagedEncryption`, `Tags`, `SecretType` and `PolicyMember`. There is
**no payload**, and — this is the part that differs from Key Vault — there is
**no version information either**. `ListSecretVersions` likewise returns
`*SecretVersion` (`Name`, `CreateTime`, `DestroyTime`, `State`, `Etag`, …) with
no payload, and its `Parent` is a single secret, so it cannot be used to sweep a
project.

The only call that returns bytes is `AccessSecretVersion`, whose response is
**measured** to be exactly `{Name string, Payload *SecretPayload}` — the resolved
version's full resource name plus `{Data []byte, DataCrc32C *int64}`.

So reading a project of *n* secrets is **one `ListSecrets` plus *n*
`AccessSecretVersion` calls**, unavoidably. There is no batch equivalent of AWS's
`BatchGetSecretValue`. This is the same N+1 as `config-azure-keyvault` D4, and
the same reversal of `config-aws-secrets` D3 — a reminder that this family shares
naming and safety conventions, not costs.

One genuine improvement over Key Vault, and it shapes the watch (D10): because
`AccessSecretVersionResponse.Name` carries the **resolved** version resource
name, a single `AccessSecretVersion` on `.../versions/latest` returns both the
value **and** the change marker. There is no second metadata call. The
compensating loss is that Key Vault's listing *does* carry each secret's version,
so a Key Vault poll can in principle detect change from the list alone; here it
cannot, because `ListSecrets` tells you nothing about versions at all. The two
services trade the same cost around.

### D5 — Version state: `latest` is followed, and falls back to the newest `ENABLED` version

> **Resolved 2026-07-27.** This decision originally proposed **skipping** a secret
> whose `latest` version is unusable, matching `config-azure-keyvault` D5. The
> human resolved it the other way: **fall back to the newest `ENABLED` version.**
> The body below is amended in place to specify the fallback, the case where no
> version is enabled at all, and the mitigation the fallback obliges — because
> the fallback was chosen *knowing* what it trades, and a spec that recorded the
> choice without specifying the mitigation would have recorded only half of it.

This is the decision with no sibling precedent, and the one that most changes
what this adapter does compared with its closest relative.

**Measured**, the state machine: `SecretVersion.State` is an enum whose values
are `STATE_UNSPECIFIED` (0), `ENABLED` (1), `DISABLED` (2) and `DESTROYED` (3).
**Documented**, what those mean: a disabled version *"cannot be accessed, but the
secret's contents still exist"*; a destroyed version's *"contents are
discarded"*.

**Measured**, the trap, from the SDK's own doc comment on
`AccessSecretVersionRequest.Name` and repeated on `GetSecretVersionRequest`:

> `projects/*/secrets/*/versions/latest` is an alias to the **most recently
> created** `SecretVersion`.

Most recently *created* — not most recently created *and enabled*. So if an
operator adds version 8 and then disables it, `latest` still resolves to version
8, and accessing it fails, even though version 7 is `ENABLED` and holds a
perfectly good credential. That is not the behaviour anyone assumes from the
name, and it is exactly the class of thing this family insists on writing down
before it is discovered at three in the morning.

Three options were considered:

**(a) Skip the secret.** The adapter reads `latest`; if that access fails, the
secret contributes no key and the rest of the project loads normally. This is
`config-azure-keyvault` D5's rule applied unchanged — an operator who disabled
the current version has said "do not use this". **Rejected**: it means an
application that was working stops finding a key because someone disabled a
version, which is the disappearing-key failure `config-azure-keyvault` D6 already
warns about, and here it would fire on the *common* case of a rotation that
half-completed rather than on the rare case of a deliberate retirement.

**(b) Fall back to the newest `ENABLED` version. Chosen.** The adapter resolves
`latest`; if that version is not `ENABLED`, it walks back to the most recently
created version that is, and serves that.

**(c) Refuse the Load.** `config-aws-secrets` D6's stance: a config layer missing
a password is not a degraded state a library should paper over. **Rejected** on
the distinction that spec's own resolution drew — AWS refuses because the adapter
*could not read what it was meant to*, an access accident; a disabled version is
an operator action, which is Key Vault's case, not AWS's.

**What (b) trades, stated plainly.** The fallback **silently serves a credential
somebody deliberately disabled.** A disabled newest version is most often a
rotation that half-failed — the new secret was written and then withdrawn, or was
never enabled — and reaching past it to the previous value means the application
keeps working while nobody learns that the rotation did not complete. The failure
then surfaces later, when the old credential is revoked in its turn, at maximum
distance from its cause. The adapter is also, straightforwardly, **overruling the
service**: `latest` means "most recently created", the operator's console shows
version 8 as current, and the application is running on version 7.

That is a real cost and it was accepted with open eyes. It is accepted because
the alternative — an application failing to start, or a key vanishing from a
running configuration, because a version was disabled — is the more common and
more disruptive outcome, and because a stale-but-working credential gives an
operator time to notice, where a missing one does not.

**Because it was chosen knowing that, the mitigation is part of the decision, not
a footnote.**

**Provenance cannot carry it, and inventing a field is not an option.**
`config.Source` has exactly four fields — `Kind`, `Name`, `Document`, `Writable`
(**measured**, `layer.go`). [config-vault R1](2026-07-22-config-vault.md) already
established that a version cannot go in `Name`, because the core requires a
writable backend's `ID()` to equal the `Source.Name` of the layers its `Load`
returns (**measured**, `store.go` — the error text says so in as many words), so
`Name` must be identical on every Load while a version changes on every write.

The structural obstacle here is stronger than config-vault's, and it is worth
recording because it closes the question for every future adapter rather than
just this one. **`Source` is per-layer; the fallback is per-key.** In flat mode
(D3) one layer carries every secret in the project, so even if `Name` were free
it could not say *which* secrets fell back — one string cannot describe n
independent resolutions. `Document` is an `int` index with the same per-layer
scope and no room for a version. And `Snapshot.Origin(path)` / `View.Explain(path)`
both return a `Source` (**measured**, `snapshot.go`, `view.go`), so neither can
show a per-key fact that `Source` cannot hold. Returning one layer per secret
would give each its own `Source`, but `Name` is still the only free-text field
and is still spoken for, so it would buy nothing.

So: **provenance says the value came from this backend, and cannot say which
version served it or that a fallback happened.** That is stated as a limitation
rather than worked around.

**The next best thing is a signal at the moment it happens.**
`WithFallbackObserver(func(Fallback))` is invoked during `Load`, once per secret
that did not resolve to `latest`:

```go
// Fallback reports one secret whose latest version was not usable.
// ServedVersion is empty when no version was enabled at all.
type Fallback struct {
	ID            string // the secret id, and the config key (D3)
	LatestVersion string // the version `latest` resolved to
	LatestState   string // ENABLED / DISABLED / DESTROYED
	ServedVersion string // the version actually served, or "" if none
}
```

A callback rather than a log line, because the module has no logger of its own —
the same reasoning that made `config-aws-secrets` reject "tolerate partial reads,
logging the failures". A consumer wires it to whatever they already alert on, and
a half-failed rotation becomes a signal instead of a silence. It is documented as
**called synchronously during `Load`, on the Store's goroutine**: it must not
block and must not call back into the Store, which is the same hazard the module
already refuses for writes from inside observers.

A queryable `Resolutions()` map on the backend was considered as a companion and
**rejected as duplication**: the callback already carries every fallback, and a
second surface holding the same facts is one more thing to keep consistent across
reloads.

**When no version is enabled at all**, which is a genuinely different case from
"latest is disabled", there is no fallback target. In **flat mode** the secret is
**skipped** — it contributes no key, the rest of the project loads, and the
observer fires with `ServedVersion: ""` so the omission is signalled rather than
silent. Skipping is right here where it was wrong for (a): there is no usable
value at any version, so no choice is being made on the operator's behalf. In
**document mode and for a pinned version** it is a **failed Load**, per the mode
asymmetry D6 sets out — the consumer named one secret as their whole
configuration source, and an empty layer with no explanation is worse than a
refusal.

**A pinned version is honoured exactly, and never falls back.** `WithVersion("7")`,
or a version alias the operator has defined (`Secret.VersionAliases` is
**measured**: a `map[string]int64` of alias to version number, resolvable by
`GetSecretVersion` and `AccessSecretVersion`), reads that version and nothing
else. If it is disabled or destroyed, that is a **failed Load**. The consumer
named a specific version, so silently substituting another would defeat the point
of naming it — and it would turn an explicit instruction into a guess. The
asymmetry between `latest` (fall back, and say so) and pinned (fail) is the
distinction between the adapter choosing and the consumer having chosen.

**Unverified, and load-bearing for everything above:** that `latest` really does
resolve to the newest version *regardless of state*. The SDK's doc comment says
"most recently created", which implies it, but implication is not observation.
**If the service in fact skips disabled versions when resolving `latest`, then
this entire fallback is dead code** — the service would already be doing it — and
D5 becomes a dated revision before release rather than an implementation. It is
first in D13's table for that reason.

Also **unverified**: the exact gRPC status code `AccessSecretVersion` returns for
a disabled or destroyed version. It is most likely `FAILED_PRECONDITION`, but the
adapter must not distinguish "operator disabled this" from "you lack permission"
by string-matching a message, so the fallback depends on that code being both
stable and distinct from `PERMISSION_DENIED`. The implementation prefers
`GetSecretVersion`'s `State` field to an error code wherever it can, precisely
because a field is a contract and an error code is a hope — and D10's metadata
poll makes that state available on the same call the watch already makes.

### D6 — A payload is bytes, and a payload that is not valid UTF-8 is skipped

**Measured**: `SecretPayload.Data` is `[]byte`, capped at 64 KiB, with no
companion field declaring a type. This is unlike both siblings — Key Vault's
`Value` is a `string`, and AWS splits `SecretString` from `SecretBinary` so the
adapter can branch on a field rather than on the data.

Secret Manager will happily store a PKCS#12 keystore, a DER certificate or a
gzip blob, and nothing in the API says so. Something has to decide, so the rule
is: **a payload that is not valid UTF-8 contributes no key, and the rest of the
project loads normally.**

That is `config-aws-secrets` D5 — skip binary, keep loading — implemented with a
validity test instead of a field, and the alternatives lose for the reasons that
spec already recorded. Base64-encoding invents a representation nobody asked for
and puts a `View` in the position of serving a blob as a string. Refusing the
Load lets one unrelated certificate in a shared project break an unrelated
application's startup, and a *project* is a much broader shared namespace than an
AWS prefix, so the blast radius is larger here.

The check is `utf8.Valid`, which is a genuine heuristic and is documented as one:
a short binary payload can be accidentally valid UTF-8 and will be admitted as a
nonsense string. That is the honest failure mode of a store that does not declare
its types, and it is stated rather than papered over. It is also why the skip is
recorded in the module documentation even though it is silent at runtime.

In **single-secret document mode** (D3) the rule inverts, as it does for the
codec: a non-UTF-8 payload there is a **failed Load**, because the consumer named
one secret as their entire configuration source and skipping it would produce an
empty layer with no explanation.

### D7 — The payload CRC32C is verified when present, and a mismatch fails the read

**Measured**: `SecretPayload.DataCrc32C` is a `*int64`, present on the payload
returned by `AccessSecretVersion`. **Documented**, on the field itself: the
service verifies a caller-supplied checksum on `AddSecretVersion`, *"stores it to
include in future `AccessSecretVersion` responses"*, and *"if a checksum is not
provided … will generate and store one for you"* — so a version created through
any normal path has one. The value is *"encoded as an Int64 for compatibility,
and can be safely downconverted to uint32"*.

The adapter **verifies it**: it computes the Castagnoli CRC32 of the received
bytes and compares. A mismatch **fails the read** with a named error, rather than
skipping the secret or serving the bytes.

The counter-argument deserves stating, because it is not weak: gRPC runs over
HTTP/2 over TLS, which already has integrity protection, so this is
belt-and-braces against a class of corruption the transport should have caught.
The argument that wins is about *what* is being protected. A silently corrupted
password does not fail loudly — it fails as an authentication error somewhere
downstream, minutes or hours later, at maximum distance from its cause, and the
first hypothesis anybody forms is "the credential was rotated". Six lines of
`hash/crc32` (stdlib, no dependency) converts that into an immediate, named,
unambiguous failure. Google's own client samples perform this check, which is
weak evidence on its own but does mean the field is intended to be used rather
than merely exposed.

Failing rather than skipping is deliberate and distinguishes this from D5 and D6.
Those are cases where the store is telling the adapter something coherent — a
version is withdrawn, a payload is not text. A checksum mismatch means the
adapter **cannot trust what it received**, and there is no honest way to carry on
past that.

When `DataCrc32C` is nil the check is **skipped, not failed**. The documentation
says the service generates one, so nil should not occur; refusing a read on the
strength of a "should" would turn a documentation assumption into an outage.

### D8 — Labels filter server-side; the name filter is a substring, not a prefix

This is where Secret Manager is genuinely better than Key Vault, and the
difference is worth exploiting rather than flattening for symmetry.

**Measured**: both `ListSecretsRequest` and `ListSecretVersionsRequest` carry a
`Filter string` field. **Documented**, from Google's filtering reference, and
performed **server-side**:

| Filter | Example | Note |
|---|---|---|
| Labels | `labels.environment=production` | exact match on a label value |
| Name | `name:mysecret` | **case-insensitive substring containment** |
| Expiration | `expire_time:*`, `expire_time<2021-07-31` | |
| Creation | `create_time>2021-01-01T01:00:00Z` | RFC 3339 |
| Version state | `state:(ENABLED OR DISABLED)` | `ListSecretVersions` only |

`config-azure-keyvault` D4 had to filter client-side because Key Vault has no
server-side name filter; here the list itself can be narrowed before it leaves
the service, which matters on the N+1 path (D4) because every secret the filter
removes is one `AccessSecretVersion` not made. On a shared project that is the
cost that scales.

So the adapter offers **`WithLabel(key, value)`**, composing into an `AND`ed
`labels.k=v` filter sent to the service. This is a real capability the sibling
could not have, and label-scoping is how GCP teams already partition a shared
project.

The name filter needs more care, and getting it wrong would be a silent bug.
`name:` is **substring containment, not prefix matching** — `name:app` matches
`legacy-app-token` as readily as `app-db-password`. So **`WithNamePrefix(p)`
sends `name:p` as a server-side *narrowing hint* and then re-checks
`strings.HasPrefix` client-side**, keeping the promise the option's name makes
while still saving the fetches. Both halves are load-bearing and the tests assert
each independently (Testing strategy), because a fake that ignores the filter
would let the client-side check alone carry a green suite, and a service that
honoured the prefix strictly would let the server-side hint alone do so.

**A raw `WithFilter(expr string)` escape hatch does not ship at v0.1.0**
(resolved 2026-07-27). It would expose the full documented grammar — create-time
windows, `expire_time:*`, boolean composition — at the cost of coupling the
module's public surface to a Google filter dialect it cannot validate, so a typo
becomes an opaque `INVALID_ARGUMENT` at Load rather than a compile error. The
family's no-speculative-surface instinct wins; it is additive later if a consumer
needs it.

### D9 — Client injected; project, location and credentials stay with the consumer

Per umbrella D3, and following `config-gcp-parameter` D6. The consumer builds a
`*secretmanager.Client` — where every auth decision lives (application default
credentials, workload identity, a service account key, an impersonated principal)
and where the choice of global or regional endpoint lives — and hands it over
with the project it addresses.

```go
// API is the slice of Secret Manager this adapter uses, behind an interface it
// owns so a fake drives the unit suite (D13). Read-only (D10): no write method.
type API interface {
	// Access reads one secret's payload at the given version ("" = "latest").
	// It returns the resolved version resource name alongside the bytes, so a
	// single call yields both the value and the watch's change marker (D4).
	// A secret or version that does not exist returns fs.ErrNotExist.
	// DATA_READ (D10).
	Access(ctx context.Context, id, version string) (Payload, error)

	// Describe reads one version's metadata — its resolved name and state —
	// without the payload. It drives the poll (D10) and the fallback
	// resolution (D5), which prefers this State field to inferring a version's
	// usability from an access error code. ADMIN_READ (D10).
	Describe(ctx context.Context, id, version string) (Version, error)

	// Versions lists a secret's versions, newest first, for the D5 fallback
	// walk. Called only when Describe reports latest is not ENABLED, so the
	// common path never pays for it.
	Versions(ctx context.Context, id string) ([]Version, error)

	// List returns every secret's metadata under the configured parent,
	// narrowed by filter server-side (D8). It carries neither payloads nor
	// version information — the service does not include them (D4).
	List(ctx context.Context, filter string) ([]Secret, error)
}

// Version is one version's metadata, without its payload.
type Version struct {
	Name  string // projects/*/secrets/*/versions/7 — resolved, never "latest"
	State string // ENABLED / DISABLED / DESTROYED (D5)
}

// Payload is one resolved access.
type Payload struct {
	VersionName string  // projects/*/secrets/*/versions/7 — the change marker (D10)
	Data        []byte  // may be non-UTF-8; skipped if so (D6)
	CRC32C      *int64  // verified when non-nil (D7)
}

// Secret is one listed secret's metadata. Labels and Annotations are surfaced
// for the consumer and for filtering; neither ever selects a decoder (D12).
type Secret struct {
	ID          string            // the last path segment, and the config key (D3)
	Labels      map[string]string
	Annotations map[string]string
	Etag        string
}
```

`Wrap(client *secretmanager.Client, project, location string) API` adapts the
real SDK, composing resource names from the coordinates: `Access` →
`AccessSecretVersion` on `{parent}/secrets/{id}/versions/{version}`; `Describe` →
`GetSecretVersion` on the same path; `Versions` → `ListSecretVersions` with
`Parent` set to the secret; `List` → `ListSecrets` with `Parent` and `Filter`,
draining the `SecretIterator`.

**Measured**, the two parent forms: a global secret is
`projects/{project}/secrets/{id}`, while a regionalised secret is
`projects/{project}/locations/{location}/secrets/{id}` — the location segment is
*absent*, not `global`, for the ordinary case. This differs from Parameter
Manager, where `global` is a literal location value, so `config-gcp-parameter`'s
signature cannot be copied verbatim. The convention is **`location == ""` meaning
the project-level parent** (resolved 2026-07-27), keeping one `Wrap` rather than
two functions. A separate `WrapRegional` was considered and judged to add a
second entry point for a difference the doc comment already states.

As in `config-gcp-parameter` D8, the adapter **takes the location and trusts the
injected client's endpoint to match it**. It does not validate the agreement and
configures no endpoint — umbrella D3 places that entirely with the consumer, and
a mismatch surfaces naturally as a `NotFound` or transport error.

**Measured**, error mapping: the SDK returns `google.golang.org/grpc/status`
errors, and `status.FromError` recovers a typed `codes.NotFound` reliably, so an
absent secret maps to `fs.ErrNotExist` without string matching and the Store
decides whether a missing source is fatal. Other codes (`PermissionDenied`,
`Unavailable`, `ResourceExhausted`) are wrapped with the secret's name so a
caller sees which read failed and why. Retry and backoff are the SDK's `gax` call
options; the adapter re-implements neither.

### D10 — Capability: read-only, statically `Sensitive: true`, polled on version metadata

> **Resolved 2026-07-27.** This decision originally polled with
> `AccessSecretVersion`, fetching every payload on every tick. The human resolved
> it to poll on **`GetSecretVersion` (`ADMIN_READ`)** and access
> **(`DATA_READ`)** only what actually moved. The capability table and the marker
> are unchanged; the poll mechanics and cost arithmetic below are amended in
> place.

| Capability | Value | Why |
|---|---|---|
| `Sensitive` | **`true`**, statically | It is a secrets manager; every value is a secret |
| Writable | *not implemented* | Umbrella D7 |
| `NativeWatch` | `false` | The adapter polls; see D11 for the change feed it declines |
| `AtomicMultiKey` | `false` | No writes |
| `PreservesComments` | `false` | Not a file format |

Statically sensitive, like Vault, AWS Secrets Manager and Key Vault, and unlike
`config-aws-ssm`, which is dynamic because SSM is a *mixed* store. Secret Manager
is not mixed. This means umbrella R2 applies: the conformance suite's read-only
check asserts the routed-beneath write is refused with `ErrSensitiveLeak`.

Secret Manager *can* be written — `AddSecretVersion` and `CreateSecret` are
**measured** on the client — so read-only here is policy (umbrella D7), not
capability, exactly as `config-vault` and `config-aws-secrets` draw the line.

**The change marker is the version actually served**, not the version `latest`
names — the distinction D5's fallback forces. **Measured**, from the SDK's
comment on `SecretVersion.Name`: version IDs *"start at 1 and are incremented for
each subsequent version"*, so a rotation moves the served version from
`…/versions/7` to `…/versions/8` and the marker changes. A secret appearing or
disappearing is also a change, because the comparison is over the whole
`id → servedVersionName` map.

Marking the *served* version rather than `latest` is what makes the fallback
observable to the watch. If version 8 is disabled and 7 is being served, the
marker is 7; an operator enabling 8 moves it to 8 and fires a change, and so does
someone disabling 7 (the served version) and dropping back to 6. Marking `latest`
instead would leave the marker on 8 throughout and the watch would notice
neither.

`Secret.Etag` was considered as a cheaper marker — it is **measured** to exist on
`Secret` and would come free with the listing, collapsing the poll to one call —
and rejected as **unverified**: nothing establishes that adding a *version* bumps
the parent *secret's* etag, and the field is documented as the etag "of the
currently stored Secret", which reads like control-plane metadata. Building a
change detector on an unconfirmed assumption is how a watch silently stops
firing. If real-service verification (D13) shows the etag does move on
`AddSecretVersion`, that is a worthwhile revision and a large cost saving; until
then the version name is the marker that is definitionally correct.

**The poll reads version metadata, not payloads.** **Documented**, from Google's
Secret Manager audit-logging reference: `AccessSecretVersion` is classified
**`DATA_READ`**; `ListSecrets`, `GetSecret`, `GetSecretVersion` and
`ListSecretVersions` are also Data Access methods but under **`ADMIN_READ`**. So
polling cannot be made audit-free — every call this adapter makes is auditable —
but only the payload access lands in the `DATA_READ` stream, which is the one an
operator watches to answer *"who read my secrets"*. A poll that accessed every
payload every tick would bury genuine access in poll noise in exactly that
stream.

So each tick issues `GetSecretVersion` on `.../versions/latest`, which returns
the resolved version `Name` and its `State` and **no payload**, and calls
`AccessSecretVersion` only for the secrets whose served version actually moved.
This also serves D5 directly: the poll sees `State` on the same call, so a
version being disabled is detected from a field rather than inferred from an
error code, and the fallback re-resolves without a separate probe.

**The cost arithmetic, for a project of *n* secrets**, of which *f* are currently
being served by a D5 fallback and *k* change on a given tick:

| | Calls | of which `DATA_READ` |
|---|---|---|
| Initial Load | 1 list + *n* access = **n + 1** | *n* |
| Quiet poll (k = 0, f = 0) | 1 list + *n* metadata = **n + 1** | **0** |
| Poll with *f* in fallback | **n + f + 1** | **0** |
| Poll with *k* changed | **n + f + k + 1** | *k* |
| *Previous (access-only) design, any tick* | *n + 1* | *n* |

Two things fall out of that table, and both were worth computing rather than
assuming. The metadata poll is **not more expensive in the steady state** — it is
`n + 1` calls either way; the extra call is paid only per *changed* secret, which
is the case that was going to fetch a payload regardless. And `DATA_READ` volume
drops from *n* per tick to *k* per tick, which for a stable project is from *n*
to zero. The design pays one extra call per change to remove a continuous audit
stream, which is a better trade than the open question that raised it implied.

The *f* term is the honest cost of D5's fallback: a secret being served from a
non-latest version needs two metadata calls per tick, one for `latest` (to notice
it becoming enabled, or a newer version arriving) and one for the served version
(to notice *it* being disabled in turn). A `ListSecretVersions` would answer both
in one call but returns every version ever created, so two point reads are
cheaper for any secret with more than a couple of versions.

**The default interval is five minutes**, matching `config-azure-keyvault` D8
rather than the family's sixty seconds. The N+1 shape (D4) is the reason: a poll
over a fifty-secret project is 51 requests, so a sixty-second cadence is 51
requests a minute indefinitely. The audit argument no longer bears on the
interval, because the metadata poll has taken it off the table — which is worth
noting, because it means five minutes is now justified by request volume alone.
`WithPollInterval` overrides it either way.

One honest qualification retained: Data Access logs are **not enabled by default**
and require explicit configuration, so the audit cost this design avoids is real
only for operators who have turned them on — which is exactly the
security-conscious operator most likely to run a secrets backend, and the reason
the design still earns its place.

### D11 — Secret Manager has a change feed, and this adapter still polls

Umbrella D6 asserts that the cloud secrets stores have "nothing to subscribe to".
For this one, **that is factually wrong**, and the correction belongs on the
record.

**Measured**: `Secret.Topics` is a `[]*Topic`, documented as *"a list of up to 10
Pub/Sub topics to which messages are published when control plane operations are
called on the secret or its versions"*, each naming a `projects/*/topics/*`
resource. Adding a version is a control-plane operation. So Secret Manager alone
among the four secrets managers offers a genuine push notification of change.

The adapter nonetheless implements `Watch` by **polling**, with `NativeWatch:
false`, on three grounds.

It would **not be free**, though it is cheaper than it looks — the measured
marginal cost is four modules (below), not a second SDK's worth. The real cost is
that `Watch` would have two mechanisms with different failure modes instead of
one.

It **is not the adapter's to configure**. The topics are set on the *secret*, by
whoever provisions it, and the subscription is a separate resource with its own
IAM. An adapter that required them would be dictating how a consumer's secrets
are provisioned — a long way outside the boundary umbrella D3 draws, where the
adapter owns no credentials and no service configuration.

And it would **move change detection outside the Store**, which owns it. This is
the same reasoning that rejected a Pub/Sub path in `config-gcp-parameter` D7,
and it has not weakened.

> **Resolved 2026-07-27.** Both, not either. The umbrella is corrected by a dated
> revision **and** the Pub/Sub watch is recorded as a tracked follow-on. Probing
> the wider claim during that correction found the umbrella is wrong about **five
> of the six systems D6 names**, not just this one — see
> [umbrella R3](2026-07-21-dynamic-backend-adapters.md).

**The umbrella correction.** Umbrella D6's claim is now known to be inaccurate
for most of the systems it covers, so it is corrected there by
**[R3 (2026-07-27)](2026-07-21-dynamic-backend-adapters.md)**, which introduces a
third category — an **out-of-band feed**, requiring infrastructure the consumer
provisions separately — and places each system in it. Correcting only the system
that happened to be caught would have left the same error in place for four
others.

**The follow-on: a separate module, not an option here.** An opt-in Pub/Sub watch
is worth building if someone needs sub-poll latency, and it is tracked as a
follow-on tied to the umbrella's Phase D. The proposal is that it lives in its
**own module** rather than behind an option in this one, so a consumer who polls
never resolves a Pub/Sub client.

The footprint argument for that split is weaker than expected and is reported
honestly rather than quietly kept. **Measured** on 2026-07-27: a package
importing `cloud.google.com/go/pubsub/v2` alone resolves **34** modules; the
union with this adapter's 31 is **35**. So Pub/Sub adds exactly **four** modules
on top of the Secret Manager stack — `cloud.google.com/go`,
`cloud.google.com/go/pubsub/v2`, `github.com/google/uuid` and
`go.opencensus.io` — because the two SDKs share essentially the whole Google
client stack. A 13% increase is not the objection I assumed when drafting D11,
and the honest position is that **the dependency argument alone does not carry
the split.**

What does carry it is the other two grounds, which are unaffected: the topics are
configured on each *secret* by whoever provisions it, so requiring them would
have the adapter dictating how a consumer's secrets are provisioned; and a
subscription is a separate resource with its own IAM and its own delivery
semantics (acks, redelivery, dead-lettering) that a `Watch` would have to own. A
separate module also keeps this one's `Watch` a single mechanism rather than two
with different failure modes. The follow-on gets its own spec (umbrella D2) and
that spec should re-argue the split on those grounds rather than on the four
modules.

### D12 — Labels, annotations and secret type are surfaced, never acted on

**Measured**: `Secret` carries `Labels` (up to 64, constrained charset),
`Annotations` (up to 16 KiB total, *"to allow client tools to store their own
state information"*), `Tags`, and — newer — `SecretType`, an enum whose values
are `SECRET_TYPE_UNSPECIFIED`, `CLOUD_SQL_DB_CREDENTIALS`, `ACCESS_KEY` and
`CERTIFICATE`.

`Labels` and `Annotations` are exposed on the narrow interface (D9). `Labels`
additionally drives the server-side filter (D8), which is a *scoping* use, not an
interpretive one. Nothing here ever selects a decoder.

That is the family convention settled when the parameter stores were specified
and restated at `config-azure-keyvault` D9 for `ContentType`: format hints are
surfaced, never auto-decoded. Deciding to parse a payload because an annotation
happens to read `application/json` is guessing at a format the store does not
enforce, and the failure lands at startup on a value the consumer may not even
use. `WithValueCodec` is explicit.

`SecretType: CERTIFICATE` deserves its own sentence, because it looks like Key
Vault's `Managed: true` and is not. Key Vault's managed secrets are *generated*
by the service to back a certificate, so their payload is reliably key material
and skipping them is safe (`config-azure-keyvault` D5). GCP's `SecretType` is an
`Optional. Immutable.` operator-set declaration that *"enforces certain
structural requirements"* — the payload is still whatever bytes were stored, and
a PEM chain is perfectly serviceable text a consumer may legitimately want.
Skipping on it would withhold a working value on the strength of a label. So
`SecretType` is **not** a skip condition; D6's UTF-8 test already removes the
payloads that genuinely cannot be served, and it tests the data rather than a
declaration about it.

### D13 — Testing: a fake, `backendconformance`, and real-service integration only

Per umbrella D10, with the same honest gap `config-azure-keyvault` D10 recorded.

The unit suite runs against a fake `API` — flat keys, document mode, the version
states of D5, the UTF-8 skip of D6, CRC mismatch, the filter split of D8,
provenance, the N+1 call counts and the poll. `backendconformance.Run` over that
fake is the gate, asserting the sensitive read-only `ErrSensitiveLeak` branch
(umbrella R2). Both are as strong here as anywhere.

The integration suite is **real-service only**, following `config-gcp-parameter`
D11 and `config-azure-keyvault` D10. There is no emulator (**measured**, see
Problem), so there is no DIND job and nothing to containerise. It lives under
`./test/integration/`, is gated on `INT_TEST_INTEGRATION` *and* the presence of a
project environment variable, and is **skipped, not failed**, when either is
absent — so it stays compiled and IDE-discoverable without credentials. It
creates and destroys its own secrets, and cleanup is part of the test rather than
an afterthought, because the resources it leaves behind cost money monthly.

The consequence must be stated plainly rather than buried: **the runtime
behaviours in D5, D6 and D7 are measured on the SDK's types and documented by
Google, but not observed.** For Vault, Consul and AWS the integration suite
caught something the fake could not each time — Vault rounding integers above
2^53, the exact prefix-matching boundary — and precisely that check is missing at
merge time here.

**So v0.1.0 is gated on running the integration suite against a real GCP
project.** The code can be written, reviewed and merged without one; the tag
waits. That is `config-azure-keyvault` D10's rule, and applying it identically is
the point — the moment one adapter ships unproven, the bar is lowered for every
one after it.

The specific claims the suite asserts directly:

| Claim | Source | Status |
|---|---|---|
| **`latest` resolves to the newest version even when that version is disabled**, rather than skipping to the newest enabled one | D5 | **unverified — load-bearing.** The whole fallback exists only if this holds; see below |
| `GetSecretVersion` on `latest` reports `State: DISABLED` for a disabled newest version, rather than erroring | D5, D10 | **unverified** — the fallback resolves from this field in preference to an error code |
| `AccessSecretVersion` on a **disabled** version returns a distinct, matchable gRPC code (not `PERMISSION_DENIED`) | D5 | **unverified** — the error-code path is the fallback's backstop where `State` is unavailable |
| `AccessSecretVersionResponse.Name` returns the **resolved** numeric version, not the literal string `latest` | D4, D10 | **unverified** — the change marker is useless if it does not |
| `ListSecretVersions` returns versions in a resolvable order with `State` populated | D5 | **unverified** — the fallback walk depends on ordering newest-first or being sortable |
| `DataCrc32C` is populated on a version created without a caller-supplied checksum | D7 | **documented**, not observed |
| `ListSecrets` returns no payload and no version information | D4 | **measured** on the types; confirmed end to end |
| `name:` filtering is substring, not prefix — `name:app` matches `legacy-app-token` | D8 | **documented**; the client-side re-check depends on it |
| `Secret.Etag` does **not** change when a version is added | D10 | **unverified** — if it does, that is a cheaper poll and a revision worth making |

**The first row is the one that can delete a decision.** D5's fallback assumes
the service does *not* already skip disabled versions when resolving `latest`. If
verification shows it does — that `latest` means "most recently created *and
enabled*" — then the fallback is not a safety net, it is **dead code that never
runs**, and the honest outcome is a dated revision withdrawing it rather than
shipping an untriggerable branch and a `Fallback` type nothing ever emits. This
is checked first in Phase 4, before any of the others, because a negative result
changes what Phase 1 should have built.

If any of the rest turns out false it is likewise a dated revision here before
release, which is the point of writing them down as claims rather than as
assumptions.

### D14 — Documentation ships with each phase, and the work is test-first

Carried forward (config-vault D15/D16, config-aws-secrets D12,
config-azure-keyvault D11), including the `TestDocLinksResolve` guard, which has
now caught the same class of defect in three successive adapters and is copied in
as a matter of course.

**Test-first, assertions watched to fail.** Recorded again because the discipline
slipped once in `config-aws-secrets` Phase 2, where the code preceded its tests.

**BDD suitability: no Gherkin in the adapter.** Unchanged reasoning — pure library
logic behind a narrow injected interface, whose wired-together contract is
`backendconformance`. The core's sensitive-leak scenarios landed separately and
cover the guard this adapter relies on.

## Revisions

### R1 (2026-07-27) — the observer is fed by the poll, and a fallback is itself a change (amends D5, D10)

D5 specified `WithFallbackObserver` as called "synchronously during `Load`". Building
Phases 1–2 showed that description is right about *where* the callback fires and
wrong about *when the situation arises*. In a long-running process it is almost
never `Load` that meets a half-completed rotation — it is a **poll**, minutes or
hours later. A `Load`-only observer would have reported the fallbacks present at
startup and gone quiet for exactly the events it exists to surface.

The contract is preserved rather than changed: the poll **collects** fallbacks
into its snapshot, and `Load` emits them. The callback still runs on the Store's
goroutine during `Load`, so a consumer's expectations hold.

The more serious half is a defect the spec as approved would have shipped. **A
fallback appearing does not move the served version.** Version 8 is created and
disabled; `latest` becomes unreadable; the adapter falls back to version 7 — which
is the version it was already serving. A change comparison over served versions
alone sees nothing, reports nothing, and the rotation stays silent for the life of
the process. That is precisely the failure the observer was added to prevent.

So change detection compares **the fallback set as well as the served versions**.
Both halves are needed: the served versions catch an ordinary rotation, and the
fallback set catches one that failed without moving anything.

### R2 (2026-07-27) — `Load` must consume the poll's snapshot (amends D10)

D10's cost table is correct only under an assumption it does not state: that a
`Load` following a poll **reuses what the poll already read**. Without that, a
detected change costs the poll's `n + f + k + 1` calls and then a further `n + 1`
payload reads to build the layer — which restores the DATA_READ traffic per tick
that the metadata poll was chosen to avoid, and undoes the decision.

Snapshot reuse is therefore part of D10 rather than an implementation detail, and
is pinned by a test asserting the call counts rather than only the values.

### R3 (2026-07-27) — version ordering is derived, not assumed (closes a D13 row)

D13 listed as a claim to verify that `ListSecretVersions` returns versions in a
resolvable order, which the fallback walk depends on. That row is now closed
without needing a real project: version IDs are integers starting at 1 and
incrementing, so `Wrap` **sorts by version number** rather than trusting the
service's ordering. Newest-first is then definitional rather than observed, and
one unverified claim leaves the release gate.

## Rejected alternatives

**Map `-` or `_` to `.` for nesting.** Rejected (D3): both are legal in ordinary
secret IDs, Secret Manager offers no escape character, and GCP conventions favour
hyphenated names — so the misfire would be the common case rather than a rare
collision. The same reasoning as `config-azure-keyvault` D3, with a wider blast
radius.

**Reserve a double separator (`__` → `.`).** Superficially safer, still rejected:
it is a convention the store does not know about, so a name containing `__` for
any other reason misbehaves anyway, and it makes the module's key names differ
from what an operator sees in the console.

**Skip a secret whose `latest` version is disabled.** This was the draft's
proposal, copying `config-azure-keyvault` D5, and was **rejected in review**
(D5, resolved 2026-07-27) in favour of falling back to the newest `ENABLED`
version. The Key Vault precedent looked stronger than it is: there, a disabled
secret is usually a deliberate retirement, so withholding it honours an
instruction. Here the common cause is a **rotation that half-completed**, and
skipping turns that into a key vanishing from a running application — the
disappearing-key failure `config-azure-keyvault` D6 warns about, fired on the
common case instead of the rare one. The fallback's own cost (silently serving a
withdrawn credential) is accepted and mitigated by `WithFallbackObserver`, not
denied.

**Refuse the Load when a secret's `latest` version is disabled.** Rejected (D5)
on `config-azure-keyvault`'s distinction: AWS refuses partial reads because the
adapter *could not read what it was meant to*, an access accident. A disabled
version is an operator action, and one withdrawn secret must not break every
application sharing the project.

**Report the served version through provenance.** Rejected because it is not
possible, not because it is undesirable (D5). `config.Source` has four fields and
none can hold a per-key fact, since one layer carries every secret in flat mode;
`Origin` and `Explain` both return a `Source`, so neither can surface it either.
Adding a field to `config.Source` for one adapter's benefit was not considered
seriously — it would put a GCP-specific concern in the core's provenance type.
The `WithFallbackObserver` callback is the next best thing and is specified as
such rather than presented as equivalent.

**A queryable `Resolutions()` map on the backend.** Rejected (D5) as duplication:
the observer already carries every fallback at the moment it happens, and a
second surface holding the same facts is one more thing to keep consistent across
reloads for no new information.

**Poll with `AccessSecretVersion`, fetching every payload each tick.** This was
the draft's design and was **rejected in review** (D10, resolved 2026-07-27).
`AccessSecretVersion` is `DATA_READ`, so it writes a continuous Data Access audit
stream for reads that found nothing changed, burying real access in poll noise.
Polling `GetSecretVersion` (`ADMIN_READ`) and accessing only what moved costs the
same `n + 1` calls in the steady state and one extra call per *changed* secret —
a cheaper trade than it appeared when the question was raised.

**Base64-encode non-UTF-8 payloads into the layer.** Rejected (D6), as it was for
`config-aws-secrets` D5: it invents a representation nobody asked for, and a
`View` serving a base64 blob as a string is worse than the key being absent and
documented.

**Skip payloads on `SecretType: CERTIFICATE`.** Rejected (D12): unlike Key
Vault's `Managed` flag, `SecretType` is an operator-set declaration, not a
statement that the service generated key material — and a PEM chain is
serviceable text a consumer may legitimately want. D6's UTF-8 test removes what
genuinely cannot be served, by testing the data rather than a label about it.

**Skip the CRC32C check as redundant over TLS.** Rejected (D7). The transport
does protect integrity, so this is belt-and-braces — but a silently corrupted
password fails as a downstream authentication error minutes later, at maximum
distance from its cause, and the first hypothesis anyone forms is "the credential
rotated". Six stdlib lines turn that into an immediate named failure.

**Use `Secret.Etag` as the poll's change marker.** Rejected for now (D10) as
**unverified**: nothing establishes that adding a version bumps the parent
secret's etag, and it reads like control-plane metadata. It would collapse the
poll to a single call, so it is worth re-testing during D13's verification — but
building change detection on an unconfirmed assumption is how a watch silently
stops firing.

**Consume the Pub/Sub change feed for a native watch in this module.** Rejected
for v0.1.0 (D11) despite the feed genuinely existing, but **not on the grounds
first written**: the measured marginal cost is four modules, not a second SDK, so
the footprint objection does not carry the argument and that is said rather than
quietly retained. What carries it is that the topics are configured per-secret by
whoever provisions them, that a subscription brings delivery semantics the
`Backend` contract has no place for, and that `Watch` would have two mechanisms
with different failure modes. Tracked as a follow-on in a separate module, with
its own spec (umbrella D2).

**Auto-decode on an annotation.** Rejected (D12): a free-text hint is not a
contract, and the family settled this when the parameter stores were specified.

**Take `*secretmanager.Client` directly in the constructor.** Rejected (D9), as
in every sibling: it couples the adapter to the SDK type, makes the unit suite
need a real or heavily mocked GCP client, and invites the adapter to reach for
project, endpoint and credential configuration that is the consumer's. `FromClient`
keeps the common path a single call.

**Use an unofficial Secret Manager emulator for CI.** Rejected on
`config-azure-keyvault` D10's ground: an integration suite whose emulator nobody
has validated produces green runs that mean less than they appear to, which is
worse than an honestly absent suite.

## Public API

- `func New(api API, opts ...Option) config.Backend` — the project's secrets as flat keys (D3)
- `func NewSecret(api API, id string, codec config.Codec, opts ...Option) config.Backend` — document mode; codec required (D3)
- `func FromClient(client *secretmanager.Client, project, location string, opts ...Option) config.Backend`
- `func FromClientSecret(client *secretmanager.Client, project, location, id string, codec config.Codec, opts ...Option) config.Backend`
- `func Wrap(client *secretmanager.Client, project, location string) API` (D9) — `location == ""` is the project-level parent
- `func WithNamePrefix(prefix string) Option` (D8) — server-side `name:` hint plus a client-side prefix re-check
- `func WithLabel(key, value string) Option` (D8) — server-side `labels.k=v`, repeatable, `AND`ed
- `func WithVersion(version string) Option` (D5) — a pinned version or alias; default `latest`, and never falls back
- `func WithFallbackObserver(fn func(Fallback)) Option` (D5) — signalled when a secret does not resolve to `latest`; called synchronously during `Load`
- `func WithValueCodec(codec config.Codec) Option` (D3) — flat mode only
- `func WithPollInterval(d time.Duration) Option` (D10)
- `const DefaultPollInterval = 5 * time.Minute` (D10)
- `const SourceKind = config.SourceKind("gcp-secret")`
- `var ErrChecksumMismatch` — the D7 refusal, matchable with `errors.Is`
- `type API`, `type Payload`, `type Version`, `type Secret`, `type Fallback`, `type Option`

`Fallback` (D5) is the module's answer to a limitation it cannot fix: `config.Source`
is per-layer and has no field for a per-key fact, so provenance cannot report
which version served a value. Consumers who need that in `Explain` output should
know it is not coming.

Read-only: the backend satisfies `config.WatchableBackend` and **not**
`config.WritableBackend`. No `config` core change is required — v0.9.2 already
carries everything this adapter needs.

`WithFilter(expr string)` is deliberately absent from this list pending the open
question on it.

## Testing strategy

Per D13, test-first per D14. What would falsely pass, and is therefore watched to
fail explicitly:

- a flat-key test whose fake returns IDs containing no hyphens or underscores —
  it would pass under a separator convention too, so a hyphenated *and* an
  underscored ID are each asserted to stay one key;
- **a fallback test that only asserts the fallback works.** A test where `latest`
  is disabled and version 7 is served would pass identically against an
  implementation that *always* walks the version list and never consults
  `latest` at all — which would be slower, would ignore a pinned version, and
  would be wrong. So the same suite asserts that a **fully-enabled** secret is
  served from `latest` **and that `Versions` is never called** for it (D9 puts
  the walk behind the `Describe` check precisely so the common path does not pay
  for it). Without the call-count assertion the two implementations are
  indistinguishable;
- a fallback test that does not assert the *observer* fired, which would let a
  silent fallback pass — the D5 mitigation is the whole reason the fallback was
  acceptable, so `Fallback{ID, LatestVersion, LatestState, ServedVersion}` is
  asserted field by field, and the no-enabled-version case is asserted to fire
  with `ServedVersion: ""` rather than not firing;
- a no-enabled-version test asserting only that the key is absent, which cannot
  distinguish skipping from refusing — the rest of the project is asserted to
  load in flat mode, and document mode and the pinned-version case are asserted
  to *fail* in the same suite, so D5's mode asymmetry is pinned rather than
  incidental;
- a UTF-8 skip test whose binary fixture is short enough to be accidentally valid
  UTF-8 — the fixture is chosen to be definitively invalid, and a separate case
  documents the accidentally-valid admission rather than pretending it cannot
  happen;
- a CRC test that only asserts an error occurs, which a nil-payload bug would
  also satisfy — the error is asserted to be `ErrChecksumMismatch` and a nil
  `DataCrc32C` is asserted to load *successfully*;
- a name-prefix test whose fake honours the filter as a prefix, which would let
  the server-side hint alone carry the suite — the fake implements `name:` as
  substring containment, exactly as documented, so the client-side re-check is
  the thing under test; and a second case asserts the filter string reaching the
  fake, so a purely client-side implementation cannot pass either;
- an N+1 test asserting only the result — the call counts are asserted, so a
  future batch-shaped refactor cannot silently change the cost, and a label
  filter is asserted to *reduce* the access count;
- a `Sensitive` assertion made only through conformance, which a
  `Sensitive: false` backend would satisfy by routing beneath — `Capabilities()`
  is asserted directly as well;
- a watch test asserting "fired at least once", which cannot distinguish working
  change detection from signalling every tick — the fake advances a version name
  and the poll is asserted quiet before and after;
- a watch test that asserts change detection but not the **call mix**, which
  would pass against the rejected access-every-tick poll — a quiet tick is
  asserted to make **zero** `Access` calls, which is the entire point of D10, and
  a tick with one changed secret is asserted to make exactly one;
- a watch test over a secret in the fallback state that only advances `latest` —
  it would never exercise the second metadata read, so *disabling the served
  version* is asserted to fire a change as well.

## Migration & compatibility

Purely additive: a consumer adds the module and a `WithBackend` call, exactly as
for any other adapter. No `config` core change, no breaking change. Ships v0.1.0
read-only; a later write capability would be a documented minor-version promotion
with its own dated revision here (umbrella D7 requires a spec justifying it).

Consumers should expect the `ErrSensitiveLeak` behaviour the other secrets
adapters introduced: with a Secret Manager layer present, `Set` on a key it
provides is refused rather than written to the file beneath.

The dependency cost (D2) is the one thing a consumer should weigh before
adopting, and the README states it first rather than in a footnote.

## Resolved (2026-07-27)

The open questions were resolved with the human and the decisions above amended
in place (never renumbered, per the specs convention). No open question remains.

1. **Disabled and destroyed `latest` versions.** **Fall back to the newest
   `ENABLED` version** (D5), reversing the draft's proposal to skip. The Key
   Vault precedent the draft copied is weaker than it looked: there a disabled
   secret is usually a deliberate retirement, whereas here the common cause is a
   rotation that half-completed, so skipping would fire the disappearing-key
   failure on the common case rather than the rare one. The cost is accepted with
   open eyes — the adapter **silently serves a credential someone deliberately
   disabled**, and overrules what the operator's console shows as current.
   Because it was chosen knowing that, the mitigation is part of the decision:
   `WithFallbackObserver` signals every non-`latest` resolution at the moment it
   happens. **Provenance cannot carry it** — `config.Source` is per-layer with
   four fields and the fallback is per-key, so `Origin` and `Explain` cannot
   report it, and that is recorded as a limitation rather than worked around. A
   secret with **no enabled version at all** is a distinct case: skipped in flat
   mode (with the observer firing on an empty `ServedVersion`), a failed Load in
   document mode and for a pinned version. A **pinned** version never falls back.

2. **Filtering surface.** `WithLabel` and `WithNamePrefix` ship; **no raw
   `WithFilter`** at v0.1.0 (D8). The family's no-speculative-surface instinct
   wins: a raw expression couples the module's public surface to a Google filter
   dialect it cannot validate, so a typo becomes an opaque `INVALID_ARGUMENT` at
   Load rather than a compile error. It is additive later if a consumer needs it.

3. **Poll interval and audit-log stream.** **Poll `GetSecretVersion`
   (`ADMIN_READ`) and access only what moved** (D10). Computing the arithmetic
   rather than assuming it showed the trade is better than the question implied:
   the metadata poll costs the same `n + 1` calls as the access poll in the
   steady state, pays one extra call only per *changed* secret, and drops
   `DATA_READ` volume from *n* per tick to *k*. It also serves D5 directly, since
   the poll sees each version's `State` on a call it was making anyway. The
   five-minute default stands, now justified by request volume alone.

4. **The umbrella's D6 claim.** **Both** — revise the umbrella *and* track the
   follow-on (D11). Checking the wider claim before correcting it found D6 wrong
   about **five of the six systems it names**, not just GCP Secret Manager: AWS
   Secrets Manager, Azure Key Vault, AWS SSM and Azure App Configuration all have
   out-of-band feeds too. [Umbrella R3](2026-07-21-dynamic-backend-adapters.md)
   introduces a third category and places each system in it. The Pub/Sub watch is
   a tracked follow-on in its **own module** — though the honest finding is that
   the dependency argument for that split is weak (four modules, measured), and
   the split rests on ownership and contract instead.

5. **The location convention.** `Wrap(client, project, location)` with
   `location == ""` meaning the project-level parent stands (D9), matching
   `config-gcp-parameter`'s arity. A separate `WrapRegional` was judged to add a
   second entry point for a difference the doc comment already states.

## Implementation phases

Each phase ships code **and** its documentation (D14), test-first. This spec is
`approved` (resolutions above), so implementation may begin.

**Phase 0 — this spec.** Approved 2026-07-27; open questions resolved above.

**Phase 0.5 — confirm the `latest` semantics.** *New, and deliberately ahead of
Phase 1.* One throwaway probe against a real project: create a secret, add two
versions, disable the newer, and observe what `AccessSecretVersion` and
`GetSecretVersion` on `.../versions/latest` return. This is the first row of
D13's table, and it is pulled forward because **a negative result deletes D5's
fallback** — if the service already skips disabled versions, building the
fallback, the observer, the `Fallback` type and their tests would be work spent
on a branch that can never run. Twenty minutes here avoids that. If it comes back
negative, record the revision withdrawing D5's fallback before writing any code.

**Phase 1 — flat read.** Scaffold the module, the narrow `API` (`Access`,
`Describe`, `Versions`, `List`) and `Wrap`, `New`/`FromClient`, `Load` over
list-then-resolve-then-access with D5's fallback and `WithFallbackObserver`, the
D6 UTF-8 skip and the D7 checksum verification, static `Sensitive` (D10), gRPC
error mapping to `fs.ErrNotExist` (D9), provenance, and the `depfootprint`
allowlist pinning the 31 modules (D2). **Docs:** README leading with the
footprint, the flat-key model, the N+1 cost, and — prominently — that a secret
whose latest version is disabled is served from an older version, with the
observer as the way to know.

**Phase 2 — filtering, document mode and watch.** `WithLabel` and
`WithNamePrefix` with the server-side/client-side split (D8),
`NewSecret`/`FromClientSecret` and `WithValueCodec` (D3), `WithVersion` and its
no-fallback rule (D5), the metadata-based polled `Watch` at the five-minute
default, marking the *served* version (D10). Run `backendconformance` over the
fake — the gate. **Docs:** README's document-secret and filtering sections.

**Phase 3 — integration suite and docs.** The real-service suite (D13), skipped
without credentials, asserting each remaining claim in D13's table directly and
cleaning up after itself. Then `how-to/gcp-secret.md` — leading on the flat-key
model, the N+1 cost, and D5's fallback with its "you may be running on an older
version than the console shows" consequence — plus the ecosystem matrix,
homepage and landing card.

**Phase 4 — verification, then release.** Run the integration suite against a
real GCP project, confirm or correct every claim in D13's table, record each
correction as a dated revision here, and only then cut **v0.1.0**. This phase is
a gate, not a formality: six of the nine claims are `unverified`, and D5 now
rests on several of them rather than one.

**Follow-on (out of scope, tracked — umbrella Phase D).** An opt-in Pub/Sub
event-driven watch in its own module (D11, umbrella R3), with its own spec.

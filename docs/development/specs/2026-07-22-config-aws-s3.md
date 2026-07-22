---
title: config-aws-s3 — the AWS S3 object-store filesystem adapter
date: 2026-07-22
author: matt.cockayne
status: approved
approved: 2026-07-22
---

# config-aws-s3

The first of the cloud object-store trio in the filesystem-adapter family
(umbrella **Phase 3**). Per the [filesystem adapters umbrella
spec](2026-07-22-filesystem-adapters.md) **D2**, no `config-<fs>` adapter is
built without its own approved spec; this is that spec for AWS S3. It cites the
umbrella and settles what D2 leaves to each adapter — the underlying type it
wraps and how each `config.FS` method maps onto it, its capability, how writes
commit and what atomicity actually holds, its dependency footprint, and its
testing surface — and, because S3 is an object store rather than a POSIX
filesystem, it earns every section the umbrella warned it would (**D2**: "an S3
wrapper carries an SDK, an object-store consistency model and a rename that is
not a rename").

`config-afero` is the reference this mirrors for *shape* — a single `Wrap` that
adapts an injected underlying filesystem to `config.FS`, claiming exactly the
optional interfaces it can honour — and `config-aws-ssm` is the reference this
mirrors for its *AWS footprint and emulator rig*: the same narrow injected AWS
client, the same allowlist `depfootprint` test, the same LocalStack
testcontainers integration story. Read both alongside this one; this document
calls out only where S3's object-store semantics make it diverge, and those
divergences are the substance.

## Problem

A tool whose configuration lives in an S3 bucket — a `config.yaml` object under
some key — cannot point this module at it today. The seam to reach it exists and
is proven (umbrella preamble: `WithFiles`, `NewFileBackend` and `NewWatcher` all
take a `config.FS`, and `config-afero` shows a sibling module can supply one).
What is missing is the shipped module — `config-aws-s3` — and the per-store
decisions the umbrella cannot make.

Unlike the `config-aws-ssm` **backend** adapter, this is a **filesystem**
adapter: it does not model object keys *as config keys* (that is the
parameter-store shape, and the umbrella's "Treating the cloud stores as
Backends" rejected alternative keeps the two apart). It reads and writes a
config *file* that happens to live in a bucket, through the same codec and
stage-and-rename write machinery a local file uses. The load-bearing question is
therefore not "how do keys nest" but "does the `config.FS` contract — whose
central operation is an atomic `Rename` commit — survive on a store that has no
atomic rename and no directories". The umbrella's **D6** answers that in
principle; this spec settles it concretely against the verified S3 API.

## Verified against the SDK

Everything in this spec about the S3 API was checked against
`github.com/aws/aws-sdk-go-v2/service/s3` **v1.106.0** (SDK core
`github.com/aws/aws-sdk-go-v2` **v1.43.0**, `github.com/aws/smithy-go`
**v1.27.3**), probed in a throwaway module — never against the config repo's
`go.mod`. The load-bearing facts:

- **Read** is `Client.GetObject(ctx, *GetObjectInput) (*GetObjectOutput, error)`.
  Input: `Bucket *string`, `Key *string`. Output: `Body io.ReadCloser` (the
  object bytes — **must be closed** and fully drained), `ContentLength *int64`,
  `LastModified *time.Time`, `ETag *string`. A missing object surfaces as
  **`*types.NoSuchKey`** (verified: `ErrorCode() == "NoSuchKey"`).
- **Write** is `Client.PutObject(ctx, *PutObjectInput) (*PutObjectOutput, error)`.
  Input: `Bucket *string`, `Key *string`, `Body io.Reader`. **Atomic per object**
  — a reader either sees the whole previous object or the whole new one, never a
  splice — which is the property the stage-and-rename commit rests on (D4). S3 is
  **strongly read-after-write consistent** for PUTs of new and overwritten
  objects (AWS, since Dec 2020), so a write is immediately visible to the next
  read.
- **Stat** is `Client.HeadObject(ctx, *HeadObjectInput) (*HeadObjectOutput, error)`.
  Output carries the same metadata as GetObject — `ContentLength *int64`,
  `LastModified *time.Time`, `ETag *string` — with **no body**. A missing object
  surfaces here as **`*types.NotFound`** (verified: `ErrorCode() == "NotFound"`)
  — a **different type** from GetObject's `NoSuchKey`, which the error mapping
  must handle (D6).
- **Copy** is `Client.CopyObject(ctx, *CopyObjectInput)`. Input: `Bucket *string`
  (destination bucket), `Key *string` (destination key), `CopySource *string` —
  the source as **`"bucket/key"`**, URL-encoded. A server-side copy: the bytes do
  not transit the client. **Atomic per object** at the destination, exactly like
  PutObject.
- **Delete** is `Client.DeleteObject(ctx, *DeleteObjectInput)`. Input: `Bucket`,
  `Key`. Idempotent — deleting an absent key is not an error.
- **No native change feed** in the S3 client. Change *can* be delivered
  out-of-band (S3 Event Notifications → SQS/SNS/EventBridge/Lambda), which is
  **out of scope** — separate services, separate SDK, separate infrastructure the
  consumer would wire. Watch, if used, is polling (D5, D7).
- **Errors** are `smithy.APIError`. Beyond the typed `*types.NoSuchKey` /
  `*types.NotFound`, an S3-**compatible** endpoint (MinIO, LocalStack, Ceph) may
  return a generic `smithy.GenericAPIError` carrying the code as a string, so the
  robust mapping is `errors.As` for the typed errors **and** a fallback on
  `smithy.APIError.ErrorCode()` for `"NoSuchKey"`/`"NotFound"`/`"NoSuchBucket"`
  (**verified**: both mappings compile and the code strings are as stated).

## Decisions

### D1 — Module `config-aws-s3`, package `configawss3`

A **cloud-qualified** name (umbrella D1): the same object-store purpose exists
under three vendors (`config-aws-s3`, `config-gcp-gcs`, `config-azure-blob`), and
the service name is what people search for, so the cloud is carried in the name.
The module is `gitlab.com/phpboyscout/go/config-aws-s3`, package `configawss3`,
matching the `config-<x>` / `configx` convention. The repo carries a README only;
this spec lives centrally here (umbrella D2). It requires `config` **v0.7.0+** —
the release carrying the `config.FS` surface, the optional `RealPather` /
`LinkReader` interfaces and the poll watcher this family depends on. The
read-only `config.ErrReadOnlyFS` sentinel added in the family's Phase 1 core
precursor (umbrella D4, Phase 1) is **not** needed here: this adapter is
read+write and never returns it.

### D2 — The adapter wraps an injected `*s3.Client` + bucket; it owns no credentials

The constructor takes an already-configured `*s3.Client` and the bucket name,
behind the module's own `Wrap` — never re-deciding region, credentials,
endpoint, retry policy or path-style addressing (umbrella D3). This is exactly
`config-afero`'s `Wrap(afero.Fs)` pattern and `config-aws-ssm`'s D5: the consumer
builds the client, which is where the SDK dependency honestly lives, and the
adapter only translates the six `config.FS` calls onto object operations. The
public constructor is:

```go
func Wrap(client *s3.Client, bucket string) config.FS
```

Taking the concrete `*s3.Client` (rather than a hand-rolled narrow interface as
`config-aws-ssm` does for its `ParamStore`) is deliberate and is argued in
Rejected alternatives: the six object operations map one-to-one onto client
methods, the fake used by the unit suite is a small interface the adapter depends
on *internally*, and `Wrap`'s signature stays the umbrella's advertised
`configs3.Wrap(s3Client, "my-bucket")`. Because the consumer supplies the client,
they also choose its endpoint — which is what lets the very same adapter drive an
**S3-compatible** store (MinIO, Ceph, LocalStack) by pointing the client's
`BaseEndpoint` at it. The adapter is oblivious to which it is talking to.

### D3 — Object keys are the filesystem's paths; an optional key prefix scopes the bucket

A `config.FS` name (`"config.yaml"`, `"app/config.yaml"`) **is** an object key,
used verbatim as `Key` against the wrapped bucket. The umbrella passes
`configs3.Wrap(s3Client, "my-bucket")` and then a name to `WithFiles`, so the
name the Store already carries is the key; no path translation is imposed.

The one nuance is the stage path the write machinery synthesises. The Store
commits a write by staging beside the target and renaming over it (fs.go: "content
is staged beside the target and renamed over it"), so it will call
`WriteFile(stageKey, …)` then `Rename(stageKey, targetKey)`. Whatever staging
key the Store chooses is used verbatim as an object key; S3 has no directories,
so a key containing `/` simply creates a flat key — there is nothing to `MkdirAll`
into being first (D5, `MkdirAll` no-op). The adapter also carries an **optional
`WithKeyPrefix`** functional option (so a bucket shared with unrelated objects can
be scoped, the way `config-aws-ssm` takes a required prefix), **shipped in
v0.1.0** (Resolved 2026-07-22, item 3): the `config.FS` name joins onto the prefix
to form the object key, and the staging key derives from that resolved key. It is
additive over the umbrella's advertised `Wrap(client, bucket)`; with no option
passed the name is used as the key verbatim.

### D4 — Capability: read **and** write; per-object-atomic commit; not natively watchable

`config-aws-s3` is a **read+write** `config.FS` (umbrella D4, D6): the underlying
store genuinely supports both, so all six methods are implemented and none
returns `config.ErrReadOnlyFS`. The mapping:

| `config.FS` method | S3 operation | Notes |
|---|---|---|
| `ReadFile(name)` | `GetObject` | drain + close `Body`; `NoSuchKey` → `fs.ErrNotExist` (D6) |
| `WriteFile(name, data, perm)` | `PutObject` | `perm` is **ignored** — S3 has no POSIX mode (D6); atomic per object |
| `Stat(name)` | `HeadObject` | synthesises `fs.FileInfo` (D6); `NotFound` → `fs.ErrNotExist` |
| `Rename(old, new)` | `CopyObject(old→new)` + `DeleteObject(old)` | **two-step, non-atomic** (D6); target replaced atomically by the copy |
| `Remove(name)` | `DeleteObject` | idempotent |
| `MkdirAll(path, perm)` | — | **no-op**, returns nil — object stores have no directories |

The commit still holds despite the non-atomic rename, and this is the crux
(umbrella D6). A write reaches S3 as: `PutObject(stageKey)` then
`CopyObject(stageKey→targetKey)` then `DeleteObject(stageKey)`. Both the initial
PUT and the copy are **atomic per object**, so the *target* is only ever replaced
whole — a reader never sees a half-written object. The single imperfection is
that a crash **between the copy and the delete** leaves the staged object behind:
garbage to be cleaned up, never corruption of the target, never a lost write.
S3 has no atomic move (umbrella **D6/R2** keeps S3 on copy-then-delete), so this
is the honest model. To keep the leak *findable*, the staged object uses a
**deterministic, recognisable key convention** — a documented suffix such as
`.config-stage` on the resolved target key — so an orphan left by a crash is
identifiable and a consumer can lifecycle-expire it by rule rather than hunting
blind. This is stated plainly, not hidden (umbrella D6/R2; Resolved item 1).

It is **not** a `RealPather` (an object key is not an operating-system path) and
**not** a `LinkReader` (S3 has no symlinks), so `Wrap` returns the plain adapter
and the watcher polls it (D5). Content-based conflict detection is unaffected: the
Store fingerprints loaded content by SHA-256 and refuses a write if the source
changed underneath it (umbrella D6), which is content-based, not rename-based, and
works unchanged over a strongly-read-after-write-consistent store.

### D5 — `Stat` synthesises `fs.FileInfo`; mode bits are nominal

S3 has no `fs.FileInfo` and no POSIX metadata, so `Stat` (via `HeadObject`)
synthesises one (umbrella D6):

- `Name()` — the base name of the key.
- `Size()` — `HeadObjectOutput.ContentLength`.
- `ModTime()` — `HeadObjectOutput.LastModified`.
- `Mode()` — a **nominal** regular-file mode, fixed at `0o644`, carrying no real
  permission information because the store has none.
- `IsDir()` — `false` (this adapter stats objects, never prefixes).
- `Sys()` — `nil` (Resolved 2026-07-22, item 2; the raw `HeadObjectOutput` may be
  exposed later by revision if a consumer needs the ETag).

The nominal mode is load-bearing for one interaction: the Store calls `Stat`
before a write "to preserve permissions across a write" (fs.go). On S3 there is
nothing to preserve — `WriteFile` ignores `perm` (D4) — so a synthesised
`0o644` is honest about there being no mode, and round-trips harmlessly. The
nominal mode value is a fixed `0o644` (Resolved 2026-07-22, item 2).

### D6 — Error & consistency semantics

- **Not-exist mapping is the contract's linchpin** (fs.go: `ReadFile` must return
  an error satisfying `errors.Is(err, fs.ErrNotExist)` when absent, because the
  Store treats a missing *optional* source as normal and a missing *base* file as
  a broken installation). `GetObject` returns `*types.NoSuchKey` and `HeadObject`
  returns `*types.NotFound` — **two different types** — so the adapter maps
  **both** to `fs.ErrNotExist`, with a `smithy.APIError.ErrorCode()` fallback for
  S3-compatible endpoints that return a generic error (D-verified). Getting only
  one of the two types would leave `Stat`-absent or `Read`-absent silently
  mis-classified.
- **Strong read-after-write consistency.** S3 has been strongly consistent for
  PUT/GET/HEAD since Dec 2020, so — unlike SSM's eventual consistency — a write is
  immediately visible, and the content-fingerprint conflict check (umbrella D6) is
  reliable. This is a genuine advantage over the parameter-store adapters and is
  stated as such.
- **`Body` handling.** `GetObject`'s `Body` is an `io.ReadCloser` that **must** be
  fully drained and closed; the adapter reads it into a `[]byte` and closes it in
  the same call, so a leaked connection is impossible.
- **Transient failures** (throttling `SlowDown`, 5xx) are left to the SDK's
  built-in adaptive retry, configured on the consumer's client (D2); a persistent
  failure surfaces as a read/write error the Store already tolerates for a
  transient source.

### D7 — Watch is polled; the adapter declares a 60-second interval via `PollIntervalHinter`; a poll is a billed call

S3 has no native change signal in-SDK (D6/verified), and the adapter is not a
`RealPather`, so — exactly as the umbrella promises (**D5**) — when a consumer
calls `Watch` the core builds a `pollWatcher` for it: it re-reads on an interval
and stays quiet if nothing resolved differently, coalesced and settled by the
Store's existing hybrid watch. **No new mechanism is needed or added** — the poll
watcher and `WithPollInterval` already ship in the core.

What this adapter owes is a *sensible interval*, and — now that R1 gives it a
mechanism — it applies one rather than only recommending it. The core's
`DefaultPollInterval` is **2 seconds**, which is far too eager for S3: every poll
is a **billed** `GetObject`/`HeadObject`, and a 2-second cadence is 43,200 API
calls per object per day. So the adapter **implements the optional
`config.PollIntervalHinter`** interface (umbrella R1), returning **60 seconds** —
consistent with the rest of the remote family (`config-aws-ssm` uses a 60-second
poll default) and reasoned the same way (config is provisioned by a pipeline, not
edited in a hot loop; up-to-a-minute staleness is acceptable while the bill is
bounded at ~1 call/min). A changed config object is still caught within a minute.

The mechanics: this adapter is a `config.FS`, and the core poll watcher tuned by
`WithPollInterval` is a *Store* setting. R1 closes the gap the draft lamented —
the adapter no longer merely *documents* a number it cannot apply. By
implementing `config.PollIntervalHinter` it **declares** its 60-second hint to
the Store, which reads the hint when it builds the poll watcher, so a consumer who
passes no `WithPollInterval` gets the adapter's 60-second cadence rather than the
core's 2-second default. `WithPollInterval`, when passed, still overrides the
hint. This removes the risk the draft could only warn about in the README.

### D8 — Dependency footprint: the S3 service SDK, stated plainly

`config-aws-s3` depends on the modular AWS SDK for Go v2, pulling **only the S3
service** and its required internals — not the whole SDK (umbrella D7). **Measured**
(`go list -deps` over a package importing `service/s3`, `service/s3/types`, `aws`
and `smithy-go`), the adapter's own library graph pulls **11 modules**:

```
github.com/aws/aws-sdk-go-v2
github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream
github.com/aws/aws-sdk-go-v2/internal/configsources
github.com/aws/aws-sdk-go-v2/internal/endpoints/v2
github.com/aws/aws-sdk-go-v2/internal/v4a
github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding
github.com/aws/aws-sdk-go-v2/service/internal/checksum
github.com/aws/aws-sdk-go-v2/service/internal/presigned-url
github.com/aws/aws-sdk-go-v2/service/internal/s3shared
github.com/aws/aws-sdk-go-v2/service/s3
github.com/aws/smithy-go
```

That is more than `config-aws-ssm`'s 5 modules, and honestly so: S3's service
package requires the checksum, s3shared, presigned-url, accept-encoding, v4a and
eventstream internal modules that Parameter Store does not. As with SSM, a
**consumer** who also builds the client with `config.LoadDefaultConfig`
additionally pulls the credential/SSO/STS/IMDS modules — but that larger graph is
the consumer's, incurred by their credential loading (D2), not the adapter's. A
`depfootprint_test.go` allowlist asserts the adapter's own module set (scoped to
`go list -deps .`, excluding both the consumer's client-build modules and the
test-only testcontainers/LocalStack deps), so an unexpected dependency — or the
SDK growing one — is a failing test, not a surprise in a consumer's `go.sum`. And
because it is its own module (umbrella D1), a consumer reading from S3 never
compiles the GCS or Azure SDK.

### D9 — Testing: a fake object store for units, LocalStack testcontainers for the real thing

Two layers, from the umbrella's **D8** and mirroring `config-aws-ssm` D11:

1. **A hand-written in-memory fake object store** — a `map[string][]byte` with
   last-modified timestamps — satisfies the small internal interface the adapter
   depends on (the six object operations), and drives the unit suite with no AWS
   and no network: read, write, the two-step rename (asserting the target is
   replaced *and* the staged object is deleted on the happy path), `Remove`
   idempotence, `MkdirAll` no-op, `Stat` synthesis (size/mod-time/nominal mode),
   and both not-exist mappings (`NoSuchKey` on read, `NotFound` on stat →
   `fs.ErrNotExist`). Crucially it also drives the module's **existing `config.FS`
   behaviour suite** (umbrella testing strategy): a wrapped store must read, write,
   stage-and-rename and round-trip content exactly as `config.OS()` does. A
   **crash-between-copy-and-delete** case is simulated to prove the target is intact
   and only the stage object is orphaned (D4, D6).
2. **LocalStack testcontainers integration** — **verified available**:
   `github.com/testcontainers/testcontainers-go/modules/localstack` **v0.43.0**
   (`localstack.Run(ctx, "localstack/localstack:3")`, then
   `container.PortEndpoint(ctx, "4566/tcp", "http")` for the endpoint). S3 is a
   **core LocalStack service**, so the endpoint is set as the client's
   `BaseEndpoint` (with path-style addressing, `UsePathStyle: true`, which
   LocalStack/MinIO need) to build a real `*s3.Client` passed through `Wrap`, and a
   real bucket is created and exercised end-to-end — read, write, rename-as-copy,
   delete — against a real S3 API surface. These live under `./test/integration/`
   and are **env-gated on `INT_TEST_INTEGRATION`** (config-aws-ssm D11), kept
   compiled and IDE-discoverable but out of the ordinary unit job. The same rig
   points at MinIO with only a different endpoint, which the README notes as the
   S3-compatible story.

Integration runs in CI on the **`go-test` component's opt-in DIND job**
(phpboyscout/cicd, config-aws-ssm D12): `enable_integration: true`,
`integration_paths: "./test/integration/..."`, which supplies `docker:dind`,
`DOCKER_HOST`, `FF_NETWORK_PER_BUILD`, `TESTCONTAINERS_RYUK_DISABLED=true` and
`INT_TEST_INTEGRATION=1`. The ordinary merge gate stays Docker-free — the fake
suite carries it — and the DIND job runs the real-S3 (LocalStack) proof.

## Rejected alternatives

**Model the bucket as a Backend (object keys as config keys).** That is a
different, legitimate thing — the parameter-store shape `config-aws-ssm`
implements — but it is not what "filesystem adapter" means (umbrella "Treating the
cloud stores as Backends" rejected alternative). This adapter reads a config
*file* that lives in a bucket, through the same codec and write machinery a local
file uses. The object-as-key model, if ever wanted, is a separate spec.

**Take a hand-rolled narrow client interface in `Wrap` (as `config-aws-ssm`
does).** Rejected for the constructor signature: the umbrella advertises
`configs3.Wrap(s3Client, "my-bucket")`, and the six object operations map
one-to-one onto `*s3.Client` methods with no data-model translation (unlike SSM's
pagination + flat-to-nested), so a narrow *public* interface buys little. The
adapter still depends on a small *internal* interface (the operations it calls) so
the fake can drive the unit suite — the testability the narrow interface exists
for — without widening the public surface.

**Emulate directories with zero-byte prefix objects so `MkdirAll` "works".**
Rejected: object stores have no directories (umbrella D6), a config write never
needs one (a flat key with `/` in it just works), and creating prefix marker
objects would litter the bucket and confuse other tools. `MkdirAll` is a no-op
(D4).

**Make `Rename` atomic with S3 multipart or conditional writes.** There is no
atomic server-side rename in S3, and conditional PUT (`If-None-Match`) guards
create-if-absent, not an atomic move. The copy-then-delete with a possible
orphaned stage object is the honest model (umbrella D6); pretending otherwise
would claim an atomicity the API cannot deliver.

**Use S3 Event Notifications for native watch.** Change *can* be pushed via
SQS/SNS/EventBridge/Lambda, which would make native watch defensible — but it is
separate services, separate SDK, separate IAM, and infrastructure the consumer
must provision, far beyond "inject a client" (umbrella D3; config-aws-ssm's
EventBridge rejection). Polling with a long stated interval (D7) is the honest
default; push is a future revision if a consumer needs the latency.

**Manage AWS auth/region/endpoint inside the adapter**
(`configawss3.New(region, bucket)`). Rejected per umbrella D3: it duplicates the
consumer's `aws.Config`, couples the adapter to one credential and endpoint model,
and — critically — is what would *prevent* the S3-compatible (MinIO/LocalStack)
use case, which falls out for free precisely because the consumer owns the
endpoint (D2).

## Public API

The exported surface (read+write `config.FS`):

- `func Wrap(client *s3.Client, bucket string, opts ...Option) config.FS` — the
  injection seam, the umbrella's advertised signature widened with variadic
  options. Returns the adapter (neither `RealPather` nor `LinkReader`, but a
  `config.PollIntervalHinter`), so the Store polls it on the adapter's 60-second
  hint (D7).
- `func WithKeyPrefix(prefix string) Option` — scopes the bucket by joining
  `prefix` onto each `config.FS` name to form the object key (D3; Resolved
  2026-07-22, item 3). Shipped in v0.1.0.

That is the public surface for v0.1.0 — the constructor plus the `WithKeyPrefix`
option, close to as minimal as `config-afero`'s `Wrap`. The nominal `Stat` mode is
fixed at `0o644` and `Sys()` is `nil` (D5; Resolved items 2), not options. Further
functional options can be added additively over `Wrap`'s variadic without breaking
it should a later dated revision want them. No change to the `config` core is
required beyond the optional `config.PollIntervalHinter` this adapter implements
(umbrella R1): the `config.FS` interface, the optional interfaces this adapter
deliberately does *not* claim, and the poll watcher all already ship (umbrella
Public API).

## Testing strategy

Per D9: an in-memory fake-object-store unit suite (read, write, two-step rename
with stage-cleanup assertion, `Remove` idempotence, `MkdirAll` no-op, `Stat`
synthesis, both not-exist mappings, a simulated crash-between-copy-and-delete
leaving the target intact and the stage orphaned), which also runs the module's
existing `config.FS` round-trip behaviour; a LocalStack testcontainers
integration suite under `./test/integration/`, env-gated on `INT_TEST_INTEGRATION`
and run on the go-test DIND job (D9); and a `depfootprint_test.go` allowlist
(D8). What would **falsely pass** and the fake's fidelity must therefore close:

- (a) a not-exist test that maps only `NoSuchKey` — a `Stat` on an absent object
  returns `NotFound`, a *different* type (D6), so a missing base file would be
  mis-reported as some other error and the Store's "broken installation" signal
  lost; the fake must return the type each operation really returns, and the test
  must assert `errors.Is(err, fs.ErrNotExist)` for **both** `ReadFile` and `Stat`.
- (b) a rename test that asserts only that the target now holds the new content —
  it would pass even if the staged object were never deleted, hiding the orphan
  leak; the test must **also** assert the staged key is gone on the happy path.
- (c) a write test that asserts the bytes but not atomicity — the fake must not
  expose a half-written object mid-`PutObject`, and the crash-simulation test must
  prove the target is either fully old or fully new, never spliced (D4, D6).

Per the module's testing rule, each assertion is watched to fail before it is
trusted.

## Resolved (2026-07-22)

All open questions were answered by the human on 2026-07-22, and the decisions
above were amended in place to match (no decision renumbered). Each records what
was decided and why, so the choice rests on rationale rather than deferral.

1. **Copy-then-delete rename — recognisable staging key, leak accepted and
   documented** (amends D3, D4). `Rename` stays `CopyObject` + `DeleteObject`;
   S3 has no atomic move and the umbrella's **D6/R2** keeps S3 on copy-then-delete,
   so pretending otherwise is off the table. The staged object uses a
   **deterministic, recognisable key convention** — a documented suffix such as
   `.config-stage` on the resolved target key — so a crash between the copy and the
   delete leaves a *findable* orphan that a consumer can lifecycle-expire by rule.
   The target is always intact: this is a **leak, never corruption**, and never a
   lost write. v0.1.0 does **no** opportunistic cleanup of prior orphans on write —
   the recognisable key plus a bucket lifecycle rule is the whole story, and
   scanning for and sweeping strangers' orphans is out of scope.

2. **`Stat` mode — fixed `0o644`, `Sys()` nil** (confirms D5). The synthesised
   `fs.FileInfo` reports a nominal **`0o644`** — it reads as an ordinary config file
   and round-trips harmlessly through the Store's preserve-permissions step, which
   has nothing to preserve on S3 anyway. The mode is **fixed**, not an option and
   not derived from object metadata (there is nothing suitable to derive it from).
   `FileInfo.Sys()` returns **`nil`** for v0.1.0; exposing the raw
   `*HeadObjectOutput` (ETag and the rest) to consumers who want it can be added
   later by a dated revision if a real need appears, without breaking anything.

3. **Key prefix — optional `WithKeyPrefix`, shipped in v0.1.0** (amends D3). The
   adapter offers an **optional `WithKeyPrefix` functional option** that scopes a
   bucket shared with unrelated objects, mirroring `config-aws-ssm`'s prefix. The
   `config.FS` name joins onto the prefix to form the object key; the staging key
   (item 1) derives from that **resolved** key, so prefix and staging compose
   cleanly. It is additive and cheap over the umbrella's advertised
   `Wrap(client, bucket)`, so rather than deferring it to a later revision it
   **ships in v0.1.0**. With no option passed, behaviour is exactly the draft's
   verbatim-key mapping.

4. **Poll cadence — implements `config.PollIntervalHinter`, 60-second default**
   (amends D7). A poll is a **billed** `GetObject`/`HeadObject`, so the core's
   2-second `DefaultPollInterval` is far too eager. Rather than only *documenting* a
   recommended `WithPollInterval` it cannot apply, `config-aws-s3` **implements the
   new optional `config.PollIntervalHinter`** (umbrella **R1**), returning
   **60 seconds** — consistent with the rest of the remote family (`config-aws-ssm`
   uses a 60-second poll default) and reasoned the same way: config is provisioned
   by a pipeline, not edited in a hot loop, so up-to-a-minute staleness is fine
   while the bill is bounded at ~1 call/min. The adapter now **declares** the number
   to the Store rather than hoping a consumer reads the README; `WithPollInterval`
   still overrides the hint when passed.

**No open questions remain.**

## Implementation phases

**Phase 0 — this spec.** Approved 2026-07-22; the four questions the draft
surfaced were settled by the human and recorded in Resolved (2026-07-22), with the
decisions amended in place (umbrella D2).

**Phase 1 — the module, read+write path.** Scaffold `config-aws-s3` (README-only,
no microsite; umbrella D2), the internal object-operation interface and its
`*s3.Client` adapter, `Wrap(client, bucket)`, the six `config.FS` methods mapping
onto GetObject/PutObject/HeadObject/CopyObject+DeleteObject/DeleteObject/no-op
(D4), `Stat` synthesis (D5), and the dual not-exist mapping (D6). Fake-object-store
unit suite including the crash-between-copy-and-delete case and the module's
`config.FS` round-trip behaviour; `depfootprint` allowlist asserting the 11-module
graph (D8).

**Phase 2 — real-S3 integration.** LocalStack testcontainers suite (D9), env-gated,
on the go-test DIND CI job — read/write/rename/delete end-to-end against a real S3
API surface, plus a MinIO-endpoint note for the S3-compatible story. Then publish
v0.1.0: the module `main`, the `config` docs page (`how-to/aws-s3.md`) bundled into
the parent site, and the landing card — the same rollout every adapter takes.

**Phase 3 (as needed) — later revisions.** The four questions are resolved and
their v0.1.0 answers land in Phases 1–2: `WithKeyPrefix` (item 3), the recognisable
`.config-stage` staging key (item 1), the fixed `0o644` mode with `Sys()` nil
(item 2) and the `config.PollIntervalHinter` 60-second hint (item 4). What remains
genuinely deferred are the resolutions' own "later by revision" hooks — exposing
the raw `*HeadObjectOutput` through `Sys()` (item 2), opportunistic cleanup of
prior orphaned stage objects (item 1), or push-based watch via S3 Event
Notifications — each added additively over `Wrap`, by a dated revision here, never
silently.

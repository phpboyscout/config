---
title: config-gcp-gcs — the GCP Cloud Storage filesystem adapter
date: 2026-07-22
author: matt.cockayne
status: draft
---

# config-gcp-gcs

The GCP member of the cloud object-store trio the [filesystem adapters umbrella
spec](2026-07-22-filesystem-adapters.md) names for **Phase 3** (alongside
`config-aws-s3` and `config-azure-blob`). Per the umbrella's **D2**, no adapter
is built without its own approved spec; this is that spec for Google Cloud
Storage. It cites the umbrella and settles what D2 leaves to each adapter: the
underlying type it wraps and how each `config.FS` method maps onto it; its
capability; how writes commit and what atomicity actually holds; its dependency
footprint; and its testing surface.

The reference for shape is [`config-afero`](https://gitlab.com/phpboyscout/go/config-afero):
a `Wrap` that adapts an injected underlying filesystem to `config.FS`, claiming
exactly the optional interfaces the underlying thing can honour. Where an object
store diverges from a POSIX filesystem — and it diverges exactly where the
umbrella's **D6** says it does — this spec says why, and matches the
footprint-honesty and emulator-testing framing of the sibling
[`config-gcp-parameter`](2026-07-22-config-gcp-parameter.md) spec, whose GCP
client stack is the heaviest in either family.

## Problem

A tool whose configuration lives in a **GCS bucket** cannot point this module at
it today. The core reads and writes through `config.FS`, and the built-in
implementations (`config.OS()`, `config.Dir`) and `config-afero` all assume a
POSIX-shaped filesystem with a real path and an atomic `rename(2)`. A
GCP-native workload — a Cloud Run service, a GKE pod, a batch job — routinely
keeps its configuration file in Cloud Storage, reached through a
`*storage.Client` the workload already builds for its data. There is no adapter
that lets such a tool load, watch and write that file through the same codec and
staging machinery a local file uses.

The umbrella settled **that** we ship a GCS filesystem adapter and **under what
shared conventions** (D1–D8). What it left to this spec is the concrete mapping:
how six `config.FS` methods land on `ObjectHandle.NewReader`/`NewWriter`/`Attrs`/
`CopierFrom`/`Delete`, what "atomic" honestly means over an object store, what a
poll costs, and what the SDK weighs. This adapter reads a config *file* that
happens to live in a bucket — it is emphatically **not** the object-keys-as-
config-keys model, which the umbrella's Rejected alternatives place in the
`Backend` family, not here.

## Decisions

### D1 — Module `config-gcp-gcs`, package `configgcpgcs`

Cloud-qualified, per umbrella **D1**: an object store carries its cloud, because
the same purpose (a bucket-backed filesystem) exists under AWS, Azure and GCP,
and "GCS" / "Cloud Storage" is what people search for. The module is
`gitlab.com/phpboyscout/go/config-gcp-gcs`, package `configgcpgcs`, matching the
`config-<x>` / `configx` convention. The repo carries a **README only**; this
spec lives centrally here (umbrella D2), and the how-to is a page in the parent
`config` docs site (`how-to/gcp-gcs.md`), bundled per the adapter docs model — no
standalone microsite.

The adapter is **read+write** (D3) and needs no core change: `config.FS`,
`RealPather`, `LinkReader` and the poll watcher (`WithPollInterval`,
`DefaultPollInterval`) all already ship. It does **not** return
`config.ErrReadOnlyFS` (umbrella D4) — that sentinel is for the read-only
members (`config-iofs`); a GCS bucket is a writable store and this adapter
implements every write method for real.

### D2 — Wraps a `*storage.Client` + bucket, verified against the real SDK

The adapter wraps an already-configured `*storage.Client` and a bucket name.
Verified against **`cloud.google.com/go/storage` v1.64.0** (`go doc`):

- **`storage.NewClient(ctx, opts...)` → `*Client`**; `client.Bucket(name)` →
  `*BucketHandle`; `bucket.Object(key)` → `*ObjectHandle`. The `ObjectHandle` is
  the unit every method below operates through.
- **`ObjectHandle.NewReader(ctx) (*Reader, error)`** — `Reader` implements
  `io.Reader`; `Close` releases it. Drives `ReadFile`.
- **`ObjectHandle.NewWriter(ctx) *Writer`** — `Writer` implements `io.Writer`;
  **the upload commits on `Close()`**, and `Close` is where a write error
  surfaces (a mid-stream failure is abandoned with `CloseWithError`). Drives
  `WriteFile`. This is the load-bearing atomicity primitive (D5).
- **`ObjectHandle.Attrs(ctx) (*ObjectAttrs, error)`** — carries `Size int64`,
  `Updated time.Time`, `Created time.Time`, `Generation int64`,
  `CRC32C uint32`, `MD5 []byte`. Drives `Stat`. `Generation` is the
  server-assigned version of the object's content, load-bearing for
  preconditions (D5).
- **`dst.CopierFrom(src).Run(ctx) (*ObjectAttrs, error)`** — server-side copy,
  no bytes through the client. Drives the copy half of `Rename` (D5).
- **`ObjectHandle.Delete(ctx) error`** — drives `Remove` and the delete half of
  `Rename`.
- **`storage.ErrObjectNotExist`** — the absent-object sentinel, checked with
  `errors.Is` (the SDK doc says so explicitly). Mapped to `fs.ErrNotExist`
  (D6).

The consumer builds the client — **all** authentication, project selection,
endpoint (global vs a custom/emulator endpoint), scopes and retry policy live
there (umbrella **D3**). The adapter re-decides none of it; it only translates
the six `config.FS` calls onto the handle. A fake in-memory object store then
drives the unit suite with no cloud (D7).

### D3 — Public API: one `Wrap`, mirroring the family

```go
// Wrap adapts a GCS bucket, reached through an already-configured client, to
// config.FS. The consumer owns the client (auth, project, endpoint, retry —
// umbrella D3); the adapter owns only the six-method translation.
func Wrap(client *storage.Client, bucket string) config.FS
```

That is the whole exported surface for v0.1.0, mirroring `configafero.Wrap` and
the umbrella's `configs3.Wrap(s3Client, "my-bucket")` sketch. The result is
passed to `WithFiles`/`NewFileBackend` exactly as `config.OS()` is:

```go
store, err := config.NewStore(ctx,
    config.WithFiles(configgcpgcs.Wrap(client, "my-bucket"), "app.yaml"))
```

A `name` passed to any method is the **object key** within the bucket
(`app.yaml`, `env/prod/app.yaml`) — object stores have a flat key namespace with
`/` as a conventional separator, not real directories (D5, `MkdirAll`). There is
no `New`/`FromClient` split as in `config-gcp-parameter`: a filesystem adapter
has no narrow multi-method client interface to fake behind — the fake is an
in-memory `config.FS`-shaped object store the tests build directly (D7), and the
only production seam is the `*storage.Client` itself, which `Wrap` takes.

### D4 — Method mapping (the six, plus the two optional)

| `config.FS` method | GCS mapping | Notes |
|---|---|---|
| `ReadFile(name)` | `Bucket(b).Object(name).NewReader(ctx)`, read to EOF, `Close` | `ErrObjectNotExist` → `fs.ErrNotExist` (D6) |
| `WriteFile(name, data, perm)` | `Object(name).NewWriter(ctx)`; `Write(data)`; `Close()` | **commits atomically on `Close`** (D5); `perm` is ignored (no POSIX mode) |
| `Stat(name)` | `Object(name).Attrs(ctx)` → synthesised `fs.FileInfo` (D5) | `ErrObjectNotExist` → `fs.ErrNotExist` |
| `Rename(old, new)` | `Object(new).CopierFrom(Object(old)).Run(ctx)`; then `Object(old).Delete(ctx)` | copy-then-delete, per umbrella **D6** (D5) |
| `Remove(name)` | `Object(name).Delete(ctx)` | `ErrObjectNotExist` → `fs.ErrNotExist` |
| `MkdirAll(path, perm)` | **no-op**, returns nil | object stores have no directories, only key prefixes (umbrella D6) |
| `RealPath` | **not implemented** | no OS path → polled, not fsnotify (D5, umbrella D5) |
| `Readlink` (`LinkReader`) | **not implemented** | GCS has no symlinks; paths used as given |

The adapter therefore returns a **plain** `config.FS` (no optional interface),
the object-store analogue of `config-afero`'s `default` case: config polls it and
uses keys as given. Every method takes a `context.Context` internally; because
`config.FS` methods are context-free, the adapter holds a base context (from
`Wrap`, or `context.Background()` — see Open questions) and derives per-call
contexts from it.

### D5 — Object-store semantics: what "atomic" honestly means (umbrella D6)

GCS is not POSIX, and the contract meets it honestly rather than pretending
otherwise. This is the umbrella's **D6** made concrete against the verified SDK:

- **`WriteFile` is atomic per object.** `NewWriter` buffers/uploads and the
  object becomes visible **only when `Close()` succeeds** — a reader doing
  `NewReader` either sees the whole previous object or the whole new one, never a
  half-written object. GCS is **strongly read-after-write consistent** (including
  for updates and deletes, since 2020), so the fingerprint conflict trap below
  holds without an eventual-consistency caveat.

- **`Rename` is copy-then-delete, and there is no atomic rename.** `WriteFile`
  maps to an upload and rename's copy maps to `CopierFrom(...).Run` — both atomic
  *per object*, so the **target** (`new`) is still replaced atomically and a
  reader never sees a half-written target. The only imperfection: a crash
  **between** the copy succeeding and the delete completing leaves the source
  object (`old`, the staged content) behind — garbage to clean up, **never**
  corruption of the target. This is stated in the README, not hidden (umbrella
  D6).

- **`MkdirAll` is a no-op.** No directories exist to create; `env/prod/app.yaml`
  is a single flat key. Returning nil is correct, not a lie — the subsequent
  `WriteFile` to that key succeeds with no parent.

- **`Stat` synthesises a `fs.FileInfo`** from `ObjectAttrs`: `Size()`→`attrs.Size`,
  `ModTime()`→`attrs.Updated`, `Name()`→the last `/`-segment of the key,
  `IsDir()`→`false`, `Mode()`→a **nominal** regular-file mode (e.g. `0644`), and
  `Sys()` may expose the raw `*ObjectAttrs` for a caller that wants
  `Generation`/`CRC32C`. Mode bits are nominal because the store has none; the
  core uses `Stat` to preserve permissions across a write and to confirm a
  watched path is the file it thinks it is, and both degrade gracefully to
  "nominal mode, real size and mtime".

- **Conflict detection still holds, content-based.** The core's write path
  fingerprints the loaded source by SHA-256 and refuses a write if the source
  changed underneath it. That is content-based, not rename-based, so it works
  unchanged over GCS given strong consistency (umbrella D6). **GCS additionally
  offers a native guard** — `ObjectHandle.If(storage.Conditions{...})` with
  `GenerationMatch` / `DoesNotExist` preconditions on the write and copy — which
  could make the commit *server-side* conflict-safe rather than relying on the
  read-modify-check window. v0.1.0 keeps the content-fingerprint model (it is the
  shared machinery and needs no per-adapter code), and preconditions are surfaced
  as an Open question, not adopted here.

- **Not a `RealPather` → polled reload (umbrella D5).** A GCS object has no OS
  path, so the adapter does not implement `RealPath`; when the consumer calls
  `Watch`, the core builds a `pollWatcher` that re-reads on an interval and stays
  quiet if nothing resolved differently. **No new mechanism** — this ships in the
  core. The one thing the adapter owes is a *sensible* interval: the core default
  is `DefaultPollInterval` = **2s**, which for GCS is far too eager because **each
  poll is a billed API call** (a `NewReader` or `Attrs` per watched key). The
  README therefore **documents the cost and recommends a much longer interval**
  via the existing `WithPollInterval` — a **60s** starting point, matching the
  conservatism of `config-gcp-parameter`'s poll default and for the same reason
  (billed-per-tick, no free long-poll). The adapter does **not** invent a new
  watch API or change the core default; it advises the consumer to set the option.
  (Open question: whether the adapter can/should nudge a default without a core
  change — see below.)

### D6 — Error and absence semantics

- **Absent object.** `storage.ErrObjectNotExist` (from `NewReader`, `Attrs`,
  `Delete`) maps to `fs.ErrNotExist`, checked with `errors.Is` per the SDK's own
  guidance. This is load-bearing: the core treats a missing **optional** source
  as normal and a missing **base** source as fatal, and only that mapping lets it
  decide (umbrella contract; `fs.go` `ReadFile` doc).
- **Other errors.** GCS errors surface as `*googleapi.Error` (HTTP) or gRPC
  status errors depending on transport; the adapter returns them **wrapped with
  context** (which bucket/key and which operation failed) so a caller sees what
  broke, but does **not** re-map `403`/`5xx`/quota errors onto `fs` sentinels —
  only not-found is meaningful to the core's optional/fatal decision. Retry and
  backoff are the client's (`storage` has built-in retry via `SetRetry` /
  `Retryer`); the adapter does not re-implement them (umbrella D3).
- **Bucket absent.** `storage.ErrBucketNotExist` is a configuration error, not a
  missing config file; it is returned wrapped, **not** mapped to `fs.ErrNotExist`
  — a nonexistent bucket must fail loudly, not be silently treated as "optional
  source absent".

### D7 — Testing: a fake object store for units, fake-gcs-server for integration

Per umbrella **D8**, two layers plus a footprint assertion:

1. **A fake in-memory object store** drives the unit suite with no network and no
   credentials. `config.FS` is only six methods, so the fake is a `map[string][]byte`
   (plus per-key mtime/generation) behind the same six-method surface, exercising:
   read; write-then-read; the stage-and-rename commit (copy-then-delete, and the
   orphaned-source-on-crash case simulated by failing between copy and delete);
   `MkdirAll` no-op; `Stat` synthesis (size + mtime real, mode nominal);
   `ErrObjectNotExist` → `fs.ErrNotExist`; and the poll watcher detecting a
   changed object. Because the only production seam is `*storage.Client`, the
   unit fake substitutes at the `config.FS` boundary, and a thin **adapter-level**
   test confirms `Wrap` wires a real client's handles to the same behaviours.

2. **Real-emulator integration**, env-gated (the umbrella's convention; e.g.
   `INT_TEST_INTEGRATION`), against **fake-gcs-server** (`fsouza/fake-gcs-server`)
   run as a Docker image **under testcontainers-go**. Verified wiring: the storage
   client is pointed at the emulator either by the **`STORAGE_EMULATOR_HOST`**
   environment variable (honoured directly by `cloud.google.com/go/storage`
   v1.64.0 — confirmed in the SDK's `doc.go` and `emulator_test.sh`) **or**, more
   hermetically, by passing `option.WithEndpoint("http://<host:port>/storage/v1/")`
   together with `option.WithoutAuthentication()` (both verified present in
   `google.golang.org/api/option`) so no ADC is required. Unlike
   `config-gcp-parameter` — which has **no** emulator and is real-service-only —
   GCS **does** have a first-class fake, so this adapter gets real integration
   tests in an ordinary DIND job (umbrella D8). testcontainers-go has **no
   dedicated GCS module** (its `gcloud` module covers Bigtable/Datastore/Firestore/
   Spanner/Pub/Sub, not GCS), so the emulator runs via a **generic container** on
   the `fsouza/fake-gcs-server` image with a fixed port and a readiness wait — a
   small, well-trodden amount of test wiring, not a blocker.

3. **A `depfootprint_test.go` allowlist** (D8) asserts the external-module set, so
   an SDK bump that grows the graph is a failing test, not a silent `go.sum`
   surprise.

What would falsely pass and the suite must prevent: a rename test whose fake
implements `Rename` as an in-place map-key swap would never exercise the
copy-then-delete path or the orphan-on-crash window — so the fake must model
rename as genuinely copy + delete and a test must assert the source object is
gone after a clean rename and **left behind** after a simulated mid-rename crash.
A poll-watch test whose fake never changes an object's generation would pass
without watching anything — so the watch test must mutate the object and assert
`onChange` fires (and does **not** fire when nothing changed).

### D8 — Dependency footprint: the GCS client stack, the heaviest in the family

`config-gcp-gcs` depends on `cloud.google.com/go/storage` and, transitively, the
full Google Cloud Go client stack. Measured against **v1.64.0** with
`go list -deps`: **46 distinct external modules / ~440 non-stdlib packages** —
**heavier than `config-gcp-parameter`'s** graph, and therefore **the largest of
any adapter in the filesystem or backend families**. GCS pulls everything the
Parameter Manager client does **plus** an extra tranche the storage client's
observability and mTLS path drags in: `cloud.google.com/go/monitoring`, the
`GoogleCloudPlatform/opentelemetry-operations-go` detector+exporter,
`go.opentelemetry.io/contrib/detectors/gcp`, `github.com/google/s2a-go`,
`github.com/spiffe/go-spiffe/v2`, `github.com/envoyproxy/go-control-plane` +
`github.com/cncf/xds/go` + `github.com/envoyproxy/protoc-gen-validate`,
`cel.dev/expr`, `github.com/go-jose/go-jose/v4` and `github.com/google/uuid`. The
distinct external module groups (v1.64.0):

```
cel.dev/expr                                cloud.google.com/go
cloud.google.com/go/auth                    cloud.google.com/go/auth/oauth2adapt
cloud.google.com/go/compute/metadata        cloud.google.com/go/iam
cloud.google.com/go/monitoring              cloud.google.com/go/storage
github.com/cespare/xxhash/v2                 github.com/cncf/xds/go
github.com/envoyproxy/go-control-plane/envoy github.com/envoyproxy/protoc-gen-validate
github.com/felixge/httpsnoop                 github.com/go-jose/go-jose/v4
github.com/go-logr/logr                      github.com/go-logr/stdr
github.com/googleapis/enterprise-certificate-proxy
github.com/googleapis/gax-go/v2              github.com/google/s2a-go
github.com/google/uuid                       github.com/spiffe/go-spiffe/v2
github.com/GoogleCloudPlatform/opentelemetry-operations-go/detectors/gcp
github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/metric
github.com/GoogleCloudPlatform/opentelemetry-operations-go/internal/resourcemapping
golang.org/x/crypto                          golang.org/x/net
golang.org/x/oauth2                          golang.org/x/sync
golang.org/x/sys                             golang.org/x/text
golang.org/x/time                            google.golang.org/api
google.golang.org/genproto                   google.golang.org/genproto/googleapis/api
google.golang.org/genproto/googleapis/rpc    google.golang.org/grpc
google.golang.org/protobuf                   go.opentelemetry.io/auto/sdk
go.opentelemetry.io/contrib/detectors/gcp    go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc
go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp
go.opentelemetry.io/otel                     go.opentelemetry.io/otel/metric
go.opentelemetry.io/otel/sdk                 go.opentelemetry.io/otel/sdk/metric
go.opentelemetry.io/otel/trace
```

This is the honest cost of a first-party cloud SDK (umbrella **D7**), and the
whole reason GCS is its **own** module (D1): a consumer reading config from S3 or
Azure — or from a local file — **never** compiles this graph. The `depfootprint`
allowlist pins the set; because the storage SDK bumps its transitive graph often,
the allowlist is expected to need periodic reviewed updates, which is the visible
cost the umbrella intends, not a reason to hide it. The exact pinned set is
captured at implementation time against the SDK version then current.

## Rejected alternatives

**Take `bucket string` only and build the client inside `Wrap`.** Rejected: it
would force the adapter to re-decide authentication, project and endpoint —
exactly what umbrella **D3** places with the consumer, and where the SDK
dependency honestly lives. `Wrap(client, bucket)` keeps configuration
consumer-owned and lets a fake `config.FS` drive units with no cloud.

**Model the bucket as object-keys-as-config-keys (a `Backend`).** Rejected by the
umbrella's own Rejected alternatives: that is a different, legitimate thing (the
parameter-store shape, `config-gcp-parameter`), not a filesystem adapter. This
adapter reads a config **file** in a bucket through the same codec a local file
uses.

**Use `ObjectHandle.Move` for `Rename` instead of copy-then-delete.** The SDK
(v1.64.0) **does** expose `ObjectHandle.Move(ctx, MoveObjectDestination)` — an
atomic, server-side, single-bucket rename with optional preconditions, which is
genuinely closer to POSIX `rename` than the umbrella's copy-then-delete model.
It is **tempting** and would remove the orphan-on-crash imperfection. It is not
adopted in this draft because the umbrella's **D6** deliberately standardises the
trio on copy-then-delete (the model S3 and Azure Blob must use, since neither has
a true atomic move), and diverging one adapter risks the family telling three
different rename stories. It is the sharpest Open question below — a decision the
human should make, not this spec.

**Poll GCS via Pub/Sub bucket notifications.** GCS **can** emit object-change
notifications to a Pub/Sub topic, which would give push-based reload. Rejected for
v0.1.0 on the same ground as `config-gcp-parameter`'s Pub/Sub rejection: it moves
change-detection outside the Store (which owns I/O and reload), requires the
consumer to provision a topic+subscription, and the core's poll watcher already
gives opt-in reload for free (umbrella D5). A notification path could be a dated
revision if a consumer needs sub-poll latency.

**Map non-not-found errors onto `fs` sentinels.** Rejected: only "object absent"
is meaningful to the core's optional-vs-fatal source decision. Mapping a `403` to
`fs.ErrPermission` or a `503` to anything would flatten a real, actionable error
into an `fs` bucket the core treats specially. Non-not-found errors are returned
wrapped, verbatim in cause.

## Open questions

*Surfaced, not resolved — for the human.*

1. **Rename: copy-then-delete (umbrella D6) vs the SDK's native
   `ObjectHandle.Move`?** `Move` is atomic and single-bucket and removes the
   orphan-on-crash window, but the umbrella standardises the object-store trio on
   copy-then-delete because S3 and Azure Blob have no atomic move. Do we keep GCS
   uniform with its siblings (copy+delete, one family story) or let GCS use `Move`
   and document the trio as "copy+delete except GCS, which has a real move"? This
   is the single sharpest decision in the spec.

2. **Server-side conflict guard via preconditions.** Should the commit use
   `If(Conditions{GenerationMatch: n})` / `DoesNotExist` to make the write
   *server-side* conflict-safe, rather than leaning only on the core's SHA-256
   content fingerprint? Preconditions close the read-modify-check window but are
   GCS-specific machinery the shared write path does not model; adopting them
   per-adapter diverges the family's conflict story just as (1) does the rename
   story.

3. **The heavy footprint — 46 modules, the largest anywhere.** The GCS SDK drags
   in monitoring, s2a/spiffe, envoy/xds and go-jose that the Parameter Manager
   client does not. Is that acceptable for a filesystem adapter, or does it argue
   for the **gRPC client** (`storage.NewGRPCClient`) or some build-tag trimming?
   (No evidence the gRPC path is lighter; it may be heavier. Flagging, not
   assuming.)

4. **Emulator endpoint wiring: `STORAGE_EMULATOR_HOST` env var vs
   `option.WithEndpoint` + `WithoutAuthentication`.** Both are verified to work.
   The env var is simplest but process-global (a hazard under `t.Parallel()`); the
   explicit options are hermetic and per-client but require the consumer's test to
   thread them. Which does the integration suite standardise on — and does that
   choice belong in the adapter at all, given **D3** says the consumer builds the
   client? (Leaning: the adapter's *tests* own it; the adapter does not.)

5. **`Stat` mode synthesis.** What nominal `fs.FileMode` should a bucket object
   report — a fixed `0644`, or a configurable default — given the core uses
   `Stat`'s mode to preserve permissions across a write (which is meaningless on
   GCS)? And should `Sys()` expose the raw `*storage.ObjectAttrs`?

6. **Poll interval default without a core change.** The core default is 2s, far
   too eager for a billed-per-poll store. The adapter cannot change the core
   default from outside; it can only *recommend* `WithPollInterval(60s)` in docs.
   Is documentation enough, or should the family provide a per-`config.FS`
   "suggested interval" hook the watcher consults? (This is arguably an umbrella-
   level question the whole cloud trio shares.)

7. **Base context for context-free `config.FS` methods.** `config.FS` methods
   take no `context.Context`, but every `storage` call needs one. Does `Wrap`
   take a `ctx` (tying the adapter's lifetime to it), default to
   `context.Background()`, or grow a `WithContext` option? A cancelled base
   context would make every subsequent read fail — a footgun to decide
   deliberately.

## Implementation phases

This spec is `draft`; it is implemented only once `approved` (open questions
resolved with the human and the decisions amended in place, per the specs
convention — never renumbered).

**Phase 0 — this spec.** Draft; open questions above are for the human. The
headline findings are already settled by probing v1.64.0: the SDK exists and maps
cleanly onto all six methods, GCS is strongly consistent so the fingerprint trap
holds, and fake-gcs-server gives a real emulator.

**Phase 1 — the module and read path.** Scaffold `config-gcp-gcs` (README-only,
no microsite), `Wrap(client, bucket)`, `ReadFile`/`Stat` with
`ErrObjectNotExist` → `fs.ErrNotExist` mapping and synthesised `fs.FileInfo`
(D4/D5/D6). Fake-object-store unit tests for read + stat + absence. `depfootprint`
allowlist (D8).

**Phase 2 — the write/commit path.** `WriteFile` (NewWriter + Write + Close,
atomic-on-Close), `Rename` (copy-then-delete, per the D6/OQ1 resolution),
`Remove`, `MkdirAll` no-op (D4/D5). Unit tests for the stage-and-rename commit,
the orphan-on-crash window, and the content-fingerprint conflict trap over the
fake.

**Phase 3 — watch, emulator integration and release.** Confirm the plain
`config.FS` (no `RealPather`) is polled by the core with a recommended long
interval (D5); env-gated integration against **fake-gcs-server** under
testcontainers-go via the resolved endpoint wiring (D7/OQ4). Then publish
v0.1.0: the module `main`, the `config` docs page (`how-to/gcp-gcs.md`) bundled
into the parent site, and the landing card — the rollout every adapter takes.
This adapter, with `config-aws-s3` and `config-azure-blob`, proves the umbrella's
object-store atomicity model (D6) in Phase 3 of the umbrella.

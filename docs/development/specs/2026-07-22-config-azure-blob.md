---
title: config-azure-blob — wrapping an Azure Blob container as a read+write config.FS
date: 2026-07-22
author: matt.cockayne
status: approved
approved: 2026-07-22
---

# config-azure-blob

The first of the cloud **object-store** filesystem adapters the [filesystem
adapters umbrella spec](2026-07-22-filesystem-adapters.md) groups in its Phase 3
— the symmetric trio `config-aws-s3`, `config-gcp-gcs`, `config-azure-blob`. Per
the umbrella's **D2**, no adapter is built without its own approved spec; this is
that spec for Azure Blob Storage. It cites the umbrella and settles the things
the umbrella leaves to each adapter: the client it wraps and how each `config.FS`
method maps onto it, that the rename is a copy (umbrella **D6**), that there is
no real path so watching is polled (umbrella **D5**), its dependency footprint
with the allowlist, and its testing surface — a fake plus, **unlike** the sibling
Azure *App Configuration* backend, a real **Azurite** emulator under
testcontainers (umbrella **D8**).

It follows the shape [`config-iofs`](2026-07-22-config-iofs.md) established for a
filesystem adapter — a `Wrap` that returns a `config.FS`, a single plain adapter
type claiming neither optional interface — and it borrows the **narrow injected
client** pattern the sibling
[`config-azure-appconfig`](2026-07-22-config-azure-appconfig.md) backend uses, so
its unit suite is fake-driven and its footprint framing (a modular Azure SDK,
`azidentity` staying the **consumer's**, an allowlist `depfootprint` test) is the
same. It diverges from `config-iofs` in the one way that matters: an object store
is **not** a disk, so this adapter is where the umbrella's D6 object-store
atomicity model is proven for the first time.

## Problem

A tool whose configuration lives in an Azure Blob container cannot point this
module at it today. The core reads and writes through its six-method
[`config.FS`](../reference), and its shipped implementations — `config.OS()`,
`config.Dir`, and the sibling `config-afero` — all assume a POSIX-shaped
filesystem with an atomic rename. Azure Blob Storage is an **object store**
reached over HTTPS with an Azure credential: it has no directories (only key
prefixes), no atomic rename (only copy-then-delete), no `stat` (only
`GetProperties` returning object metadata), and no native change notification a
watcher can subscribe to.

The seam to reach it exists and is proven (umbrella preamble; `WithFiles`,
`NewFileBackend` and `NewWatcher` all take a `config.FS`, and `config-afero`
shows a sibling module can supply one). What is missing is the first-class module
— `config-azure-blob` — and the per-store decisions the umbrella cannot make:
which of the SDK's several client types to wrap, how each `config.FS` method maps
onto an object operation, what "atomic" means when the rename is a copy, how the
non-real-path filesystem is watched, and how the whole thing is tested against a
fake and against Azurite.

This is a **filesystem** adapter, not a backend: it reads and writes a config
*file* that happens to live in a blob, through the same codec and stage-and-
rename write machinery a local file uses. The object-as-config-key model (blob
names *as* config keys, the parameter-store shape) is a different, legitimate
thing the umbrella explicitly keeps out of this family (umbrella Rejected: "the
cloud stores as Backends"); `config-azure-appconfig` is Azure's member of *that*
family. This adapter serves a `config.yaml` in a bucket.

## Decisions

### D1 — Module `config-azure-blob`, package `configazureblob`

A **cloud-qualified** name (umbrella D1): an object store carries its cloud, so
`config-azure-blob` sits alongside `config-aws-s3` and `config-gcp-gcs`, and a
consumer reading config from S3 never compiles the Azure SDK. The module is
`gitlab.com/phpboyscout/go/config-azure-blob`, package `configazureblob`,
matching the `config-<x>` / `configx` convention every adapter uses. The repo
carries a README only; this spec lives centrally here (umbrella D2).

Unlike the read-only `config-iofs`, this adapter is **read+write** (D5), so it
does **not** return or depend on `config.ErrReadOnlyFS` — that sentinel is for
the read-only members of the family (umbrella D4). It needs **no new core
symbol**: `config.FS`, the optional interfaces it deliberately does *not* claim,
and the poll watcher that reloads a non-real-path filesystem (umbrella D5) all
already ship and are sufficient, exactly as `config-afero` proves. It requires
the `config` release carrying the filesystem-adapter family baseline (umbrella
Phase 1), for family consistency, though it exercises no symbol that release
adds.

### D2 — Data model: a blob name is a file path; the container is fixed at `Wrap`

An Azure Blob container is a flat namespace of **blobs**, each a
`(name, content, properties)` object; the name conventionally uses `/` as a
hierarchy separator (`app/config.yaml`), which the portal renders as folders but
which is just part of the key. This adapter treats a blob's **name as the file
path** the consumer passes to `WithFiles`:

```go
store, err := config.NewStore(ctx,
	config.WithFiles(configazureblob.Wrap(client, "my-container"), "app/config.yaml"))
```

reads the blob named `app/config.yaml` from container `my-container`. The name is
passed to the object operations **unchanged** — the adapter does not root, clean,
or rewrite it, and it takes **no prefix** the way the App Configuration backend
does (that backend nests a flat key space into a config tree; this adapter reads
one file whose *content* the codec parses, so there is nothing to nest). The
container is bound once, at `Wrap`, and every operation is scoped to it.

This is the crucial divergence from `config-azure-appconfig`: that adapter's data
model is prefix→nested-tree because it reads *many settings as config keys*; this
adapter's data model is name→file because it reads *one document as a file*. The
umbrella's flat-to-nested question does not arise here.

### D3 — The six methods map onto object operations, with `BlobNotFound` → `fs.ErrNotExist`

Each `config.FS` method maps onto exactly one honest object operation (client
type in D4; atomicity in D6):

| `config.FS` method | Object operation |
|---|---|
| `ReadFile(name)` | **download** the blob's content; `BlobNotFound` → `fs.ErrNotExist` |
| `WriteFile(name, data, perm)` | **upload** `data` as a block blob (one atomic put); `perm` is ignored (see below) |
| `Stat(name)` | **GetProperties**, synthesising an `fs.FileInfo` (D6); `BlobNotFound` → `fs.ErrNotExist` |
| `Rename(old, new)` | **copy** `old`→`new` then **delete** `old` (D6) |
| `Remove(name)` | **delete** the blob; a `BlobNotFound` is treated as already-removed |
| `MkdirAll(path, perm)` | **no-op** — object stores have no directories (D6) |

`ReadFile` returning an error satisfying `errors.Is(err, fs.ErrNotExist)` for an
absent blob is **load-bearing** (`config.FS` contract): the Store treats a
missing *optional* source as normal and anything else as fatal. The SDK surfaces
a missing blob as a typed storage error the adapter maps with
`bloberror.HasCode(err, bloberror.BlobNotFound)` → `fs.ErrNotExist` (verified
against azblob v1.8.0; `bloberror.BlobNotFound` and `bloberror.ContainerNotFound`
are the relevant codes). A `ContainerNotFound` is *not* mapped to `ErrNotExist` —
a missing container is a misconfiguration, not a normally-absent overlay, so it
surfaces as its raw error.

**`perm` is ignored on write and nominal on stat.** A blob has no POSIX mode; the
adapter neither sets a mode on upload nor invents a meaningful one on
`GetProperties` (D6 synthesis gives a nominal mode). The core preserves
permissions across a write by `Stat`-ing then re-applying, which is a harmless
no-op here.

### D4 — Wrap the account-scoped `*azblob.Client`; derive per-blob clients for stat and copy

The public constructor is `Wrap(client *azblob.Client, container string)`
(umbrella Public API; task-mandated signature), and **`*azblob.Client` is the
right client type** — verified by probing the SDK. The reasoning is decisive and
worth recording, because the SDK offers four client types:

- **`azblob.Client`** (account/service-scoped, chosen). Its methods take a
  `containerName` *per call*, so it is **not** pre-bound to a container — which is
  exactly what a `Wrap(client, container string)` signature needs, and the only
  client type that fits it. It natively covers three of the four object
  operations: `DownloadStream(ctx, container, blob, …)` (ReadFile),
  `UploadBuffer(ctx, container, blob, data, …)` (WriteFile, uploads a block
  blob), and `DeleteBlob(ctx, container, blob, …)` (Remove).
- **`container.Client`** and **`blob.Client`/`blockblob.Client`** are **bound at
  construction** — a container client to one container, a blob client to one blob
  — so they cannot express a `Wrap(client, container)` signature for a filesystem
  of *many* files. `blob.Client` alone carries the two methods `azblob.Client`
  lacks (`GetProperties`, `CopyFromURL`/`StartCopyFromURL`), but it is
  single-blob-scoped, so it is the wrong *top-level* type.

`azblob.Client` lacks `GetProperties` (for `Stat`) and a copy operation (for
`Rename`) at the top level. The adapter derives a per-blob `blob.Client` for
those two, from the same wrapped client, with no second credential and no new
construction:

```go
bc := client.ServiceClient().NewContainerClient(container).NewBlobClient(name)
props, err := bc.GetProperties(ctx, nil)          // Stat
_, err = dstClient.CopyFromURL(ctx, srcClient.URL(), nil)   // Rename's copy
```

Verified: this drill-down compiles and type-checks against azblob v1.8.0. So
`azblob.Client` is the one type that both *fits the signature* and *reaches every
operation*, three natively and two by a cheap, credential-free derivation.

### D5 — Capability: read+write; not a `RealPather`, not a `LinkReader`; watched by polling

`Wrap` returns a **single plain adapter type** that satisfies `config.FS` and,
deliberately, **neither** optional *capability* interface (`RealPather` /
`LinkReader`) — the `config-iofs` shape, not the four-type `config-afero` switch,
because an object store's capabilities do not vary by instance. (It *does*
implement the `config.PollIntervalHinter` hint interface — a poll cadence
advertisement, not a filesystem capability — see below.)

- **Not a `RealPather`.** A blob has no operating-system path (that is the whole
  point of an object store), so the adapter cannot honour `RealPath` and, per the
  interface's own contract, **must not** satisfy it — a type that returned
  `false` from `RealPath` would still look watchable and send the fsnotify
  watcher after nothing. Because it is not a `RealPather`, a wrapped container is
  **polled** when watched (umbrella D5), via the core's existing `pollWatcher` —
  no new mechanism, no per-adapter watch API.
- **Not a `LinkReader`.** Blob storage has no symlink notion; paths are used as
  given.

**On the poll interval — the adapter advertises a sane floor.** Umbrella D5 says
each cloud adapter recommends "a much longer default" than the core's 2-second
`DefaultPollInterval`, because a poll of an object store is a billed API call. A
**backend** adapter like `config-azure-appconfig` owns its `Watch` loop and so
*can* default to 30 s. A **filesystem** adapter historically could not — the poll
lived in the core's `pollWatcher`, whose cadence was the Store-level
`WithPollInterval` setting a `config.FS` had no hook to change. Umbrella **R1**
closes that gap with an optional `config.PollIntervalHinter` interface the Store
consults on the wrapped `config.FS`, so this adapter now **implements
`config.PollIntervalHinter`, returning a 60-second default** — consistent with the
rest of the remote family — rather than relying on the consumer reading the
README. `WithPollInterval` still overrides the hint. Each core poll re-reads the
blob via `ReadFile` → download (a billed GET); an ETag-conditional short-circuit
is possible but is an optimisation of the core poll path — deferred to a later
revision (Resolved (2026-07-22), item 2), not a v0.1.0 requirement.

Read+write is genuine: reading config from a blob and writing it back
(`config.Apply`, a snapshot, an edit) are both legitimate and common, and Blob
Storage is strongly read-after-write consistent, which is what makes the write
path's conflict detection hold (D6).

### D6 — Object-store atomicity: the rename is a copy, and that is fine (umbrella D6)

This adapter is where the umbrella's D6 model is first proven. Blob Storage is
met honestly rather than pretended to be a disk:

- **`Rename` is copy-then-delete.** There is no atomic rename. `WriteFile` maps to
  a block-blob **put** and rename's copy maps to a server-side **copy**, both
  **atomic per object** — so the *target* blob is still replaced atomically and a
  reader never observes a half-written object. The copy uses **`CopyFromURL`**
  (synchronous, server-side, completes within the one call for a same-account
  source ≤ 256 MiB — the case every config file is) rather than
  `StartCopyFromURL` (asynchronous, returns a copy ID to poll); a small config
  blob copies in-line, so the write commit stays a bounded two-call sequence
  (copy, then delete the source). The one imperfection is that a crash **between**
  the copy and the delete leaves the staged source blob behind: garbage to clean
  up, **never** corruption of the target. This is the exact trade the umbrella
  names and accepts.
- **`MkdirAll` is a no-op** — there are no directories, only key prefixes.
- **`Stat` synthesises** an `fs.FileInfo` from `GetProperties`: `Size()` from
  `ContentLength`, `ModTime()` from `LastModified`, `Name()` from the blob name's
  base, `IsDir()` false, and a **nominal** `Mode()` (blobs have no POSIX mode).
  The `fs.FileInfo` is a small adapter-owned type; `Sys()` may expose the raw
  properties (including the ETag) for a consumer who wants it.
- **Conflict detection still holds, content-based.** The core write path
  fingerprints the source content by **SHA-256 at load** and refuses a write if
  the source changed underneath it. This is content-based, not rename-based, so it
  works unchanged over an object store — and Blob Storage's strong read-after-
  write consistency means a re-read to check the fingerprint sees the latest
  bytes. No ETag machinery is required for correctness (an ETag *If-Match* on the
  put could harden it further — deferred, Resolved 2).

The consequence — a couple of extra API calls per commit and a possible orphaned
staging blob on crash — is stated in the README, not hidden (umbrella D6).

### D7 — The constructor wraps a configured client; the adapter owns no credentials

`Wrap` takes an **already-configured** `*azblob.Client`; the adapter never
re-decides authentication, account endpoint, or credential (umbrella D3). The
consumer builds the client — `azblob.NewClient(serviceURL, cred, nil)` with a
credential from **`azidentity`** (a managed identity, a service principal, the
Azure CLI), or `azblob.NewClientFromConnectionString(cs, nil)` for the Azurite /
shared-key path — and hands it in. That is where every credential decision lives,
and it is why **`azidentity` is the consumer's dependency, not the adapter's**
(measured absent from the library graph, D8). This is exactly `config-afero`'s
`Wrap(afero.Fs)` pattern and `config-azure-appconfig`'s D6: the SDK dependency
honestly lives with whoever configures auth, and the adapter only translates the
six `config.FS` calls.

To keep the unit suite fake-driven with no cloud and no real client (umbrella D8,
D3), the adapter defines a **narrow interface** over the handful of object
operations it actually calls, and `Wrap` adapts the real client to it:

```go
// BlobStore is the slice of Blob Storage this adapter uses, scoped to one
// container. A fake satisfying it drives the whole unit suite; the real
// *azblob.Client is adapted to it by Wrap (D4).
type BlobStore interface {
	Download(ctx context.Context, name string) ([]byte, error)
	Upload(ctx context.Context, name string, data []byte) error
	GetProperties(ctx context.Context, name string) (BlobInfo, error)
	Copy(ctx context.Context, src, dst string) error
	Delete(ctx context.Context, name string) error
}

// BlobInfo is the object metadata Stat synthesises an fs.FileInfo from.
type BlobInfo struct {
	Size         int64
	LastModified time.Time
	ETag         string // opaque; azcore.ETag rendered to string
}
```

`New(store BlobStore) config.FS` is the injection seam (a fake in tests);
`Wrap(client *azblob.Client, container string) config.FS` is the convenience path
that binds the container and adapts the SDK client to a `BlobStore`, so a consumer
writes `configazureblob.Wrap(client, "my-container")` and never sees the narrow
interface unless testing. `BlobStore` and `New` are **exported** — consistent with
the sibling App Configuration backend, so a consumer can inject a custom
implementation or a fake (Resolved 1).

### D8 — Dependency footprint: the modular Azure Blob SDK, `azidentity` absent

`config-azure-blob` depends on
`github.com/Azure/azure-sdk-for-go/sdk/storage/azblob`, the honest cost of
talking to Blob Storage (umbrella D7). It is a **modular** SDK — the adapter pulls
only the storage data-plane package and its `azcore` runtime, **not** the whole
Azure SDK and **not `azidentity`**, which is the consumer's dependency for
building the credential (D7). Measured at **`azblob v1.8.0`** (latest), the
shipped library graph is **five modules including itself** — the same strikingly
small footprint as the sibling App Configuration adapter:

```
github.com/Azure/azure-sdk-for-go/sdk/storage/azblob   v1.8.0
github.com/Azure/azure-sdk-for-go/sdk/azcore           v1.22.0
github.com/Azure/azure-sdk-for-go/sdk/internal         v1.12.0
golang.org/x/net                                       v0.56.0
golang.org/x/text                                      v0.38.0
```

A `depfootprint_test.go` allowlist — modelled on `config-afero`'s and
`config-azure-appconfig`'s — asserts exactly this set plus the `config` graph, so
a dependency creeping in, or the SDK growing one via a version bump, is a failing
test rather than a surprise in a consumer's `go.sum`. **`azidentity` must not
appear in it** (verified absent from the graph): if it does, the adapter has taken
on credential configuration it was supposed to leave to the consumer (D7), so the
allowlist doubles as a guard on that boundary — exactly as it does for
`config-azure-appconfig`. `testcontainers-go` and its Azurite module are **test**
dependencies (D9) and do not reach a consumer importing this package; the
`go list -deps .` scan (library graph, not `./...`) excludes them.

### D9 — Testing: a fake `BlobStore` for units, real **Azurite** under testcontainers for integration

Three layers, from umbrella D8 — and this is where the adapter's testing story is
**better** than its App Configuration sibling, because **Blob Storage has an
emulator and App Configuration does not**:

1. **A hand-written fake `BlobStore`** — in-memory, container-scoped — drives the
   unit suite: reads, a missing blob asserting `fs.ErrNotExist`, writes, the
   stage-and-rename commit (copy-then-delete, asserting the target is replaced
   atomically and the source removed), `Stat` synthesis (size / mod-time / nominal
   mode), `MkdirAll` as a no-op, and `Remove`. No cloud, no account, IDE-runnable,
   the primary suite. Negative capability assertions — that the returned type is
   **not** a `config.RealPather` and **not** a `config.LinkReader` (D5) — mirror
   how `config-iofs` and `config-afero` test their plain adapters' *absence* of
   capability.

2. **The module's own filesystem-behaviour suite run over the wrapped fake**
   (umbrella Testing strategy): a wrapped filesystem must read, write,
   stage-and-rename, and behave as `config.OS()` does against any `config.FS`,
   which the core's existing suite already asserts. This proves the object-store
   mapping honours the contract the write machinery relies on.

3. **Real-service integration against Azurite, under testcontainers, env-gated.**
   Verified: **`mcr.microsoft.com/azure-storage/azurite`** is the Blob/Queue/Table
   emulator, and **`github.com/testcontainers/testcontainers-go/modules/azurite`**
   resolves (to v0.43.0 at time of writing) — so, **unlike**
   `config-azure-appconfig` (whose service has no emulator and no testcontainers
   module, forcing a real-service-only suite), this adapter **can** run a real
   emulator in a Docker-in-Docker job. The suite starts Azurite, builds an
   `*azblob.Client` against it via the well-known Azurite **connection string**
   (`NewClientFromConnectionString`, the `devstoreaccount1` shared-key
   development account Azurite ships), creates a container, and exercises the real
   download / upload / copy / delete / get-properties path end to end. It is
   **env-gated** (kept compiled and IDE-discoverable behind an
   `INT_TEST_INTEGRATION`-style skip) and wired into the go-test DIND job the way
   the umbrella's other emulator-backed adapters (LocalStack, fake-gcs-server) are
   (umbrella D8) — a self-contained container, no cloud account, no billable
   resource, so it runs on ordinary merge pipelines rather than a protected
   scheduled job.

## Rejected alternatives

**Wrap `*container.Client` (already container-scoped) instead of
`*azblob.Client` + a container string.** Cleaner-looking — the container is baked
in, and `container.Client` exposes `NewBlobClient`/`NewBlockBlobClient` for the
per-blob operations. Rejected because it does not fit the family's
`Wrap(client, container string)` signature (the container would be redundant or
conflicting), and it pushes the container decision into client construction where
a consumer wiring one client for several containers cannot reuse it. `azblob.Client`
+ an explicit container is the account-scoped shape the signature expects (D4). A
consumer who genuinely holds only a `container.Client` can still reach an
`azblob.Client`'s abilities, but the *adapter's* seam is the account client.

**Take a `blob.Client` or `blockblob.Client` in the constructor.** These are
bound to a *single blob* at construction, so they cannot serve a filesystem of
many named files. They are the right *derived* type for `Stat` and the copy (D4),
not the right wrapped type.

**Depend on `azidentity` and offer `Wrap(serviceURL, cred, container)`.** Would
let a consumer skip building the client. Rejected on umbrella D3 grounds and
measured in D8: it pulls a credential library and its transitive graph into every
consumer, couples the adapter to one auth story, and makes the unit suite reach
for real credentials. The client — and therefore the credential — is the
consumer's; the narrow `BlobStore` seam keeps units fake-driven.

**Use `StartCopyFromURL` (async) for the rename copy.** Correct, but it returns a
copy ID the adapter would then have to poll to completion, turning a bounded
commit into a poll loop. For the small blobs a config file is, the synchronous
`CopyFromURL` completes in the one call (source ≤ 256 MiB, same account), which is
simpler and bounded (D6). `StartCopyFromURL` is the right fallback only for a
large or cross-account source, which a config file is not.

**Claim a `RealPather` that returns `false`, or a poll interval the adapter
enforces.** The first violates the interface contract and makes every wrapped
container look watchable (D5). The second is impossible for a filesystem adapter:
the poll cadence is the core's Store-level `WithPollInterval`, not something a
`config.FS` can default. The adapter documents the recommended interval instead
of pretending to set one (D5).

**Treat blob *names as config keys* (the parameter-store shape).** That is a
`Backend`, not a filesystem adapter — a different, legitimate model the umbrella
keeps out of this family (umbrella Rejected). This adapter reads a config *file*
in a blob; `config-azure-appconfig` is the key-value member of Azure's story.

## Public API

```go
// Wrap adapts an Azure Blob container to a read+write config.FS. The client is
// already configured (endpoint + credential are the consumer's, D7); container
// is bound for the lifetime of the returned FS.
func Wrap(client *azblob.Client, container string) config.FS

// New is the injection seam: a fake BlobStore in tests, a Wrap-adapted client in
// production. Exported, like the App Configuration sibling (Resolved 1).
func New(store BlobStore) config.FS

type BlobStore interface { … }   // Download, Upload, GetProperties, Copy, Delete
type BlobInfo struct { … }        // Size, LastModified, ETag
```

Used exactly as `config.OS()` is:

```go
cred, _ := azidentity.NewDefaultAzureCredential(nil)          // consumer's dep
client, _ := azblob.NewClient("https://acct.blob.core.windows.net/", cred, nil)

store, err := config.NewStore(ctx,
	config.WithFiles(configazureblob.Wrap(client, "config"), "app/config.yaml"))
```

The result satisfies `config.FS` and, deliberately, **neither** `config.RealPather`
nor `config.LinkReader` (D5), so the core polls it when watched. **No change to
the `config` core is required** — the filesystem-adapter family baseline already
ships everything a read+write, non-real-path adapter needs, and this adapter
returns no `config.ErrReadOnlyFS` because it is not read-only.

## Testing strategy

Per D9: a fake-`BlobStore` unit suite (reads, missing-is-`ErrNotExist`, writes,
copy-then-delete commit, `Stat` synthesis, `MkdirAll` no-op, `Remove`, and the
negative capability assertions); the module's own filesystem-behaviour suite over
the wrapped fake; a real **Azurite** integration suite under testcontainers,
env-gated and wired into the DIND job (the emulator this adapter has and its App
Configuration sibling lacks); and a `depfootprint_test.go` allowlist (D8, which
also guards that `azidentity` never enters the library graph).

What would falsely pass, and how it is guarded:

- **A rename test that only asserts the target exists** — would pass even if the
  copy-then-delete left the *source* behind (the orphaned-staging bug the code
  must avoid on the happy path, D6). The commit test must assert **both** that the
  target holds the new content **and** that the source blob is gone.
- **A missing-blob test asserting only "an error"** rather than
  `errors.Is(err, fs.ErrNotExist)` — the `BlobNotFound` mapping (D3) is
  load-bearing for the Store's optional-source handling, so the suite asserts the
  sentinel specifically; a future refactor returning a raw storage error would
  fail it.
- **A capability test that only checks the positive interfaces** — the adapter's
  correctness includes *not* being a `RealPather`/`LinkReader` (D5), so the suite
  asserts the negatives, mirroring `config-iofs`.
- **An integration suite that quietly skips when Azurite is unavailable and reads
  green** — the env-gate skip must be explicit and the DIND job must fail if the
  container cannot start, exactly as the sibling emulator adapters' jobs do, so an
  absent emulator is a red pipeline, not a silent pass.

## Migration & compatibility

Purely additive for consumers: add the module and pass
`configazureblob.Wrap(client, container)` to `WithFiles`, exactly as for
`config-afero`. No `config` core change, no breaking change anywhere. The module
ships **read+write** at v0.1.0, so there is no later read→write promotion to
communicate. The two behaviours a consumer must understand up front — and which
the README states — are that the **rename is a copy-then-delete** (a crash can
orphan a staging blob; never corrupt the target, D6) and that **watching is
polled and the 2 s default is far too eager for a billed store**, so a watching
consumer should raise `WithPollInterval` (D5).

## Resolved (2026-07-22)

All open questions were answered by the human on 2026-07-22, and the decisions
above were amended in place to match (no decision renumbered). Each records what
was verified, so the choice rests on facts.

1. **Export `New(BlobStore)` and the `BlobStore` interface.** The adapter exports
   a narrow `BlobStore` interface and `New(BlobStore)` alongside `Wrap`, consistent
   with the sibling `config-azure-appconfig` backend, giving a clean injectable
   seam for consumer tests and a real fake. `Wrap(client, container)` remains the
   ordinary constructor over a real `*azblob` client, adapting it to a `BlobStore`;
   a consumer never sees the narrow interface unless testing. Export wins over the
   smaller `config-afero`/`config-iofs` surface because a cloud adapter's fake is
   worth handing consumers, and it keeps the Azure family's two members shaped
   alike.

2. **ETag-conditional writes/polls — deferred.** v0.1.0 relies on the shared write
   path's content-SHA conflict detection (umbrella **D6**), which is correct without
   ETags. Blob Storage's `If-Match` / `If-None-Match` access conditions (verified:
   `blob.AccessConditions` / `ModifiedAccessConditions` on the upload and copy
   options) would layer belt-and-braces hardening on the write and let the poll do
   an `If-None-Match` conditional GET (304, no body, no egress) instead of a full
   download each interval — but both are **optimisations / hardenings, not
   correctness**, so they do not earn v0.1.0 and can be added by a later dated
   revision here.

3. **Poll cadence — implements `config.PollIntervalHinter`, 60-second default**
   (amends D5). A poll is a billed blob read, so rather than relying on the consumer
   reading the README, `config-azure-blob` implements the new optional
   `config.PollIntervalHinter` (umbrella **R1**) and returns **60 seconds** —
   consistent with the rest of the remote family. The core's Store consults the
   hint when it polls a wrapped `config.FS`; `WithPollInterval` overrides it. This
   turns the previous "documentation, not a default" limit into a real advertised
   floor.

4. **`Remove` of an absent blob — idempotent success** (confirms D3). A
   `BlobNotFound` on `Remove` returns nil, matching the idempotent intent of
   discarding staged content (the primary caller). This diverges from `os.Remove`
   of a missing file, which the README notes. `ReadFile` of an absent blob still
   returns an `fs.ErrNotExist`-shaped error, which the Store relies on for its
   optional-source handling — only `Remove` is idempotent.

5. **Container existence — the container must exist (consumer provisions)**
   (confirms D6, D7). `Wrap` binds a container but does **not** create it
   (`MkdirAll` is a no-op, D6); a write to a missing container fails with
   `ContainerNotFound`. Provisioning is the consumer's, consistent with **D7** (the
   adapter owns no credentials or provisioning) and with the account/endpoint being
   the consumer's to stand up. There is **no** opt-in create-if-absent in v0.1.0.

**No open questions remain.**

## Implementation phases

Gated on D2 of the umbrella: this spec is `approved`, with the open questions
resolved (see Resolved (2026-07-22)) **before** any code.

**Phase 0 — this spec.** Approved 2026-07-22, its open questions resolved with the
human (Resolved (2026-07-22)). Blocked on the umbrella's Phase 1 (the
filesystem-adapter family baseline) being released, though this adapter adds no
core symbol of its own.

**Phase 1 — the module, read + write.** Scaffold `config-azure-blob`
(README-only, no microsite; umbrella D2), the narrow `BlobStore` interface and
`BlobInfo`, `Wrap` (adapting `*azblob.Client` with the per-blob `blob.Client`
derivation for `Stat` and copy, D4) and `New`, the six `config.FS` methods with
the `BlobNotFound`→`fs.ErrNotExist` mapping (D3) and the copy-then-delete rename
(D6), and the plain adapter type that claims neither optional interface (D5).
Fake-`BlobStore` unit suite including the negative capability assertions;
`depfootprint` allowlist asserting the five-module graph and `azidentity`'s
absence (D8).

**Phase 2 — the Azurite integration suite.** The env-gated testcontainers suite
against `mcr.microsoft.com/azure-storage/azurite` (D9), wired into the DIND job —
the emulator-backed proof this adapter can have and its App Configuration sibling
cannot. Run the module's own filesystem-behaviour suite over the wrapped fake as
the contract gate.

**Phase 3 — publish v0.1.0.** The module `main`, the `config` docs page
(`how-to/azure-blob.md`) bundled into the parent site, and the landing card — the
same rollout every adapter takes. Anything this adapter shows the umbrella got
wrong about object-store filesystems is corrected there by dated revision, never
silently (umbrella Phase 4 discipline), before the sibling `config-aws-s3` and
`config-gcp-gcs` reuse the model.

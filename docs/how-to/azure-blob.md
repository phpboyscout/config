---
title: Read & write config in Azure Blob
description: Point config at a configuration file that lives in an Azure Blob Storage container, through an azblob client you own.
tags: [how-to, filesystem, azure, blob, cloud]
---

# Read & write config in Azure Blob

When a tool's configuration file lives in an **Azure Blob Storage container**, the
[`config-azure-blob`](https://gitlab.com/phpboyscout/go/config-azure-blob) sibling module wraps an
[`*azblob.Client`](https://pkg.go.dev/github.com/Azure/azure-sdk-for-go/sdk/storage/azblob) as a
`config.FS`.

```bash
go get gitlab.com/phpboyscout/go/config-azure-blob
```

You build and authenticate the client — `azidentity`, a connection string, a shared key, whatever you
already use — and hand it in with the container name:

```go
import (
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"

	"gitlab.com/phpboyscout/go/config"
	configazureblob "gitlab.com/phpboyscout/go/config-azure-blob"
)

cred, _ := azidentity.NewDefaultAzureCredential(nil)
client, _ := azblob.NewClient("https://acct.blob.core.windows.net/", cred, nil)

store, err := config.NewStore(ctx,
	config.WithFiles(configazureblob.Wrap(client, "my-container"), "config.yaml"),
	config.WithEnv("APP"),
)
```

The `config.FS` name is the blob name. The **container must already exist** — `MkdirAll` is a no-op,
so `config-azure-blob` never creates it; provisioning the container is yours, as authenticating is.
Because the adapter takes only the SDK client, `azidentity` stays *your* dependency and never enters
the adapter's own graph.

## When you do *not* need this

If you read one blob once at startup, fetching it and handing the bytes to
[`WithReaders`](load-and-merge.md) needs no adapter.

Reach for `config-azure-blob` when the container should behave like a filesystem —
several files, writes routed back, or hot-reload noticing a change made elsewhere.

## Testing against a fake

Alongside `Wrap`, the module exports a narrow `BlobStore` interface and `New(BlobStore)`, so a fake
drives your tests with no Azure:

```go
store := configazureblob.New(myFakeBlobStore)   // BlobStore: Download / Upload / Delete / List
```

## The write is a synchronous copy-then-delete

Azure Blob has no atomic move, so the commit is a copy-then-delete — using the **synchronous**
copy-from-URL so it completes within the call rather than returning a pending async operation. The
target is replaced atomically; a crash between copy and delete can leave a recognisable staging blob
behind (a findable orphan, never a corrupted target). Conflict detection is the core's SHA-256
content fingerprint at load.

!!! note "Copying a private blob with a token credential"
    The synchronous copy authorises the *source* blob via its URL, not the request credential. When
    the client carries a shared key (a connection string), the adapter signs the source with a
    short-lived read SAS, so a private same-account blob copies fine. A **token-credential
    (`azidentity`) client cannot mint that SAS**, so it falls back to the bare source URL — which
    works for a public source but not yet for a private same-account blob. If you write config to a
    private container with an `azidentity` client, keep this edge in mind; hardening it (a
    user-delegation SAS or a `CopySourceAuthorization` header) is a tracked follow-on.

## Hot-reload is polled, at a calm cadence

A blob has no local path, so it is watched by **polling**. Because each poll is a billed read,
`config-azure-blob` declares a **60-second** default through `config.PollIntervalHinter`, overridable
with `WithPollInterval`.

## Getting a client

Building the client yourself is the default. Rung 3 has **two shapes**, and the
ambient rung lives in a **subpackage**:

```go
// A principal, with the account URL supplied separately.
fsys, err := configazureblob.FSFromCredential(cred, serviceURL, "config")

// A connection string, which carries the account URL AND the secret together.
fsys, err := configazureblob.FSFromConnectionString(conn, "config")

// Nothing but the target — note the separate import.
import blobambient "gitlab.com/phpboyscout/go/config-azure-blob/ambient"

fsys, err := blobambient.Default(ctx, serviceURL, "config")
```

**Nothing here needs closing.** `azblob.Client` has no `Close` at all — it is
HTTP-backed with no connection to release — so these rungs return a plain
`config.FS` rather than an owned type. Where an SDK gives nothing to release,
inventing a `Close` would be ceremony.

**The connection string is a secret** — it embeds the account key, is never
logged, and a malformed one is reported without echoing the input.

**The service URL and container are both required**, and a nil credential is
refused including a typed nil.

## What it costs

| | |
|---|---|
| Modules added | **14** — 5 for the Blob Storage SDK, 9 for the `config` graph |

The `azblob` SDK (three Azure modules — `azidentity` deliberately not among them), plus `config`,
asserted by an allowlist test. The tests run against [Azurite](https://github.com/Azure/Azurite)
under testcontainers, so the suite needs no Azure account.

## Related

- [How filesystem adapters work](../explanation/filesystem-adapters.md) — the object-store commit
  model and polled reload
- [Read & write config in S3](aws-s3.md) · [GCS](gcp-gcs.md) — the sibling object stores
- [Backends & capabilities](../explanation/backends.md) — the `config.FS` interface

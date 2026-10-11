---
title: Read & write config in Cloud Storage
description: Point config at a configuration file that lives in a GCP Cloud Storage bucket, through a storage.Client you own.
tags: [how-to, filesystem, gcs, gcp, cloud]
---

# Read & write config in Cloud Storage

When a tool's configuration file lives in a **GCP Cloud Storage bucket**, the
[`config-gcp-gcs`](https://gitlab.com/phpboyscout/go/config-gcp-gcs) sibling module wraps a
[`*storage.Client`](https://pkg.go.dev/cloud.google.com/go/storage#Client) as a `config.FS`.

```bash
go get gitlab.com/phpboyscout/go/config-gcp-gcs
```

You build and configure the storage client, so credentials, project and options all stay yours, and
hand it in with the bucket name:

```go
import (
	"cloud.google.com/go/storage"

	"gitlab.com/phpboyscout/go/config"
	configgcpgcs "gitlab.com/phpboyscout/go/config-gcp-gcs"
)

client, _ := storage.NewClient(ctx) // your credentials, your options
defer client.Close()

store, err := config.NewStore(ctx,
	config.WithFiles(configgcpgcs.Wrap(client, "my-bucket"), "config.yaml"),
	config.WithEnv("APP"),
)
```

The `config.FS` name is the object name, so `Wrap(client, "my-bucket")` reads and writes the object
`config.yaml`.

## When you do *not* need this

If you read one object once at startup, fetch it and hand the bytes to
[`WithReaders`](load-and-merge.md). No adapter needed.

Reach for `config-gcp-gcs` when the bucket should behave like a filesystem: several
files, writes routed back, or hot-reload noticing a change made elsewhere.

## A base context, because the SDK needs one

Every `storage` call takes a `context.Context`, but the `config.FS` methods do not, so `Wrap`
defaults to `context.Background()`. If you want the adapter's operations bound to a cancellable
context, pass one:

```go
configgcpgcs.Wrap(client, "my-bucket", configgcpgcs.WithContext(ctx))
```

A cancelled base context makes every subsequent adapter operation fail, so bind a context whose
lifetime matches the store's.

## The write is a real atomic move

Unlike S3 and Azure Blob, Cloud Storage has a **native server-side move**, so the commit is a single
`ObjectHandle.Move`. There is no copy-then-delete, and no window where a crash could orphan a staging
object. Conflict detection is the core's SHA-256 content fingerprint, captured at load, so a write is
refused if the object changed underneath it.

## Hot-reload is polled, at a calm cadence

An object store has no local path, so it is watched by **polling**. Because each poll is a billed
object read, `config-gcp-gcs` declares a **60-second** default through `config.PollIntervalHinter`.
Override it with `config.WithPollInterval` on `Store.Watch`.

## Getting a client

Building the client yourself is the default. Two further rungs exist:

```go
// You hold client options: an emulator endpoint, an explicit credentials file,
// or a credential resolved once with go/gcpclient and shared across adapters.
fsys, err := configgcpgcs.FromOptions(ctx, "my-bucket", opts)

// Application Default Credentials.
fsys, err := configgcpgcs.Default(ctx, "my-bucket")

store, err := config.NewStore(ctx, config.WithFiles(fsys, "config.yaml"), config.WithCloser(fsys))
defer store.Close()
```

To detect Application Default Credentials **once** and feed several GCP adapters
from it, resolve with
[`go/gcpclient`](https://gcpclient.go.phpboyscout.uk) and pass the
options it yields to `FromOptions`. It hands out client options rather than a
client precisely because the three GCP adapters need three different client
types.

Both return `*OwnedFS`, which owns the client it built, so **close it**. `store.Close()` does that
for you once you hand the filesystem over with `WithCloser`, as above. The obligation is real but
weaker than for the Secret Manager sibling: `storage.Client` is HTTP-backed and its documentation
says `Close` "need not be called at program exit", where `secretmanager.Client` says it "must be
Closed". An empty bucket returns `ErrNoBucket`. A filesystem from `Wrap` is not owned, and the
client you built stays yours to close.

This is the simplest zero-conf rung in the family: a bucket name is globally unique, so unlike
Secret Manager and Parameter Manager there is no project or location to supply. The why behind the
ladder is in [who owns the connection](../explanation/connection-ownership.md).

## What it costs

| | |
|---|---|
| Adds | the Cloud Storage SDK, on top of the `config` graph |

The Cloud Storage SDK, plus `config`, asserted by an allowlist test. The SDK is a heavy graph, and
the honest cost of the client. Because the adapter is its own module, only a consumer reading from
GCS compiles it. The integration tests run against
[fake-gcs-server](https://github.com/fsouza/fake-gcs-server) under testcontainers, so the suite needs
no GCP project.

## Related

- [How filesystem adapters work](../explanation/filesystem-adapters.md): the object-store commit
  model and polled reload
- [Read & write config in S3](aws-s3.md) and [Azure Blob](azure-blob.md): the sibling object stores
- [Backends & capabilities](../explanation/backends.md): the `config.FS` interface

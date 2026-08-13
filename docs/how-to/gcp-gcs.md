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

You build and configure the storage client — credentials, project, options all stay yours — and hand
it in with the bucket name:

```go
import (
	"cloud.google.com/go/storage"

	"gitlab.com/phpboyscout/go/config"
	configgcpgcs "gitlab.com/phpboyscout/go/config-gcp-gcs"
)

client, _ := storage.NewClient(ctx)   // your credentials, your options
defer client.Close()

store, err := config.NewStore(ctx,
	config.WithFiles(configgcpgcs.Wrap(client, "my-bucket"), "config.yaml"),
	config.WithEnv("APP"),
)
```

The `config.FS` name is the object name — `Wrap(client, "my-bucket")` reads and writes the object
`config.yaml`.

## When you do *not* need this

If you read one object once at startup, fetching it and handing the bytes to
[`WithReaders`](load-and-merge.md) needs no adapter.

Reach for `config-gcp-gcs` when the bucket should behave like a filesystem — several
files, writes routed back, or hot-reload noticing a change made elsewhere.

## A base context, because the SDK needs one

Every `storage` call takes a `context.Context`, but the `config.FS` methods do not, so `Wrap`
defaults to `context.Background()`. If you want the adapter's operations bound to a cancellable
context, pass one:

```go
configgcpgcs.Wrap(client, "my-bucket", configgcpgcs.WithContext(ctx))
```

Note that a cancelled base context makes every subsequent read fail — bind a context whose lifetime
matches the store's.

## The write is a real atomic move

Unlike S3 and Azure Blob, Cloud Storage has a **native server-side move**, so the commit is a single
atomic `ObjectHandle.Move` — no copy-then-delete, and no window where a crash could orphan a staging
object. Conflict detection is the core's SHA-256 content fingerprint, captured at load: a write is
refused if the object changed underneath it.

## Hot-reload is polled, at a calm cadence

An object store has no local path, so it is watched by **polling**. Because each poll is a billed
object read, `config-gcp-gcs` declares a **60-second** default through `config.PollIntervalHinter`,
overridable with `WithPollInterval`.

## What it costs

| | |
|---|---|
| Modules added | **54** — 45 for the Cloud Storage SDK, 9 for the `config` graph |

The Cloud Storage SDK — the heaviest dependency graph in the adapter family, and the honest cost of
the client — plus `config`, asserted by an allowlist test. Because it is its own module, only a
consumer reading from GCS compiles it. The tests run against
[fake-gcs-server](https://github.com/fsouza/fake-gcs-server) under testcontainers, so the suite needs
no GCP project.

## Related

- [How filesystem adapters work](../explanation/filesystem-adapters.md) — the object-store commit
  model and polled reload
- [Read & write config in S3](aws-s3.md) · [Azure Blob](azure-blob.md) — the sibling object stores
- [Backends & capabilities](../explanation/backends.md) — the `config.FS` interface

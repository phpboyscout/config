---
title: Read & write config in S3
description: Point config at a configuration file that lives in an AWS S3 bucket, through an s3.Client you own.
tags: [how-to, filesystem, s3, aws, cloud]
---

# Read & write config in S3

When a tool's configuration file lives in an **AWS S3 bucket**, the
[`config-aws-s3`](https://gitlab.com/phpboyscout/go/config-aws-s3) sibling module wraps an
[`*s3.Client`](https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/service/s3) as a `config.FS`.

```bash
go get gitlab.com/phpboyscout/go/config-aws-s3
```

You build and configure the S3 client, so region, credentials and endpoint all stay yours, and hand it
in with the bucket name:

```go
import (
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"gitlab.com/phpboyscout/go/config"
	configawss3 "gitlab.com/phpboyscout/go/config-aws-s3"
)

cfg, _ := awsconfig.LoadDefaultConfig(ctx) // your region, your credentials
client := s3.NewFromConfig(cfg)

store, err := config.NewStore(ctx,
	config.WithFiles(configawss3.Wrap(client, "my-bucket"), "config.yaml"),
	config.WithEnv("APP"),
)
```

The `config.FS` name is the object key, so `Wrap(client, "my-bucket")` reads and writes the object
`config.yaml`. If the bucket is shared with unrelated objects, add a key prefix:

```go
configawss3.Wrap(client, "my-bucket", configawss3.WithKeyPrefix("apps/mytool/"))
```

## When you do *not* need this

If you read one object once at startup, fetch it with the AWS SDK and hand the
bytes to [`WithReaders`](load-and-merge.md). No adapter needed.

Reach for `config-aws-s3` when you want the bucket to behave like a filesystem: several
files, writes routed back to the object, or hot-reload picking up a change someone else
made.

## The write is a copy-then-delete

S3 has no atomic rename, so the commit is a sequence of `CopyObject` and `DeleteObject` calls. The
target is still replaced atomically, because `PutObject` and `CopyObject` are atomic per object and a
reader never sees a half-written object. The one imperfection is a crash part-way through the
sequence, which can leave a staged object behind under a `.config-stage` key. That is a findable
orphan you can expire with a lifecycle rule, not a corrupted target or a lost write. Conflict
detection is unaffected: the write path fingerprints content by SHA-256 at load and refuses a write
if the object changed underneath it.

## Hot-reload is polled, at a calm cadence

An object store has no local path, so it is watched by **polling**. Because each poll is a billed
`GetObject`, `config-aws-s3` declares a **60-second** default through `config.PollIntervalHinter`. That is far
calmer than the 2-second local default, and you can override it with `WithPollInterval`.

## Getting a client

Building the client yourself is the default. Two further rungs exist, and the
second lives in a subpackage:

```go
// You resolved the config; the adapter builds the client.
fsys, err := configawss3.FSFromConfig(cfg, "my-bucket")

// Nothing at all. Note the separate import.
import s3ambient "gitlab.com/phpboyscout/go/config-aws-s3/ambient"

fsys, err := s3ambient.Default(ctx, "my-bucket")
```

The subpackage keeps the ambient chain's extra dependencies away from anyone who builds their own
client. It saves less here than for the sibling AWS adapters, because S3's larger service graph
already carries `internal/v4a`, `accept-encoding` and `presigned-url`.

There is no default region, and the bucket is required. AWS documents no region default, so a config
with none returns `ErrNoRegion` rather than a guess, and an empty bucket returns `ErrNoBucket`. For
the ambient rung, set `AWS_REGION`.

None of these return anything you need to close; the S3 client holds no connection of its own. The
why behind the ladder is in [who owns the connection](../explanation/connection-ownership.md).

## What it costs

| | |
|---|---|
| Adds | the AWS S3 SDK, on top of the `config` graph |

The AWS SDK for Go v2 S3 packages, plus `config` and what it already brings. An allowlist test pins
this to exactly the S3 SDK modules, so a consumer reading from S3 never compiles another cloud's SDK.
The integration tests run against [LocalStack](https://localstack.cloud/) under testcontainers, so the
suite needs no AWS account.

## Related

- [How filesystem adapters work](../explanation/filesystem-adapters.md): the object-store commit
  model and polled reload
- [Read & write config in GCS](gcp-gcs.md) and [Azure Blob](azure-blob.md): the sibling object stores
- [Backends & capabilities](../explanation/backends.md): the `config.FS` interface

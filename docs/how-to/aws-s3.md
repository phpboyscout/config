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

You build and configure the S3 client — region, credentials, endpoint all stay yours — and hand it
in with the bucket name:

```go
import (
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"gitlab.com/phpboyscout/go/config"
	configawss3 "gitlab.com/phpboyscout/go/config-aws-s3"
)

cfg, _ := awsconfig.LoadDefaultConfig(ctx)   // your region, your credentials
client := s3.NewFromConfig(cfg)

store, err := config.NewStore(ctx,
	config.WithFiles(configawss3.Wrap(client, "my-bucket"), "config.yaml"),
	config.WithEnv("APP"),
)
```

The `config.FS` name is the object key — `Wrap(client, "my-bucket")` reads and writes the object
`config.yaml`. To scope a bucket shared with unrelated objects, add a key prefix:

```go
configawss3.Wrap(client, "my-bucket", configawss3.WithKeyPrefix("apps/mytool/"))
```

## When you do *not* need this

If you read one object once at startup, fetching it with the AWS SDK and handing the
bytes to [`WithReaders`](load-and-merge.md) needs no adapter at all.

Reach for `config-aws-s3` when you want the bucket to behave like a filesystem: several
files, writes routed back to the object, or hot-reload picking up a change someone else
made.

## The write is a copy-then-delete, and that is fine

S3 has no atomic rename, so the commit is `CopyObject` + `DeleteObject`. The **target is still
replaced atomically** — `PutObject`/`CopyObject` are atomic per object, so a reader never sees a
half-written object. The one imperfection is a crash between the copy and the delete, which leaves a
staged object behind under a recognisable `.config-stage` key — a findable orphan you can
lifecycle-expire by rule, never a corrupted target or a lost write. Conflict detection is unaffected:
the write path fingerprints content by SHA-256 at load and refuses a write if the object changed
underneath it.

## Hot-reload is polled, at a calm cadence

An object store has no local path, so it is watched by **polling**. Because each poll is a billed
`GetObject`, `config-aws-s3` declares a **60-second** default through `config.PollIntervalHinter` —
far calmer than the 2-second local default, and overridable with `WithPollInterval`.

## What it costs

| | |
|---|---|
| Modules added | **20** — 11 for the AWS S3 SDK, 9 for the `config` graph |

The AWS SDK for Go v2 S3 packages, plus `config` and what it already brings — asserted by an
allowlist test pinned to exactly the S3 SDK modules, so a consumer reading from S3 never compiles
another cloud's SDK. The tests run against [LocalStack](https://localstack.cloud/) under
testcontainers, so the suite needs no AWS account.

## Related

- [How filesystem adapters work](../explanation/filesystem-adapters.md) — the object-store commit
  model and polled reload
- [Read & write config in GCS](gcp-gcs.md) · [Azure Blob](azure-blob.md) — the sibling object stores
- [Backends & capabilities](../explanation/backends.md) — the `config.FS` interface

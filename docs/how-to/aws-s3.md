---
title: Read and write config in S3
description: Point config at a configuration file that lives in an AWS S3 bucket, through an s3.Client you own.
tags: [how-to, filesystem, s3, aws, cloud]
---

# Read and write config in S3

When a tool's configuration file lives in an **AWS S3 bucket**, the
[`config-aws-s3`](https://gitlab.com/phpboyscout/go/config-aws-s3) sibling module wraps an
[`*s3.Client`](https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/service/s3) as a `config.FS`. You
build and configure the client, so region, credentials and endpoint all stay yours, and hand it in
with the bucket name.

```bash
go get gitlab.com/phpboyscout/go/config-aws-s3
```

```go
import (
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"gitlab.com/phpboyscout/go/config"
	configawss3 "gitlab.com/phpboyscout/go/config-aws-s3"
)

cfg, err := awsconfig.LoadDefaultConfig(ctx) // your region, your credentials
if err != nil {
	return err
}

store, err := config.NewStore(ctx,
	config.WithFiles(configawss3.Wrap(s3.NewFromConfig(cfg), "my-bucket"), "config.yaml"),
	config.WithEnv("APP"),
)
if err != nil {
	return err
}

host := store.View().GetString("db.host") // from s3://my-bucket/config.yaml
```

The object is read, merged, written and watched exactly as a local file would be.

## Connect to S3

Building the client yourself is the default. Two further rungs exist, and the ambient one lives in a
subpackage:

| Use | When |
|---|---|
| `Wrap(client, bucket)` | You built the `*s3.Client`: a specific endpoint (MinIO, Ceph, LocalStack through `BaseEndpoint`), retry policy, or a client shared with other code. |
| `FSFromConfig(cfg, bucket)` | You resolved an `aws.Config` (a profile, an assumed role) and want the adapter to build the client from it. |
| `ambient.Default(ctx, bucket)` | Nothing to configure: the ambient AWS chain (environment, profile, SSO session or instance role). |

```go
// You resolved the config; the adapter builds the client.
fsys, err := configawss3.FSFromConfig(cfg, "my-bucket")

// Nothing at all. Note the separate import.
import s3ambient "gitlab.com/phpboyscout/go/config-aws-s3/ambient"

fsys, err := s3ambient.Default(ctx, "my-bucket")
```

None of these return anything you need to close; the S3 client holds no connection of its own. The
why behind the ladder is in [who owns the connection](../explanation/connection-ownership.md).

The subpackage keeps the ambient chain's extra dependencies away from anyone who builds their own
client. It saves less here than for the sibling AWS adapters, because S3's larger service graph
already carries `internal/v4a`, `accept-encoding` and `presigned-url`. To share one resolved chain
across several adapters, pass it to `s3ambient.FSFromSource`.

### Name a region

There is no default region, and the bucket is required. AWS documents no region default, so a
config with none returns `ErrNoRegion` rather than a guess, and an empty bucket returns
`ErrNoBucket`. For the ambient rung, set `AWS_REGION`, or take the region from a flag with
`s3ambient.FSFromSource(ctx, awsclient.Ambient(s3ambient.WithRegion(region)), "my-bucket")`.

## Map objects to keys

The `config.FS` name is the object key, so `Wrap(client, "my-bucket")` reads and writes the object
`config.yaml`. The config keys come from the object's contents, parsed by the same codec a local
file would use. If the bucket is shared with unrelated objects, add a key prefix:

```go
configawss3.Wrap(client, "my-bucket", configawss3.WithKeyPrefix("apps/mytool/"))
```

With the prefix, `config.yaml` resolves to the object `apps/mytool/config.yaml`.

## Write back to the object

`Store.Apply` writes to the object through the core's stage-and-rename machinery. S3 has no atomic
rename, so the commit is a sequence of `CopyObject` and `DeleteObject` calls. The target is still
replaced atomically, because `PutObject` and `CopyObject` are atomic per object and a reader never
sees a half-written object.

The one imperfection is a crash part-way through the sequence, which can leave a staged object
behind under a `.config-stage` key. That is a findable orphan you can expire with a lifecycle rule,
not a corrupted target or a lost write. Conflict detection is unaffected: the write path
fingerprints content by SHA-256 at load and refuses a write if the object changed underneath it.

## Watch for changes

An object store has no local path, so it is watched by **polling**. Because each poll is a billed
`GetObject`, `config-aws-s3` declares a **60-second** default through `config.PollIntervalHinter`.
That is far calmer than the 2-second local default. The adapter has no interval option of its own:
an explicit `config.WithPollInterval` passed to `Store.Watch` overrides the hint.

```go
stop, err := store.Watch(ctx, config.WithPollInterval(5*time.Minute))
```

See [hot-reload](hot-reload.md) for the store side.

## Test without S3

The adapter's in-memory object store is internal to its own test suite, so a test of yours has two
routes. Point the client's `BaseEndpoint` at an S3-compatible server such as
[LocalStack](https://localstack.cloud/) or MinIO, and the adapter drives it unchanged. Or, when the
test is about your configuration rather than S3, hand `config.WithFiles` an in-memory filesystem
through [`config-afero`](afero.md) instead.

The module's integration suite, under `test/integration`, runs against LocalStack under
testcontainers, so it needs no AWS account. It is skipped unless `INT_TEST_INTEGRATION=1` is set,
and needs a Docker daemon.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `configawss3.ErrNoRegion` | `FSFromConfig` got a config with no region. | Set one on the config. |
| `awsclient.ErrNoRegion` | `ambient.Default` found no region in the environment. | Set `AWS_REGION`, or use `FSFromSource` with `WithRegion`. |
| `configawss3.ErrNoBucket` | A rung was called with an empty bucket name. | Pass the bucket the object lives in. |
| `config.ErrConflict` | The object changed between load and write. | Reload and apply the change again. |
| `fs.ErrNotExist` | The object or bucket is absent. | Check the bucket, key and prefix. The store tolerates it as an empty layer unless it is the first source and you set `RequireFirstSource`. |

## Limits

- **No directories.** `MkdirAll` is a no-op; a key containing `/` is a flat key.
- **No POSIX mode.** The write ignores its permission argument, and `Stat` reports a fixed `0644`.
- **No symlinks and no local path**, which is why the store polls rather than using a native file
  watcher.

## When you don't need this

If you read one object once at startup, fetch it with the AWS SDK and hand the
bytes to [`WithReaders`](load-and-merge.md). No adapter needed.

Reach for `config-aws-s3` when you want the bucket to behave like a filesystem: several
files, writes routed back to the object, or hot-reload picking up a change someone else
made.

## What it costs

| | |
|---|---|
| Adds | the AWS S3 SDK, on top of the `config` graph |

The AWS SDK for Go v2 S3 packages, plus `config` and what it already brings. An allowlist test pins
this to exactly the S3 SDK modules, so a consumer reading from S3 never compiles another cloud's SDK.

## Related

- [How filesystem adapters work](../explanation/filesystem-adapters.md): the object-store commit
  model and polled reload
- [Read and write config in GCS](gcp-gcs.md) and [Azure Blob](azure-blob.md): the sibling object stores
- [Backends & capabilities](../explanation/backends.md): the `config.FS` interface

---
title: Read and write config in Azure Blob
description: Point config at a configuration file that lives in an Azure Blob Storage container, through an azblob client you own.
tags: [how-to, filesystem, azure, blob, cloud]
---

# Read and write config in Azure Blob

When a tool's configuration file lives in an **Azure Blob Storage container**, the
[`config-azure-blob`](https://gitlab.com/phpboyscout/go/config-azure-blob) sibling module wraps an
[`*azblob.Client`](https://pkg.go.dev/github.com/Azure/azure-sdk-for-go/sdk/storage/azblob) as a
`config.FS`. You build and authenticate the client and hand it in with the container name.

```bash
go get gitlab.com/phpboyscout/go/config-azure-blob
```

```go
import (
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"

	"gitlab.com/phpboyscout/go/config"
	configazureblob "gitlab.com/phpboyscout/go/config-azure-blob"
)

cred, err := azidentity.NewDefaultAzureCredential(nil)
if err != nil {
	return err
}
client, err := azblob.NewClient("https://acct.blob.core.windows.net/", cred, nil)
if err != nil {
	return err
}

store, err := config.NewStore(ctx,
	config.WithFiles(configazureblob.Wrap(client, "my-container"), "config.yaml"),
	config.WithEnv("APP"),
)
if err != nil {
	return err
}

host := store.View().GetString("db.host") // from the blob config.yaml
```

The blob is read, merged, written and watched exactly as a local file would be.

## Connect to Blob Storage

Building the client yourself is the default (`azidentity`, a connection string, a shared key,
whatever you already use). Because the adapter takes only the SDK client, `azidentity` stays *your*
dependency and never enters the adapter's own graph. The middle rung has two shapes, and the
ambient rung lives in a subpackage:

| Use | When |
|---|---|
| `Wrap(client, container)` | You built the `*azblob.Client`, or share it with other code. Read the note on [private containers](#write-back-to-the-blob) first if you write. |
| `FSFromCredential(cred, serviceURL, container)` | You hold a principal (`azcore.TokenCredential`) and the account URL. |
| `FSFromConnectionString(conn, container)` | You hold a connection string, which carries the account URL and the secret together. |
| `ambient.Default(ctx, serviceURL, container)` | Nothing but the target: the ambient Azure identity chain. |

```go
// A principal, with the account URL supplied separately.
fsys, err := configazureblob.FSFromCredential(cred, serviceURL, "config")

// A connection string, which carries the account URL AND the secret together.
fsys, err := configazureblob.FSFromConnectionString(conn, "config")

// Nothing but the target. Note the separate import.
import blobambient "gitlab.com/phpboyscout/go/config-azure-blob/ambient"

fsys, err := blobambient.Default(ctx, serviceURL, "config")
```

Nothing here needs closing. `azblob.Client` has no `Close` at all, because it is HTTP-backed with no
connection to release, so these rungs return a plain `config.FS` rather than an owned type. The why
behind the ladder is in [who owns the connection](../explanation/connection-ownership.md).

The service URL and container are both required (`ErrNoServiceURL`, `ErrNoContainer`), an empty
connection string returns `ErrNoConnectionString`, and a nil credential is refused with
`ErrNoCredential`, including a typed nil.

### Treat the connection string as a secret

The connection string embeds the account key. The adapter never logs it, and reports a malformed
one without echoing the input.

## Map blobs to keys

The `config.FS` name is the blob name, and the config keys come from the blob's contents, parsed by
the same codec a local file would use. The **container must already exist**. `MkdirAll` is a no-op,
so `config-azure-blob` never creates it. Provisioning the container is yours, as authenticating is.

## Write back to the blob

Azure Blob has no atomic move, so the commit is a copy followed by a delete of the source. The copy
is the **synchronous** copy-from-URL, so it completes within the call rather than returning a pending
async operation. The target is replaced atomically. A crash between the copy and the delete can leave
the temporary blob behind, which is a findable orphan and never a corrupted target. Conflict detection
is the core's SHA-256 content fingerprint at load.

!!! note "Copying a private blob depends on how the client was built"
    The synchronous copy authorises the *source* blob separately from the request. What the adapter can
    do about that depends on what it holds:

    - **A shared-key client** (a connection string) signs the source with a short-lived read SAS, so a
      private same-account blob copies fine.
    - **`FSFromCredential` and the ambient rung** hold your token credential, so the source carries a
      bearer token instead. A private container works.
    - **A client you built yourself with `azidentity` and passed to `Wrap`** keeps its credential out
      of the adapter's reach, so it cannot sign the source. The copy goes unsigned, which works for a
      public container but is refused for a private one. If you write config to a private container,
      use `FSFromCredential`, or sign with a shared key.

## Watch for changes

A blob has no local path, so it is watched by **polling**. Because each poll is a billed read,
`config-azure-blob` declares a **60-second** default through `config.PollIntervalHinter`. The
adapter has no interval option of its own: override it with `config.WithPollInterval` on
`Store.Watch`. See [hot-reload](hot-reload.md) for the store side.

## Test without Azure

Alongside `Wrap`, the module exports a narrow five-method `BlobStore` interface (`Download`,
`Upload`, `GetProperties`, `Copy`, `Delete`) and `New(BlobStore)`, so a fake drives your tests with
no Azure. A missing blob must come back as `fs.ErrNotExist`:

```go
type fakeBlobs map[string][]byte

func (f fakeBlobs) Download(_ context.Context, name string) ([]byte, error) {
	if b, ok := f[name]; ok {
		return b, nil
	}
	return nil, fs.ErrNotExist
}

func (f fakeBlobs) Upload(_ context.Context, name string, data []byte) error {
	f[name] = data
	return nil
}

func (f fakeBlobs) GetProperties(_ context.Context, name string) (configazureblob.BlobInfo, error) {
	if b, ok := f[name]; ok {
		return configazureblob.BlobInfo{Size: int64(len(b))}, nil
	}
	return configazureblob.BlobInfo{}, fs.ErrNotExist
}

func (f fakeBlobs) Copy(_ context.Context, src, dst string) error {
	f[dst] = f[src]
	return nil
}

func (f fakeBlobs) Delete(_ context.Context, name string) error {
	delete(f, name)
	return nil
}

fsys := configazureblob.New(fakeBlobs{"config.yaml": []byte("db:\n  host: db.internal\n")})
```

The module's integration suite, under `test/integration`, runs against
[Azurite](https://github.com/Azure/Azurite) under testcontainers, so it needs no Azure account. It
is skipped unless `INT_TEST_INTEGRATION=1` is set, and needs a Docker daemon.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `config.ErrConflict` | The blob changed between load and write. | Reload and apply the change again. |
| `configazureblob.ErrNoServiceURL` | `FSFromCredential` or the ambient rung got no account URL. | Pass the service URL. |
| `configazureblob.ErrNoContainer` | A rung was called with no container name. | Pass the container. |
| `configazureblob.ErrNoConnectionString` | `FSFromConnectionString` got an empty string. | Pass the connection string. |
| `configazureblob.ErrNoCredential` | `FSFromCredential` got a nil (or typed-nil) credential. | Check the error from the credential constructor. |
| `fs.ErrNotExist` | The blob is absent. | Check the name. The store tolerates it as an empty layer unless it is the first source and you set `RequireFirstSource`. |
| A storage error (no sentinel) | The container does not exist, or a write to a private container went through `Wrap` with an `azidentity` client. | Create the container; for the copy, see [the note above](#write-back-to-the-blob). |

## Limits

- **No directories and no POSIX mode.** `MkdirAll` does nothing, and the container is never created
  for you.
- **No local path and no symlinks**, which is why the store polls rather than using a native file
  watcher.

## When you don't need this

If you read one blob once at startup, fetch it and hand the bytes to
[`WithReaders`](load-and-merge.md). No adapter needed.

Reach for `config-azure-blob` when the container should behave like a filesystem:
several files, writes routed back, or hot-reload noticing a change made elsewhere.

## What it costs

| | |
|---|---|
| Adds | the Blob Storage SDK, on top of the `config` graph |

The `azblob` SDK (deliberately without `azidentity`), plus `config`,
asserted by an allowlist test.

## Related

- [How filesystem adapters work](../explanation/filesystem-adapters.md): the object-store commit
  model and polled reload
- [Read and write config in S3](aws-s3.md) and [GCS](gcp-gcs.md): the sibling object stores
- [Backends & capabilities](../explanation/backends.md): the `config.FS` interface

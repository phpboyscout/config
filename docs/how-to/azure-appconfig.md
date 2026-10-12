---
title: Read and write Azure App Configuration
description: Make Azure App Configuration a config layer with config-azure-appconfig, covering labels, prefix scoping, ETag compare-and-swap writes, and the polling cadence.
tags: [how-to, adapters, backends, azure, appconfig]
---

# Read and write Azure App Configuration

[Azure App Configuration](https://learn.microsoft.com/azure/azure-app-configuration/overview) becomes
a read-write config layer through the sibling module
[`config-azure-appconfig`](https://gitlab.com/phpboyscout/go/config-azure-appconfig), taken only by
consumers who need it. You build the App Configuration client and hand it in with a **key prefix**.

```bash
go get gitlab.com/phpboyscout/go/config-azure-appconfig
```

```go
import (
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azappconfig/v2"
	"gitlab.com/phpboyscout/go/config"
	configazureappconfig "gitlab.com/phpboyscout/go/config-azure-appconfig"
)

cred, err := azidentity.NewDefaultAzureCredential(nil)
if err != nil {
	return err
}
client, err := azappconfig.NewClient(endpoint, cred, nil)
if err != nil {
	return err
}

store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/app.yaml"),
	config.WithBackend(configazureappconfig.FromClient(client, "app/")), // outranks the file
)
if err != nil {
	return err
}

host := store.View().GetString("db.host") // from the setting app/db/host
```

The layer takes part in precedence, per-key merge, provenance and hot-reload like a file.

## Connect to App Configuration

Building the client yourself is the default, so the endpoint and credential (an `azidentity` token
or a connection string) stay yours. The middle rung has two shapes here, because App Configuration
genuinely has two ways in:

| Use | When |
|---|---|
| `FromClient(client, prefix)` | You built the `*azappconfig.Client`, or share it with other code. |
| `FromCredential(cred, endpoint, prefix)` | You hold a principal (`azcore.TokenCredential`) and the store's endpoint. |
| `FromConnectionString(conn, prefix)` | You hold a connection string, which carries the endpoint and the secret together. |
| `ambient.Default(ctx, endpoint, prefix)` | Nothing but the endpoint: the ambient Azure identity chain. |

```go
// A principal, with the endpoint supplied separately.
b, err := configazureappconfig.FromCredential(cred, endpoint, "app/")

// A connection string, which carries the endpoint AND the secret together.
b, err := configazureappconfig.FromConnectionString(conn, "app/")

// Nothing but the endpoint. Note the separate import.
import acambient "gitlab.com/phpboyscout/go/config-azure-appconfig/ambient"

b, err := acambient.Default(ctx, endpoint, "app/")
```

Neither credential shape is a special case of the other, so both are rungs. Nothing in the ladder
needs closing. The why behind the ladder is in
[who owns the connection](../explanation/connection-ownership.md).

The endpoint is always required (`ErrNoEndpoint`), and a nil credential is refused (`ErrNoCredential`),
including a typed nil. An empty connection string returns `ErrNoConnectionString`. The ambient rung
resolves the Azure identity chain lazily, so constructing it does no I/O, and it takes the endpoint
because an Azure credential names a principal but not a store.

### Treat the connection string as a secret

The connection string embeds an access key. Treat it as you would a password. The adapter reports
a malformed one without echoing the input, because that is how a secret reaches a log.

## Map settings to keys

Keys under the prefix nest into the tree. Values are scalar strings by default; pass a
`config.Codec` with `WithValueCodec` to decode a document-valued setting. The declared
content-type is **not** used to pick a codec. Decoding is always the codec you inject.

Feature-flag settings are filtered out (they are a distinct concern), and a Key Vault reference is
left as an opaque string rather than resolved. Resolving secrets is [Key Vault's](azure-keyvault.md)
job.

### Pick a label

App Configuration keys are identified by **(key, label)**. Scope a backend to one label with
`WithLabel`; omitted, it reads the no-label set:

```go
configazureappconfig.FromClient(client, "app/", configazureappconfig.WithLabel("prod"))
```

The label scopes reads and pins writes, and provenance names it (`azure-appconfig:app/@prod`). A backend
holds one label; to combine several, stack backends as separate layers.

## Write back to App Configuration

`config-azure-appconfig` is read **and** write. A write is a per-key **compare-and-swap** on the
setting's ETag captured at load. If the setting moved since, the write is refused with
`config.ErrConflict` rather than clobbering it.

A multi-key batch is applied per key (App Configuration has no transaction, so `AtomicMultiKey` is
false), and a mid-batch conflict rolls the already-written keys back, best effort. Writes always
create or update flat settings, so a value that a codec decoded from a document is not written back
into it; the key lands as a sibling flat setting. A value with no flat form (a map, a slice, or a
lossy number such as a `float32`) is refused with `config.ErrBackendUnsafe`.

## Watch for changes

App Configuration has no change feed, so the adapter joins hot-reload by **polling**
(`NativeWatch: false`) and declares a **30-second** default through `config.PollIntervalHinter`.
The adapter has no interval option of its own: `config.WithPollInterval` on `Store.Watch` overrides
the cadence.

Name a **sentinel key** with `WithSentinelKey` and each poll becomes one conditional GET that returns
304 until the sentinel's ETag changes. Without one, each poll re-lists the whole prefix and fires
when any setting's ETag differs, which is correct but heavier. Update the sentinel last when you
change a group of settings, so one poll picks the group up together.

```go
configazureappconfig.FromClient(client, "app/", configazureappconfig.WithSentinelKey("app/sentinel"))
```

A failed poll is not treated as a change; the watch backs off for a second and retries. See
[hot-reload](hot-reload.md) for the store side.

## Test without Azure

The adapter reaches the service through the four-method `configazureappconfig.Store` interface
(`List`, `Get`, `Set`, `Delete`), so a unit test hands `New` a fake and needs no Azure. A fake for a
read-only test can report every write as a moved ETag:

```go
type fakeAppConfig []configazureappconfig.Setting

func (f fakeAppConfig) List(context.Context, string, string) ([]configazureappconfig.Setting, error) {
	return f, nil
}

func (fakeAppConfig) Get(context.Context, string, string, string) (configazureappconfig.Setting, bool, error) {
	return configazureappconfig.Setting{}, false, nil
}

func (fakeAppConfig) Set(context.Context, string, string, string, string, string) (bool, error) {
	return false, nil
}

func (fakeAppConfig) Delete(context.Context, string, string, string) (bool, error) {
	return false, nil
}

backend := configazureappconfig.New(fakeAppConfig{
	{Key: "app/db/host", Value: "db.internal", ETag: "1"},
}, "app/")
```

`Wrap(client)` is the real implementation, and what `FromClient` uses. App Configuration has no
local emulator, so the module's integration suite, under `test/integration`, talks to a real,
billable store. It is skipped unless `INT_TEST_INTEGRATION=1` is set, and needs
`APPCONFIG_CONNECTION_STRING`.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `config.ErrConflict` | A setting moved between load and write. | Reload and apply the change again. |
| `config.ErrBackendUnsafe` | The written value is a map, a slice or a lossy number, which a flat setting cannot hold. | Write scalars, one key at a time. |
| `configazureappconfig.ErrNoEndpoint` | A rung was called with no store endpoint. | Pass the endpoint. |
| `configazureappconfig.ErrNoCredential` | `FromCredential` got a nil (or typed-nil) credential. | Check the error from the credential constructor. |
| `configazureappconfig.ErrNoConnectionString` | `FromConnectionString` got an empty string. | Pass the connection string. |
| `fs.ErrNotExist` | Nothing exists under the prefix and label. | Check both. The store tolerates it as an empty layer unless it is the first source and you set `RequireFirstSource`. |

## When you don't need this

If the values are static, or already arrive as environment variables, `WithEnv` covers
them with no SDK.

Reach for App Configuration when values are managed centrally and change without a
redeploy, or for its labels, which let one store serve several environments from the
same keys.

## What it costs

| | |
|---|---|
| Adds | the App Configuration SDK, on top of the `config` graph |

The config graph plus the App Configuration SDK (`azappconfig`, `azcore` and their support
modules, asserted by an allowlist test). `azidentity` is **yours**, built with the client, not the
adapter's.

## Related

- [How dynamic backends work](../explanation/dynamic-backends.md): injected client, polling,
  compare-and-swap conflicts
- [The adapter ecosystem](../explanation/adapters.md): every adapter, with status and roadmap

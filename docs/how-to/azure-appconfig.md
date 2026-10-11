---
title: Read and write Azure App Configuration
description: Make Azure App Configuration a config layer with config-azure-appconfig, covering labels, prefix scoping, ETag compare-and-swap writes, and the polling cadence.
tags: [how-to, adapters, backends, azure, appconfig]
---

# Read and write Azure App Configuration

[Azure App Configuration](https://learn.microsoft.com/azure/azure-app-configuration/overview) becomes
a config layer through the sibling module
[`config-azure-appconfig`](https://gitlab.com/phpboyscout/go/config-azure-appconfig), taken only by
consumers who need it.

```bash
go get gitlab.com/phpboyscout/go/config-azure-appconfig
```

You build and configure the App Configuration client, so the endpoint and credential (an `azidentity`
token or a connection string) stay yours, and hand it in with a **key prefix**:

```go
import (
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azappconfig"
	"gitlab.com/phpboyscout/go/config"
	configazureappconfig "gitlab.com/phpboyscout/go/config-azure-appconfig"
)

cred, _ := azidentity.NewDefaultAzureCredential(nil)
client, _ := azappconfig.NewClient(endpoint, cred, nil)

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),
	config.WithBackend(configazureappconfig.FromClient(client, "app/")), // outranks the file
)
```

Keys under the prefix nest into the tree, and the layer takes part in precedence, per-key merge,
provenance and hot-reload like a file. Values are scalar strings by default; pass a
`config.Codec` with `WithValueCodec` to decode a document-valued setting. The declared
content-type is **not** used to pick a codec. Decoding is always the codec you inject.

## When you do *not* need this

If the values are static, or already arrive as environment variables, `WithEnv` covers
them with no SDK.

Reach for App Configuration when values are managed centrally and change without a
redeploy, or for its labels, which let one store serve several environments from the
same keys.

## Labels

App Configuration keys are identified by **(key, label)**. Scope a backend to one label with
`WithLabel`; omitted, it reads the no-label set:

```go
configazureappconfig.FromClient(client, "app/", configazureappconfig.WithLabel("prod"))
```

The label scopes reads and pins writes, and provenance names it (`azure-appconfig:app/@prod`). A backend
holds one label; to combine several, stack backends as separate layers. Feature-flag settings are
filtered out (they are a distinct concern), and a Key Vault reference is left as an opaque string
rather than resolved. Resolving secrets is [Key Vault's](azure-keyvault.md) job.

## Writing

`config-azure-appconfig` is read **and** write. A write is a per-key **compare-and-swap** on the
setting's ETag captured at load. If the setting moved since, the write is refused with
`config.ErrConflict` rather than clobbering it. A multi-key batch is applied per key (App
Configuration has no transaction, so `AtomicMultiKey` is false), and a mid-batch conflict rolls the
already-written keys back, best effort. Writes always create or update flat settings, so a value
that a codec decoded from a document is not written back into it; the key lands as a sibling flat
setting.

## Watching

App Configuration has no change feed, so the adapter joins hot-reload by **polling**
(`NativeWatch: false`) and declares a **30-second** default through `config.PollIntervalHinter`.
Name a **sentinel key** with `WithSentinelKey` and each poll becomes one conditional GET that returns
304 until the sentinel's ETag changes. Without one, each poll re-lists the whole prefix and fires
when any setting's ETag differs, which is correct but heavier. Update the sentinel last when you
change a group of settings, so one poll picks the group up together. `config.WithPollInterval` on
`Store.Watch` overrides the cadence.

## Getting a client

Building the client yourself is the default. Rung 3 has two shapes here,
because App Configuration genuinely has two ways in:

```go
// A principal, with the endpoint supplied separately.
b, err := configazureappconfig.FromCredential(cred, endpoint, "app/")

// A connection string, which carries the endpoint AND the secret together.
b, err := configazureappconfig.FromConnectionString(conn, "app/")

// Nothing but the endpoint. Note the separate import.
import acambient "gitlab.com/phpboyscout/go/config-azure-appconfig/ambient"

b, err := acambient.Default(ctx, endpoint, "app/")
```

Neither shape is a special case of the other, so both are rungs.

The connection string is a secret, because it embeds an access key. Treat it as you would a
password. The adapter reports a malformed one without echoing the input, because that is how a
secret reaches a log.

The endpoint is always required (`ErrNoEndpoint`), and a nil credential is refused (`ErrNoCredential`),
including a typed nil. An empty connection string returns `ErrNoConnectionString`. The ambient rung
resolves the Azure identity chain lazily, so constructing it does no I/O, and it takes the endpoint
because an Azure credential names a principal but not a store. Nothing in the ladder needs closing.
The why behind the ladder is in [who owns the connection](../explanation/connection-ownership.md).

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

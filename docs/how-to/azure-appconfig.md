---
title: Read and write Azure App Configuration
description: Make Azure App Configuration a config layer with config-azure-appconfig — labels, prefix scoping, ETag compare-and-swap writes, and the polling cadence.
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

You build and configure the App Configuration client — endpoint and credential (an `azidentity`
token or a connection string) stay yours — and hand it in with a **key prefix**:

```go
import (
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azappconfig"
	"gitlab.com/phpboyscout/go/config"
	configazure "gitlab.com/phpboyscout/go/config-azure-appconfig"
)

cred, _ := azidentity.NewDefaultAzureCredential(nil)
client, _ := azappconfig.NewClient(endpoint, cred, nil)

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),
	config.WithBackend(configazure.FromClient(client, "app/")),   // outranks the file
)
```

Keys under the prefix nest into the tree, and the layer takes part in precedence, per-key merge,
provenance and hot-reload like a file. Values are scalar strings by default; pass a
`config.Codec` with `WithValueCodec` to decode a document-valued setting. The declared
content-type is **not** used to pick a codec — decoding is always the one you inject.

## Labels

App Configuration keys are identified by **(key, label)**. Scope a backend to one label with
`WithLabel`; omitted, it reads the no-label set:

```go
configazure.FromClient(client, "app/", configazure.WithLabel("prod"))
```

The label scopes reads and pins writes; provenance names it (`azure-appconfig:app/@prod`). Feature-flag
settings are filtered out (they are a distinct concern), and a Key Vault reference is left as an
opaque string rather than resolved — resolving secrets is Key Vault's own adapter.

## Writing

`config-azure-appconfig` is read **and** write. A write is a per-key **compare-and-swap** on the
setting's ETag captured at load: if the setting moved since, the write is refused with
`config.ErrConflict` rather than clobbering it. A multi-key batch is applied per key (App
Configuration has no transaction, so `AtomicMultiKey` is false); a mid-batch conflict rolls the
already-written keys back, best effort.

## Watching

The adapter joins hot-reload by **polling** (`NativeWatch: false`). The efficient default is a
conditional GET on a **sentinel key** (`WithSentinelKey`) — one cheap request that 304s until the
sentinel's ETag changes — falling back to a full re-list. `WithPollInterval` sets the cadence.

## What it costs

| | |
|---|---|
| Modules added | **14** — 5 for the App Configuration SDK, 9 for the `config` graph |

The config graph plus the App Configuration SDK (`azappconfig`, `azcore` and two support modules —
five, asserted by an allowlist test). `azidentity` is **yours**, built with the client, not the
adapter's.

## Related

- [How dynamic backends work](../explanation/dynamic-backends.md) — injected client, polling,
  compare-and-swap conflicts
- [The adapter ecosystem](../explanation/adapters.md) — every adapter, with status and roadmap

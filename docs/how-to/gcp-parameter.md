---
title: Read GCP Parameter Manager
description: Make GCP Parameter Manager a config layer with config-gcp-parameter — one parameter as a document or a prefix of many, why it is read-only, and the polling cadence.
tags: [how-to, adapters, backends, gcp, parameter-manager]
---

# Read GCP Parameter Manager

[Google Cloud Parameter Manager](https://cloud.google.com/secret-manager/parameter-manager/docs/overview)
becomes a config layer through the sibling module
[`config-gcp-parameter`](https://gitlab.com/phpboyscout/go/config-gcp-parameter), taken only by
consumers who need it.

```bash
go get gitlab.com/phpboyscout/go/config-gcp-parameter
```

You build and configure the Parameter Manager client — project, location and credentials stay
yours. Parameter Manager parameters are heavyweight, versioned, document-shaped resources, so the
adapter reads them **two ways**.

## When you do *not* need this

If the values are static, or already arrive as environment variables, `WithEnv` covers
them with no SDK.

Reach for Parameter Manager when values are managed centrally and change without a
deploy. Read-only here, so pair it with a writable layer if your application persists
anything.

## Single document (default)

One named parameter is one layer: its latest version's payload is the whole document. Pass a
`config.Codec` to decode it into a tree:

```go
import (
	parametermanager "cloud.google.com/go/parametermanager/apiv1"
	"gitlab.com/phpboyscout/go/config"
	configgcp "gitlab.com/phpboyscout/go/config-gcp-parameter"
	configjson "gitlab.com/phpboyscout/go/config-json"
)

client, _ := parametermanager.NewClient(ctx)

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),
	config.WithBackend(configgcp.FromClient(client, "my-project", "global", "app-config",
		configgcp.WithValueCodec(configjson.Codec{}))),   // decode the JSON payload
)
```

Parameter Manager declares each parameter's format (`JSON`/`YAML`/`UNFORMATTED`), but that field is
**not** used to pick a codec — decoding is always the one you inject, uniform with the rest of the
family. Without a codec the payload is a single string.

## Prefix (many parameters)

For a store organised as many small parameters, `FromClientPrefix` scans a name prefix and nests
each parameter as a leaf, Consul-style:

```go
config.WithBackend(configgcp.FromClientPrefix(client, "my-project", "global", "app-"))
```

Either way the layer takes part in precedence, per-key merge, provenance and hot-reload like a file.
`New`/`NewPrefix` take the narrow `PM` interface directly for testing.

## Read-only

Parameter Manager versions are immutable and it offers no compare-and-swap, so
`config-gcp-parameter` is **read-only**: a write to a key it defines routes to the writable layer
beneath, never to Parameter Manager. The adapter returns the **raw** payload (it does not resolve
embedded Secret Manager references), so the layer is not sensitive. Write, and a secret-rendering
variant, are tracked follow-ons.

## Watching

The adapter joins hot-reload by **polling** for a new latest version or a changed parameter set
(`NativeWatch: false`); `WithPollInterval` sets the cadence (60s default, since each poll is a
billed call).

## Getting a client

Building the client yourself is the default. Two further rungs exist, each in
both shapes:

```go
// You hold client options — an emulator endpoint, an explicit credentials file,
// or a credential resolved once with go/gcpclient and shared.
b, err := configgcpparameter.FromOptions(ctx, "my-project", "global", "app-config", opts)
b, err := configgcpparameter.FromOptionsPrefix(ctx, "my-project", "global", "app-", opts)

// Application Default Credentials.
b, err := configgcpparameter.Default(ctx, "my-project", "global", "app-config")
defer b.Close()
```

**All four return `*OwnedBackend`, which you should `Close`** — see
[gcp-secret](gcp-secret.md) for why.

**Two things are still required.** Like Secret Manager these rungs need the
**project**, because credentials name a principal rather than a project. Unlike
it they also need the **location**: Parameter Manager has no project-level parent,
so every parameter lives under one and `global` is the ordinary value.

## What it costs

| | |
|---|---|
| Modules added | **39** — 30 for the Parameter Manager SDK, 9 for the `config` graph |

The config graph plus the Google Cloud Parameter Manager client — the **heaviest** graph in the
adapter family (the first-party gRPC/protobuf/auth stack), asserted honestly by an allowlist test.

## Related

- [How dynamic backends work](../explanation/dynamic-backends.md) — injected client, polling, and
  why some stores are read-only
- [The adapter ecosystem](../explanation/adapters.md) — every adapter, with status and roadmap

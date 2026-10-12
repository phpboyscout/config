---
title: Read GCP Parameter Manager
description: Make GCP Parameter Manager a config layer with config-gcp-parameter, covering one parameter as a document or a prefix of many, why it is read-only, and the polling cadence.
tags: [how-to, adapters, backends, gcp, parameter-manager]
---

# Read GCP Parameter Manager

[Google Cloud Parameter Manager](https://cloud.google.com/secret-manager/parameter-manager/docs/overview)
becomes a config layer through the sibling module
[`config-gcp-parameter`](https://gitlab.com/phpboyscout/go/config-gcp-parameter), taken only by
consumers who need it. You build the Parameter Manager client and hand it in with the project, the
location and the parameter to read.

```bash
go get gitlab.com/phpboyscout/go/config-gcp-parameter
```

```go
import (
	parametermanager "cloud.google.com/go/parametermanager/apiv1"
	"gitlab.com/phpboyscout/go/config"
	configgcpparameter "gitlab.com/phpboyscout/go/config-gcp-parameter"
	configjson "gitlab.com/phpboyscout/go/config-json"
)

client, err := parametermanager.NewClient(ctx)
if err != nil {
	return err
}
defer client.Close()

store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/app.yaml"),
	config.WithBackend(configgcpparameter.FromClient(client, "my-project", "global", "app-config",
		configgcpparameter.WithValueCodec(configjson.Codec{}))), // decode the JSON payload
)
if err != nil {
	return err
}

host := store.View().GetString("db.host") // from the app-config parameter's payload
```

The layer takes part in precedence, per-key merge, provenance and hot-reload like a file.

## Connect to Parameter Manager

Building the client yourself is the default, so project, location and credentials stay yours. Two
further rungs exist, each in both shapes (one parameter, or a prefix of many):

| Use | When |
|---|---|
| `FromClient(client, project, location, parameter)` / `FromClientPrefix(...)` | You built the `*parametermanager.Client`, or share it with other code. It stays yours to close. |
| `FromOptions(ctx, project, location, parameter, opts)` / `FromOptionsPrefix(...)` | You hold client options: an emulator endpoint, an explicit credentials file, or a credential resolved once with `go/gcpclient`. |
| `Default(ctx, project, location, parameter)` / `DefaultPrefix(...)` | Nothing but the names: Application Default Credentials. |

```go
// You hold client options: an emulator endpoint, an explicit credentials file,
// or a credential resolved once with go/gcpclient and shared across adapters.
b, err := configgcpparameter.FromOptions(ctx, "my-project", "global", "app-config", opts)
b, err := configgcpparameter.FromOptionsPrefix(ctx, "my-project", "global", "app-", opts)

// Application Default Credentials.
b, err := configgcpparameter.Default(ctx, "my-project", "global", "app-config")
b, err := configgcpparameter.DefaultPrefix(ctx, "my-project", "global", "app-")

store, err := config.NewStore(ctx, config.WithBackend(b), config.WithCloser(b))
defer store.Close()
```

`FromOptions`, `FromOptionsPrefix`, `Default` and `DefaultPrefix` all return `*OwnedBackend`, which
owns the client it built and **must be closed**. Hand it to the store with `WithCloser`, as above;
[gcp-secret](gcp-secret.md) says why. A backend from `FromClient` is not owned, and the client you
built stays yours to close. The why behind the ladder is in
[who owns the connection](../explanation/connection-ownership.md).

To detect Application Default Credentials **once** and feed several GCP adapters
from it, resolve with
[`go/gcpclient`](https://gcpclient.go.phpboyscout.uk) and pass the
options it yields to `FromOptions`. It hands out client options rather than a
client precisely because the three GCP adapters need three different client
types.

### Name the project and the location

Two things are still required. Like Secret Manager, these rungs need the **project**, because
credentials name a principal rather than a project (`ErrNoProject`). Unlike it, they also need the
**location** (`ErrNoLocation`). Parameter Manager has no project-level parent, so every parameter
lives under a location and `global` is the ordinary value.

## Map parameters to keys

Parameter Manager parameters are heavyweight, versioned, document-shaped resources, so the adapter
reads them **two ways**.

### One parameter as a document (default)

One named parameter is one layer, and its current version's payload is the whole document. Parameter
Manager has no "latest" alias, so the adapter lists the versions and takes the most recently created
**enabled** one. A parameter with no enabled version is treated as absent. Pass a `config.Codec` to
decode the payload into a tree, as in the snippet above.

Parameter Manager declares each parameter's format (`JSON`/`YAML`/`UNFORMATTED`), but that field is
**not** used to pick a codec. Decoding is always the codec you inject, uniform with the rest of the
family. Without a codec, or when the codec rejects the payload, the payload is a single string held
under the parameter's own id.

### A prefix of many parameters

For a store organised as many small parameters, `FromClientPrefix` scans a name prefix and nests
each parameter as a leaf of the key tree:

```go
config.WithBackend(configgcpparameter.FromClientPrefix(client, "my-project", "global", "app-"))
```

The prefix is stripped from each parameter id and the rest is split on **both `-` and `/`** to make
the key path. With prefix `app-`, the parameter `app-db-host` becomes `db.host`. Parameter ids allow
hyphens but not slashes, so hyphens are what gives you nesting here, and a parameter whose name has a
hyphen you meant literally will be split. This costs one payload fetch per matching parameter, and
each parameter is its own layer, so provenance names the exact version a value came from.

## Read-only, and where writes go

Parameter Manager versions are immutable and it offers no compare-and-swap, so
`config-gcp-parameter` is **read-only**: a write to a key it defines routes to the writable layer
beneath, never to Parameter Manager. The adapter returns the **raw** payload and does not resolve
embedded Secret Manager references, so the layer is not `Sensitive`. Keep secrets in
[Secret Manager](gcp-secret.md). Write support, and a secret-rendering variant, are tracked
follow-ons.

## Watch for changes

Parameter Manager has no change feed, so the adapter joins hot-reload by **polling**
(`NativeWatch: false`) for a new current version or a changed parameter set. The default is **60
seconds**, declared through `config.PollIntervalHinter` because each poll is a billed call. The
`WithPollInterval` option changes the backend's own cadence, and `config.WithPollInterval` on
`Store.Watch` overrides both:

```go
configgcpparameter.FromClient(client, "my-project", "global", "app-config",
	configgcpparameter.WithPollInterval(5*time.Minute))
```

A failed poll is not treated as a change; the watch backs off for a second and retries. See
[hot-reload](hot-reload.md) for the store side.

## Test without GCP

`New` and `NewPrefix` take the narrow two-method `configgcpparameter.PM` interface directly, so a
unit test hands them a fake and needs no project. `Get` returns `fs.ErrNotExist` for an absent
parameter:

```go
type fakePM map[string]string

func (f fakePM) Get(_ context.Context, id string) (configgcpparameter.Parameter, error) {
	payload, ok := f[id]
	if !ok {
		return configgcpparameter.Parameter{}, fs.ErrNotExist
	}
	return configgcpparameter.Parameter{ID: id, VersionName: id + "/versions/1", Payload: []byte(payload)}, nil
}

func (f fakePM) List(context.Context, string) ([]configgcpparameter.Parameter, error) {
	return nil, nil
}

backend := configgcpparameter.New(fakePM{"app-config": `{"db":{"host":"db.internal"}}`}, "app-config",
	configgcpparameter.WithValueCodec(configjson.Codec{}))
```

`Wrap(client, project, location)` is the real implementation, and what `FromClient` uses. Parameter
Manager has no emulator, so the module's integration suite, under `test/integration`, talks to a
real project. It is skipped unless `INT_TEST_INTEGRATION=1` is set, and needs
`INT_TEST_GCP_PROJECT` (with `INT_TEST_GCP_LOCATION` optional, defaulting to `global`) and
Application Default Credentials.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `configgcpparameter.ErrNoProject` | A rung was called with no project. | Pass the project. |
| `configgcpparameter.ErrNoLocation` | A rung was called with no location. | Pass the location; `global` is the ordinary value. |
| `fs.ErrNotExist` | The parameter is absent or has no enabled version, or the prefix matched nothing. | Check the names. The store tolerates it as an empty layer unless it is the first source and you set `RequireFirstSource`. |
| A gRPC error (wrapped, no sentinel) | The credentials lack access to the parameter, or the call itself failed. | Grant the principal access to read parameters. |

## When you don't need this

If the values are static, or already arrive as environment variables, `WithEnv` covers
them with no SDK.

Reach for Parameter Manager when values are managed centrally and change without a
deploy. It is read-only here, so pair it with a writable layer if your application persists
anything.

## What it costs

| | |
|---|---|
| Adds | the Parameter Manager SDK, on top of the `config` graph |

The config graph plus the Google Cloud Parameter Manager client, which brings the first-party
gRPC, protobuf and auth stack. That is a heavy graph, and an allowlist test pins exactly what it
is.

## Related

- [How dynamic backends work](../explanation/dynamic-backends.md): injected client, polling, and
  why some stores are read-only
- [The adapter ecosystem](../explanation/adapters.md): every adapter, with status and roadmap

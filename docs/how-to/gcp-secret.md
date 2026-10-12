---
title: Read secrets from GCP Secret Manager
description: Read GCP Secret Manager as an ordinary config layer, with flat keys, a document mode, label filtering, and the leak guard that stops a secret being written to a plain file.
tags: [how-to, backends, gcp, secrets]
---

# Read secrets from GCP Secret Manager

[GCP Secret Manager](https://cloud.google.com/secret-manager) secrets become an ordinary config
layer through [`config-gcp-secret`](https://gitlab.com/phpboyscout/go/config-gcp-secret), a sibling
module, so a consumer who reads secrets from GCP takes it and its SDK, and one who does not pays
nothing. You build the client and hand it in with the project it should read.

```bash
go get gitlab.com/phpboyscout/go/config-gcp-secret
```

```go
import (
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"gitlab.com/phpboyscout/go/config"
	configgcpsecret "gitlab.com/phpboyscout/go/config-gcp-secret"
)

client, err := secretmanager.NewClient(ctx)
if err != nil {
	return err
}
defer client.Close()

store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/app.yaml"),                           // YAML defaults
	config.WithBackend(configgcpsecret.FromClient(client, "my-project", "")), // secrets outrank them
)
if err != nil {
	return err
}

password := store.View().GetString("db-password") // from the secret db-password
```

The empty third argument is the location: `""` reads the project-global parent, and a region name
reads a regional one.

## Connect to Secret Manager

`FromClient` never authenticates, so every GCP credential mechanism (Application Default
Credentials, a service account, workload identity) works because it knows about none of them.
Building the client yourself is the default. Two further rungs exist:

| Use | When |
|---|---|
| `FromClient(client, project, location)` / `FromClientSecret(client, project, location, id, codec)` | You built the `*secretmanager.Client`, or share it with other code. It stays yours to close. |
| `FromOptions(ctx, project, location, opts)` | You hold client options: an emulator endpoint, an explicit credentials file, or a credential resolved once with `go/gcpclient`. |
| `Default(ctx, project, location)` | Nothing but the project: Application Default Credentials. |

```go
// You hold client options: an emulator endpoint, an explicit credentials file,
// or a credential resolved once with go/gcpclient and shared across adapters.
b, err := configgcpsecret.FromOptions(ctx, "my-project", "", opts)

// Application Default Credentials.
b, err := configgcpsecret.Default(ctx, "my-project", "")

store, err := config.NewStore(ctx, config.WithBackend(b), config.WithCloser(b))
defer store.Close()
```

The document shape is offered only on the first rung, as `FromClientSecret`.

`FromOptions` and `Default` return `*OwnedBackend`, which **must be closed**.
`secretmanager.NewClient` opens a gRPC connection its own documentation says must
be closed, and `config.Backend` has no `Close`. Hand the backend to the store with
`WithCloser` and closing the store releases it; otherwise a client the adapter
built for itself lives for the whole process. Nothing for a one-shot CLI; a leak
for a long-lived service. See
[Some rungs hand you something to close](../explanation/connection-ownership.md#some-rungs-hand-you-something-to-close).
A backend from `FromClient` is not owned, and the client you built stays yours to close.

To detect Application Default Credentials **once** and feed several GCP adapters
from it, resolve with
[`go/gcpclient`](https://gcpclient.go.phpboyscout.uk) and pass the
options it yields to `FromOptions`. It hands out client options rather than a
client precisely because the three GCP adapters need three different client
types.

### Name the project

The project is still required. Application Default Credentials authenticate a
*principal* and do not name the project whose secrets to read, and guessing
would read another project's, so `ErrNoProject` refuses at construction.

## Map secrets to keys

Secret IDs allow only letters, digits, hyphens and underscores, with no dots and no slashes. There is
**no hierarchy to map**, so an ID becomes a config key exactly as it appears in the console:

```
db-password  = "s3cr3t"        store.View().GetString("db-password")
api_key      = "k-123"    becomes    store.View().GetString("api_key")
```

Hyphens and underscores are **not** separators, for the reason [Azure Key
Vault](azure-keyvault.md) gives: both are legal in ordinary IDs with no character left to escape
with, so treating either as a separator would silently restructure every secret you own.

### Structure comes from a document

```go
import configjson "gitlab.com/phpboyscout/go/config-json"

config.WithBackend(configgcpsecret.FromClientSecret(client, "my-project", "", "app-config", configjson.Codec{}))
```

The codec is a **parameter, not an option**, because a payload is one opaque blob and without
something to decode it there is no tree to contribute. A payload the codec cannot decode fails the
load with `config.ErrBackendParse`. In flat mode `WithValueCodec` is optional and decodes any
secret holding a document into a subtree under its own key.

### Scope what gets read

Reading a whole project is one listing plus one access per secret, so scoping matters:

```go
configgcpsecret.FromClient(client, "my-project", "",
	configgcpsecret.WithLabel("app", "checkout"),   // server-side
	configgcpsecret.WithNamePrefix("checkout-")) // hint + client-side re-check
```

`WithLabel` filters **server-side** and is the one to reach for, because the service does the work
and nothing else is fetched. It can be repeated, and the labels are ANDed.

`WithNamePrefix` is weaker than it looks and the adapter compensates. Secret Manager's `name:`
filter is **case-insensitive substring containment, not a prefix match**, so `name:app` also matches
`legacy-app-token`. So the prefix is sent as a *hint* to narrow the listing, and then re-checked
client-side, because the service's answer is broader than the word "prefix" suggests.

### Version states, and the one behaviour that will surprise you

`latest` in Secret Manager means the **most recently created** version, not the most recently
enabled. So disabling a freshly-created version does not hand `latest` back to the one before it;
it makes `latest` unreadable.

This adapter **falls back to the newest ENABLED version** in that case, so the application keeps
running through a half-completed rotation. This behaviour has not yet been confirmed against the
real service; see [Test without GCP](#test-without-gcp).

!!! warning "The fallback keeps you up, and hides something"

    When it fires, your application is serving **an older version than the console shows as
    current**, and nothing in the configuration says so. That is usually a rotation that failed
    part-way: version 8 was created and disabled, and version 7 is still in use.

    Provenance cannot carry this, because a `Source` describes a whole layer while the fallback is
    per-key, so the adapter tells you through a callback instead:

    ```go
    configgcpsecret.WithFallbackObserver(func(f configgcpsecret.Fallback) {
        log.Warn("serving an older secret version",
            "secret", f.ID, "latest", f.LatestVersion, "state", f.LatestState, "served", f.ServedVersion)
    })
    ```

    **Wire it up.** Without it the fallback is silent, which is the whole risk. The callback runs
    synchronously during `Load`, once per affected secret, so it must not block or call back into
    the store.

A secret with **no** enabled version contributes no key in flat mode, and fails the load in
document mode, where there is nothing to build a tree from. `WithVersion` pins an explicit version
(a number, or an alias you defined on the secret). A pinned version never falls back: if you named it
and it is unusable, that is an error rather than a substitution.

### Payload integrity

Secret Manager returns a CRC32C checksum with each payload, and the adapter **verifies it**. A
mismatch fails the read with `ErrChecksumMismatch` rather than serving bytes that did not survive
the trip. A payload that arrives with no checksum is served unchecked. The documentation says the service
generates one whenever the writer did not supply their own, so that case should not occur.

Payloads are arbitrary bytes with nothing declaring text versus binary, so a payload that is not
valid UTF-8 is skipped in flat mode and fails the load in document mode. The check is a heuristic,
so a short binary payload that happens to be valid UTF-8 is read as a string.

## Read-only, and where writes go

Every value here is a secret, so this backend declares itself `Sensitive`, and it is **read-only**
by policy. A write to a key it provides would otherwise fall through to the next writable layer,
typically a plain YAML file. The core **refuses that write**:

```go
_, err := store.Apply(ctx, config.Set("db-password", "rotated"))
// errors.Is(err, config.ErrSensitiveLeak): refused, and app.yaml is untouched
```

See [sensitive read-only backends](../explanation/dynamic-backends.md#sensitive-read-only-backends)
for the reasoning. If a key needs to be writable, do not source it from Secret Manager.

## Watch for changes

There is no watch the backend can open by itself, so this polls, at **five minutes** by default. The
interval you pass to `Store.Watch` does not apply to this layer; set this layer's own with
`WithPollInterval`:

```go
configgcpsecret.FromClient(client, "my-project", "",
	configgcpsecret.WithPollInterval(15*time.Minute))
```

The poll deliberately reads **version metadata** rather than payloads. That matters for audit:
`AccessSecretVersion` is logged as `DATA_READ`, while `GetSecretVersion` is `ADMIN_READ`, so a
quiet poll stays out of the data-access stream your security team watches. It costs no more in the
steady state, and only accesses what actually moved.

A poll notices a rotation and a secret appearing or disappearing. It also notices **a fallback
appearing**, which is the case worth naming: a failed rotation does not move the served version at
all, so change detection tracks the version actually served rather than the one `latest` names.

!!! note "Secret Manager does have a change feed"

    A secret can publish control-plane events to Pub/Sub topics. This adapter does not subscribe.
    The topics are configured on each secret by whoever provisions it, and a subscription is a
    separate resource with its own IAM and delivery semantics, which is a different shape from a
    watch the backend can open by itself. A Pub/Sub watch is tracked as a follow-on. See [how dynamic backends
    work](../explanation/dynamic-backends.md) for where that sits.

## Test without GCP

The adapter reaches the service through the four-method `configgcpsecret.API` interface (`Access`,
`Describe`, `Versions`, `List`), so a unit test hands `New` (or `NewSecret`) a fake and needs no
project. `Access` returns `fs.ErrNotExist` for an absent secret, and `Describe` must report a
version `ENABLED` for it to be read:

```go
type fakeSecrets map[string]string

func (f fakeSecrets) Access(_ context.Context, id, _ string) (configgcpsecret.Payload, error) {
	v, ok := f[id]
	if !ok {
		return configgcpsecret.Payload{}, fs.ErrNotExist
	}
	return configgcpsecret.Payload{VersionName: id + "/versions/1", Data: []byte(v)}, nil
}

func (fakeSecrets) Describe(_ context.Context, id, _ string) (configgcpsecret.Version, error) {
	return configgcpsecret.Version{Name: id + "/versions/1", State: "ENABLED"}, nil
}

func (fakeSecrets) Versions(context.Context, string) ([]configgcpsecret.Version, error) {
	return nil, nil
}

func (f fakeSecrets) List(context.Context, string) ([]configgcpsecret.Secret, error) {
	var out []configgcpsecret.Secret
	for id := range f {
		out = append(out, configgcpsecret.Secret{ID: id})
	}
	return out, nil
}

backend := configgcpsecret.New(fakeSecrets{"db-password": "s3cr3t"})
```

`Wrap(client, project, location)` is the real implementation, and what `FromClient` uses.

!!! warning "Released, but its version-state behaviour has not had a recorded real run"

    The conformance suite passes and the unit tests drive a hand-written fake built from Google's
    documentation and the SDK's types, not from observed behaviour. Secret Manager has no emulator,
    so **the client wiring and the version-state behaviour described above have not been confirmed
    against the actual service**. A fake cannot prove either.

    Nothing suggests it is wrong. It has simply not been demonstrated, and this page would rather say
    so than let a version number imply otherwise. The module has an integration suite under
    `test/integration/` that asserts each of these claims against a real project. It needs
    `INT_TEST_INTEGRATION=1` and `GCP_SECRET_PROJECT`, and it does not run in CI. This warning comes
    off when a real run has been recorded.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `config.ErrSensitiveLeak` | An `Apply` set a key this layer provides, and the next writable layer is plainer. | Do not write Secret Manager keys; change them in GCP. |
| `configgcpsecret.ErrChecksumMismatch` | A payload's bytes did not match the CRC32C the service returned. | Retry the load. A persistent mismatch means the stored payload, or the path to it, is suspect. |
| `config.ErrBackendParse` | The codec could not decode the document secret's payload. | Fix the payload, or pass the codec that matches it. |
| `configgcpsecret.ErrNoProject` | `FromOptions` or `Default` got no project. | Pass the project. |
| `fs.ErrNotExist` | No usable secret matched, or the document secret is absent. | Check the IDs, labels and prefix. The store tolerates it as an empty layer unless it is the first source and you set `RequireFirstSource`. |
| A plain error (no sentinel) | In document mode, the secret has no enabled version, or its payload is not valid UTF-8. | Enable a version, or store text. |

## When you don't need this

If the secret already arrives as an environment variable or a mounted file, `WithEnv`
or `WithFiles` read it with no SDK.

Reach for Secret Manager when your process should fetch for itself: a version that
moves under a running process, or an audit trail naming your application.

## What it costs

| | |
|---|---|
| Adds | the Secret Manager SDK, on top of the `config` graph |
| Requires | the `config` version named in this module's `go.mod`; `go get` brings it |
| Capability since | `config` **v0.7.0**, the release whose `backendconformance` requires a sensitive read-only backend to refuse the routed-beneath write |

This is one of the heavier adapters in the toolkit, because the Google API client stack is large.
Worth knowing before you add it to a small binary. An allowlist test pins the graph in both
directions.

## Related

- [How dynamic backends work](../explanation/dynamic-backends.md): the mechanics every remote backend shares
- [Read secrets from Azure Key Vault](azure-keyvault.md): the other flat-namespace secrets store
- [Read secrets from Vault](vault.md) and [from AWS Secrets Manager](aws-secrets.md)
- [The adapter ecosystem](../explanation/adapters.md): every adapter, status and roadmap

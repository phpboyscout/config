---
title: Read secrets from Azure Key Vault
description: Read Azure Key Vault as an ordinary config layer, with flat keys, a document mode for structure, and the leak guard that stops a secret being written to a plain file.
tags: [how-to, backends, azure, secrets]
---

# Read secrets from Azure Key Vault

[Azure Key Vault](https://azure.microsoft.com/en-gb/products/key-vault) secrets become an ordinary
config layer through
[`config-azure-keyvault`](https://gitlab.com/phpboyscout/go/config-azure-keyvault), a sibling module:
a consumer who reads secrets from Key Vault takes it and its SDK, and one who does not pays nothing.
You build the client, with the vault URL and credential both, and hand it in.

```bash
go get gitlab.com/phpboyscout/go/config-azure-keyvault
```

```go
import (
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"gitlab.com/phpboyscout/go/config"
	configazurekeyvault "gitlab.com/phpboyscout/go/config-azure-keyvault"
)

cred, err := azidentity.NewDefaultAzureCredential(nil)
if err != nil {
	return err
}
client, err := azsecrets.NewClient("https://my-vault.vault.azure.net/", cred, nil)
if err != nil {
	return err
}

store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/app.yaml"),              // YAML defaults
	config.WithBackend(configazurekeyvault.FromClient(client)), // secrets outrank them
)
if err != nil {
	return err
}

password := store.View().GetString("db-password") // from the secret db-password
```

A Key Vault layer takes part in precedence, per-key merge, provenance and hot-reload exactly as a
file does.

## Connect to Key Vault

`FromClient` never authenticates and never reads the environment, which is what lets managed
identity, workload identity, a service principal or `DefaultAzureCredential` all work without it
knowing any of them. It also keeps **`azidentity` out of your dependency graph unless you put it
there**, and a test in the module asserts that it never appears.

Building the client yourself is the default. Two further rungs exist, each in both shapes (the whole
vault, or one secret), and the ambient one lives in a subpackage:

| Use | When |
|---|---|
| `FromClient(client)` / `FromClientSecret(client, name, codec)` | You built the `*azsecrets.Client`, or share it with other code. |
| `FromCredential(cred, vaultURL)` / `FromCredentialSecret(cred, vaultURL, name, codec)` | You hold a credential and want the adapter to build the client. |
| `ambient.Default(ctx, vaultURL)` / `ambient.DefaultSecret(ctx, vaultURL, name, codec)` | Nothing but the vault: the ambient Azure identity chain. |

```go
// You hold a credential; the adapter builds the client.
b, err := configazurekeyvault.FromCredential(cred, vaultURL)
b, err := configazurekeyvault.FromCredentialSecret(cred, vaultURL, "app-config", codec)

// Nothing but the vault. Note the separate import.
import kvambient "gitlab.com/phpboyscout/go/config-azure-keyvault/ambient"

b, err := kvambient.Default(ctx, vaultURL)
```

Nothing in the ladder needs closing. The why behind the ladder is in
[who owns the connection](../explanation/connection-ownership.md).

Resolving the ambient Azure identity chain pulls in `azidentity`, MSAL, `golang-jwt` and, notably,
`pkg/browser`, so an interactive credential can open a sign-in page. That is reasonable for a
developer tool to carry and odd for a service, hence the separate package.

The vault URL is always required (`ErrNoVaultURL`). Unlike an AWS region there is nothing ambient to
fall back on, because an Azure credential names a *principal* and carries no endpoint.

A nil credential is refused with `ErrNoCredential`, **including a typed nil**, which is what a
dropped constructor error leaves you holding and which would otherwise panic at the first request.

### Grant get and list

The vault access policy or RBAC role needs **get** and **list** on secrets.

## Map secrets to keys

Key Vault secret names allow only letters, digits and hyphens, with no dots and no slashes. There is
**no hierarchy to map**, so a name becomes a config key exactly as it appears in the portal:

```
db-password  = "s3cr3t"        store.View().GetString("db-password")
api-key      = "k-123"    →    store.View().GetString("api-key")
```

**Hyphens are not separators.** `my-service-key` is one key, not three levels of nesting. A hyphen
is legal in ordinary names and Key Vault gives no character to escape with, so treating it as a
separator would silently restructure every hyphenated secret you own with no way to opt out. This
is the same [refuse-to-guess
stance](../explanation/dynamic-backends.md#ambiguous-structure-is-refused-not-guessed) the family
takes elsewhere, applied here to every key rather than to a rare collision.

If you have come from [the Vault](vault.md) or [AWS Secrets Manager](aws-secrets.md) how-tos, this
is the adapter where the prefix-to-tree model does not exist. That is the store's doing, not the adapter's.

### Structure comes from a document

Since the key space cannot carry a tree, put one in a secret and decode it:

```go
import configjson "gitlab.com/phpboyscout/go/config-json"

config.WithBackend(configazurekeyvault.FromClientSecret(client, "app-config", configjson.Codec{}))
// {"db":{"host":"db.internal","port":5432}}  becomes  GetString("db.host"), GetInt("db.port")
```

The codec is a **parameter here, not an option**. A secret's value is one opaque string, so without
something to decode it there is no tree to contribute, and requiring it at compile time beats
failing at startup. A value the codec cannot decode fails the load with `config.ErrBackendParse`.
Reading one named secret also costs a single request rather than the vault-wide cost below.

In flat mode the codec stays optional. `WithValueCodec` decodes any secret holding a document into
a subtree **under its own name**, while a plain password beside it stays a string:

```go
config.WithBackend(configazurekeyvault.FromClient(client,
	configazurekeyvault.WithValueCodec(configjson.Codec{})))
```

### The layer holds the secrets fit to use

Three kinds of secret are **skipped**. They contribute no key, and the rest of the vault loads
normally:

| Skipped | Why |
|---|---|
| **Managed** secrets | They back a Key Vault *certificate* and hold its PFX or PEM, which is key material rather than configuration |
| **Disabled** secrets | The service will not return them, and an operator disabling one has said not to use it |
| **Expired** secrets | An operator who set an expiry meant something by it |

A **start date** (`nbf`) is deliberately *not* a skip. An expiry retires a credential and a start date
prepares one, and since the adapter reads each secret's current version, a future start date says
nothing about whether that value works.

!!! warning "A key can disappear while the secret still exists"

    Key Vault treats expiry as **informational**: it will happily return an expired secret's value.
    This adapter will not.

    So an application that was working can stop finding a key simply because a date passed. The
    secret is still there, still readable in the portal, and no longer in your configuration.

    **If a key goes missing, check the secret's expiry first.** It is the least obvious cause and
    the most likely one.

A secret that *is* fit to use but that the vault refuses to hand over is a different matter. It is
an error rather than a skip, because that is the vault saying no, not an operator saying no.

### Reading a vault costs one request per secret

Key Vault's listing returns metadata **without values**, and there is no batch read, so loading a
vault of *n* secrets is one listing plus *n* requests. That is the service's shape, not the
adapter's, and the opposite of [AWS Secrets Manager](aws-secrets.md), where a batch read makes a
whole prefix cost one call.

- `WithNamePrefix("app-")` avoids *fetching* secrets outside your prefix. It cannot avoid the
  listing, because there is no server-side name filter, but in a shared vault the fetches are the
  cost that grows. The prefix is **not** stripped from the key.
- Prefer the document mode for a vault shared with other applications: one request, and no
  dependence on what else lives there.

## Read-only, and where writes go

Every value here is a secret, so this backend declares itself `Sensitive`, and it is **read-only**
by policy. A write to a key it provides cannot land in Key Vault, so it would otherwise fall through
to the next writable layer, typically a plain YAML file. The core **refuses that write**:

```go
_, err := store.Apply(ctx, config.Set("db-password", "rotated"))
// errors.Is(err, config.ErrSensitiveLeak): refused, and app.yaml is untouched
```

See [sensitive read-only backends](../explanation/dynamic-backends.md#sensitive-read-only-backends)
for the reasoning. If a key needs to be writable, do not source it from Key Vault.

## Watch for changes

There is no change feed, so watching is **polling**, at **five minutes** by default. That is slower
than the rest of this toolkit because each poll pays that same per-secret cost. The interval you
pass to `Store.Watch` does not apply to this layer; set this layer's own with `WithPollInterval`:

```go
config.WithBackend(configazurekeyvault.FromClient(client,
	configazurekeyvault.WithPollInterval(15*time.Minute)))
```

A poll notices a rotation and a secret appearing or disappearing. It also notices **a secret ceasing
to be fit to use**, which removes a key without any version changing at all. A failed poll is not
treated as a change; the watch backs off for a second and retries. See [hot-reload](hot-reload.md)
for the store side.

## Test without Key Vault

The adapter reaches the vault through the two-method `configazurekeyvault.SecretsAPI` interface, so
a unit test hands `New` (or `NewSecret`) a fake and needs no Azure. `List` returns metadata only, as
the service does, and `Get` returns the value:

```go
type fakeVault map[string]string

func (f fakeVault) Get(_ context.Context, name string) (*configazurekeyvault.Secret, error) {
	v, ok := f[name]
	if !ok {
		return nil, nil // nil, nil is "absent"
	}
	return &configazurekeyvault.Secret{
		Name: name, Value: v, Version: "1",
		Fitness: configazurekeyvault.Fitness{Enabled: true},
	}, nil
}

func (f fakeVault) List(context.Context) ([]configazurekeyvault.Properties, error) {
	var out []configazurekeyvault.Properties
	for name := range f {
		out = append(out, configazurekeyvault.Properties{
			Name: name, Version: "1", Fitness: configazurekeyvault.Fitness{Enabled: true},
		})
	}
	return out, nil
}

backend := configazurekeyvault.New(fakeVault{"db-password": "s3cr3t"})
```

A fake secret needs `Enabled: true`, or the adapter skips it as disabled. `Wrap(client)` is the real
implementation, and what `FromClient` uses.

!!! note "Tested against a real vault, but not on every change"

    Key Vault has no emulator, so the unit tests drive a hand-written fake of the SDK surface and the
    family's conformance suite. The integration suite under `test/integration/` runs against a real
    vault. It was first run on 2026-08-17 and every claim it tests held. The managed-secret
    claim has no test yet, so that one is still taken from Microsoft's documentation. It needs
    `INT_TEST_INTEGRATION=1` and `AZURE_KEYVAULT_URL`, and it does not run in the module's CI
    pipeline, so a regression in the service wiring would show up when someone next runs it by hand.
    [How dynamic backends work](../explanation/dynamic-backends.md) tells the story of that first run.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `config.ErrSensitiveLeak` | An `Apply` set a key this layer provides, and the next writable layer is plainer. | Do not write Key Vault keys; change them in the vault. |
| `config.ErrBackendParse` | The codec could not decode a document secret's value. | Fix the secret's value, or pass the codec that matches it. |
| `configazurekeyvault.ErrNoVaultURL` | A rung was called with no vault URL. | Pass the vault URL. |
| `configazurekeyvault.ErrNoCredential` | `FromCredential` or `FromCredentialSecret` got a nil (or typed-nil) credential. | Check the error from the credential constructor. |
| `fs.ErrNotExist` | No secret fit to use matched, or the named secret is absent or skipped. | Check the names, prefix, and each secret's expiry. The store tolerates it as an empty layer unless it is the first source and you set `RequireFirstSource`. |
| An Azure error (wrapped, no sentinel) | The vault refused a secret fit to use, or the credential lacks get or list. | Grant get and list on secrets. |

## When you don't need this

If the secret already arrives as an environment variable or a file mounted by the CSI
driver, `WithEnv` or `WithFiles` read it with no SDK.

Reach for Key Vault when your process should fetch and authenticate for itself: rotation
picked up without a restart, or an audit trail naming your application.

## What it costs

| | |
|---|---|
| Adds | the Key Vault SDK, on top of the `config` graph |
| Requires | the `config` version named in this module's `go.mod`; `go get` brings it |
| Capability since | `config` **v0.7.0**, the release whose `backendconformance` requires a sensitive read-only backend to refuse the routed-beneath write |

Pinned by an allowlist test in both directions, so a version bump that widens *or* narrows the graph
fails the build rather than arriving quietly.

## Related

- [How dynamic backends work](../explanation/dynamic-backends.md): the mechanics every remote backend shares
- [Read secrets from Vault](vault.md) and [from AWS Secrets Manager](aws-secrets.md): the other secrets managers
- [The adapter ecosystem](../explanation/adapters.md): every adapter, status and roadmap

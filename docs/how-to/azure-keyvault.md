---
title: Read secrets from Azure Key Vault
description: Read Azure Key Vault as an ordinary config layer — flat keys, a document mode for structure, and the leak guard that stops a secret being written to a plain file.
tags: [how-to, backends, azure, secrets]
---

# Read secrets from Azure Key Vault

The core reads and writes files. [Azure Key
Vault](https://azure.microsoft.com/en-gb/products/key-vault) is a remote **secrets** backend,
provided by a sibling module,
[`config-azure-keyvault`](https://gitlab.com/phpboyscout/go/config-azure-keyvault), so a consumer
who reads secrets from Key Vault takes it — and its SDK — and one who does not pays nothing.

```bash
go get gitlab.com/phpboyscout/go/config-azure-keyvault
```

You build the client — vault URL and credential both — and hand it in:

```go
import (
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"gitlab.com/phpboyscout/go/config"
	configazurekeyvault "gitlab.com/phpboyscout/go/config-azure-keyvault"
)

cred, _ := azidentity.NewDefaultAzureCredential(nil)
client, _ := azsecrets.NewClient("https://my-vault.vault.azure.net/", cred, nil)

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                      // YAML defaults
	config.WithBackend(configazurekeyvault.FromClient(client)),   // secrets outrank them
)
```

The adapter never authenticates and never reads the environment, which is what lets managed
identity, workload identity, a service principal or `DefaultAzureCredential` all work without it
knowing any of them. It also keeps **`azidentity` out of your dependency graph unless you put it
there** — a test in the module asserts that it never appears.

The vault access policy or RBAC role needs **get** and **list** on secrets.

## Names are keys, verbatim

Key Vault secret names allow only letters, digits and hyphens — no dots, no slashes. There is **no
hierarchy to map**, so a name becomes a config key exactly as it appears in the portal:

```
db-password  = "s3cr3t"        store.View().GetString("db-password")
api-key      = "k-123"    →    store.View().GetString("api-key")
```

**Hyphens are not separators.** `my-service-key` is one key, not three levels of nesting. A hyphen
is legal in ordinary names and Key Vault gives no character to escape with, so treating it as a
separator would silently restructure every hyphenated secret you own with no way to opt out. This
is the same [refuse-to-guess
stance](../explanation/dynamic-backends.md#ambiguous-structure-is-refused-not-guessed) the family
takes elsewhere — applied here to every key rather than to a rare collision.

If you have come from [the Vault](vault.md) or [AWS Secrets Manager](aws-secrets.md) how-tos, this
is the adapter where the prefix→tree model does not exist. It is the store, not the adapter.

## Structure comes from a document

Since the key space cannot carry a tree, put one in a secret and decode it:

```go
import configjson "gitlab.com/phpboyscout/go/config-json"

config.WithBackend(configazurekeyvault.FromClientSecret(client, "app-config", configjson.Codec{}))
// {"db":{"host":"db.internal","port":5432}}  →  GetString("db.host"), GetInt("db.port")
```

The codec is a **parameter here, not an option**: a secret's value is one opaque string, so without
something to decode it there is no tree to contribute, and requiring it at compile time beats
failing at startup. Reading one named secret also costs a single request rather than the vault-wide
cost below.

In flat mode the codec stays optional. `WithValueCodec` decodes any secret holding a document into
a subtree **under its own name**, while a plain password beside it stays a string:

```go
config.WithBackend(configazurekeyvault.FromClient(client,
	configazurekeyvault.WithValueCodec(configjson.Codec{})))
```

## The layer holds the secrets fit to use

Three kinds of secret are **skipped** — contributing no key, while the rest of the vault loads
normally:

| Skipped | Why |
|---|---|
| **Managed** secrets | They back a Key Vault *certificate* and hold its PFX or PEM — key material, not configuration |
| **Disabled** secrets | The service will not return them, and an operator disabling one has said not to use it |
| **Expired** secrets | An operator who set an expiry meant something by it |

A **start date** (`nbf`) is deliberately *not* a skip. An expiry retires a credential; a start date
prepares one — and since the adapter reads each secret's current version, a future start date says
nothing about whether that value works.

!!! warning "A key can disappear while the secret still exists"

    Key Vault treats expiry as **informational**: it will happily return an expired secret's value.
    This adapter will not.

    So an application that was working can stop finding a key simply because a date passed. The
    secret is still there, still readable in the portal, and no longer in your configuration.

    **If a key goes missing, check the secret's expiry first.** It is the least obvious cause and
    the most likely one.

A secret that *is* fit to use but that the vault refuses to hand over is a different matter, and is
an error rather than a skip — that is the vault saying no, not an operator saying no.

## Reading a vault costs one request per secret

Key Vault's listing returns metadata **without values**, and there is no batch read, so loading a
vault of *n* secrets is one listing plus *n* requests. That is the service's shape, not the
adapter's — and the opposite of [AWS Secrets Manager](aws-secrets.md), where a batch read makes a
whole prefix cost one call.

- `WithNamePrefix("app-")` avoids *fetching* secrets outside your prefix. It cannot avoid the
  listing — there is no server-side name filter — but in a shared vault it is the cost that grows.
  The prefix is **not** stripped from the key.
- Prefer the document mode for a vault shared with other applications: one request, and no
  dependence on what else lives there.

## Watching for change

No change feed, so watching is **polling**, at **five minutes** by default — slower than the rest of
this toolkit because each poll pays that same per-secret cost:

```go
config.WithBackend(configazurekeyvault.FromClient(client,
	configazurekeyvault.WithPollInterval(15*time.Minute)))
```

A poll notices a rotation, a secret appearing or disappearing — **and a secret ceasing to be fit to
use**, which removes a key without any version changing at all.

## Writing a secrets-provided key is refused

Every value here is a secret, so this backend declares itself `Sensitive`, and it is **read-only**.
A write to a key it provides cannot land in Key Vault, so it would otherwise fall through to the
next writable layer — typically a plain YAML file. The core **refuses that write**:

```go
_, err := store.Apply(ctx, config.Set("db-password", "rotated"))
// errors.Is(err, config.ErrSensitiveLeak) — refused, and app.yaml is untouched
```

See [sensitive read-only backends](../explanation/dynamic-backends.md#sensitive-read-only-backends)
for the reasoning. If a key needs to be writable, do not source it from Key Vault.

## What it costs

| | |
|---|---|
| Modules added | **15** — 6 for the Key Vault SDK, 9 for the `config` graph |
| Requires | `config` **v0.7.0+** — the release whose `backendconformance` requires a sensitive read-only backend to refuse the routed-beneath write |

Pinned by an allowlist test in both directions, so a version bump that widens *or* narrows the graph
fails the build rather than arriving quietly.

## Related

- [How dynamic backends work](../explanation/dynamic-backends.md) — the mechanics every remote backend shares
- [Read secrets from Vault](vault.md) and [from AWS Secrets Manager](aws-secrets.md) — the other secrets managers
- [The adapter ecosystem](../explanation/adapters.md) — every adapter, status and roadmap

---
title: Read secrets from GCP Secret Manager
description: Read GCP Secret Manager as an ordinary config layer — flat keys, a document mode, label filtering, and the leak guard that stops a secret being written to a plain file.
tags: [how-to, backends, gcp, secrets]
---

# Read secrets from GCP Secret Manager

The core reads and writes files. [GCP Secret
Manager](https://cloud.google.com/secret-manager) is a remote **secrets** backend,
provided by a sibling module,
[`config-gcp-secret`](https://gitlab.com/phpboyscout/go/config-gcp-secret), so a consumer who reads
secrets from GCP takes it — and its SDK — and one who does not pays nothing.

```bash
go get gitlab.com/phpboyscout/go/config-gcp-secret
```

!!! warning "Released, but not yet exercised against a real project"

    The conformance suite passes and the unit tests drive a hand-written fake of the SDK
    surface — but Secret Manager has no emulator, so **the client wiring has never met the
    actual service**. The version-state behaviour described below is precisely the part a
    fake cannot prove.

    Nothing suggests it is wrong. It has simply not been demonstrated, and this page
    would rather say so than let a version number imply otherwise. The integration suite
    is written and waiting on credentials; this warning comes off when it passes.

You build the client — Application Default Credentials, a service account, workload identity — and
hand it in with the project it should read:

```go
import (
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"gitlab.com/phpboyscout/go/config"
	configgcpsecret "gitlab.com/phpboyscout/go/config-gcp-secret"
)

client, _ := secretmanager.NewClient(ctx)

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                                   // YAML defaults
	config.WithBackend(configgcpsecret.FromClient(client, "my-project", "")),  // secrets outrank them
)
```

The empty third argument is the location: `""` reads the project-global parent, and a region name
reads a regional one. The adapter never authenticates — every GCP credential mechanism works
because it knows about none of them.

## Names are keys, verbatim

Secret IDs allow only letters, digits, hyphens and underscores — no dots, no slashes. There is **no
hierarchy to map**, so an ID becomes a config key exactly as it appears in the console:

```
db-password  = "s3cr3t"        store.View().GetString("db-password")
api_key      = "k-123"    →    store.View().GetString("api_key")
```

Hyphens and underscores are **not** separators, for the reason [Azure Key
Vault](azure-keyvault.md) gives: both are legal in ordinary IDs with no character left to escape
with, so treating either as a separator would silently restructure every secret you own.

Structure comes from a document instead:

```go
import configjson "gitlab.com/phpboyscout/go/config-json"

config.WithBackend(configgcpsecret.FromClientSecret(client, "my-project", "", "app-config", configjson.Codec{}))
```

The codec is a **parameter, not an option** — a payload is one opaque blob, so without something to
decode it there is no tree to contribute. In flat mode `WithValueCodec` is optional and decodes any
secret holding a document into a subtree under its own key.

## Scoping what gets read

Reading a whole project is one listing plus one access per secret, so scoping matters:

```go
configgcpsecret.FromClient(client, "my-project", "",
	configgcpsecret.WithLabel("app", "checkout"),      // server-side
	configgcpsecret.WithNamePrefix("checkout-"))       // hint + client-side re-check
```

`WithLabel` filters **server-side** and is the one to reach for — the service does the work and
nothing else is fetched.

`WithNamePrefix` is weaker than it looks and the adapter compensates. Secret Manager's `name:`
filter is **case-insensitive substring containment, not a prefix match** — `name:app` also matches
`legacy-app-token`. So the prefix is sent as a *hint* to narrow the listing, and then re-checked
client-side, because the service's answer is broader than the word "prefix" suggests.

## Version states, and the one behaviour that will surprise you

`latest` in Secret Manager means the **most recently created** version — not the most recently
enabled. So disabling a freshly-created version does not hand `latest` back to the one before it;
it makes `latest` unreadable.

This adapter **falls back to the newest ENABLED version** in that case, so the application keeps
running through a half-completed rotation.

!!! warning "The fallback keeps you up, and hides something"

    When it fires, your application is serving **an older version than the console shows as
    current**, and nothing in the configuration says so. That is usually a rotation that failed
    part-way: version 8 was created and disabled, and version 7 is still in use.

    Provenance cannot carry this — a `Source` describes a whole layer, while the fallback is
    per-key — so the adapter tells you through a callback instead:

    ```go
    configgcpsecret.WithFallbackObserver(func(f configgcpsecret.Fallback) {
        log.Warn("serving an older secret version",
            "secret", f.ID, "latest", f.LatestVersion, "state", f.LatestState, "served", f.ServedVersion)
    })
    ```

    **Wire it up.** Without it the fallback is silent, which is the whole risk.

A secret with **no** enabled version contributes no key in flat mode, and fails the load in
document mode — there is nothing to build a tree from. `WithVersion` pins an explicit version, and
a pinned version never falls back: if you named it and it is unusable, that is an error rather than
a substitution.

## Payload integrity

Secret Manager returns a CRC32C checksum with each payload, and the adapter **verifies it**. A
mismatch fails the read with `ErrChecksumMismatch` rather than serving bytes that did not survive
the trip. A payload with no checksum is served unchecked — the service generates one only when the
writer did not supply their own.

Payloads are arbitrary bytes with nothing declaring text versus binary, so a payload that is not
valid UTF-8 is skipped in flat mode and fails the load in document mode.

## Watching for change

No first-class client watch, so this polls, at **five minutes** by default.

The poll deliberately reads **version metadata** rather than payloads. That matters for audit:
`AccessSecretVersion` is logged as `DATA_READ`, while `GetSecretVersion` is `ADMIN_READ`, so a
quiet poll stays out of the data-access stream your security team watches. It costs no more in the
steady state, and only accesses what actually moved.

A poll notices a rotation, a secret appearing or disappearing — **and a fallback appearing**, which
is the case worth naming: a failed rotation does not move the served version at all, so change
detection compares the fallback set too.

!!! note "Secret Manager does have a change feed"

    A secret can publish to up to 10 Pub/Sub topics on change. This adapter does not subscribe —
    that needs a Pub/Sub client and a subscription you provision, which is a different shape from a
    watch the backend can open by itself. See [how dynamic backends
    work](../explanation/dynamic-backends.md) for where that sits.

## Writing a secrets-provided key is refused

Every value here is a secret, so this backend declares itself `Sensitive`, and it is **read-only**.
A write to a key it provides would otherwise fall through to the next writable layer — typically a
plain YAML file. The core **refuses that write**:

```go
_, err := store.Apply(ctx, config.Set("db-password", "rotated"))
// errors.Is(err, config.ErrSensitiveLeak) — refused, and app.yaml is untouched
```

## What it costs

| | |
|---|---|
| Modules added | **39** — 31 for the Secret Manager SDK, 9 for the `config` graph |
| Requires | `config` **v0.7.0+** — the release whose `backendconformance` requires a sensitive read-only backend to refuse the routed-beneath write |

This is **the heaviest adapter in the toolkit** — roughly five times AWS Secrets Manager's five
modules or Azure Key Vault's six, because the Google API client stack is large and shared. Worth
knowing before you add it to a small binary. The figure is pinned by an allowlist test in both
directions.

## Related

- [How dynamic backends work](../explanation/dynamic-backends.md) — the mechanics every remote backend shares
- [Read secrets from Azure Key Vault](azure-keyvault.md) — the other flat-namespace secrets store
- [Read secrets from Vault](vault.md) and [from AWS Secrets Manager](aws-secrets.md)
- [The adapter ecosystem](../explanation/adapters.md) — every adapter, status and roadmap

---
title: Read secrets from Vault
description: Read HashiCorp Vault KV v2 secrets as an ordinary config layer, with the leak guard that stops a secret being written to a plain file.
tags: [how-to, backends, vault, secrets]
---

# Read secrets from Vault

The core reads and writes files. [HashiCorp Vault](https://www.vaultproject.io/) is a remote
**secrets** backend, provided by a sibling module,
[`config-vault`](https://gitlab.com/phpboyscout/go/config-vault), so a consumer who reads secrets
from Vault takes it — and its Vault SDK — and one who does not pays nothing.

```bash
go get gitlab.com/phpboyscout/go/config-vault
```

You build and **authenticate** the Vault client — that is where every auth method, address,
namespace and TLS decision lives — and hand it in with the KV v2 mount and the secret path:

```go
import (
	vaultapi "github.com/hashicorp/vault/api"
	"gitlab.com/phpboyscout/go/config"
	configvault "gitlab.com/phpboyscout/go/config-vault"
)

client, _ := vaultapi.NewClient(vaultapi.DefaultConfig())
client.SetToken(token)

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                             // YAML defaults
	config.WithBackend(configvault.FromClient(client, "secret", "app")), // Vault outranks them
)
```

A Vault layer takes part in precedence, per-key merge, provenance and hot-reload exactly as a file
does. The secret's fields become the layer, keeping the structure Vault stores them with:

```
secret/app = {                          store.View().GetString("db.host") // "db.internal"
  "db": { "host": "db.internal",   →    store.View().GetInt("db.port")    // 5432
          "port": 5432 } }
```

Unlike the byte-valued stores — Consul, the parameter stores — Vault returns **already-structured**
JSON, so nested maps, slices, booleans and nulls all survive the round trip. There is no value
codec here and none is needed.

## Authenticate however you like — the adapter never does

`config-vault` has no auth method, no address and no credential anywhere in its API. It uses the
client you give it, which is what lets every Vault auth method work without the adapter knowing any
of them.

Every method ends the same way — an authenticated `*api.Client` goes into `FromClient`:

```go
// AppRole — the common service-to-service path.
appRole, _ := approle.NewAppRoleAuth(roleID, &approle.SecretID{FromEnv: "APPROLE_SECRET_ID"})
client.Auth().Login(ctx, appRole)

// Kubernetes — the common in-cluster path.
k8s, _ := kubernetes.NewKubernetesAuth("your-role")
client.Auth().Login(ctx, k8s)

// …then, either way:
config.WithBackend(configvault.FromClient(client, "secret", "app"))
```

The auth helper packages (`vault/api/auth/approle`, `.../kubernetes`) are separate modules — add
whichever you use; `config-vault` depends on none of them.

!!! warning "Tokens expire, and the adapter does not renew them"

    When a Vault token lapses, reads through this backend start failing — reloads and watch polls
    included. That is Vault rejecting the token, not a defect in the adapter, and it surfaces as a
    load error rather than being swallowed.

    If your process outlives its token's TTL, renew it:

    ```go
    watcher, _ := client.NewLifetimeWatcher(&vaultapi.LifetimeWatcherInput{Secret: login})
    go watcher.Start()
    defer watcher.Stop()
    ```

    Vault Agent or your platform's sidecar does the same job. Runnable versions of all of the above
    are in the module's [package
    examples](https://pkg.go.dev/gitlab.com/phpboyscout/go/config-vault#pkg-examples), compiled by
    its test suite so they cannot drift from the API.

## One secret, or a tree of them

`FromClient` reads **one secret** — the common shape, one request, one `read` grant in your policy.
When configuration genuinely spans a tree, `FromClientPrefix` walks a prefix recursively, stripping
it and nesting each secret's fields under its remaining path segments:

```
secret/app        = { "name": "checkout" }         name: checkout
secret/app/db     = { "user": "admin" }       →    db:    { user: admin }
secret/app/cache  = { "url": "redis://…" }         cache: { url: "redis://…" }
```

```go
config.WithBackend(configvault.FromClientPrefix(client, "secret", "app"))
```

Prefix mode is opt-in because its cost is real: Vault has no recursive list, so a walk is one
request per directory **plus** one per secret, and it needs the `list` capability on every
directory it descends — which least-privilege policies often withhold. Each watch poll pays that
same cost again.

## A field and a child secret cannot share a name

In Vault a path is **both a secret and a directory** — listing a prefix returns `app` and `app/`
for the same name. In prefix mode that means a secret's own fields merge with its child secrets
into one node, and they can collide:

```
secret/app     = { "db": "postgres://…" }     ← a field named "db"
secret/app/db  = { "host": "db.internal" }    ← a child secret also named "db"
```

Both claim `db`. There is no correct silent answer — either resolution throws away a value you can
see in Vault — so **the load is refused**:

```go
_, err := config.NewStore(ctx,
	config.WithBackend(configvault.FromClientPrefix(client, "secret", "app")))

// errors.Is(err, configvault.ErrFieldCollision)
// configvault: secret field collides with a child secret: "app" holds a field "db" and a child secret "app/db"
```

Fix it in Vault — rename the field, or move the child secret. The error names the path and the
segment so you know which two to look at. This is the same choice
[`config-xml`](xml.md) makes for an attribute colliding with a child element, and the reasoning is
in [How dynamic backends work](../explanation/dynamic-backends.md#ambiguous-structure-is-refused-not-guessed).

**Single-secret mode cannot produce this**, having only one flat map of fields — a further reason
it is the default.

## Writing a Vault-provided key is refused

Every value in Vault is a secret, so this backend declares itself `Sensitive`, and it is
**read-only**. Those two facts combine into a guard worth meeting here rather than in production.

Because the layer is read-only, a write to a key it provides cannot land in Vault — so it would
otherwise fall through to the next writable layer, typically a plain YAML file on disk. The core
**refuses that write**:

```go
_, err := store.Apply(ctx, config.Set("db.password", "rotated"))
// errors.Is(err, config.ErrSensitiveLeak) — refused, and app.yaml is untouched
```

That is the core stopping a secret being written into a plaintext file. If a key needs to be
writable, do not source it from Vault. See [sensitive read-only
backends](../explanation/dynamic-backends.md#sensitive-read-only-backends) for the full reasoning.

## Watching for change

Vault's Go client offers no change feed, so watching is **polling**, at **60 seconds** by default —
deliberately slower than the file watcher, because every poll is an authenticated read that lands
in Vault's audit log.

```go
config.WithBackend(configvault.FromClient(client, "secret", "app",
	configvault.WithPollInterval(5*time.Minute)))
```

A poll compares the secret's KV v2 version, so another client writing it is noticed and reaches
your observers. Vault being briefly unreachable — sealed, restarting, mid-renewal — backs off and
retries rather than ending the watch.

## Two limits worth knowing

**Vault rounds integers above 2^53.** Vault decodes submitted JSON numbers through a float, so an
integer larger than `9007199254740992` is rounded *on write*, before this adapter sees it:

```go
kv.Put(ctx, "app", map[string]any{"id": 9007199254740993})
store.View().Get("id")  // 9007199254740992
```

If you keep large identifiers or nanosecond timestamps in Vault, **store them as strings**, which
round-trip exactly. The adapter converts integers as `int64` rather than through a float, so it
adds no further loss — but it cannot recover what Vault has already discarded.

**KV v2 only.** Vault's default and recommended secrets engine. KV v1 is legacy, has no version
metadata for the watch to compare, and lists through a different path.

Vault Enterprise **namespaces** are set on the client (`client.SetNamespace(…)`), so the adapter is
namespace-agnostic and takes no parameter for it.

## What it costs

| | |
|---|---|
| Modules added | **26** — 17 for the Vault SDK, 9 for the `config` graph |
| Requires | `config` **v0.7.0+** |

A backend adapter carries its system's client, and the Vault SDK is the largest thing here. That
cost is pinned by an allowlist test in the module, so a version bump that widens the graph fails
its build rather than arriving quietly.

## Related

- [How dynamic backends work](../explanation/dynamic-backends.md) — the mechanics every remote backend shares
- [The adapter ecosystem](../explanation/adapters.md) — every adapter, status and roadmap
- [Backends and capabilities](../explanation/backends.md) — the interface split all backends implement
- [Read and write Consul](consul.md) — the reference backend, read + write + native watch

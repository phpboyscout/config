---
title: Read secrets from Vault
description: Read HashiCorp Vault KV v2 secrets as an ordinary config layer, with the leak guard that stops a secret being written to a plain file.
tags: [how-to, backends, vault, secrets]
---

# Read secrets from Vault

[HashiCorp Vault](https://www.vaultproject.io/) KV v2 secrets become an ordinary config layer
through [`config-vault`](https://gitlab.com/phpboyscout/go/config-vault), a sibling module: you take
it and its Vault SDK only if you read from Vault. You authenticate the Vault client and hand it in
with the KV v2 mount and the secret path.

```bash
go get gitlab.com/phpboyscout/go/config-vault
```

```go
import (
	vaultapi "github.com/hashicorp/vault/api"
	"gitlab.com/phpboyscout/go/config"
	configvault "gitlab.com/phpboyscout/go/config-vault"
)

client, err := vaultapi.NewClient(vaultapi.DefaultConfig())
if err != nil {
	return err
}
client.SetToken(token)

store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/app.yaml"),                      // YAML defaults
	config.WithBackend(configvault.FromClient(client, "secret", "app")), // Vault outranks them
)
if err != nil {
	return err
}

host := store.View().GetString("db.host") // from secret/app
```

A Vault layer takes part in precedence, per-key merge, provenance and hot-reload exactly as a file
does.

## Connect to Vault

`FromClient` is the default: you build and **authenticate** the client, and the adapter uses it. It
has no auth method, address or credential anywhere in its API, which is what lets every Vault auth
method work without the adapter knowing any of them.

| Use | When |
|---|---|
| `FromClient(client, mount, path)` | The process outlives its token, uses any auth method other than `VAULT_TOKEN`, or shares a client. |
| `FromConfig(cfg, mount, path)` | You assembled a `*vaultapi.Config` and want the adapter to build the client from it. |
| `Default(mount, path)` | Nothing to configure: `VAULT_ADDR`, `VAULT_TOKEN` and friends in the environment. Fine for a short-lived command. |

Each has a prefix twin (`FromClientPrefix`, `FromConfigPrefix`, `DefaultPrefix`), covered under
[how keys map](#map-secrets-to-keys). Nothing here returns anything to close, and none of the rungs
contacts Vault: `vaultapi.NewClient` does no network I/O, so reaching the server waits for `Load`.
The reasoning behind the ladder is in [who owns the connection](../explanation/connection-ownership.md).

The `FromConfig` and `Default` rungs build a client but do not log in for you. They check what the
SDK makes easy to miss: `vaultapi.DefaultConfig` reports failure by populating the `Error` **field**
on the config it returns rather than returning an error, so a config that looks fine can already be
broken. `Default` inherits
**Vault's own** documented `https://127.0.0.1:8200`, not an address this adapter invented. To share
one Vault client across this adapter, `go/signing` and `go/encryption`, use
[`go/vaultclient`](https://vaultclient.go.phpboyscout.uk) and hand its client to `FromClient`.

### Log in before you hand the client in

Every auth method ends the same way, with an authenticated `*api.Client` going into `FromClient`:

```go
// AppRole, the common service-to-service path.
appRole, _ := approle.NewAppRoleAuth(roleID, &approle.SecretID{FromEnv: "APPROLE_SECRET_ID"})
client.Auth().Login(ctx, appRole)

// Kubernetes, the common in-cluster path.
k8s, _ := kubernetes.NewKubernetesAuth("your-role")
client.Auth().Login(ctx, k8s)

// ...then, either way:
config.WithBackend(configvault.FromClient(client, "secret", "app"))
```

The auth helper packages (`vault/api/auth/approle`, `.../kubernetes`) are separate modules. Add
whichever you use; `config-vault` depends on none of them. Runnable versions are in the module's
[package examples](https://pkg.go.dev/gitlab.com/phpboyscout/go/config-vault#pkg-examples),
compiled by its test suite so they cannot drift from the API.

### Renew the token yourself

Tokens expire and no rung renews them. `vaultapi.NewClient` reads `VAULT_TOKEN` a single time and
holds it for the life of the client, so a backend carries whatever token was set when it was built.
Once the TTL lapses, every read through the backend fails, reloads and watch polls included. That is
Vault rejecting the token, and it surfaces as a load error rather than being swallowed.

A command rarely outlives its token. A long-lived process reloading configuration does, and
`Default` is where it is easiest to meet this without noticing, because you never handle the token.
Build the client yourself, keep a lifetime watcher renewing it, and pass it to `FromClient`:

```go
watcher, _ := client.NewLifetimeWatcher(&vaultapi.LifetimeWatcherInput{Secret: login})
go watcher.Start()
defer watcher.Stop()
```

Vault Agent or your platform's sidecar does the same job.

## Map secrets to keys

`FromClient` reads **one secret**, and its fields become the layer, keeping the structure Vault
stores them with:

```
secret/app = {                          store.View().GetString("db.host") // "db.internal"
  "db": { "host": "db.internal",   →    store.View().GetInt("db.port")    // 5432
          "port": 5432 } }
```

Unlike the byte-valued stores (Consul, the parameter stores), Vault returns **already-structured**
JSON, so nested maps, slices, booleans and nulls survive the round trip. There is no value codec
here and none is needed. Integers come through as `int64` and other numbers as `float64`.

### Read a tree of secrets

When configuration spans a tree, `FromClientPrefix` walks a prefix recursively, strips it, and nests
each secret's fields under its remaining path segments:

```
secret/app        = { "name": "checkout" }         name: checkout
secret/app/db     = { "user": "admin" }       →    db:    { user: admin }
secret/app/cache  = { "url": "redis://…" }         cache: { url: "redis://…" }
```

```go
config.WithBackend(configvault.FromClientPrefix(client, "secret", "app"))
```

Prefix mode is opt-in because its cost is real. Vault has no recursive list, so a walk is one
request per directory **plus** one per secret, and it needs the `list` capability on every
directory it descends, which least-privilege policies often withhold. Each watch poll pays that
cost again.

### A field and a child secret cannot share a name

A Vault path is both a secret and a directory, so in prefix mode a secret's own fields merge with
its child secrets into one node and can collide:

```
secret/app     = { "db": "postgres://…" }     ← a field named "db"
secret/app/db  = { "host": "db.internal" }    ← a child secret also named "db"
```

Either resolution would throw away a value you can see in Vault, so **the load is refused** with
`configvault.ErrFieldCollision`, naming the path and the segment. Rename the field or move the
child secret in Vault. Single-secret mode cannot produce this. The same choice is made by
[`config-xml`](xml.md) for an attribute colliding with a child element, and the reasoning is in
[How dynamic backends work](../explanation/dynamic-backends.md#ambiguous-structure-is-refused-not-guessed).

## Read-only, and where writes go

Vault is never written to. Every value in it is a secret, so the backend declares itself
`Sensitive` and read-only by policy, and those two facts combine into a guard worth meeting here
rather than in production.

A write to a key the Vault layer provides cannot land in Vault, so it would otherwise fall through
to the next writable layer, typically a plain YAML file on disk. The core **refuses that write**:

```go
_, err := store.Apply(ctx, config.Set("db.password", "rotated"))
// errors.Is(err, config.ErrSensitiveLeak): refused, and app.yaml is untouched
```

That is the core stopping a secret being written into a plaintext file. If a key needs to be
writable, do not source it from Vault. See [sensitive read-only
backends](../explanation/dynamic-backends.md#sensitive-read-only-backends) for the full reasoning.

## Watch for changes

Vault's Go client offers no change feed, so watching is **polling**, at **60 seconds** by default.
That is deliberately slower than the file watcher, because every poll is an authenticated read that
lands in Vault's audit log. The interval you pass to `Store.Watch` does not apply to this layer; set
this layer's own with `WithPollInterval`:

```go
config.WithBackend(configvault.FromClient(client, "secret", "app",
	configvault.WithPollInterval(5*time.Minute)))
```

A poll compares the secret's KV v2 version, so another client writing it is noticed and reaches
your observers. Vault being briefly unreachable (sealed, restarting, mid-renewal) backs off for a
second and retries rather than ending the watch. See [hot-reload](hot-reload.md) for the store side.

## Test without Vault

The adapter reaches Vault through a two-method `configvault.KV` interface, so a unit test hands
`New` (or `NewPrefix`) a fake and needs no server:

```go
type fakeKV map[string]*configvault.Secret

func (f fakeKV) Get(_ context.Context, path string) (*configvault.Secret, error) {
	return f[path], nil // nil, nil is "absent"
}

func (fakeKV) List(context.Context, string) ([]string, error) { return nil, nil }

backend := configvault.New(fakeKV{
	"app": {Data: map[string]any{"db": map[string]any{"host": "db.internal"}}, Version: 1},
}, "app")
```

`Wrap(client, mount)` is the real implementation, and what `FromClient` uses. The module's
integration suite, under `test/integration`, runs against a real Vault in a container and is skipped
unless `INT_TEST_INTEGRATION=1` is set; it needs a Docker daemon. It exists because the fake was
modelled on behaviour probed from a real Vault, so only the real one can show the fake is right.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `configvault.ErrFieldCollision` | Prefix mode found a field and a child secret with the same name. | Rename the field or move the child secret in Vault. |
| `config.ErrSensitiveLeak` | An `Apply` set a key the Vault layer provides, and the next writable layer is plainer. | Do not write Vault-sourced keys; change them in Vault. |
| `configvault.ErrNoConfig` | `FromConfig` or `FromConfigPrefix` got a nil config. | Pass a config, or use `Default`. |
| `configvault.ErrNoAddress` | The config names no Vault address. | Set `Address` on the config, or `VAULT_ADDR`. |
| `fs.ErrNotExist` | The secret (or the whole prefix) is absent, soft-deleted, destroyed or empty. | Check the mount and path. The store tolerates it as an empty layer unless it is the first source and you set `RequireFirstSource`. |
| A permission error from Vault (wrapped, no sentinel) | The token expired or the policy lacks `read` (or `list` in prefix mode). | [Renew the token](#renew-the-token-yourself), or widen the policy. |

## Limits

- **Vault rounds integers above 2^53.** Vault decodes submitted JSON numbers through a float, so
  `9007199254740993` is stored as `9007199254740992` before this adapter sees it. Store large
  identifiers and nanosecond timestamps as strings. The adapter converts integers as `int64`, so it
  adds no loss of its own.
- **KV v2 only.** KV v1 is legacy, has no version metadata for the watch to compare, and lists
  through a different path.
- **Namespaces** (Vault Enterprise) are set on the client with `client.SetNamespace(…)`; the adapter
  takes no parameter for them.

## When you don't need this

If your platform already delivers the secret, as an injected environment variable or a file mounted
by an agent or sidecar, then `WithEnv`, [`WithFiles`](load-and-merge.md) or
[`config-filekv`](filekv.md) read it with no Vault SDK and no token to manage.

Reach for `config-vault` when your process should authenticate and fetch for itself: short-lived
credentials, values that rotate under a running process, or an audit trail that has to name your
application rather than the agent.

## What it costs

| | |
|---|---|
| Adds | the Vault SDK, on top of the `config` graph |
| Requires | the `config` version named in this module's `go.mod`; `go get` brings it |
| Capability since | `config` **v0.7.0**, the release whose `backendconformance` requires a sensitive read-only backend to refuse the routed-beneath write |

A backend adapter carries its system's client, and the Vault SDK is the largest thing here. That
cost is pinned by an allowlist test in the module, so a version bump that widens the graph fails
its build rather than arriving quietly.

## Related

- [How dynamic backends work](../explanation/dynamic-backends.md): the mechanics every remote backend shares
- [The adapter ecosystem](../explanation/adapters.md): every adapter, status and roadmap
- [Backends and capabilities](../explanation/backends.md): the interface split all backends implement
- [Read and write Consul](consul.md): the reference backend, with read, write and native watch

---
title: Keep tokens in the OS keychain
description: Make the operating system keychain a config layer, so tokens are written there rather than into a plaintext config file.
tags: [how-to, backends, keychain, secrets]
---

# Keep tokens in the OS keychain

A tool that obtains an OAuth token has to put it somewhere. Without a layer that can hold one, the
somewhere is the config file, which is how a refresh token ends up on disk in the clear, next to
the port number. [`config-keychain`](https://gitlab.com/phpboyscout/go/config-keychain) makes the
operating system's keychain (macOS Keychain, Windows Credential Manager, or a Secret Service
implementation on Linux) a layer, so tokens have somewhere better to go and the core's own rules
keep them there.

```bash
go get gitlab.com/phpboyscout/go/config-keychain
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	configkeychain "gitlab.com/phpboyscout/go/config-keychain"
	_ "gitlab.com/phpboyscout/go/credentials/keychain" // activates the OS keychain
)

store, err := config.NewStore(ctx,
	config.WithEnv("MYAPP"),                        // overrides
	config.WithFiles(config.OS(), "config.yaml"),   // ordinary settings
	config.WithBackend(configkeychain.New(          // secrets, on top
		configkeychain.Registered(), "myapp", map[string]string{
			"platforms.instagram.access_token": "instagram-access-token",
			"platforms.tiktok.refresh_token":   "tiktok-refresh-token",
		})),
)
if err != nil {
	return err
}

token := store.View().GetString("platforms.instagram.access_token") // from the keychain
```

## Connect to the keychain

The keychain is reached through `credentials.Backend`, the *interface*, so the adapter is handed one
rather than finding one:

| Use | When |
|---|---|
| `New(configkeychain.Registered(), service, keys)` | Production: whatever keychain you activated by blank-importing `credentials/keychain`. |
| `New(backend, service, keys)` | Any other `credentials.Backend`: a fake in tests, or one of your own. |

Nothing here needs closing: the keychain is reached call by call, and the backend holds no
connection.

### `go-keyring` stays out of your binary

The adapter never imports the `go-keyring` implementation, so it does not pull `go-keyring` or
`godbus` into your dependency graph. A test asserts they are absent and fails if that changes.

You activate a real keychain by blank-importing `credentials/keychain` yourself. A build that must
not carry session-bus or keychain IPC code simply does not, and the linker drops it.
`Registered()` then resolves to whatever you activated.

### It will not hang your application

A locked keychain blocks on an unlock prompt. On a headless host nobody can answer it, and a
configuration layer that waits forever is worse than one that fails.

Each call is therefore bounded. `DefaultTimeout` is 10 seconds and `WithTimeout` changes it, and
the adapter imposes that bound **itself** rather than trusting the injected backend to honour a
context. That distinction is not theoretical: `credentials` discarded its context until v0.2.2, and
a context bounds a call only if the callee consults it.

```go
configkeychain.New(backend, "myapp", keys,
	configkeychain.WithTimeout(30*time.Second)) // a desktop user may be typing a passphrase
```

!!! warning "An unavailable keychain fails the load"

    `Load` returns `ErrKeychainUnavailable` rather than quietly contributing nothing.

    That is deliberate. Contributing nothing is indistinguishable from "no tokens stored yet",
    which is an ordinary state, and degrading in silence is exactly what put tokens in plaintext
    files to begin with.

    If you would rather carry on without one, check `Available()` and omit the layer. That is a decision
    written in your code, rather than one nobody made.

## Map tokens to keys

Every platform keychain offers get, set and delete, and nothing that enumerates. So unlike every
other backend in this toolkit, this one cannot discover its own key space, and the mapping is
declared:

```
config path                        ->  keychain account
platforms.instagram.access_token   ->  instagram-access-token
```

Both sides are explicit rather than derived. A naming convention would compute account names your
existing entries do not have, quietly orphaning every credential already stored. Every account
lives under the one service name you pass to `New`.

A declared key the keychain does not hold contributes nothing, and that is not an error. It is the
state before a token has been obtained. A key you did **not** declare is ordinary configuration and
routes to the file as before. The mapping bounds what this backend owns.

## Write back to the keychain

[Vault](vault.md), [AWS Secrets Manager](aws-secrets.md), [Azure Key Vault](azure-keyvault.md) and
[GCP Secret Manager](gcp-secret.md) are all read-only, because secrets there are provisioned by a
separate, audited process and a config library writing to them would be a surprising power to hand
it. A local keychain is not that. It holds a token *this* application just obtained, on the user's
own machine, and refusing to write it would leave you doing what this module exists to prevent.

```go
store.Apply(ctx, config.Set("platforms.instagram.access_token", token))
```

**A first token goes into the keychain**, even when it holds nothing yet, because the keychain is
the highest-precedence *writable* layer and [a new key lands in the highest-precedence writable
target](../explanation/backends.md). No pinning, no branching on whether a keychain exists.

Two consequences follow from what a keychain is:

- **Writes are not atomic.** Setting three tokens is three keychain operations; a failure part-way
  leaves the earlier ones stored, and rollback undoes them best-effort.
- **Conflict detection compares values, not versions**, because a keychain has neither a version
  nor a modification time. It catches another process having changed an entry, which is the case
  worth catching, but cannot tell "changed and changed back", and there is a small window between the
  check and the write.

A token is a string. A value with no string form, such as a map, is refused with
`ErrUnwritableValue`.

## Watch for changes

The keychain has no change feed, and this backend does not offer a watch, so `Store.Watch` passes
it over. A token another process changes is read at the next `Reload`, and an `Apply` that meets
it is refused as a conflict rather than overwriting it.

## Keep secrets out of plainer layers

**A token already in the keychain can never be written to the file beneath.** The layer is
`Sensitive`, and the core refuses a write whose target is not, so what used to fall back to disk
now returns [`ErrSensitiveLeak`](../explanation/dynamic-backends.md#sensitive-read-only-backends).
Neither this nor the first-token routing above needs a special case: both fall out of routing the
core already does, and both are the whole point.

## Test without a keychain

`credentials/test` ships `MemoryBackend`, an in-memory `credentials.Backend` that never touches a
session bus or platform API. Hand it to `New` in place of `Registered()`:

```go
import credtest "gitlab.com/phpboyscout/go/credentials/test"

keys := map[string]string{"platforms.instagram.access_token": "instagram-access-token"}

backend := &credtest.MemoryBackend{}
_ = backend.Store(ctx, "myapp", "instagram-access-token", "t0k3n")

store, err := config.NewStore(ctx,
	config.WithBackend(configkeychain.New(backend, "myapp", keys)))
```

`credtest.Install(t)` instead registers a fresh `MemoryBackend` process-wide for one test, so code
that calls `Registered()` sees it. It mutates global state, so a test that uses it must not run in
parallel. To test the unavailable path, hand `New` a backend whose `Available()` reports false.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `configkeychain.ErrKeychainUnavailable` | No keychain is activated, or the platform keychain cannot serve requests. | Blank-import `credentials/keychain`, run in a desktop session, or check `Available()` and omit the layer. |
| `config.ErrSensitiveLeak` | An `Apply` would put a keychain-held token into a plainer layer. | Write the token to the keychain; that is the guard working. |
| `config.ErrConflict` | Another process changed the entry since it was read. | Reload and apply the change again. |
| `configkeychain.ErrUndeclaredKey` | A write targeted this backend at a path it does not declare. | Declare the path in the `keys` map, or write it elsewhere. |
| `configkeychain.ErrUnwritableValue` | The value has no string form a keychain entry can hold. | Write a string. |
| `context.DeadlineExceeded` (wrapped) | A keychain call outlasted the timeout, typically waiting on an unlock prompt. | Unlock the keychain, or raise `WithTimeout`. |

## Replace a hand-rolled resolver

If you already walk *environment, then keychain, then config file* per credential, as
[keryx](https://gitlab.com/phpboyscout/keryx) does with `oauth.Store` and
[go/forge](https://gitlab.com/phpboyscout/go/forge) does in `ResolveToken`, that chain is a
per-credential reimplementation of what a layer already is.

The equivalent is a layer order rather than a struct:

```go
config.WithEnv("MYAPP"),                                     // was EnvVar
config.WithFiles(fsys, "config.yaml"),                       // was ConfigKey
config.WithBackend(configkeychain.New(                       // was KeychainService/Account
	configkeychain.Registered(), "myapp", keys)),
```

Reads resolve in the same order, and `Explain` can now say which layer answered.

The write is where the two ladders differ, and it is worth checking which one you have. keryx's
`Save` falls back to writing the token into the config file when no keychain is available; that
fallback disappears, because the core refuses it. A ladder that already treats a failed keychain
write as an error, as go-tool-base's setup wizard does, loses nothing here.

One thing a layer does not reproduce for free: a ladder can be **lazy**, resolving a credential
only when something asks for it, whereas a layer loads with the store. If a code path deliberately
avoids touching the keychain (SSH-authenticated Git is the usual case), check that before
replacing it.

## When you don't need this

The OS keychain needs a **logged-in desktop session**. On a headless server, in a
container or in CI there is nothing to unlock, and this adapter is the wrong tool
however convenient it looks on a laptop. Use the environment, a mounted file or a
secrets manager there.

Reach for `config-keychain` in a CLI a human runs on their own machine, where the
alternative is a token sitting in a dotfile.

## What it costs

| | |
|---|---|
| Adds | `credentials`, on top of the `config` graph |
| Requires | the `config` version named in this module's `go.mod`; `go get` brings it |
| Capability since | `config` **v0.10.0**, the release adding `BoundedKeySpace`, which a declared-key backend needs; and `credentials` **v0.2.2**, the release in which the keychain backend began honouring its context |

Both floors are hard rather than preferences. Against an earlier `credentials` a locked keyring
blocks with nothing able to recover it, and against an earlier `config` the conformance suite
demands an invented account name this backend has no way to honour.

## Related

- [How dynamic backends work](../explanation/dynamic-backends.md): the mechanics every remote backend shares
- [Read secrets from Vault](vault.md): the read-only secrets managers, for contrast
- [The adapter ecosystem](../explanation/adapters.md): every adapter, status and roadmap

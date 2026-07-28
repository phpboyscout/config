---
title: Keep tokens in the OS keychain
description: Make the operating system keychain a config layer, so tokens are written there rather than into a plaintext config file.
tags: [how-to, backends, keychain, secrets]
---

# Keep tokens in the OS keychain

A tool that obtains an OAuth token has to put it somewhere. Without a layer that can hold one, the
somewhere is the config file — which is how a refresh token ends up on disk in the clear, next to
the port number.

[`config-keychain`](https://gitlab.com/phpboyscout/go/config-keychain) makes the operating system's
keychain — macOS Keychain, Windows Credential Manager, or a Secret Service implementation on Linux
— a layer, so tokens have somewhere better to go and the core's own rules keep them there.

```bash
go get gitlab.com/phpboyscout/go/config-keychain
```

!!! info "Not yet tagged"

    This adapter is built and its conformance suite passes; the first release follows shortly.
    `go get` resolves it to a pseudo-version from `main` in the meantime.

```go
import (
	"gitlab.com/phpboyscout/go/config"
	_ "gitlab.com/phpboyscout/go/credentials/keychain" // activates the OS keychain
	configkeychain "gitlab.com/phpboyscout/go/config-keychain"
)

store, err := config.NewStore(ctx,
	config.WithEnv("MYAPP"),                                // overrides
	config.WithFiles(fsys, "config.yaml"),                  // ordinary settings
	config.WithBackend(configkeychain.New(                  // secrets, on top
		configkeychain.Registered(), "myapp", map[string]string{
			"platforms.instagram.access_token": "instagram-access-token",
			"platforms.tiktok.refresh_token":   "tiktok-refresh-token",
		})),
)
```

## What the layer ordering buys you

Two behaviours fall out of routing the core already does. Neither needs a special case, and both
are worth understanding because they are the whole point:

```go
store.Apply(ctx, config.Set("platforms.instagram.access_token", token))
```

- **A first token goes into the keychain**, even when it holds nothing yet, because the keychain is
  the highest-precedence *writable* layer and [a new key lands in the highest-precedence writable
  target](../explanation/backends.md). No pinning, no branching on whether a keychain exists.
- **A token already in the keychain can never be written to the file beneath.** The core refuses a
  write whose target is not sensitive, so what used to fall back to disk now returns
  [`ErrSensitiveLeak`](../explanation/dynamic-backends.md#sensitive-read-only-backends).

A key you did **not** declare is ordinary configuration and routes to the file as before. The
mapping bounds what this backend owns.

## The mapping is declared, because a keychain cannot be listed

Every platform keychain offers get, set and delete — and nothing that enumerates. So unlike every
other backend in this toolkit, this one cannot discover its own key space:

```
config path                        →  keychain account
platforms.instagram.access_token   →  instagram-access-token
```

Both sides are explicit rather than derived. A naming convention would compute account names your
existing entries do not have, quietly orphaning every credential already stored.

A declared key the keychain does not hold contributes nothing, and that is not an error — it is the
state before a token has been obtained.

## It will not hang your application

A locked keychain blocks on an unlock prompt. On a headless host nobody can answer it, and a
configuration layer that waits forever is worse than one that fails.

Each call is therefore bounded — `DefaultTimeout` is 10 seconds, `WithTimeout` to change it — and
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
    which is an ordinary state — and degrading in silence is exactly what put tokens in plaintext
    files to begin with.

    If you would rather carry on without one, check `Available()` and omit the layer: a decision
    written in your code, rather than one nobody made.

## Writable, unlike every other secrets backend here

[Vault](vault.md), [AWS Secrets Manager](aws-secrets.md), [Azure Key Vault](azure-keyvault.md) and
[GCP Secret Manager](gcp-secret.md) are all read-only, because secrets there are provisioned by a
separate, audited process and a config library writing to them would be a surprising power to hand
it.

A local keychain is not that. It holds a token *this* application just obtained, on the user's own
machine, and refusing to write it would leave you doing what this module exists to prevent.

Two consequences follow:

- **Writes are not atomic.** Setting three tokens is three keychain operations; a failure part-way
  leaves the earlier ones stored, and rollback undoes them best-effort.
- **Conflict detection compares values, not versions**, because a keychain has neither a version
  nor a modification time. It catches another process having changed an entry — the case worth
  catching — but cannot tell "changed and changed back", and there is a small window between the
  check and the write.

## `go-keyring` stays out of your binary

The keychain is reached through `credentials.Backend`, the *interface*. The adapter never imports
the `go-keyring` implementation, so it does not pull `go-keyring` or `godbus` into your dependency
graph — a test asserts they are absent and fails if that changes.

You activate a real keychain by blank-importing `credentials/keychain` yourself. A build that must
not carry session-bus or keychain IPC code simply does not, and the linker drops it.
`Registered()` then resolves to whatever you activated.

## Replacing a hand-rolled resolver

If you already walk *environment → keychain → config file* per credential — as
[keryx](https://gitlab.com/phpboyscout/keryx) does with `oauth.Store`, and `go-tool-base` does in
`forge.ResolveToken` — that chain is a per-credential reimplementation of what a layer already is.

The equivalent is a layer order rather than a struct:

```go
config.WithEnv("MYAPP"),                                     // was EnvVar
config.WithFiles(fsys, "config.yaml"),                       // was ConfigKey
config.WithBackend(configkeychain.New(                       // was KeychainService/Account
	configkeychain.Registered(), "myapp", keys)),
```

Reads resolve in the same order, and `Explain` can now say which layer answered. The difference is
the write: the fallback that put a token in the config file when no keychain was available is gone
— the write either reaches the keychain or is refused.

## What it costs

| | |
|---|---|
| Modules added | **21** — 9 for the `config` graph, 12 for `credentials` |
| Requires | `config` **v0.10.0+** — the release adding `BoundedKeySpace`, which a declared-key backend needs — and `credentials` **v0.2.2+**, the release in which the keychain backend began honouring its context |

Both floors are hard rather than preferences. Against an earlier `credentials` a locked keyring
blocks with nothing able to recover it, and against an earlier `config` the conformance suite
demands an invented account name this backend has no way to honour.

## Related

- [How dynamic backends work](../explanation/dynamic-backends.md) — the mechanics every remote backend shares
- [Read secrets from Vault](vault.md) — the read-only secrets managers, for contrast
- [The adapter ecosystem](../explanation/adapters.md) — every adapter, status and roadmap

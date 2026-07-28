---
title: Bound what a backend contributes
description: Use allow and deny lists to narrow a backend's key space, without narrowing the backend itself.
tags: [how-to, backends, filtering, secrets]
---

# Bound what a backend contributes

A backend contributes everything it can see. That is right for a config file you own,
and wrong when the credential is broader than the application — a Consul prefix or an
SSM path the token can read entirely, where this service should only be reading its own
subtree.

`config.Filtered` wraps any backend and bounds its key space:

```go
config.NewStore(ctx,
    config.WithFiles(fsys, "config.yaml"),
    config.WithBackend(config.Filtered(vault,
        config.Allow("db.**", "cache.**"),
        config.Deny("db.password"))),
)
```

It is a decorator over the `Backend` interface, so the same rules apply to a secrets
manager, a parameter store, a file, or a nested store. There is nothing per-backend to
learn.

## What the patterns mean

Patterns match the **dotted path a key resolves at**, not the backend's native
addressing — you do not need to know whether a store nests by `/`, by underscore, or not
at all.

| Pattern | Matches | Does not match |
|---|---|---|
| `db.host` | `db.host` | `db`, `db.host.name` |
| `db.*` | `db.host` | `db.primary.host`, `db` |
| `db.**` | `db.host`, `db.primary.host` | `db`, `cache.host` |
| `**` | everything | — |

The segment boundary is the whole design. `*` stops at one; `**` crosses any number.
Deliberately not regular expressions: a regexp invites patterns that cross boundaries in
ways that are hard to reason about against a tree, and a mistake in one silently
**widens** a filter rather than narrowing it — the wrong direction when what it is
holding back is a secret.

## Allow, deny, and which wins

- **Neither** — everything passes.
- **`Allow` only** — a whitelist.
- **`Deny` only** — a blacklist.
- **Both** — allow, then deny. **Deny wins.**

Deny winning is the safe reading: a caller who has written both has expressed a
narrowing intention twice, and resolving the overlap the other way would let an `Allow`
re-expose something explicitly denied.

## A denied key is invisible, not an error

The layer simply does not carry it. Reads fall through to whatever lower layer defines
it, and if nothing does, the key is absent exactly as though the backend never held it.

Writes follow from that without a special case: routing skips a backend that defines
nothing for a key, so a write to a denied key lands in the next writable layer down —
which is what *"this backend does not own this key"* should mean.

!!! warning "This is a visibility bound, not a security control"

    Filtering happens **after** the backend has fetched and parsed. A denied key is
    still read from the remote system, still crosses the network, and still sits in
    memory briefly.

    If your goal is that the process never receives a value, narrow the **credential**
    or the backend's own configuration. `Filtered` bounds what your configuration
    *exposes*, not what your client *fetches*.

## Filtering a secrets backend

A denied key on a `Sensitive` backend keeps its sensitive marking even though it is
hidden. So this still fails:

```go
store.Apply(ctx, config.Set("db.password", "new"))
// → ErrSensitiveLeak
```

That asymmetry is deliberate, and it is the one place `Filtered` behaves differently
depending on what it wraps.

The leak guard works by asking which source *defines* a key. A filtered-out key defines
nothing — so without this, denying `db.password` on Vault would make a write of it route
into the plain file beneath and **not** be refused. The filter would have quietly
re-opened the hole [`ErrSensitiveLeak`](../explanation/backends.md) exists to close, and
the failure would be a secret on disk with no error at all.

Two consequences worth knowing:

- **A denied secret becomes unwritable anywhere**, rather than routing down. That is the
  intended outcome: *"do not expose this secret"* should not mean *"so write it
  somewhere worse"*.
- **Only paths the backend actually holds are guarded.** The withheld set is recorded
  from real data rather than derived from the rules, so denying a key the backend does
  not have refuses nothing.

## Capabilities pass through

A filtered writable backend is still writable; a filtered watchable one still watchable.
The wrapper claims exactly what the backend underneath it claims — no more, so the store
never takes a path the backend cannot honour, and no less, so a capability you
configured is not silently dropped.

`ID()` and `Capabilities()` forward unchanged, which matters more than it looks:

- The write path finds a backend by matching a layer's `Source.Name` against `ID()`, so
  a decorator that renamed itself would contribute layers no write could ever reach.
- The store reads `Capabilities().Sensitive` directly, so a decorator returning empty
  capabilities would strip the flag from a secrets backend entirely.

Both are asserted by tests that fail if the forwarding is removed.

## Related

- [Backends and capabilities](../explanation/backends.md) — what a backend declares, and what the store does with it
- [Write configuration](write-config.md) — where a write lands, and how to name a layer yourself
- [Keep tokens in the OS keychain](keychain.md) — a sensitive backend worth bounding

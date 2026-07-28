---
title: Backend key filtering — allow and deny lists over any backend
date: 2026-07-28
author: matt.cockayne
status: draft
---

# Backend key filtering

A decorator that bounds which keys a backend contributes, and which it will
accept a write for.

Second of three related specs. It is deliberately **not** part of [store
aggregation](2026-07-28-store-aggregation.md), though that is where the need
surfaced — see D1. It uses the target surface from [write-target
options](2026-07-28-write-target-options.md).

## Problem

A backend contributes everything it can see. That is right for a config file,
and wrong in at least three situations:

- **A shared store, partially exposed.** A parent store aggregating a shared
  configuration source wants a subtree of it, not all of it.
- **A broad credential.** A Consul prefix or an SSM path that the token can read
  entirely, where this application should only be reading its own subtree —
  and reading more is both noise in `Explain` and a larger blast radius.
- **A backend that must not be written to for certain keys**, while remaining
  writable for the rest. There is no way to express that today: `WritableBackend`
  is all-or-nothing.

The workaround is to configure the backend more narrowly — a longer prefix, a
tighter path. That works when the shape happens to be a prefix and fails
otherwise, and it pushes a policy decision into each adapter's own
configuration, differently spelled every time.

## Decisions

### D1 — A decorator over `Backend`, not a feature of aggregation

Filtering is orthogonal to nesting. Written as a decorator it applies to Vault,
Consul, a file, an environment layer and a nested store alike:

```go
config.WithBackend(config.Filtered(vault, config.Allow("db.*", "cache.*")))
```

Bundling it into aggregation would have produced a filter that only works on
nested stores, and then a second mechanism the first time someone wants to bound
a Consul prefix. One concept, one implementation.

This is why the original single request was split. The two features share
nothing but the motivation.

### D2 — The decorator delegates `ID()` and `Capabilities()` unchanged

Non-negotiable, and the subtlest thing in this spec.

The write path finds a backend by matching a layer's `Source.Name` against
`Backend.ID()` (`store.go:744`, whose error message says so explicitly). A
decorator that changed `ID()` while passing the inner backend's layers through
would produce layers no write could ever be routed back to, failing at apply with
`ErrInternal`.

`Capabilities()` matters for a different reason. `Store.sensitiveSources` reads
`bl.backend.Capabilities().Sensitive` (`store.go:997`). A decorator that returned
a zero `Capabilities` would **silently strip the sensitive flag from Vault**,
and the leak guard would stop firing for every key that backend owns. The
failure mode is a secret written into a plain file, with no error.

So the decorator forwards both, and a test asserts it for a sensitive inner
backend specifically.

### D3 — Filtering applies to keys, on the resolved dotted path

Patterns match the dotted path a key resolves at — `db.password`,
`platforms.*.access_token` — not the backend's native addressing. A caller
should not have to know whether a backend nests by `/`, by underscore or not at
all.

Consequence worth stating: filtering happens **after** the backend has fetched
and parsed. It is a visibility bound, not a fetch optimisation. A denied key is
still read from the remote system, still crosses the network, and still sits in
memory briefly. **Anyone reaching for this as a security control needs to know
that**, and the documentation must say it in those words.

### D4 — Glob syntax, single-segment `*` and terminal `**`

`db.*` matches `db.host`, not `db.primary.host`. `db.**` matches both. This is
the least surprising reading of a dotted path and matches how the module's own
documentation writes key patterns.

Regular expressions were rejected: they invite patterns that cross segment
boundaries in ways that are hard to reason about against a tree, and a mistake
in one silently widens rather than narrows.

### D5 — Allow and deny compose, deny wins

```go
config.Filtered(b, config.Allow("db.**"), config.Deny("db.password"))
```

Allow-then-deny, deny decisive. With neither, everything passes. With allow
only, it is a whitelist. With deny only, a blacklist.

Deny winning is the safe default: a caller who has written both has expressed a
narrowing intention twice, and resolving the overlap the other way would let an
`Allow` re-expose something explicitly denied.

### D6 — A denied key is invisible, not an error

The layer simply does not carry it. Reads fall through to whatever lower layer
defines it; if nothing does, the key is absent exactly as if the backend never
held it.

The alternative — an error on reading a denied key — was rejected because it
makes the filter a runtime hazard rather than a scoping tool, and because
`config`'s read path has no error channel per key. `View.GetString` returns a
string.

### D7 — A write to a denied key routes *past* the backend, it does not fail

A filter that made the backend visible-but-unwritable for a key would break the
router's model: `findTarget` walks writable targets to find one that defines the
path, and a backend that defines nothing for a denied key is correctly skipped.
Since D6 makes the key invisible, this falls out for free — the routing needs no
change at all.

The consequence is that a denied write lands in the next writable layer down,
which is what "this backend does not own this key" should mean.

### D8 — Filtering a sensitive backend can disarm the leak guard, and this is the one real hazard

If Vault is the only sensitive source defining `db.password`, and a filter denies
`db.password`, then `sensitiveDefiner` no longer sees it — so a write of
`db.password` routes to the plain file beneath **and is not refused**. The filter
has re-opened the hole `ErrSensitiveLeak` exists to close.

This is not hypothetical and it is not fixable by making the filter cleverer: a
filtered-out key is, by construction, one the store does not know is sensitive.

Options, none yet chosen — **open question O1**:

1. **Refuse the combination.** `Filtered` over a backend whose `Capabilities()`
   report `Sensitive: true` returns an error unless the caller passes an
   explicit acknowledgement option. Loud, and possibly too loud.
2. **Deny-only for sensitive backends.** A `Deny` on a sensitive backend removes
   the key from *reads* but keeps it in the sensitive set, so the guard still
   fires. Preserves safety; means "denied" means two different things depending
   on the backend, which is exactly the kind of split the rest of this module
   avoids.
3. **Document it and move on.** Cheapest, and consistent with the module's habit
   of stating limits plainly rather than preventing every misuse — but this
   particular limit produces a silent secret leak, which is the failure class the
   guard was added for.

Leaning towards (2), because it makes the safe thing automatic and the cost is
a documented asymmetry rather than a silent hole. Wants a decision before
implementation.

### D9 — Watchability and writability pass through

`Filtered` returns a value implementing exactly the optional interfaces the inner
backend implements — a filtered writable backend is still writable, a filtered
watchable one still watchable. This is the `config-afero` capability-honesty
lesson: claim what the underlying thing has, no more and no less.

Mechanically this needs the four-way type-switch construction the filesystem
adapters already use, since Go cannot express "same interfaces as that value".
Unpleasant but well-precedented.

## Public API

```go
type FilterOption interface{ ... }   // sealed; Allow and Deny are the implementations

func Allow(patterns ...string) FilterOption
func Deny(patterns ...string) FilterOption

func Filtered(b Backend, opts ...FilterOption) Backend
```

`Filtered` with no options returns the backend unchanged rather than a
pass-through wrapper, so the zero configuration costs nothing.

## Testing strategy

- Pattern matching, table-driven: `*` stopping at a segment boundary, `**`
  crossing it, deny beating allow, neither meaning everything.
- **`ID()` and `Capabilities()` forward.** Asserted against a sensitive inner
  backend, and watched to fail by returning a zero `Capabilities` — this is the
  D2 hazard and a passing test that cannot fail is worse than none.
- A denied key is absent from the layer and resolves from the layer beneath.
- A write to a denied key routes past the filtered backend (D7), asserted
  through `Plan` so the target is visible rather than inferred.
- The D8 case, asserted **whichever way O1 resolves** — either the combination is
  refused, or the guard still fires, or the leak is a documented and *tested*
  behaviour. It must not be untested in any of the three outcomes.
- `backendconformance` run against a filtered backend whose filter allows the
  suite's key, confirming decoration does not break the contract.

## Migration & compatibility

Additive. New exported functions, no change to existing behaviour, no change to
routing. A minor version.

## Open questions

- **O1 — How does filtering interact with `Sensitive`?** The three options in
  D8. This is the only question that blocks implementation; the rest of the
  spec stands whichever way it goes.
- **O2 — Does a filter belong in provenance?** `Explain` currently names the
  source a value came from. It cannot currently say "and this backend also holds
  `db.password`, which is filtered out" — and arguably should not, since that
  leaks the existence of a key the filter was told to hide. Leaning: no, and say
  so in the explanation page.
- **O3 — Should `Allow` accept an exact-path fast path?** Most uses will be a
  list of literal paths with no wildcard, and a map lookup beats a glob walk per
  key per load. Premature until there is a backend big enough to notice.

---
title: Backend key filtering — allow and deny lists over any backend
date: 2026-07-28
author: matt.cockayne
status: implemented
approved: 2026-07-28
implemented: 2026-07-28
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

### D8 — Filtering a sensitive backend hides the key from reads but keeps its sensitive marking

The hazard first, because the decision only makes sense against it.

If Vault is the only sensitive source defining `db.password`, and a filter denies
`db.password`, then a naive implementation removes the key from the layer —
`sensitiveDefiner` no longer sees it, a write of `db.password` routes to the
plain file beneath, and **it is not refused**. The filter has re-opened the hole
`ErrSensitiveLeak` exists to close, silently.

This is not fixable by making the filter cleverer. A filtered-out key is, by
construction, one the store does not otherwise know is sensitive.

So filtering a **sensitive** backend is asymmetric, deliberately: a denied key is
removed from the layer's values, but the backend's sensitive key set still
carries it, and the leak guard still fires on a write. The key is invisible to
reads and to routing; it is not invisible to the guard.

Consequences worth naming:

- **"Denied" means slightly different things on a sensitive and a non-sensitive
  backend.** That asymmetry is the price. It is worth paying because the failure
  it prevents is a secret written to disk with no error, and because the safe
  behaviour is the automatic one — a caller cannot forget to ask for it.
- A denied key on a sensitive backend therefore becomes **unwritable anywhere**
  rather than routing down: the guard refuses the plain target, and the backend
  itself no longer offers the key. That is the correct outcome — "this store
  should not expose that secret" should not mean "so write it somewhere worse".
- This requires the sensitive key set to be tracked separately from layer values,
  which the store does not do today. `sensitiveSources` currently marks whole
  *sources*, not keys. **This is the one piece of core change the spec needs**,
  and it is small: the filter must be able to say "I am sensitive, and I also own
  these hidden paths".

The two rejected options are recorded in [Rejected alternatives](#rejected-alternatives).

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
- **The D8 case.** A `Deny` on a sensitive backend hides the key from reads
  *and* leaves the leak guard firing on a write of it. Watched to fail by
  dropping the hidden path from the sensitive set — the resulting silent leak is
  the exact failure this decision exists to prevent, so the test must be able to
  catch it.
- `backendconformance` run against a filtered backend whose filter allows the
  suite's key, confirming decoration does not break the contract.

## Rejected alternatives

**Refuse `Filtered` over a sensitive backend** unless the caller passes an
acknowledgement option. Rejected as too loud: narrowing what a broadly-scoped
Vault token exposes is a legitimate and desirable thing to do, and a design that
makes the good practice require an opt-out teaches people to reach for the
opt-out.

**Document the hazard and let the caller own it.** Rejected despite being
consistent with this module's habit of stating limits rather than preventing
misuse. The limits stated elsewhere produce visible failures — a refused write,
an error at load. This one produces a secret on disk with no error at all, which
is the failure class the guard was added for, and a documented footgun is still
a footgun.

## Resolved

**2026-07-28 — O1, how filtering interacts with `Sensitive`.** Deny hides the
key from reads but keeps its sensitive marking, so the guard still fires (D8).
Chosen because it makes the safe behaviour the automatic one; the cost is that
"denied" is asymmetric between sensitive and non-sensitive backends, and a small
piece of core change to track hidden sensitive paths per backend rather than per
source.

## Migration & compatibility

Additive for consumers — new exported functions, no change to existing
behaviour or routing. Internally it needs the sensitive set to be expressible
per key rather than only per source (D8), which is a core change but not a
breaking one. A minor version.

## Open questions

- **O2 — Does a filter belong in provenance?** `Explain` currently names the
  source a value came from. It cannot currently say "and this backend also holds
  `db.password`, which is filtered out" — and arguably should not, since that
  leaks the existence of a key the filter was told to hide. Leaning: no, and say
  so in the explanation page.
- **O3 — Should `Allow` accept an exact-path fast path?** Most uses will be a
  list of literal paths with no wildcard, and a map lookup beats a glob walk per
  key per load. Premature until there is a backend big enough to notice.

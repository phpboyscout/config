---
title: Write-target options — naming a layer at the point of the change
date: 2026-07-28
author: matt.cockayne
status: draft
---

# Write-target options

Deliberate shadowing — writing a key into a layer other than the one routing
would pick — is already supported. This spec is about making it reachable.

It is the first of three related specs, and the least independent of them:
[store aggregation](2026-07-28-store-aggregation.md) needs this surface to
express **promotion** — moving a setting from a project config up into a user's
global one — which is the concrete need the whole group came from. [Backend key
filtering](2026-07-28-backend-key-filtering.md) also changes what "a layer" can
be. Settling the surface here first means neither has to invent one.

## Problem

The mechanism exists. `Change.Target *Source` overrides routing, `routeOne`
branches on it (`plan.go:202`), and `matchTarget` resolves it by name against
the writable sources that actually exist (`plan.go:257`). `Operation.ShadowedBy`
then reports the shadowing the caller has just created, so `Plan` describes the
consequence before anything is written. The behaviour is right.

The **surface** is not. From [how to write
configuration](../../how-to/write-config.md):

```go
target := config.Source{Name: "/etc/app/base.yaml"}

change := config.Set("server.port", 9090)
change.Target = &target

plan, err := store.Plan(change)
```

Three statements and a pointer to express one intention. Worse, they interact
badly with the batch that `Apply` is built around: `Set` and `Remove` return
values, so a caller pinning two of five changes cannot write them as a list, and
has to break the batch apart into named variables purely to reach a field.

Two smaller problems compound it:

- **`Source` is a five-field struct of which two are read.** `matchTarget`
  compares `Name` and `Document`; `Kind`, `Writable` and the rest are ignored. A
  caller cannot tell that from the type, so the documentation has to say so in a
  comment above the example — and a comment is the weakest possible place to put
  an invariant.
- **There is no way to ask a store what its targets are.** `Store.Sources()`
  returns `[]string`, not `[]Source`, and includes non-writable layers. The only
  route to a real target today is `store.Plan(...).Targets()` — plan a write to
  discover where you could have pinned it, then throw the plan away.

## Decisions

### D1 — A variadic option on `Set` and `Remove`, not a new constructor

```go
func Set(path string, value any, opts ...ChangeOption) Change
func Remove(path string, opts ...ChangeOption) Change
```

Source-compatible: every existing call site compiles unchanged, because Go
permits a variadic parameter to receive nothing. So this is additive in the
strict sense — no deprecation, no second spelling of `Set` to explain, and the
batch form keeps working:

```go
store.Apply(ctx,
    config.Set("server.port", 9090),
    config.Set("db.password", secret, config.To("vault")),
    config.Remove("legacy.flag"),
)
```

The alternative — `SetIn(target, path, value)` — was rejected because it
multiplies: `SetIn`, `RemoveIn`, and then a third pair the moment any other
per-change option appears.

### D2 — `To(name string)` takes a name, not a `Source`

```go
func To(name string) ChangeOption
```

The caller names the layer. They do not construct a `Source`, because
constructing one invites filling in fields that are ignored — and a field a
caller sets, believing it selects something, is worse than a field they cannot
reach.

This is not a new matching rule. `matchTarget` already matches by name and
document only, and already returns `ErrInvalidTarget` naming the unmatched
string rather than falling back to routing. `To` exposes what the router does.

### D3 — `ToDocument(name string, doc int)` for the multi-document case

A multi-document file contributes one layer per document, all sharing a `Name`
and distinguished by `Document`. Addressing the second document of a file is a
real need and `To` alone cannot express it.

Separate function rather than a variadic index on `To`, because `To(name, 0)`
and `To(name)` would have to mean the same thing while looking like a choice.

### D4 — Pinning still does not opt out of the sensitive-leak guard

`routeOne` refuses a non-removal write into a non-sensitive target when a
sensitive source defines the key, **whether or not the target was pinned** —
recorded at `plan.go:220` as *"a safety invariant, not a routing preference"*.

That stands. This spec is about reaching the existing router, and the router
refuses this. A caller who genuinely wants a secret in a plain file is asking
for the thing `ErrSensitiveLeak` exists to prevent, and the honest answer is
that this module will not do it.

Worth stating explicitly because the feature reads like an escape hatch. It is
an escape hatch from *routing*, not from the guard.

### D5 — `Store.WritableTargets() []Source` for discovery

Pinning by name requires knowing the names. Today that means planning a write
and inspecting it, which is backwards and allocates a plan the caller discards.

```go
func (s *Store) WritableTargets() []Source
```

Returns exactly what `matchTarget` will match against, in precedence order —
including the synthesised entry for a writable backend that has not contributed
a layer yet (`sourceOrder`, `store.go:947`), because that is a legitimate target
and omitting it would make the list lie about what `To` accepts.

`Sources()` is left alone. It answers a different question — "what is in this
store, readable or not" — and narrowing it would break callers.

### D6 — `ChangeOption` is a named function type, not an interface

```go
type ChangeOption func(*Change)
```

Consistent with `StoreOption` and `WatchOption`, which the module already has.
An interface would allow a third party to define options this package cannot
validate, and there is no case for that here: the set of things a change can
carry is closed by `Change`'s own fields.

### D7 — Documentation leads on when *not* to use it

The how-to already says *"reach for this sparingly: routing's default is right
far more often than a hand-picked target, and pinning is how an edit ends up
written somewhere nobody reads."* Making pinning a one-liner makes that warning
more necessary, not less, so it moves up next to the new form rather than
staying below the old one.

The page should also state the cases that actually justify it, so the guidance
is falsifiable rather than merely discouraging. The first is settled:
**promoting a setting between composed stores**. With [store
aggregation](2026-07-28-store-aggregation.md), moving a value from a project
config up into the user's global CLI config is
`Set(path, value, To("~/.krites/config.yaml"))` — the same write mechanism,
conflict detection and plan as any other write, rather than a manual
read-from-one-write-to-the-other. That is the worked example the page should
lead the section with.

## Public API

```go
type ChangeOption func(*Change)

func To(name string) ChangeOption
func ToDocument(name string, doc int) ChangeOption

func Set(path string, value any, opts ...ChangeOption) Change
func Remove(path string, opts ...ChangeOption) Change

func (s *Store) WritableTargets() []Source
```

`Change.Target` stays exported and unchanged. It is the mechanism; these are the
ergonomics over it, and removing the field would break callers for no gain.

## Testing strategy

Unit, table-driven — this is routing, not I/O, so there is nothing to fake:

- `To` on a change reaching `matchTarget` produces the same `Operation` as
  setting `Change.Target` by hand. **The equivalence is the contract**: if these
  two paths can diverge, the option is a second router.
- `To` naming a non-existent layer returns `ErrInvalidTarget` naming the string
  the caller passed.
- `To` naming a real but read-only layer returns `ErrInvalidTarget`, not
  `ErrNoWritableLayer` — the caller named something specific and wrong, which is
  a different fault from having nowhere to write.
- `ToDocument` addresses the second document of a multi-document file, and
  `To` on the same file resolves to document 0.
- A pinned write of a key a sensitive source defines, into a non-sensitive
  target, returns `ErrSensitiveLeak` (D4). **Watched to fail** by removing the
  guard, since this is the assertion most likely to rot.
- `WritableTargets` returns every entry `matchTarget` accepts and nothing it
  rejects — asserted by feeding each returned name back through `To` and
  requiring no error.

The last one is the important one: it is the only test that stops the discovery
list and the matcher drifting apart.

## Migration & compatibility

Purely additive. `Set` and `Remove` keep their existing call syntax, the
`Change.Target` field is untouched, and no error behaviour changes. A minor
version.

## Open questions

- **O1 — Does `To` need a companion that pins *without* failing?** A caller who
  wants "write to `app.yaml` if it is a target, otherwise route normally" has no
  way to say so. That may be a real need (a tool with an optional user-scope
  file) or may be exactly the ambiguity D2 removes. Leaning: leave it out until
  something asks.
- **O2 — Are there justifying cases beyond promotion?** Promotion between
  composed stores is settled (D7) and is the motivating one. A second candidate
  is writing to a base file deliberately so an overlay keeps winning — staging a
  default that the user's own layer is expected to override. Worth naming only
  if it is a case anyone actually has.
- **O3 — Should `WritableTargets` exclude targets a filter has emptied?** Only
  meaningful once [backend key filtering](2026-07-28-backend-key-filtering.md)
  exists. Recorded here so the two specs do not answer it differently.

---
title: config-keychain — the OS keychain as a config layer
date: 2026-07-27
author: matt.cockayne
status: approved
approved: 2026-07-27
---

# config-keychain

A backend adapter over the operating system's keychain — macOS Keychain, Windows
Credential Manager, or a Secret Service implementation on Linux — reached through
the `Backend` seam that
[`gitlab.com/phpboyscout/go/credentials`](https://gitlab.com/phpboyscout/go/credentials)
already defines.

It cites the [dynamic backend adapters
umbrella](2026-07-21-dynamic-backend-adapters.md) and settles the nine things the
umbrella leaves to each adapter (**D2**). It also diverges from that umbrella in
one significant way, argued in D5.

## Problem

Tokens are ending up in plaintext config files.

[keryx](https://gitlab.com/phpboyscout/keryx) is the concrete case, and it has
already built most of the answer by hand. Four packages — instagram, tiktok,
linkedin and youtube — each carry a `tokenstore.go` declaring the same shape:

```go
var tokenStore = oauth.Store{
	EnvVar:          credEnvName,
	KeychainService: "keryx",
	KeychainAccount: "instagram-access-token",
	ConfigKey:       "platforms.instagram.access_token",
}
```

`Resolve` walks environment → keychain → config file. `Save` writes to the
keychain when one is available and **falls back to the config file when it is
not**, returning the label *"the config file (plaintext — keep it out of version
control)"*. The comment on the type says the same thing. The behaviour is
deliberate and documented, and it is still a secret written to disk in the clear.

**And keryx is not the only one.** `go-tool-base` — the framework the other five
consumers are built on — hand-rolls the same chain independently, in
`forge.ResolveToken`: an environment reference, then the keychain, then
`auth.value`, a literal secret read straight from a config key. It is driven off
config keys (`vcs.ConfigFromReader(cfg).Sub(forgeName)`) and inherited by every
project that depends on both modules — go-tool-base, gtb-config-migration,
haileys-app, keryx, phpbotscout and scoutdm.

So the pattern exists twice already, written twice, and the second copy is in the
framework. That is what settles this as a toolkit component rather than something
keryx should keep to itself: it is not speculative surface for a hypothetical
second consumer, it is a third implementation being avoided.

Two observations make this a `config` problem rather than a keryx one.

First, `oauth.Store` is a **per-credential reimplementation of what a config
layer already is**: an ordered set of sources, first-match resolution, and a
write that lands in the right place. Doing it per credential is why it is
duplicated four times, and why the precedence is invisible to `Explain`.

Second, the core already has the guard this needs. `ErrSensitiveLeak` exists
precisely to stop a value a secret store provides being written into a plainer
layer beneath. Nothing in keryx can use it, because its keychain is not a layer.

The complex part — talking to three operating systems' credential stores, and
keeping that dependency out of builds that must not carry it — is done, in
`credentials`. This adapter is the wrapper that makes it a layer.

## Decisions

### D1 — Module `config-keychain`, package `configkeychain`

Vendor-neutral, so no cloud qualifier (umbrella D1). Requires `config`
**v0.9.2+** (per [config-aws-secrets R2](2026-07-25-config-aws-secrets.md):
pinning a bare feature floor silently runs an older `backendconformance`) and
`credentials` **v0.2.0+**.

### D2 — The interface is injected; the keychain implementation is not

`credentials` splits its `Backend` interface from the `go-keyring`-backed
implementation in `credentials/keychain`, which registers itself on blank import.
That split is deliberate: it exists so a regulated build can omit session-bus and
keychain IPC code entirely, and Go's dead-code elimination then drops
`go-keyring` and `godbus` from the binary.

**This adapter takes `credentials.Backend`, the interface — never the
implementation.** So `config-keychain` adds `credentials` (twelve modules, mostly
`cockroachdb/errors`) to a consumer's graph and **not** `go-keyring` or `godbus`.
The consumer blank-imports `credentials/keychain` exactly as they do today, and a
build that must not carry keychain code still cannot acquire it through this
adapter. A `depfootprint` test asserts `go-keyring` and `godbus` are absent, the
same way `config-azure-keyvault` asserts `azidentity` is.

It also means the unit suite needs no keychain: `credentials/test.MemoryBackend`
already exists and satisfies the interface.

### D3 — The key set is declared, because a keychain cannot be enumerated

**Measured**: `zalando/go-keyring` exposes `Get`, `Set`, `Delete` and
`DeleteAll(service)` — and no listing. `credentials.Backend` mirrors that, with
`Store`/`Retrieve`/`Delete`/`Available`. There is no way to ask a keychain what
it holds.

That is decisive, and it makes this adapter unlike every backend the family has
built. All of them `Load()` by enumerating a prefix; this one cannot enumerate
anything. So the consumer **declares** the mapping:

```go
configkeychain.New(backend, "keryx", map[string]string{
	"platforms.instagram.access_token": "instagram-access-token",
	"platforms.tiktok.refresh_token":   "tiktok-refresh-token",
})
```

Config path on the left, keychain account on the right, under one service name.
Explicit in both directions, with no derivation rule — which matters for
adoption: keryx's existing keychain entries keep working untouched, because the
account names are whatever they already are rather than whatever a convention
would compute. Inventing a path→account rule would have made every existing
entry unreadable.

A declared key the keychain does not hold contributes nothing. That is not an
error: it is the ordinary state before a token has been obtained, and the layer
beneath is expected to supply it or the application is expected to go and get it.

### D4 — Read **and** write, which is a departure from umbrella D7

Umbrella D7 makes secrets backends read-only by default, on the reasoning that
"secrets are provisioned by a separate, audited process, and a config library
writing to Vault or a secrets manager is a surprising and risky capability".

**That reasoning does not hold here, and the divergence is the point of the
adapter.** A local keychain is not an audited provisioning system; it is the
user's own machine. And the value being written is not a secret someone else
minted — it is a token this very application just obtained from an OAuth flow and
must persist to be useful. Refusing to write it would leave the caller doing
exactly what keryx does now: falling back to a file.

So this backend implements `WritableBackend`, and it is the family's first
writable `Sensitive` backend. The umbrella's rule is not being ignored: D7 says
write support "requires a per-adapter spec that justifies the need and specifies
the guard". This is that justification, and D6 is that guard.

### D5 — Writing a token routes into the keychain by default

This is why the adapter is worth building rather than calling `credentials`
directly, and it falls out of routing the core already does.

**Verified** in `plan.go`: `findTarget` walks the writable layers from highest
precedence down, returns the one that already defines the key, and otherwise —
*"Nothing defines it, so it is new. The highest-precedence writable target is
where it will be visible."*

So with the keychain layer added last, above the file:

| Situation | Where a `Set` lands | Why |
|---|---|---|
| Keychain already holds the token | **The keychain** | It defines the key, so routing targets it |
| First token, keychain empty | **The keychain** | Nothing defines it; the keychain is the highest-precedence writable layer |
| A key the keychain does not declare | The file | The keychain contributes nothing there, so it is an ordinary config key |

And the guard: `routeOne` refuses a write whose target is **not** sensitive while
a sensitive source defines the key. Writing *into* a sensitive layer is
permitted, so the two cases compose — tokens go to the keychain, and a token that
came from the keychain can never be written to the file beneath.

That converts keryx's silent plaintext fallback into either a correct write or a
refusal.

### D6 — The guard is value-driven, and this is its honest limit

The protection above must not be oversold, because its boundary is not where
someone would assume.

`sensitiveDefiner` asks whether a sensitive source **currently defines the key**.
It is a fact about values, not a declaration about ownership. Therefore:

- If the keychain **is unavailable** — no Secret Service, a regulated build, a
  locked keychain — the backend contributes no layer at all. Nothing sensitive
  defines the key, the guard does not fire, and a `Set` lands in the file exactly
  as it does today.
- Declaring a path in the mapping does **not** by itself protect it. Only holding
  a value does.

So `config-keychain` closes the leak for tokens the keychain holds, and does not,
by itself, stop a first-ever token reaching disk on a machine with no keychain.
Pretending otherwise would be worse than the current behaviour, because a
consumer would stop checking.

The mitigation is to make that state loud rather than silent, and **that is the
default**: when the injected backend reports `Available() == false`, `Load`
returns a distinguishable error rather than an absent source. A consumer then
decides — refuse to start, prompt, or accept the file knowingly. Which it picks
is its business; being told is not optional.

The cost is real and worth naming: an adapter that previously degraded quietly on
a headless box now fails there. That is the intended trade. Silence is what
produced the problem this module exists to fix, and a consumer that genuinely
wants the old behaviour can have it by checking `Available()` and not adding the
layer — which is a decision written down in their code rather than one nobody
made.

### D7 — Environment precedence is the core's job, not this adapter's

keryx's `oauth.Store` checks an environment variable first. That belongs in the
store's layer order — `WithEnv` beneath the file beneath the keychain — not
inside this adapter.

Rebuilding it here would duplicate precedence logic the core owns, which is what
made the four tokenstores bespoke in the first place. The migration note in the
how-to shows the equivalent layer stack rather than an option.

### D8 — Capability: writable, statically `Sensitive: true`, no watch at v0.1.0

| Capability | Value | Why |
|---|---|---|
| `Sensitive` | **`true`**, statically | Everything in a keychain is a credential |
| Writable | **yes** (D4) | The adapter exists to persist tokens |
| `AtomicMultiKey` | `false` | Each entry is written on its own; there is no transaction |
| `NativeWatch` | `false` | No change feed |
| `PreservesComments` | `false` | Not a file format |

**No `Watch` at v0.1.0.** A keychain has no change feed, and the usual argument
for polling — another client may write — is weak here: the writer is almost
always this process, going through the Store, which already knows. Polling *n*
declared keys locally is cheap, so this is not a cost argument but a
no-speculative-surface one. Flagged as an open question rather than settled,
because a second process sharing a keychain is not far-fetched.

`AtomicMultiKey: false` is worth stating plainly: a `Set` of three tokens is
three independent writes, and a failure part-way leaves the earlier ones stored.
The Store's rollback will attempt to undo them, and the adapter must implement
that path rather than pretending the question does not arise.

### D9 — Conflict detection has nothing to compare, and says so

The trap `backendconformance` exists to catch is comparing a version captured at
`Prepare` rather than at `Load`. A keychain offers **no version, etag or
modification time** — `Retrieve` returns a string. So there is nothing to capture
and no compare-and-swap to perform.

The honest consequence: a write can overwrite a change another process made
between this process's `Load` and its `Commit`. The window is small and the
scenario is rare (two processes writing the same account), but the spec records
it rather than leaving a reader to assume a guarantee that is not there. Should
`credentials` ever grow a compare-and-swap, this becomes a revision.

### D10 — Testing: the memory backend, `backendconformance`, and no CI keychain

`credentials/test.MemoryBackend` already implements the interface and has an
`Install(t)` helper, so the unit suite needs nothing new and no keychain.

**`backendconformance.Run` is the gate**, and this adapter exercises a
combination none of its siblings can: a **writable** backend that is also
**sensitive**. The suite's write path — `write_round_trips` and
`conflict_detected` — has only ever run against non-sensitive backends, and its
sensitive path only against read-only ones. This is the first adapter in the
intersection, so the run is worth watching rather than assuming: if the suite has
an assumption baked in about that combination, this is where it surfaces, and
that would be a `config` fix rather than an adapter one.

Integration against a **real** keychain is deliberately **not** wired into CI. A
headless runner has no Secret Service, and standing one up would test the
container's keyring rather than a user's. The suite is env-gated and skips, as
`config-azure-keyvault`'s does — this family now has three adapters whose real
verification happens off CI, and pretending otherwise is the thing to avoid.

### D11 — Documentation and test-first, as the family now does them

Docs ship with each phase; the `TestDocLinksResolve` guard is copied in, having
caught the same defect in two successive adapters. Test-first, assertions watched
to fail.

The how-to carries one thing beyond the usual: a **migration section for
`oauth.Store`**, showing the layer stack that replaces it. Four tokenstores
collapsing into one declared mapping is the adapter's main practical benefit, and
a consumer will not derive the equivalence unaided.

## Rejected alternatives

**Wrap `credentials/keychain` directly rather than the interface.** Simpler by
one indirection, and it would drag `go-keyring` and `godbus` into every
consumer's graph — destroying the property `credentials` was deliberately split
to preserve. Rejected in D2.

**Derive keychain accounts from config paths.** Less to declare, and it would
make keryx's existing entries unreadable overnight while inventing a convention
the keychain knows nothing about. Rejected in D3.

**Read-only, leaving writes to `credentials`.** Consistent with every other
secrets adapter, and it leaves the plaintext fallback exactly where it is. The
whole justification for this adapter is the write path (D4).

**Build the environment-variable check into the adapter.** It would make
migration a one-line swap, at the cost of duplicating the precedence the core
owns. Rejected in D7.

**Claim the leak guard protects declared keys.** It protects keys a sensitive
source currently *defines*. Overstating it would be worse than the status quo,
because a consumer would stop checking. Stated as a limit in D6.

## Public API

- `func New(backend credentials.Backend, service string, keys map[string]string, opts ...Option) config.Backend`
- `func WithService(service string) Option` — override per key, for a mapping that spans services
- `const SourceKind = config.SourceKind("keychain")`
- A named error for the unavailable-backend case (D6), matchable with `errors.Is`

The returned backend satisfies `config.WritableBackend` and not
`config.WatchableBackend`. No `config` core change is required.

## Testing strategy

Per D10, test-first per D11. What would falsely pass, and is therefore watched to
fail:

- a write test asserting only that `Set` succeeds, without asserting **which
  layer it landed in** — the entire point is the routing, and a write to the file
  beneath would also "succeed";
- a leak-guard test whose keychain layer is absent, which would pass while
  proving nothing (D6 is exactly this distinction);
- a declared-but-missing-key test that does not also assert the rest of the layer
  loaded — absent and error look alike from one key;
- a `Sensitive` assertion made only through conformance, which a
  `Sensitive: false` backend satisfies by routing beneath;
- a partial-write test that does not assert the earlier writes were rolled back
  (D8).

## Migration & compatibility

Purely additive; no core change. For keryx it replaces four `oauth.Store` values
with one backend and a layer order, and the `Save` fallback disappears: a token
either reaches the keychain or the write is refused.

Existing keychain entries are read unchanged, because the accounts are declared
rather than derived (D3) — so adoption does not require re-authenticating.

## Resolved (2026-07-27)

1. **This is a toolkit module, not a keryx one.** The decisive evidence is that
   the pattern is already written twice — keryx's `oauth.Store` and
   `go-tool-base`'s `forge.ResolveToken` — and the second is framework-level,
   inherited by six projects. Building it here avoids a third implementation
   rather than speculating about a second consumer.

   **Retiring `forge.ResolveToken` onto this layer is explicitly NOT part of this
   work.** It was considered and deferred: that resolver returns a `string` and
   bakes in `credentials`' three modes, so moving it onto a layer touches every
   project built on GTB. If it happens it needs its own spec and its own blast
   radius; this module does not assume it.

2. **An unavailable keychain fails the Load** (D6), rather than contributing
   nothing. Silence is what produced the problem, and the alternative reproduces
   it. The cost — failing where the adapter previously degraded quietly — is
   accepted deliberately.

3. **No `Watch` at v0.1.0** (D8). A keychain has no change feed, and the writer is
   almost always this process going through the Store, which already knows. Added
   on demand if a second process sharing a keychain turns out to be real, rather
   than built on the speculation that it might be.

All open questions resolved.

## Implementation phases

Each phase ships code **and** its documentation (D11), test-first.

**Phase 0 — this spec.** Approve it, resolving the questions above.

**Phase 1 — read.** Scaffold, `New`, `Load` over the declared mapping, static
`Sensitive`, provenance, the `depfootprint` allowlist asserting `go-keyring` and
`godbus` absent (D2).

**Phase 2 — write.** `WritableBackend` with its rollback path (D8), and
`backendconformance` — the gate, and the first run of the writable-and-sensitive
combination (D10).

**Phase 3 — docs and release.** The env-gated real-keychain suite, then
`how-to/keychain.md` including the `oauth.Store` migration, the ecosystem matrix,
homepage and landing card, and v0.1.0.

**Phase 4 — keryx adoption.** Replace the four tokenstores. Not part of this
module, and the reason it exists.

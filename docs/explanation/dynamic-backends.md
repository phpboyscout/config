# How dynamic backends work

A *dynamic backend* is a source of configuration that is not a file — Consul, a cloud parameter
store, a secrets manager, etcd. Each ships as its own `config-<system>` module over the
[`Backend`](backends.md) seam, and they are more alike than not. This page explains what the family
shares and where its members legitimately differ, so that reading one adapter's
[how-to](../how-to/consul.md) tells you most of what you need for the next. For the reference
implementation in depth, see [How the Consul backend works](consul-backend.md); for what exists,
[the adapter ecosystem](adapters.md).

## The shape they all share

- **The client is injected; the adapter owns no credentials.** You build and configure the
  system's client — address, auth, region, TLS — and hand it in behind a narrow interface the
  adapter defines. The adapter re-decides none of it, and a fake satisfying that interface drives
  the whole unit suite with no network. Every cloud authenticates differently and the consumer has
  already chosen; the adapter has no business duplicating that.
- **A prefix scopes; keys are paths that nest.** A remote store is a flat, path-keyed namespace. A
  backend takes a prefix, reads only beneath it, strips it, and nests the remaining segments into
  the layer's tree — so provenance can name the full remote key and precedence works per-key.
- **Values are bytes; structured ones decode through an injected codec.** A remote value is
  bytes, so it is a scalar string by default and the View's typed accessors coerce it. A value that
  is a whole JSON or YAML document decodes into a subtree when you pass a
  [`config.Codec`](../how-to/json.md) with `WithValueCodec` — the same codec the format adapters
  export. Some stores *declare* a value's format (Azure a content-type, GCP a `Format` enum), but
  the family deliberately does not auto-decode from that hint: decoding is always the codec you
  inject, so behaviour is uniform and no adapter takes a codec dependency of its own.

Once loaded, a dynamic backend is indistinguishable from a file layer: precedence, per-key merge,
provenance, shadowing and hot-reload all work the same.

## Watching: native where the system offers it, polled where it does not

Hot-reload needs a backend to notice change. The systems split:

- **Native change feed.** Consul blocking queries, etcd watch, Kubernetes informers — a real
  subscription, so a change arrives push-fast and the backend reports `NativeWatch: true`.
- **No change feed.** The cloud parameter stores have nothing to subscribe to, so they **poll** and
  report `NativeWatch: false`. Polling inherits everything else for free: the Store still coalesces
  a burst, re-reads, re-merges, and stays quiet if the resolved configuration did not actually
  change. The only visible difference is latency, and each adapter states its default interval —
  conservative, because a poll is an API call that may be rate-limited or billed. `WithPollInterval`
  overrides it.

## The conflict spectrum

Writing safely turns on the **conflict trap**: the version a write is checked against must be
captured at *load*, so a change that landed since is refused rather than overwritten. Whether a
store can honour that depends on whether it offers **compare-and-swap**, and this is where the
family genuinely diverges:

| Store | Compare-and-swap | Consequence |
|---|---|---|
| Consul | transaction with per-key CAS | full read+write; conflict refused with `ErrConflict` |
| Azure App Configuration | per-key ETag (`If-Match`) | full read+write; conflict refused |
| AWS SSM | **none** (`PutParameter` overwrites) | **read-only** — a write can't be made safe |
| GCP Parameter Manager | **none** (immutable versions) | **read-only** |
| Vault | KV v2 check-and-set | capable, but **read-only by policy** — see below |

Read-only is a first-class outcome, not a failure: a write to a key a read-only backend defines
routes to the writable layer beneath and is reported shadowed, so the module still tells you the
write did not take effect where you might have expected. Write support for the no-CAS stores is a
tracked follow-on — the honest answer today is that a config tool cannot compare-and-swap them, and
those parameters are usually provisioned by IaC anyway.

Vault is the row that shows the two axes are independent. It *has* a real compare-and-swap, so it
could be written safely — it is read-only because writing a secret from a configuration-consuming
tool is a riskier act than reading one, and that needs justifying per adapter rather than
inheriting. Capability is what the system permits; what an adapter ships is a separate decision.

## The same abstraction costs different amounts

Two adapters in this family read a tree of secrets, and they **default to opposite modes** — which
looks like an inconsistency until you look at what each one charges.

[Vault](../how-to/vault.md) makes its prefix walk opt-in. It has no recursive list, so walking costs
one request per directory plus one per secret, and needs a `list` capability least-privilege
policies routinely withhold. [AWS Secrets Manager](../how-to/aws-secrets.md) makes the prefix the
*default*, because `BatchGetSecretValue` accepts a name-prefix filter and returns every matching
secret's value in a single request.

The principle is worth stating because it will keep recurring: **the family shares naming and safety
conventions, not costs.** Copying an adapter's shape while discarding the measurement that produced
it would give Secrets Manager consumers a worse default for no reason. Where an adapter diverges
from its siblings, its spec says what it measured.

The same asymmetry shows up inside one adapter. Selecting a staging label on Secrets Manager turns
its one-request read into one request per secret, because the bulk API has no staging parameter —
so the option exists, and documents its price at the point of use.

## Ambiguous structure is refused, not guessed

A remote store's shape does not always map cleanly onto a config tree, and where two things claim
the same key the family **refuses the load** rather than picking one.

Vault is the case that makes this concrete. A Vault path is simultaneously a secret *and* a
directory — listing a prefix returns both `app` and `app/` for the same name — so when a whole tree
is read, a secret's own fields merge with its child secrets into one node. A field named `db` and a
child secret `app/db` both claim `db`. Either resolution silently discards a value an operator can
see in the store, and for a secrets manager "the password quietly wasn't the one you set" is the
worst failure available. So `config-vault` refuses it with a named error identifying the path and
the segment.

This is the same reasoning the file adapters use — `config-xml` refuses an attribute colliding with
a child element, `config-hcl` an attribute colliding with a block type. An ambiguous merge is a
defect in the source, and a startup failure that names it is cheaper than a value that silently
disappeared.

## Sensitive read-only backends

A backend that holds secret material declares `Sensitive: true`, and the core enforces a rule:
a value a sensitive layer defines must never be written into a layer that is not. Because secrets
backends are read-only, a write to a key one owns routes *down* to the next writable layer — a
plain file — and the core refuses that with `ErrSensitiveLeak` rather than let the secret land
there.

This has a consequence worth naming, because it is counter-intuitive: for a sensitive read-only
backend, a routed-beneath write being **refused** is the correct behaviour, not a bug. The shared
[`backendconformance`](adapters.md) suite asserts exactly that — a sensitive read-only backend
refuses the write with `ErrSensitiveLeak`, where a non-sensitive one routes it beneath.

[`config-vault`](../how-to/vault.md) was the first backend to be **statically** sensitive — every
value in Vault is a secret, so the flag is unconditional — and therefore the first to exercise that
branch of the suite in anger. [`config-aws-secrets`](../how-to/aws-secrets.md) is the second, which
is what turns a contract written for one adapter into a family invariant. The remaining Phase-B
secrets managers inherit the same shape.

[`config-keychain`](../how-to/keychain.md) then bent the rule, and it is worth saying why it bent
rather than broke. Secrets are read-only here because they are provisioned by a separate, audited
process, and a config library writing to Vault would be a surprising power to hand it. A local
keychain is not that: it holds a token the application itself just obtained, on the user's own
machine. So it is the family's first **writable** sensitive backend — which the core permits,
because the leak guard refuses a write whose target is *not* sensitive and says nothing about
writing into one that is.

That combination had never been exercised. The suite's write path had only met non-sensitive
backends and its sensitive path only read-only ones, so running the gate against the first adapter
in the intersection was where any hidden assumption would surface — and one did. The suite took for
granted that a writable backend accepts arbitrary keys, which is true of a file, a prefix or a
bucket and false of a keychain, whose key space is declared because it cannot be enumerated.
`Suite.BoundedKeySpace` now lets a backend say so, and the fix went into the core rather than being
papered over in the adapter.

AWS
SSM is the in-between case: it reports `Sensitive` only when a decrypted `SecureString` is actually
in the loaded prefix, because it is a mixed store where most parameters are ordinary
configuration, so an all-plain prefix routes normally.

## Testing them without the cloud

Every adapter is tested three ways: a hand-written fake of its narrow client drives the unit suite
with no network; the shared `backendconformance` suite proves it takes part as a first-class layer
and — the trap the family turns on — captures its conflict version at load; and an env-gated
integration suite hits the real service. Whether that last one runs in CI depends on the system: a
store with a local emulator (Consul, or AWS via LocalStack) runs it in a Docker-in-Docker job,
while Azure App Configuration and GCP Parameter Manager have no emulator, so their integration
tests are real-service-only and gated to run where credentials exist.

## What "tested" means when there is no emulator

Every adapter here is tested three ways — a fake for units, `backendconformance` for the layer
contract, and an integration suite against the real service. The third is the one that keeps
finding things: it caught Vault rounding integers above 2^53, and the exact boundary of a name
prefix filter. The reason is structural rather than lucky. A unit fake is built from what the
author believes the service does, so if that belief is wrong the fake is wrong *identically*, and
every unit test still passes.

That makes the emulator question a real one rather than a convenience. Where a service has a
credible local stand-in — Consul, Vault, LocalStack for AWS, Azurite and fake-gcs-server for the
object stores — the suite runs in CI on every change. Where it does not, the family does not
pretend otherwise:

- [Azure App Configuration](../how-to/azure-appconfig.md), [GCP Parameter
  Manager](../how-to/gcp-parameter.md) and [Azure Key Vault](../how-to/azure-keyvault.md) have no
  official emulator, so their integration suites are **real-service only** and skip cleanly without
  credentials.
- An unofficial emulator whose fidelity nobody has checked is **not** a substitute. A green run
  against it would be weaker evidence than an honestly acknowledged gap, because it looks like
  proof.

For `config-azure-keyvault` the consequence is written into its plan: several of its behaviours are
documented rather than observed, each is listed in its spec as a **claim** with a test that asserts
it directly, and the release waits on running that suite against a real vault. If a claim turns out
false it becomes a dated revision before v0.1.0 — which is the point of recording them as claims
instead of assumptions.

## Related

- [How the Consul backend works](consul-backend.md) — the reference backend, in depth
- [Backends and capabilities](backends.md) — the interface split all backends implement
- [The adapter ecosystem](adapters.md) — every adapter, status and roadmap
- [Write a custom backend](../how-to/custom-backend.md) — build one for a system not yet covered

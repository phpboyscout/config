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

Read-only is a first-class outcome, not a failure: a write to a key a read-only backend defines
routes to the writable layer beneath and is reported shadowed, so the module still tells you the
write did not take effect where you might have expected. Write support for the no-CAS stores is a
tracked follow-on — the honest answer today is that a config tool cannot compare-and-swap them, and
those parameters are usually provisioned by IaC anyway.

## Sensitive read-only backends

A backend that holds secret material declares `Sensitive: true`, and the core enforces a rule:
a value a sensitive layer defines must never be written into a layer that is not. Because secrets
backends are read-only, a write to a key one owns routes *down* to the next writable layer — a
plain file — and the core refuses that with `ErrSensitiveLeak` rather than let the secret land
there.

This has a consequence worth naming, because it is counter-intuitive: for a sensitive read-only
backend, a routed-beneath write being **refused** is the correct behaviour, not a bug. The shared
[`backendconformance`](adapters.md) suite asserts exactly that — a sensitive read-only backend
refuses the write with `ErrSensitiveLeak`, where a non-sensitive one routes it beneath. The Phase-B
secrets managers are all statically sensitive read-only backends and inherit this. (AWS SSM is the
in-between case: it reports `Sensitive` only when a decrypted `SecureString` is actually in the
loaded prefix, so an all-plain prefix routes normally.)

## Testing them without the cloud

Every adapter is tested three ways: a hand-written fake of its narrow client drives the unit suite
with no network; the shared `backendconformance` suite proves it takes part as a first-class layer
and — the trap the family turns on — captures its conflict version at load; and an env-gated
integration suite hits the real service. Whether that last one runs in CI depends on the system: a
store with a local emulator (Consul, or AWS via LocalStack) runs it in a Docker-in-Docker job,
while Azure App Configuration and GCP Parameter Manager have no emulator, so their integration
tests are real-service-only and gated to run where credentials exist.

## Related

- [How the Consul backend works](consul-backend.md) — the reference backend, in depth
- [Backends and capabilities](backends.md) — the interface split all backends implement
- [The adapter ecosystem](adapters.md) — every adapter, status and roadmap
- [Write a custom backend](../how-to/custom-backend.md) — build one for a system not yet covered

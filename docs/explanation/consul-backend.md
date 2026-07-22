# How the Consul backend works

[`config-consul`](https://gitlab.com/phpboyscout/go/config-consul) is the first *backend* adapter
— a source that is not a file. Where a file format is a [`Codec`](backends.md#a-files-format-is-a-codec-by-the-same-reasoning)
plugged into shared file machinery, a backend owns its whole conversation with a remote system:
reading, versioning, staging, committing, watching. This page explains how the Consul backend does
each of those, and why it is shaped the way it is. For the task recipes, see
[Read and write Consul](../how-to/consul.md); for the decision record, the
[config-consul spec](../development/specs/2026-07-22-config-consul.md).

## The client is injected; the adapter owns no credentials

You construct and configure the Consul client — address, token, TLS, datacenter — and pass it in
with `FromClient(client, prefix)`. The adapter never reaches for any of that.

The reason is that every deployment authenticates to Consul differently, and the consumer has
already made those decisions when they built the client. An adapter that re-derived them would
duplicate that configuration and couple itself to one way of doing it. Injection also makes the
adapter testable without Consul at all: the client is taken behind a narrow interface the adapter
defines, so a fake satisfying it drives the whole unit suite. `FromClient` is a convenience over
that interface — it wraps the real SDK client — so the common path is one call and the seam is
there when you need it.

This is the same principle the [custom-backend guide](../how-to/custom-backend.md) is built on,
and it is why the adapter's own dependency surface stays small even though the Consul SDK behind it
is not.

## A prefix scopes the store; keys are paths that nest

Consul's key-value store is a flat namespace of `/`-separated keys holding opaque bytes. The
adapter takes a **prefix** — a required scoping control, the way `WithEnv`'s prefix is — and reads
only beneath it, strips it, and splits the remaining path on `/` into the layer's nested tree:

```
app/server/port = "8080"   ─┐  prefix "app/" stripped, "server/port" split
app/server/host = "localhost" ├─▶  server: {port: "8080", host: "localhost"}
```

Provenance names the full remote key (`consul:app/`), so a value is always traceable to where it
lives. This flat-to-nested step is the same few lines each flat file adapter writes; there is
deliberately no shared core helper for it, because the helper would be trivial and hide more than
it saved.

## Values are bytes: strings by default, documents by codec

Consul stores bytes, not typed values, and its data comes in two shapes. The **flat-key** shape
spreads configuration across many small keys, each a scalar. The **blob** shape stores a whole
JSON or YAML document under one key.

By default the adapter reads every value as a scalar **string**, and the View's typed accessors
coerce — `GetInt("server.port")` parses `"8080"`. This is the honest model for a byte store and
needs no configuration.

For the blob shape, you inject a value codec — any `config.Codec`, the interface the sibling format
adapters implement. Each value is then decoded: a value that decodes to a mapping becomes a subtree
at its key's path, and a value the codec rejects — a bare scalar, or bytes that are not a document
— falls back to a string. So a prefix mixing flat keys and blobs reads correctly, and enabling a
codec never turns a scalar key into a load error.

Reusing `config.Codec` rather than inventing a decoder is deliberate: the machinery already exists,
you inject the one format your store actually holds so the adapter takes no codec dependency of its
own, and the same question faces every byte-valued backend. It is the family convention, recorded
as revision **R1** of the [dynamic backend adapters spec](../development/specs/2026-07-21-dynamic-backend-adapters.md).

## Conflict detection: the version is captured at Load

This is the subtlety the whole backend design turns on, and the easiest thing to get wrong. When
the store loads, the adapter records the index Consul reports for the prefix, and each key's
`ModifyIndex`. A write compares against **those recorded values, not a fresh read**.

Why it matters: routing decided *where* a write goes based on what the load saw. A change that
landed since then invalidates that decision. If the adapter fetched a fresh version at write time
and compared against *that*, it would be comparing the intruder's data with itself — and every
stale write would be accepted while conflict detection silently never fired. Capturing at load,
and comparing at write, is what makes a write that raced another writer fail with
`config.ErrConflict` instead of clobbering it.

This trap is not unique to Consul — it is the same one every writable backend faces, and it is
exactly why the toolkit ships a shared `backendconformance` suite that asserts it. config-consul
runs against that suite, so the trap is proven closed rather than assumed.

## Writing is one atomic transaction

A batch of `Set`s and `Remove`s under this backend becomes a single Consul transaction of
compare-and-swap operations — so `AtomicMultiKey` is true: the whole batch lands, or none of it
does. Each operation carries the `ModifyIndex` from load as its compare-and-swap guard; a new key
uses index zero, which Consul treats as create-if-absent. If any key moved since load, Consul
rejects the whole transaction atomically and the adapter returns `ErrConflict`.

Writes target flat Consul keys. Writing *into* a value that a codec decoded from a blob is out of
scope: it would mean re-encoding the document, and a config-consuming tool editing a blob another
system owns is a surprising capability. Such a write lands as a sibling flat key instead.

## Watching is a Consul blocking query

Consul offers a real change feed, so the adapter implements `WatchableBackend` over a blocking
query: it asks Consul to hold the connection open until anything under the prefix changes past the
last-seen index, then reports the change. Latency is push, not poll, which is what `NativeWatch:
true` promises. `WithPollInterval` bounds each query; unset, Consul's own default applies. The
store does the rest — it coalesces a burst, re-reads, re-merges, and stays quiet if the resolved
configuration did not actually change.

## Not sensitive — Consul KV is configuration

The backend declares `Sensitive: false`. Consul's key-value store is where application
*configuration* lives; secret material belongs in Vault, which is the sensitive member of this
adapter family. Declaring Consul sensitive would be dishonest and would blunt the
[core's sensitive-write guard](backends.md#what-capabilities-is-for-and-what-it-is-not), which
exists to stop a value from a secrets store being written into a plain file. A consumer who stores
secrets in plain Consul KV has already made that choice upstream; the adapter does not paper over
it with a capability Consul does not enforce.

## The SDK is the honest cost

Unlike the zero-dependency file adapters, a backend adapter carries its system's client library,
and the Consul SDK is the largest thing in config-consul's graph. That is stated plainly: an
allowlist test in the module asserts exactly which modules it pulls in, so an SDK that grows a
dependency — or a stray import — fails the build rather than arriving quietly in a consumer's
`go.sum`. The testcontainers integration suite is test-only and reaches no consumer.

## Related

- [Backends and capabilities](backends.md) — the general backend contract this implements
- [Write a custom backend](../how-to/custom-backend.md) — the seam, walked end to end
- [config-consul spec](../development/specs/2026-07-22-config-consul.md) — the full decision record

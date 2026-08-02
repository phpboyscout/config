---
title: Precedence & merge model
description: How a value is resolved when several sources define it — precedence, per-key merge and shadowing.
tags: [explanation, precedence, merge]
---

# Precedence & merge model

This page explains how a value is resolved: which source wins when several define the
same key, and how nested structures fold together. Understanding it means you can predict
what `view.GetString("server.host")` will return without tracing the code, and — more
usefully — work out why it returned something else.

## There is no ranking baked into the module

Most configuration libraries ship a fixed hierarchy: explicit set beats flag beats
environment beats file beats default, and that ordering is a property of the library. This
one has no such table. There is exactly one rule:

> **Backends are read in the order they are added, and later ones win.**

The environment is not special. Flags are not special. Compiled-in defaults are not
special. Each is a layer, they fold in the order you listed them, and the last layer to
define a key is the one in effect.

```go
store, err := config.NewStore(ctx,
	config.WithReaders(config.NamedSource{Name: "embedded:defaults.yaml", Content: defaults}),
	config.WithFiles(config.OS(), "/etc/mytool/config.yaml", "/home/me/.mytool/config.yaml"),
	config.WithEnv("MYTOOL"),
	config.WithFlags(cmd.Flags()),
)
```

That resolves, highest precedence first: changed flags, then `MYTOOL_*` environment
variables, then the user's file, then the system file, then the embedded defaults.

The reason to have no built-in table is that every fixed hierarchy is wrong for somebody.
A daemon may want its operator-managed file to outrank the environment its container
runtime injects; a CLI almost certainly wants the opposite. A library that decides this
centrally forces one of those callers into a workaround, and workarounds around
precedence are how configuration becomes unpredictable.

**The trade-off is real, and it is yours.** Order is now something you have to get right
at construction, and getting it wrong produces a store that behaves plausibly and
incorrectly. The ordering above is the recommended one and each placement earns its keep:
defaults sit at the bottom as an ordinary layer rather than a special case, the
environment sits above files because that is what an operator expects an environment
variable to do, and an explicit flag goes last because it is the most deliberate input a
user can give. Deviate when you have a reason, not by accident.

`store.Sources()` lists every backend in precedence order, which is the quickest way to
check that what you built is what you meant.

## Everything is a layer

The single ordering rule works because the Store has only one notion of a source. A file,
a document within a multi-document file, an in-memory reader, the environment, the flag
set, a layer added at runtime with `AddLayer` — all of them are `Layer` values with a
`Source` identifying where they came from.

Everything else in the module is expressed in terms of that ordering. Merge order is layer
order. [Provenance](provenance.md) names a layer. Write routing walks the layer order in
reverse looking for the first writable one. Shadowing is one layer outranking another.
There is no second mechanism for the environment and no special case for defaults, which
means there is nowhere for a special case to disagree with the general rule.

## Merging is per-leaf and deep

When several layers define parts of the same structure, they **deep-merge**. A later layer
overriding one nested key does not discard its siblings:

```yaml
# base.yaml
server:
  http:
    port: 8080
    tls:
      enabled: false
  grpc:
    port: 50051
```

```yaml
# overlay.yaml
server:
  http:
    tls:
      enabled: true
      cert: /etc/cert.pem
```

The result keeps `server.http.port: 8080` and `server.grpc.port: 50051` from the base
while taking `server.http.tls.enabled: true` and the new `cert` from the overlay. An
overlay never has to restate a subtree in order to change one value inside it.

This is what makes small overlay files viable. Whole-node replacement would mean a user
who wants to change one TLS setting must copy the entire `server` block, and their copy
then silently stops tracking changes to everything else in it.

### A scalar replacing a subtree takes the whole path

The recursion only continues while both sides are mappings. If a higher layer sets a
scalar where a lower layer had a subtree, the subtree is gone — not merged into, not
partially retained — and the provenance of everything beneath it is discarded with it.

That is the honest outcome. Reporting that `db.host` came from `base.yaml` when
`db: disabled` has replaced the lot would send somebody to edit a value nobody reads.

### Sequences replace; they do not append

A sequence is treated as a value, not as a structure to merge into. A later layer defining
a list **replaces** the earlier one entirely:

```yaml
# base.yaml
plugins: [a, b]
```

```yaml
# overlay.yaml
plugins: [c]
```

The resolved value is `[c]`. Not `[a, b, c]`, and not `[c, b]`.

This surprises people, so it is worth saying why nothing better was available. There is no
correct general answer for merging two lists. Appending is right for a list of plugins and
wrong for a list of allowed hosts, where an overlay usually means "these instead of
those". Positional merge assumes the two lists describe the same things in the same order,
which is almost never true. Identity-based merge needs to know which field is the identity,
which is a per-key schema the module does not have and would have to invent.

Every one of those choices is right sometimes and silently wrong the rest of the time, and
a silently wrong merge of an allow-list is a security bug. Replacement is at least
predictable, inspectable, and easy to reason about: the winning layer is the whole answer.
If you want accumulation, model it as a mapping — whose merge behaviour is well defined —
or assemble the list in your own code from keys that each layer can contribute.

### Empty containers are values

`extras: {}` and `plugins: []` are present. They resolve, they satisfy `Has`, they appear
in `Keys()`, they carry provenance like any scalar, and they survive a write untouched.

Emptiness is a value, never an absence. Code that legitimately requires a section to exist
while holding nothing in it should be able to say so, and a model that quietly deleted
empty containers would make `Has` disagree with `Keys()` and both disagree with the file.
That class of inconsistency is precisely what this design exists to remove.

An empty container is also a *leaf* for merging and provenance purposes, because it holds
a value rather than entries.

## A document is a layer

A file holding several YAML documents contributes **one layer per document**, and later
documents override earlier ones exactly as later files do:

```yaml
a: 1
b: 1
---
b: 2
```

resolves `a: 1`, `b: 2`, and provenance renders the second document as `config.yaml#1`.

Be clear that this is *this module's* interpretation, adopted for consistency with how it
already treats files, not something the YAML specification says. The specification
describes a multi-document stream as a sequence of independent documents and takes no
position on overlaying them. The alternative — refusing multi-document files, or reading
only the first — would have meant a second rule for a construct that the layer model
handles for free.

## Keys are lower-cased

Configuration keys are normalised to lower case as they are merged, so `Port` and `port`
are the same setting. This is not cosmetic: schema validation compares field names against
configuration keys, and struct decoding derives keys from field names. A case-sensitive
model would break both, and would let a file define two keys that every consumer sees as
one.

## The environment layer resolves names against the keys beneath it

Mapping an environment variable name back to a dotted key is genuinely ambiguous:
`MYTOOL_SERVER_PORT` could mean `server.port` or `server_port`, and nothing in the
variable name distinguishes them.

Rather than guess, the environment backend resolves each name against the keys the layers
beneath it already define — which is nearly always what a variable is overriding. A name
matching nothing falls back to treating every underscore as a separator. When two existing
keys would both be spelled the same way, the load fails with `ErrAmbiguousEnvKey` naming
both candidates, rather than picking one in map iteration order and behaving differently
between runs of the same program.

The prefix `WithEnv` requires is a security control rather than tidiness: without one, any
environment variable matching a configuration key could silently reconfigure your tool on
a shared CI runner or a multi-tenant host. See
[Load & merge configuration](../how-to/load-and-merge.md#read-the-environment).

!!! note "Never log a configuration value"
    When recording that a variable overrode a key, log the **key** and the **variable
    name**, never the value. That applies to every key, not only ones that look like
    credentials.

## Reading a subtree

`view.Sub("database")` returns a `*View` scoped to that subtree, reading through the same
snapshot rather than holding a detached copy of it — every read is still resolved through
the full path, so a scoped view cannot serve values the rest of the configuration has
moved past. It returns `nil` when the key is absent, so `if sub != nil` guards behave.

`SectionExists` reports whether a path holds a mapping. An empty path asks about the
view's own root, which lets a caller check a scoped view without special-casing the top
level.

## What the model deliberately does not do

- **It does not merge sequences.** Covered above. Replacement is the rule.
- **It does not inject defaults from struct tags.** A `default:` tag is documentation and
  hint text. Defaults are a layer, contributed with `WithReaders`, so they take part in
  precedence and provenance like everything else.
- **It does not read a `.env` file.** Nothing in this module loads one. If you want the
  contents of a `.env` file to reach configuration, load it yourself and contribute it —
  as a reader source at construction, or with `AddLayer` at runtime.
- **It does not interpolate.** There is no `${...}` expansion, no reference from one key
  to another, and no computed values. Every leaf comes verbatim from exactly one layer,
  which is what makes `Origin` able to answer honestly.
- **It does not give a populated subtree a single origin.** A subtree is assembled from
  however many layers contributed to it; `Origin` reports not-found and `Shadowed` gives
  the full list. See [Provenance](provenance.md).

## Related

- [The Store](the-store.md) — why one component owns configuration I/O
- [Provenance](provenance.md) — asking which layer supplied a value, and what shadows it
- [Load & merge configuration](../how-to/load-and-merge.md) — building a store, in order
- [Bind CLI flags](../how-to/bind-cli-flags.md) — why only changed flags contribute
- [History](../about/history.md) — where this model came from
- [Keys and paths](../reference/keys-and-paths.md) — the same rules stated as a lookup table
- [Environment variables](../reference/environment-variables.md) — the full name-resolution algorithm

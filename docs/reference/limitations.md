---
title: Limitations
description: What config does not do — the unsupported combinations, the deliberate omissions, and the guarantees that stop short of where people expect.
tags: [reference, limitations, unsupported]
---

# Limitations

What this module does not do. Some of these are deliberate, some are the honest edge of a
guarantee, and a few are simply not built yet. Each says which.

Everything here is stated because a constraint nobody wrote down is indistinguishable from a
bug you have not hit yet.

## Writes

### There is no cross-source transaction

A write spanning several sources is prepared everywhere, verified everywhere, then committed
everywhere. That narrows the window in which a half-applied set can be observed to the
duration of the renames, and everything likely to fail has already happened by then — but it
is **not** a transaction. A commit that fails partway and cannot be rolled back returns
[`ErrPartialCommit`](errors.md#errpartialcommit) naming what is in which state.

Real transactionality needs a journal, which is not built. *Deliberate, for now.*

### A conflict is detected, not prevented

Another process writing the same file between routing and commit is caught by comparing a
content hash, and the write is refused with [`ErrConflict`](errors.md#errconflict). Nothing
stops the other process from writing. Lock files would, and are not portable enough to be
worth the failure modes they add. *Deliberate.*

### Some layers can never be written to

| Layer | Why |
|---|---|
| Environment variables | A write would not survive the process. |
| Command-line flags | A flag lives for one invocation. |
| `WithReaders` sources | In-memory; there is nowhere to persist to. |
| `AddLayer` layers | Same, and they sit at the top of the order, so a write lands beneath and is reported as shadowed. |
| A nested store | Read-only unless built with `NestedPromotable`, and even then only reachable by naming it with `To`. |

Routing skips all of them. A store built only from these returns
[`ErrNoWritableLayer`](errors.md#errnowritablelayer) for any change. *Deliberate.*

### Setting a whole map replaces the subtree

Comments, anchors and block styles *within* the replaced subtree may not survive. That is
what supplying a whole map asserts — deep-merging it instead would make "this subtree is now
exactly this" inexpressible. If you know what changed, issue targeted `Set` and `Remove`
calls instead. *Deliberate;* see [What survives a write](../explanation/write-fidelity.md#why-replacing-a-map-is-different).

### A write is not byte-for-byte reproducible

Guaranteed to survive: the data, comments attached to their keys, key order, quoting style,
block scalars, anchors, aliases and merge keys. **Not** guaranteed: blank lines, indentation,
comment alignment, the `---` marker on a single-document file, or byte identity. Flow style
may be normalised to block style. *Deliberate* — guaranteeing byte-level identity would mean
never being able to fix anything about layout.

### Key casing is not preserved for new keys

A key this module creates is written lower-cased, because keys are lower-cased on the way
in. An existing key keeps the spelling the file has, since a write edits the existing node.
So a file mixing `Server:` (yours) and `log:` (ours) is the expected outcome of writing a new
section into a camel-cased file. *A consequence of case-insensitive keys, not a choice made
separately.*

### Some documents are refused before you can write to them

A multi-line flow collection with interior comments cannot be round-tripped safely, so
`NewStore` refuses the source with [`ErrBackendUnsafe`](errors.md#errbackendunsafe) rather
than letting you discover it at commit time. *Deliberate* — failing at load is the kinder
failure.

## Reading

### Provenance is leaf-only

`Origin` answers for a leaf value. A populated mapping is assembled from however many layers
contributed to it, so naming a single source for it would be dishonest; `Origin` returns
false and `Explain` says it is "a subtree assembled from" the sources involved. Ask
`Shadowed` instead. *Deliberate.*

### `Explain` renders the value

`Explain("db.password")` puts the value in the string it returns. It is a diagnostic, not a
redacting formatter, and this module does not know which of your keys are secret. Do not log
`Explain` output for a key a secrets backend supplies. *A known gap;* use `Origin` and
`Shadowed` when you need provenance without the value.

### A key containing a literal dot cannot be addressed

Paths split on `.` unconditionally and there is no escape syntax. `Keys()` will list such a
key; nothing else can reach it. *A known gap;* see
[Keys and paths](keys-and-paths.md#a-key-containing-a-literal-dot-cannot-be-addressed).

### List elements cannot be addressed by index

There is no `servers.0.host`. Read the sequence and index it in Go, or decode it into a
struct. *Deliberate* — paths address mappings.

### Sequences replace; they never merge

An overlay that mentions a list at all replaces it entirely. There is no append, no
de-duplicate, and no per-index override. *Deliberate;* see
[Precedence & merge model](../explanation/precedence-and-merge.md#sequences-replace-they-do-not-append).

### Coherence is per-view, not global

A `View` is pinned to one snapshot, so a sequence of reads through it cannot straddle a
reload. Two *separate* `store.View()` calls can. Use `Store.With` for a block of reads that
must agree with each other. *Deliberate* — a handle held indefinitely would serve values
that quietly grew arbitrarily old.

## Validation

### Only `required` is honoured in the `validate` tag

`min`, `max`, `oneof`, `gte` and every other spelling from other validation libraries are
parsed and discarded without warning. `enum` and `required` are the whole vocabulary.
*A known gap;* write the rest as your own check.

### `required` cannot mean non-zero

A `bool` set to `false` and an `int` set to `0` are configured values and satisfy `required`.
Only an absent key — or a present-but-empty string — fails. *Deliberate;* judging by
zero-ness would reject an operator deliberately turning something off.

### The type model is coarse

Unsigned integers, slices, maps, `time.Time` and structs all map to the schema type
`string`, so a validated struct with a `uint16` or a `[]string` field fails its own type
check. Use `int` and leave collections out of the validated struct. *A known gap;* see
[Struct tags](struct-tags.md#what-the-type-check-actually-compares).

### Values from the environment or a flag are strings, and validation says so

An `int` field routinely supplied by `MYTOOL_SERVER_PORT` fails with `expected type int but
got string`. Reading is unaffected; validation is not a reading path. Either leave the key
out of the typed schema or declare it a `string`. *Deliberate* — the alternative is a schema
that cannot tell `8080` from `"8080"` when it matters.

### `default:` never sets anything

The tag is recorded on the field schema and used for nothing but documentation. Defaults
belong in exactly one place — a `WithReaders` layer — because a second source of defaults
will drift from the first. *Deliberate.*

### `description:` is recorded and unused

The tag is parsed onto `FieldSchema.Description` and no message this module produces reads
it. *A known gap.*

### `config:"-"` does not exclude a struct field

`-` and "no tag at all" take the same branch, and that branch walks a struct. A struct field
tagged `config:"-"` still contributes its children under its lower-cased field name. Leave
such a field out of the validated struct instead. *A known gap;* see
[Struct tags](struct-tags.md#how-untagged-struct-fields-become-key-prefixes).

## Hot-reload

### Only file-backed and watchable backends are covered

Environment variables and flags do not change under a running process, and an in-memory
source is changed by the code that owns it. `Store.Reload` re-reads everything; `Store.Watch`
covers what can report its own changes. *Deliberate.*

### Settling does not make a multi-file change atomic

Writes spaced further apart than the settle window are seen as separate changes. The
guarantee belongs to whoever writes the files. *Deliberate* — timing is the last resort,
used only where the filesystem genuinely offers no other information.

### You cannot write configuration from inside an observer

`Apply`, `AddLayer` and `Reload` all return
[`ErrWriteFromObserver`](errors.md#errwritefromobserver) when called from an observer
callback. Record what is needed, return, and write from elsewhere. *Deliberate* — the
cascade has no natural end, and the store cannot break it without either dropping
notifications or silently reordering what changed when.

### Observers do not fire at startup

Section delivery is change-only. `ObservedSection.ApplyInitial` exists if you want startup
delivery. *Deliberate.*

## Composition and backends

### A store appears at most once in a graph

Not just "no cycles" — two copies of one store would contribute their layers at two
precedences, so every value would be shadowed by a copy of itself.
[`ErrCyclicStore`](errors.md#errcyclicstore) at construction. *Deliberate.*

### An outer store does not see writes made directly to the inner one

Not until it next reloads — on the next tick when watching, or on an explicit
`Store.Reload`. *Deliberate.*

### A backend cannot be removed after construction

`AddLayer` adds a source at the highest precedence, and it survives reloads. There is no
public way to withdraw it, or to remove any backend the store was built with. Build a new
store. *A known gap.*

### Filtering is not a security boundary

`Allow` and `Deny` bound what a backend *contributes*, after it has fetched and parsed. A
denied key still crosses the network and still sits in memory briefly. Use the remote
system's own access control to stop a value being fetched. *Deliberate;* the filter's job is
visibility and write routing.

### Comment preservation is document-backend-only

`Capabilities.PreservesComments` is meaningful for a file; a key–value store has nowhere to
put a comment. Do not assume a round-trip through a parameter store keeps anything but the
value. *Inherent.*

### Only YAML is built in

Every other format — JSON, TOML, HCL, XML, dotenv, INI, Java properties — is a separate
adapter module, and so is every filesystem beyond local disk and every remote backend. That
is what keeps a consumer reading TOML from compiling an XML parser. *Deliberate;* see
[the adapter ecosystem](../explanation/adapters.md).

## Related

- [Errors](errors.md) — several of these limits are enforced by a named error
- [The Store](../explanation/the-store.md#what-this-design-does-not-give-you) — the
  architectural version of this list
- [Hot-reload safety](../explanation/hot-reload-safety.md#what-this-does-not-give-you) — the
  reload-specific version
- [Precedence & merge model](../explanation/precedence-and-merge.md#what-the-model-deliberately-does-not-do)
  — the merge-specific version

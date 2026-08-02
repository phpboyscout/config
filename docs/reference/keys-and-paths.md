---
title: Keys and paths
description: The exact rules for dotted paths, key casing, how layers merge per value shape, and what the filter patterns match.
tags: [reference, keys, paths, merge, filter]
---

# Keys and paths

Every read, every write, every schema field and every filter pattern names a value with a
dotted path. This page is the exact grammar and the exact matching rules.

## What a valid dotted path looks like

A path is one or more non-empty segments joined by `.`:

```text
server.port
db.primary.host
log
```

The rules, in full:

| Rule | Consequence |
|---|---|
| Segments are separated by `.` | `server.port` addresses key `port` inside mapping `server`. |
| A segment must not be empty | `a..b`, `.a` and `a.` are all invalid. |
| The whole path must not be blank | `""` and `"   "` are invalid. |
| Every segment is lower-cased before use | `Server.Port`, `SERVER.PORT` and `server.port` are the same path. |
| Surrounding whitespace is not trimmed per segment | `"server .port"` looks for a segment literally named `server ` (with the space). |

An invalid path passed to `Store.Apply` or `Store.Plan` returns
[`ErrInvalidPath`](errors.md#errinvalidpath). An invalid path passed to a *read* accessor
is not an error — reads return the zero value for anything they cannot resolve, including a
path that is malformed.

## Keys are lower-cased, everywhere

Every key entering the store is lower-cased, recursively, including keys inside sequence
elements. So a file written as:

```yaml
Server:
  Port: 8080
```

is read as `server.port`, and `view.GetInt("Server.Port")` and `view.GetInt("server.port")`
both find it.

This is not cosmetic. Schema validation compares field names against configuration keys and
struct decoding derives keys from field names; a case-sensitive model would make `Port` and
`port` two different settings and break both.

**Casing is not preserved on write.** A key this module creates is written lower-cased. A
key that already exists in the file keeps the spelling the file has, because a write edits
the existing node rather than re-serialising the document.

## A key containing a literal dot cannot be addressed

There is no escape syntax. A path is split on `.` unconditionally, so a configuration key
that itself contains a dot is unreachable through every path-taking API:

```yaml
weird.key: 7
```

```go
view.Keys()            // includes "weird.key"
view.Get("weird.key")  // nil — it looks for key "key" inside mapping "weird"
```

`Keys()` will list such a key, which makes the situation visible but not fixable. If you
control the file, do not put a dot in a key name. If you do not, read the subtree with
`View.Unmarshal` or `Value[map[string]any]` on the parent and index it yourself.

## List elements cannot be addressed by index

Paths walk mappings only. There is no `servers.0.host` syntax:

```yaml
servers:
  - name: a
  - name: b
```

```go
view.Get("servers")         // []any{map[string]any{"name":"a"}, …}
view.Get("servers.0.name")  // nil
```

Read the whole sequence and index it in Go, or decode it into a struct with
`Value[[]Server]` or `View.UnmarshalKey`.

## What counts as a leaf

`Keys()`, provenance and schema validation all work on **leaf** paths. A leaf is any value
that is not a populated mapping:

| Value in the file | Leaf? | Appears in `Keys()` |
|---|---|---|
| `port: 8080` | yes | `server.port` |
| `hosts: [a, b]` | yes — a sequence is a leaf value | `hosts` |
| `empty: {}` | yes — an empty mapping is a value, just not entries | `empty` |
| `server: {host: …, port: …}` | no — it is assembled from its children | its children, not `server` |

`View.Has("server")` is still true for a populated mapping — presence and leafness are
different questions. What a populated mapping does *not* have is a single origin, so
`Origin` returns false for it and `Explain` says it is "a subtree assembled from" the
sources that contributed.

## How two layers merge, by value shape

Layers fold in precedence order, lowest first, and the rule depends on what the higher layer
holds at that path:

| Higher layer holds | Result |
|---|---|
| A mapping, over a mapping | Merged key by key, recursively. Siblings the higher layer does not mention survive. |
| A mapping, over a scalar | The scalar is replaced. Its provenance is dropped. |
| A scalar, over a mapping | The whole subtree is replaced, and the provenance of every key beneath it is dropped — those keys are no longer reachable. |
| A sequence, over anything | Replaced wholesale. Sequences are **never** appended, merged or de-duplicated. |
| An empty mapping, over a mapping | Replaced — an empty container is a value, so it wins as any scalar would. |

The sequence rule is the one that surprises people. There is no way to add one item to a
list from an overlay: an overlay that mentions the list at all replaces it entirely.

See [Precedence & merge model](../explanation/precedence-and-merge.md) for why it is built
this way.

## Filter patterns for `Allow` and `Deny`

`config.Filtered` bounds what a backend contributes. Its patterns are dotted paths with two
wildcards — deliberately not regular expressions, because a mistake in a regexp silently
*widens* a filter, which is the wrong direction when what it holds back is a secret.

| Token | Matches |
|---|---|
| `name` | Exactly that segment. |
| `*` | Exactly one segment, whatever it is. |
| `**` | One or more remaining segments. Only meaningful as the last segment; anything written after it can never match. |

A pattern with no `**` must have exactly as many segments as the path it matches.

### Worked matches

Against a backend contributing `db.host`, `db.password`, `db.primary.host`, `top` and
`empty`:

| Filter | Keys the backend contributes |
|---|---|
| *(no options)* | `db.host`, `db.password`, `db.primary.host`, `empty`, `top` |
| `Allow("db")` | *(none)* |
| `Allow("db.*")` | `db.host`, `db.password` |
| `Allow("db.**")` | `db.host`, `db.password`, `db.primary.host` |
| `Allow("*")` | `empty`, `top` |
| `Allow("**")` | everything |
| `Deny("db.password")` | everything except `db.password` |
| `Allow("db.**")` + `Deny("db.password")` | `db.host`, `db.primary.host` |

### Patterns match leaf paths, not subtree roots

`Allow("db")` matches nothing, and that is the rule rather than a bug. Patterns are tested
against **leaf** paths only; the intermediate mappings on the way to a leaf are never
themselves offered to the matcher. `db` is not a leaf, so no leaf path equals it.

To keep a subtree, write `Allow("db.**")`. To keep only its direct children, write
`Allow("db.*")`.

### Deny beats Allow, and no Allow means "everything"

- With no `Allow`, every key is permitted unless a `Deny` excludes it.
- With any `Allow`, a key must match at least one of them.
- A `Deny` match excludes the key regardless of any `Allow`. Someone who wrote both has
  expressed a narrowing intention twice, and resolving the overlap the other way would let
  an `Allow` re-expose something explicitly denied.
- Several `Allow` options, or several patterns in one call, union.
- `Filtered` with no options returns the backend unchanged, so the zero configuration costs
  nothing.

### What filtering does not do

Filtering happens **after** the backend has fetched and parsed. A denied key is still read
from the remote system, still crosses the network, and still sits in memory briefly. It is
a visibility bound, not a fetch optimisation and not a security control against the backend
itself.

## Related

- [Precedence & merge model](../explanation/precedence-and-merge.md) — why merging is
  per-leaf and deep, and why sequences replace
- [Bound what a backend contributes](../how-to/filter-a-backend.md) — the task guide for
  `Allow` and `Deny`
- [Environment variables](environment-variables.md) — how a variable name becomes one of
  these paths
- [Errors](errors.md#errinvalidpath) — what a malformed path returns

---
title: Reference
description: The exact rules — key syntax, environment-variable mapping, struct tags, error values, defaults and limits.
tags: [reference]
---

# Reference

Facts to look up rather than read through: the exact syntax of a key, the algorithm that
turns an environment variable into one, every struct tag that means something, every error
value and what causes it, every default and every hard limit.

Everything here is checked against the source of the module it describes. Where a rule has
an edge, the edge is stated.

## What is in this section

| Page | Answers |
|---|---|
| [Keys and paths](keys-and-paths.md) | How a dotted path is parsed, how keys are cased, how layers merge per value shape, and what the `Allow`/`Deny` filter patterns mean. |
| [Environment variables](environment-variables.md) | How a variable name becomes a configuration key, what the prefix does, when a name is ambiguous, and what type the value arrives as. |
| [Struct tags](struct-tags.md) | The tags validation reads, the tags section decoding reads, which are honoured and which are silently ignored. |
| [Errors](errors.md) | Every exported error value: what returns it, what causes it, and what to do about it. |
| [Defaults and limits](defaults-and-limits.md) | Every default interval, file mode, and built-in bound, with the value and where it applies. |
| [Limitations](limitations.md) | What the module does not do, will not do, and cannot do — collected in one place. |

## Where the Go API reference lives

The type-by-type, method-by-method API reference is on
**[pkg.go.dev](https://pkg.go.dev/gitlab.com/phpboyscout/go/config)**. It is generated from
the source it documents, so it cannot drift from it, and duplicating it here would create a
second copy that can.

This section covers what pkg.go.dev cannot: the surfaces a *user* writes rather than the
ones a *caller* calls. A dotted key in a YAML file, an environment variable name, a struct
tag, a glob pattern in a deny list — none of those are Go symbols, and none of them are
described by a function signature.

Two things that are Go symbols are documented here anyway, because looking them up by name
is how you actually meet them:

- **Error values**, in [Errors](errors.md). `errors.Is` needs the name, and knowing which
  call returns which error is not recoverable from the signature `(*Snapshot, error)`.
- **Defaults**, in [Defaults and limits](defaults-and-limits.md). A constant's value is on
  pkg.go.dev; which behaviour it governs, and what overrides it, is not.

## Reference for the adapter modules

Each format, filesystem and backend adapter ships as its own module with its own
constructor and its own options. Those are documented on the adapter's how-to page and in
its own pkg.go.dev entry — see [the adapter ecosystem](../explanation/adapters.md) for the
full list and its status.

The rules in this section apply to every adapter, because an adapter contributes ordinary
layers: keys are cased the same way, filter patterns mean the same thing, and the same
error values come back.

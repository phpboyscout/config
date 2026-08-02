---
title: Environment variables
description: How a variable name becomes a configuration key — the prefix rule, the underscore-to-dot mapping, ambiguity, and what type the value arrives as.
tags: [reference, environment, env]
---

# Environment variables

`config.WithEnv("MYTOOL")` adds a layer that contributes environment variables. This page is
the exact mapping: which variables are seen, what key each becomes, and what the value is
once it arrives.

## What the prefix does, and why it is required

The prefix is a security control, not tidiness. Without one, any environment variable
matching a configuration key could silently change behaviour — on a shared CI runner, a
container host or a multi-tenant box, an unrelated process exporting `LOG_LEVEL` would
reconfigure every tool running there. With a prefix, unprefixed variables cannot reach
configuration at all.

The prefix you pass is normalised before use:

| You pass | Scanned prefix | Notes |
|---|---|---|
| `"mytool"` | `MYTOOL_` | Upper-cased. |
| `"MYTOOL"` | `MYTOOL_` | Unchanged. |
| `"mytool_"` | `MYTOOL_` | **One** trailing underscore is trimmed. |
| `"mytool__"` | `MYTOOL__` | Only one underscore is trimmed, so this scans a double underscore. Almost certainly a typo. |
| `""` | *(none)* | The layer contributes **nothing at all** — it does not fall back to scanning the whole environment. |

Pass the prefix without a trailing underscore. A variable named exactly `MYTOOL_`, with
nothing after the separator, is skipped.

## How a variable name becomes a key

For each variable whose name starts with `PREFIX_`, the prefix and separator are stripped
and the remainder — the *suffix* — is resolved against the keys the layers **below** this one
already define:

1. **Exactly one known key spells itself as this suffix** — that key is used. A key spells
   itself by upper-casing it and replacing every `.` with `_`, so `server.port` spells
   itself `SERVER_PORT`.
2. **No known key matches** — the suffix is lower-cased and *every* underscore becomes a
   dot. `MYTOOL_LOG_LEVEL` becomes `log.level`.
3. **More than one known key matches** — the variable cannot say which was meant, and the
   load fails with [`ErrAmbiguousEnvKey`](errors.md#errambiguousenvkey) naming the
   candidates.

### Why the fallback needs the resolution step

`MYTOOL_SERVER_PORT` could mean `server.port` or `server_port`, and nothing in the name says
which. Resolving against the keys that already exist removes the guesswork where it matters,
because a variable is nearly always overriding something a file already defines.

```yaml
# config.yaml
a:
  b_c: 0
```

```text
MYTOOL_A_B_C=1   →  a.b_c   (the existing key wins over the a.b.c fallback)
```

With no such file, the same variable becomes `a.b.c`.

### Ordering matters, because "below" is literal

"Known keys" means the leaf keys of the layers added **before** the environment layer. Add
`WithEnv` after your files, which is what you want anyway — environment variables are
expected to override what is on disk, and precedence follows the order backends are added.

Add it first and there is nothing below it, so every variable falls through to rule 2 and
every underscore becomes a dot.

### When a name is ambiguous

Two existing keys that spell themselves identically make a variable unanswerable:

```yaml
a_b:
  c: 1
a:
  b_c: 2
```

```text
MYTOOL_A_B_C=x
```

```text
config: environment variable is ambiguous: it could mean a.b_c or a_b.c: MYTOOL_A_B_C
```

The load fails rather than choosing. Choosing would be worse than it sounds: the candidates
arrive in map iteration order, so the winner would vary between runs of the same program
with the same environment. Rename one of the keys, or drop the variable and set the value in
a file.

## What a variable contributes

| Property | Value |
|---|---|
| Layer per variable | One. Ten matching variables contribute ten layers, all at the environment backend's position in the precedence order. |
| `Source.Kind` | `SourceEnv` |
| `Source.Name` | The full variable name, including the prefix — `MYTOOL_SERVER_PORT`. |
| Rendered in provenance as | `env:MYTOOL_SERVER_PORT` |
| `Source.Writable` | `false` |
| Value type | **Always a string.** The environment holds text and nothing else. |
| Backend `ID()` | `env:MYTOOL` |

Variables are read in sorted order, so when two of them map onto overlapping key paths the
winner is the same on every run. The process environment block itself has no defined order.

## Consequences of the value always being a string

**Reads coerce; validation does not.** `GetInt("server.port")` reads `"9090"` as `9090`. A
schema field declared `int` and supplied by an environment variable fails its type check
with `expected type int but got string` — see
[Struct tags](struct-tags.md#what-the-type-check-actually-compares).

**Booleans follow the reader's rules.** `GetBool` accepts `1`, `t`, `T`, `TRUE`, `true`,
`True` and their false equivalents. Anything else reads as `false` rather than as an error.

**An empty variable is an empty string, not an absent key.** `MYTOOL_LOG_LEVEL=` makes
`log.level` present with the value `""`. `View.Has` reports it as set, and a
`validate:"required"` string rejects it — an empty string is the one case `required` treats
as missing, because YAML writes an absent value the same way.

## What the environment layer cannot do

- **It is never a write target.** Persisting to it would not survive the process, so routing
  skips it entirely and a write lands in the file beneath. If the variable still outranks
  that file, `Plan` reports the write as shadowed. See
  ["Written, but it still does not take effect"](../how-to/write-config.md#written-but-it-still-does-not-take-effect).
- **It is not watched.** Environment variables do not change under a running process, so
  `Store.Watch` does not cover them. `Store.Reload` re-reads them.
- **It cannot be governed by strict mode.** `Source.Authored()` is false for the
  environment, so an unrecognised prefixed variable never counts as an unknown key. This is
  deliberate: an orchestrator exporting `MYTOOL_VERSION` or `MYTOOL_HOME` must not be able to
  stop the application starting.
- **It has no nesting syntax beyond underscores.** There is no way to express a list element
  or a key containing a literal underscore that resolution cannot disambiguate.

## Testing without touching the process environment

Process environment is global state, so mutating it makes parallel tests interfere.
`WithEnviron` supplies the variables instead — it takes a function returning entries in
`NAME=value` form, exactly as `os.Environ` does:

```go
store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "config.yaml"),
	config.WithEnv("MYTOOL", config.WithEnviron(func() []string {
		return []string{"MYTOOL_SERVER_PORT=9090"}
	})),
)
```

## Related

- [Load & merge configuration](../how-to/load-and-merge.md#read-the-environment) — the task
  guide
- [Precedence & merge model](../explanation/precedence-and-merge.md#the-environment-layer-resolves-names-against-the-keys-beneath-it)
  — why resolution against lower layers exists
- [Keys and paths](keys-and-paths.md) — the dotted-path grammar a variable maps onto
- [Errors](errors.md#errambiguousenvkey) — the ambiguity failure

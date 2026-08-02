---
title: Errors
description: Every error value config exports — what returns it, what caused it, and what to do about it.
tags: [reference, errors]
---

# Errors

Every error this module returns wraps one of the sentinel values below, so `errors.Is`
works on all of them:

```go
if errors.Is(err, config.ErrConflict) {
	// the file changed under us — re-read and retry
}
```

The tables give the message prefix each value carries, so an error you have in a log can be
matched back to a name.

## Which errors you must handle

Most callers need three:

| Situation | Error | What to do |
|---|---|---|
| Building a store | [`ErrInvalidConfig`](#errinvalidconfig) | The store is still usable. Fail fast in a service; carry on in a repair tool. |
| Writing | [`ErrConflict`](#errconflict) | Something else changed the file. Reload and re-plan. |
| Writing | [`ErrSensitiveLeak`](#errsensitiveleak) | You are about to write a secret into a plain file. Do not retry — fix the target. |

Everything else is either a programming mistake caught early, or an environment failure you
would report rather than recover from.

## Construction and validation

### `ErrNoSources`

`config: no sources configured`

Returned by `NewStore` when no backend was supplied. A store with nothing to read has
nothing to serve, so it is refused rather than returned empty. Add at least one of
`WithFiles`, `WithReaders`, `WithEnv`, `WithFlags` or `WithBackend`.

### `ErrInvalidConfig`

`config: configuration is not valid`

Returned when a candidate configuration fails schema validation. It comes from four places:

- `NewStore`, when the first load parses but violates the schema. **The store is returned
  alongside the error and is usable** — a tool whose job is to repair configuration needs
  something to repair it through.
- `Store.Reload`, when a reload fails validation. The previous configuration stays live.
- `Store.Apply`, when a change would *introduce* a violation. Violations that were already
  there do not block the write and are reported separately as pre-existing.
- `ValidateStruct[T]`, which wraps whatever the schema objected to.

A service should fail fast on it. A repair tool checks for it specifically:

```go
store, err := config.NewStore(ctx, opts...)
if err != nil && !errors.Is(err, config.ErrInvalidConfig) {
	return err // genuinely unusable: no sources, unreadable, or unparseable
}
```

### `ErrEmptySchema`

`config: schema has no fields defined`

Returned by `NewSchema` and `SchemaOf[T]` when tag parsing produced no fields — usually
because the struct's fields carry no `config` tag, or carry `config:"-"`. A schema that
constrains nothing would validate everything, so it is refused rather than accepted as a
no-op. See [Struct tags](struct-tags.md#validation-tags).

### `ErrNoMergeFunc`

`config: section defaults require a merge function`

Returned when a typed section supplies defaults but no way to combine them with the
configured values, and the section exists in the configuration. Silently preferring one over
the other would drop half the settings without saying so. Pass a merge function to
`WithSectionDefaults` or `WithSectionDefaultFunc`.

## Reading and decoding

### `ErrAmbiguousEnvKey`

`config: environment variable is ambiguous`

Returned during load when a set environment variable could designate more than one existing
configuration key — `MYTOOL_A_B_C` where both `a.b_c` and `a_b.c` exist. The message names
both candidates and the variable.

Honouring it would mean guessing, and the guess would vary between runs of the same program.
Rename one of the keys, or set the value in a file instead. See
[Environment variables](environment-variables.md#when-a-name-is-ambiguous).

### `ErrInvalidTarget`

`config: invalid target`

One error value covering four "you cannot ask that of this" cases:

| Where | Cause |
|---|---|
| `View.Unmarshal`, `View.UnmarshalKey`, `Value[T]` | The decode target is nil, is not a pointer, or is a nil pointer. |
| `Store.AddLayer` | The layer was given no name; a layer needs one to appear in provenance. |
| `Store.Apply`, `Store.Plan` with `To(…)` | The pinned name matches no writable source. |
| A nested store's write path | The edit named a layer the aggregate does not carry. |

`Store.WritableTargets()` lists exactly the names `To` accepts.

## Writing and routing

### `ErrNoChanges`

`config: no changes to apply`

Returned by `Store.Plan` and `Store.Apply` when called with no changes. Guard the call site
rather than treating it as a failure.

### `ErrInvalidPath`

`config: invalid path`

Returned when a change names a malformed dotted path: blank, or with an empty segment
(`a..b`, `.a`, `a.`). See [Keys and paths](keys-and-paths.md#what-a-valid-dotted-path-looks-like).

Reads do not return this — an unresolvable path reads as the zero value.

### `ErrNoWritableLayer`

`config: no writable layer for change`

Returned when a change has nowhere to go: every layer that could hold it is read-only. A
store built only from `WithReaders`, `WithEnv` and `WithFlags` can never accept a write.
Add a file backend, or a backend that implements `WritableBackend`.

### `ErrNotWritable`

`config: backend is not writable`

Returned during `Apply` when a change was routed at a backend that cannot persist. Where
`ErrNoWritableLayer` means "nowhere at all", this means "the chosen place refused" — most
often a pinned target inside a nested store that was not promoted with `NestedPromotable`.

### `ErrSensitiveLeak`

`config: refusing to write a sensitive key into a non-sensitive layer`

Returned when a write would land a key that a `Sensitive` backend defines into a layer that
is not itself sensitive — writing secret-category material into a plain file.

Secrets backends are read-only, so a write to a key one of them owns routes *down* to the
next writable layer. Refusing it is what keeps that safe. It also fires for a key a
sensitive backend holds but a [filter](keys-and-paths.md#filter-patterns-for-allow-and-deny)
is hiding, because a deny list must not quietly turn a secret into a plaintext write.

**Pinning the target does not opt out.** This is a safety invariant, not a routing
preference. A removal is exempt, because a removal writes no value and so cannot leak one.

### `ErrConflict`

`config: source changed since it was read`

Returned when a source changed between being read and being written, so committing would
silently discard someone else's work. Detection is by content hash rather than modification
time, because timestamp granularity varies and is coarse on some filesystems.

This is detection, not prevention — the most that is portably achievable without lock files.
Reload, re-plan against the new state, and retry.

### `ErrPartialCommit`

`config: commit partially applied`

Returned when a write spanning several sources failed partway through and the already
committed parts could not be fully rolled back. The message always names what is in which
state; a caller is never left guessing.

There is no automatic recovery. Read the message, inspect the named sources, and reconcile
by hand.

### `ErrWriteFromObserver`

`config: cannot change configuration from inside an observer`

Returned by `Store.Apply`, `Store.AddLayer` and `Store.Reload` when called from inside an
observer callback. Each such write is itself a change, which notifies, which runs the
observer again — a cascade with no natural end.

Take the change *out* of the observation: record what is needed, return, and write from
somewhere else — a worker goroutine, a queue, the next tick. Serialising and de-duplicating
those deferred writes belongs to you, because you are the only party that knows which of
them still matter.

## Backends, files and watching

### `ErrBackendParse`

`config: source could not be parsed`

Returned when a source is not valid for its format. The message names the file and, for a
multi-document source, the document index. It surfaces from `NewStore`, from `Store.Reload`
and from a write that re-reads the file.

A reload that hits it is rejected outright and the last known good configuration stays live.

### `ErrBackendUnsafe`

`config: source cannot be safely edited`

Returned at load when a source contains a construct that cannot be round-tripped, so editing
it would risk corrupting it. For YAML the case is a multi-line flow collection with interior
comments: the closing delimiter is swallowed into the comment, producing YAML no parser will
accept.

`NewStore` refuses such a source up front, naming the file and the offending construct,
rather than letting you discover it at commit time with nowhere to put your edits. Reformat
the collection onto one line, or move the comments out of it. See
[What survives a write](../explanation/write-fidelity.md#some-documents-are-refused-at-load).

### `ErrReadOnlyFS`

`config: filesystem is read-only`

Returned by the write methods — `WriteFile`, `Rename`, `Remove`, `MkdirAll` — of a
filesystem adapter over an inherently read-only source, such as an `io/fs.FS` or an HTTP
URL. Nothing in this module returns it; the adapter modules do.

It exists so a caller can `errors.Is` it, rather than getting a bare `fs.ErrPermission` that
a genuine permission failure is indistinguishable from.

### `ErrWatchUnavailable`

`config: cannot watch sources`

Returned by `Store.Watch` when watching cannot work for the sources given — no watchable
backend, or a polling watcher handed nothing to watch.

It is deliberately loud. A watcher that silently does nothing is worse than none: the
application believes it will hear about changes and never will.

### `ErrCyclicStore`

`config: store is already present in this graph`

Returned when composing stores would put one store into the graph twice, whether or not that
closes a cycle. Two copies of the same store would contribute their layers at two
precedences, so every value would be shadowed by a copy of itself.

Enforcement is by store identity alone, which leaves no room for a reachability analysis to
be subtly wrong.

### `ErrDuplicateLayer`

`config: two layers in the composed store are indistinguishable`

Returned when two layers in a composed store are equal in kind, name and document, so nothing
downstream can tell them apart. The observable symptom it prevents is a wrong plan: a key
defined only in the hidden copy is invisible to shadow detection, so a write is reported as
effective when it is not.

Two stores pointed at the same file is a composition mistake. Point one of them somewhere
else.

### `ErrInternal`

`config: internal invariant violated`

An invariant of this module does not hold. It is never the caller's fault and is always worth
reporting — [open an issue](https://gitlab.com/phpboyscout/go/config/-/issues) with the
message and, if you can, the configuration that produced it.

## Related

- [Write configuration](../how-to/write-config.md#errors-worth-branching-on) — the write
  errors in the context of a task
- [Hot-reload safety](../explanation/hot-reload-safety.md) — why a rejected reload changes
  nothing, and why writing from an observer is refused
- [Limitations](limitations.md) — what the module will not do, of which several of these
  errors are the enforcement

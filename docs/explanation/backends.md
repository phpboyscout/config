# Backends and capabilities

A backend is a source of configuration. The Store owns everything about
coordination — merging, provenance, snapshot construction, notification,
serialising access — and a backend owns only the knowledge of how to read, and
where relevant write and watch, its own sources.

That division is the whole design. If coordination ever becomes per-backend, the
fragility a single owner removes returns once per backend.

## What a backend can do is answered by the type system

There is no flag saying whether a backend can be written to or watched. Those
are answered by whether it implements the optional interfaces:

```go
type Backend interface {                     // every backend
	ID() string
	Load(ctx context.Context, below []Layer) ([]Layer, error)
	Capabilities() Capabilities
}

type WritableBackend interface {             // optional
	Backend
	Prepare(ctx context.Context, edits []Edit) (Pending, error)
}

type WatchableBackend interface {            // optional
	Backend
	Watch(ctx context.Context, interval time.Duration, onChange func()) (func(), error)
}
```

The reason is not tidiness. A flag can disagree with the code: a backend could
report itself writable and have no way to persist, or report itself unwritable
and implement `Prepare` anyway. Neither state means anything, and a caller
checking the flag and a caller checking the method would then disagree about the
same backend. Asking the type system settles it once, at compile time, and a
backend cannot claim one thing and do another.

This module learned that the hard way. An earlier version carried a `Writable`
flag *as well as* the interface, and writability ended up being decided three
different ways — so a backend could be excluded as a write target while its file
was missing and routed the moment it existed. The same source was writable or
not depending on whether it happened to exist yet.

## A file's format is a codec, by the same reasoning

Everything a *file* backend does — reading bytes, translating a missing file,
fingerprinting content for conflict detection, staging to a temporary path,
committing by atomic rename, preserving mode, resolving symlinks, rolling back,
watching — is identical whatever format the file holds. Only two things depend
on the format: turning bytes into values, and editing bytes in place. Those two
are a `Codec`:

```go
type Codec interface {                       // every file format
	Decode(path string, src []byte) ([]map[string]any, error)
}

type EditingCodec interface {                // optional
	Codec
	Check(path string, src []byte) error
	Apply(path string, src []byte, edits []Edit) ([]byte, error)
	Empty() []byte
}
```

This is the same capability split as `Backend`/`WritableBackend`, one level down.
A format that cannot be round-tripped safely implements only `Decode`, and
`NewCodecBackend` returns a backend that is *not* a write target — routing skips
it and a write lands in the next writable layer down, reported as shadowed rather
than failing. A format that can be edited implements `EditingCodec`, and the
backend it produces is writable. The type system decides, not a flag, exactly as
above.

```go
config.WithBackend(config.NewCodecBackend(fsys, "/app.json", jsonCodec{}))
```

YAML is the one codec the core ships, because it is the default and its
comment-preserving editor is the module's headline feature. Every other file
format is a sibling `config-<format>` module supplying its own codec, so a
consumer who needs JSON does not acquire a TOML parser in their dependency graph.
Writing a codec is how a new file format is added without touching this module:
the file machinery — including the conflict-detection subtlety that is easy to
get wrong, where the fingerprint must be taken at load rather than at write — is
written once and shared, rather than reimplemented and mis-implemented per format.
→ [Write a custom backend](../how-to/custom-backend.md) · the
[non-YAML format adapters spec](../development/specs/2026-07-20-non-yaml-format-adapters.md)

## What `Capabilities` is for, and what it is not

`Capabilities` does not describe what a backend *can do*. It describes
properties of a backend that the rest of the system has to reason about when
several backends of different kinds are in play:

| Field | What it declares |
|---|---|
| `PreservesComments` | Whether an edit retains comments and formatting. True for document-like sources; meaningless for a key-value store, which has nowhere to put a comment. |
| `AtomicMultiKey` | Whether several keys can be written as one indivisible operation. |
| `NativeWatch` | Whether the backend can report foreign changes itself, or has to be polled. |
| `Sensitive` | Whether it holds secret material. |

`Sensitive` is enforced; the rest are declared ahead of the backend that will
read them, because each carries a consequence that is much cheaper to record now
than to discover later:

- **A value from a `Sensitive` backend must never be written into a layer that
  is not.** This is a security property. Configuration written back from a
  secret store into a plain file is how credentials end up in version control.
  The write path enforces it: because secrets backends are read-only, a write to
  a key one owns routes down to the next writable layer — a plain file — so the
  core refuses that write with `ErrSensitiveLeak` rather than let the secret land
  there.
- **The comment guarantee is document-backend-only.** Everything this module
  says about preserving comments applies to YAML files. A key-value backend
  cannot honour it, so the promise has to be scoped rather than implied.
- **Cross-backend atomicity is impossible.** A change spanning a file and a
  remote parameter store cannot be atomic. That has to be either refused or
  declared plainly, never papered over.
- **Foreign-change latency differs per backend** and has to be stated, so an
  application knows whether it will hear about a change in milliseconds or at
  the next poll.

If those seem like a lot of unread fields, that is the point: they are the
questions a second backend will force, written down while the answers are
obvious rather than when something is already broken.

## `Load` receives what is beneath it

```go
Load(ctx context.Context, below []Layer) ([]Layer, error)
```

Most backends ignore `below`. One kind cannot: a backend whose reading of its
own input depends on what is already defined.

The environment backend is that case. `APP_SERVER_PORT` could mean `server.port`
or `server_port`, and nothing in the variable name says which — so it resolves
the name against the keys the lower-precedence layers already define.

That dependency used to be satisfied by a separate call made before `Load`,
which the Store had to remember to make. It was forgotten once, and the
consequence was subtle: a write could pass validation, land on disk, and then
break the reload it triggered, leaving the file changed and the process running
on last-known-good.

Passing it as an argument removes the possibility. There is no separate step to
forget, because there is no separate step.

## Related

- [The Store](the-store.md) — why one component owns configuration I/O
- [Provenance](provenance.md) — how a layer's identity is recorded and reported
- [Precedence and the merge model](precedence-and-merge.md)

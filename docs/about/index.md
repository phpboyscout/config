# What it does, and why

The [landing page](../index.md) is the short version. This is the whole case: what each
part does, why it is built that way, and what it measurably costs or saves.

Read it end to end if you are deciding whether to adopt the module, or jump to the part
you are weighing up.

## Reading: answers, not just values

Any library can hand you a value. Three things are harder, and all three are things you
have wanted at two in the morning.

**"Why is this value 9090?"** Provenance is recorded *during* the merge, not reconstructed
afterwards, so the question has a real answer:

```go
fmt.Println(view.Explain("server.port"))
// server.port = 9090 (from /home/me/.mytool/config.yaml); also defined in embedded:defaults.yaml
```

`Origin` names the layer that supplied a value, `Shadowed` lists every layer defining it,
and `Keys` enumerates the lot. "Which file do I edit?" and "why is my edit not taking
effect?" are the same question from two directions, and both are answerable.
→ [Provenance](../explanation/provenance.md)

**"Do these two values agree with each other?"** A `View` is pinned to one immutable
snapshot, so a sequence of reads cannot land either side of a reload and return a
configuration that never existed. That is not a claim about care taken; it is structural,
and it is measured — two related keys read while the file changes underneath, hammered for
a second:

| | reads | mismatched pairs |
|---|---|---|
| a library reading live mutable state | 6,764,880 | **1,759** |
| `config` | 11,639,791 | **0** |

A mismatched pair here means `host` from before a reload and `port` from after — a host
and port that were never configured together, about to be dialled. The test that produces
the second row is
[in the suite](https://gitlab.com/phpboyscout/go/config/-/blob/main/coherence_test.go) and
fails the build if it ever stops being true.

**"What happens when the new config is broken?"** Reloading is fail-closed. A candidate
that will not parse, or violates your schema, is rejected — the last-known-good
configuration stays live, and you never read a mixture of before and after. A watcher that
cannot function returns an error rather than silently doing nothing.
→ [Hot-reload safety](../explanation/hot-reload-safety.md)

Beyond that, the ordinary things done deliberately:

- **Precedence is the order you added the sources**, readable off the call site, rather
  than a fixed ranking baked into the library that you have to look up and cannot change.
- **Read any type, including your own.** `Value[T]` reads whatever `T` is — durations, IP
  addresses, URLs, timezones, and anything implementing `encoding.TextUnmarshaler`, so
  your own enums and domain types decode without this module having heard of them.
  → [Read configuration values](../how-to/read-values.md)
- **Typed sections that stay current.** `ObserveSection[T]` decodes a subtree onto your
  struct and republishes it across reloads, delivering only when the struct actually
  changed. Each snapshot decodes in one operation, so it can never hold some fields from
  before a reload and some from after. A package consuming those settings declares a
  one-method interface over its own struct and **never imports this module at all**.
  → [Typed sections](../how-to/typed-sections.md)
- **No global singleton, and tests that do not fight each other.** There is no package-level
  instance to configure; stores are values. Read the environment from a function instead of
  the process, the filesystem from `config.Dir(t.TempDir())` or your own `config.FS`, and
  use the published mocks — so config-dependent
  tests run in parallel without touching global state. → [Test with the mocks](../how-to/test-with-mocks.md)
- **The environment prefix is required**, and it is a security control rather than
  tidiness. Without one, any variable matching a configuration key could reconfigure your
  tool — on a shared CI runner or multi-tenant host, an unrelated process setting
  `LOG_LEVEL` would reach every program running there. Ambiguous variable names are
  reported rather than resolved in map-iteration order.

## Reacting to change: told once, told in order, told the truth

The usual shape for hot-reload is a callback handed a **filesystem event**. That leaves
every hard question to you: the event says a file was touched, not what changed, not
whether the result was usable, and not whether it is still current by the time you read
it. You are expected to re-read from live state — which may already have moved on again.

An observer here is handed a **configuration**, not an event, and the guarantees around
that delivery are the point:

| Guarantee | What it means |
|---|---|
| **Exactly once per logical change** | One `Apply` touching four keys across three files is *one* notification, not four or three. Not once per filesystem event. |
| **Never for a change that was rejected** | A file that will not parse, or fails your schema, notifies nobody. Announcing it would be a lie: the values did not move. Rejections travel on `OnReloadError` instead. |
| **Never for a change that changed nothing** | A write that leaves the resolved configuration identical does not notify, even though the file really was rewritten. |
| **In order, always** | Observers are never handed an older snapshot after a newer one. Under concurrent writes, a superseded snapshot is dropped rather than delivered late — each snapshot is complete, so the newer delivery has already said everything the older one would have. |
| **Pinned for the whole callback** | Every observer sees one immutable snapshot, so it cannot read half of one configuration and half of the next partway through reacting. |
| **One observer failing does not silence the others** | Errors are collected and routed to `OnObserverError`; the remaining observers still run. |
| **Writing from inside an observer is refused** | `ErrWriteFromObserver`, rather than a cascade with no natural end. Capture what you need, return, and write from elsewhere. |

The ordering and exactly-once properties are structural — delivery is serialised and
version-checked in one place — rather than something each observer has to defend itself
against. That matters, because an observer *cannot* defend itself: by the time an older
snapshot arrives, it has no way of knowing a newer one already did.

!!! info "The limit of exactly-once, and what is done about it"
    Exactly-once is per logical change **as the Store understands it**. An `Apply` is one
    logical change however many files it touches, because the Store performs it and knows
    the batch.

    Two files changed by *something else* are separate events, and nothing in the
    filesystem says they were meant as one. So foreign changes are coalesced: the Store
    waits for the burst to settle — 250ms by default, set with `WithSettleInterval` —
    before reloading once.

    **That reduces the problem rather than removing it.** Writes spaced further apart than
    the window look exactly like two separate changes, so observers run twice and the
    first run sees a combination nobody intended. Each snapshot is still internally
    coherent — a real read of the files at a moment, never a mixture of two reads — so no
    value is ever torn; what you can see is a real state nobody meant to exist.

    A change spanning several files is atomic only if whoever makes it makes it atomically.
    Keep settings that change together in **one file**, which is always read atomically, or
    swap the whole directory at once as a Kubernetes ConfigMap update does. Both the
    coalescing and the residual case are asserted in the suite, so neither is folklore.

Change detection itself is hybrid and per-path: native OS notification where it genuinely
works, and polling where it does not — an in-memory filesystem, a network mount, a
container where inotify watches are exhausted. Each path is settled independently, so one
unwatchable file does not downgrade the rest. If native notification stops being
trustworthy mid-run, the affected paths fall back to polling and carry on. Only if polling
cannot be established either has the watch genuinely stopped working — and then you are
told. A watcher that has silently gone quiet is the one failure mode this design refuses.

!!! info "For comparison"
    [viper](https://github.com/spf13/viper) v1.21.0 calls `OnConfigChange` with the
    `fsnotify.Event`. Reading its watch loop, the callback is invoked **after a failed
    re-read as well as a successful one** — the parse error goes to viper's internal
    logger, and the callback is told "changed" regardless. Confirmed by running it: a file
    edited to invalid YAML still fires the callback, and the callback has no way to
    discover the reload failed.

    It retains the last good values, which is right. But an observer that reacts to a
    notification by re-reading, and rebuilds a connection pool or reopens a listener each
    time it is told something changed, will do that work for a change that did not happen.

All the guarantees above are asserted in
[`observer_contract_test.go`](https://gitlab.com/phpboyscout/go/config/-/blob/main/observer_contract_test.go).

## Writing: where it gets genuinely hard

Everything above is a better answer to a problem other libraries also address. This next
part is one most of them cannot address at all, because it is foreclosed by their
architecture.

The moment your program **writes** configuration back — a settings screen, a `--set` flag,
a first-run wizard, an `auth login` that stores a token — the file stops being an input and
becomes something you are responsible for. Here is a file with one setting changed. The
user changed `server.port`, and nothing else:

=== "Before"

    ```yaml
    # Which port the public listener binds to.
    # Changing this needs a firewall change too — talk to platform first.
    server:
      host: localhost   # loopback only in dev
      port: 8080

    # Feature flags. Keep alphabetical.
    features:
      beta_ui: false
    ```

=== "After — merged-view write"

    ```yaml
    db:
        password: hunter2-prod-secret
    features:
        beta_ui: false
    log:
        level: info
    server:
        host: localhost
        port: 9090
    ```

=== "After — `config`"

    ```yaml
    # Which port the public listener binds to.
    # Changing this needs a firewall change too — talk to platform first.
    server:
      host: localhost # loopback only in dev
      port: 9090

    # Feature flags. Keep alphabetical.
    features:
      beta_ui: false
    ```

    One value changed. Note the inline comment's *padding* was normalised —
    [alignment is not part of the contract](../explanation/write-fidelity.md#the-contract),
    retention is.

Every comment is gone. The key order is now alphabetical rather than meaningful. A default
the program never had written down has been materialised into the user's file, where it
will silently stop tracking future changes to that default.

And a production database password, which arrived from an environment variable and was
never in any file, is now sitting in one — very probably one that is in git.

!!! info "This is not a bug in anything"
    That output is what "serialise the merged view over the target file" means, and it is
    the ordinary behaviour of most configuration libraries. The one above is
    [viper](https://github.com/spf13/viper) v1.21.0 with `SetEnvKeyReplacer` configured
    the standard documented way, but the shape is not specific to viper — it follows from
    merging eagerly.

    Once every source has been folded into one map, the record of who contributed what is
    gone. A writer holding only that map has nothing to write *but* the whole of it. It
    cannot leave the environment's contribution out, because it can no longer tell which
    contribution came from the environment.

## What a write does instead

This module never folds its layers away, so a writer still knows which layer contributed
what — and can therefore change one key in one file and leave everything else alone:

- **Writes preserve what a human wrote.** `Apply` edits the target document in place, so
  comments stay attached to their keys, and order, quoting and block style survive.
  Repeated writes converge rather than drifting. Invisible bidirectional characters are
  escaped on the way out, because a config file is exactly where
  [Trojan Source](../explanation/write-fidelity.md#invisible-characters-are-escaped-on-write)
  matters. → [What survives a write](../explanation/write-fidelity.md)
- **A write lands in the layer that owns the key** — so the value you set is the value you
  read back, rather than being immediately shadowed by an overlay above it. Nothing from
  another layer comes along for the ride. → [Write configuration](../how-to/write-config.md)
- **A write that cannot take effect says so** instead of appearing to succeed. If an
  environment variable still outranks the file you just wrote, you are told — which is the
  single most common way a settings screen appears broken to its user.
- **`Plan` is a dry run that cannot drift**, because it *is* the routing pass `Apply`
  runs, not a second implementation of it that has to be kept in step.
- **Writing from inside an observer is refused outright**, so the write-notify-write
  cascade is unrepresentable rather than something you must remember to break.
- **Everything is a layer** — a file, one document within a multi-document file, embedded
  defaults, the environment, the flag set, something computed at runtime. All ordered by
  precedence, with no special case to remember, whether you are reading it or writing it.

## Any source can be a layer

Layers are not limited to files and readers. `WithBackend` takes anything satisfying a
three-method `Backend` interface, so a source this module has never heard of — Consul, a
secrets manager, an HTTP endpoint, a database table, a device's NVRAM — becomes an
ordinary layer:

```go
store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),
	config.WithBackend(consulBackend{client: c, prefix: "app/"}),  // ← outranks the file
	config.WithEnv("APP"),
)
```

It takes part in precedence, provenance and shadowing exactly as a file does. `Explain`
will name it. A file-based write that your backend shadows is reported as shadowed. There
is no second-class tier for sources the library did not ship.

Capability beyond reading is **opt-in through interfaces you may simply not implement**:

| Implement | To get |
|---|---|
| `Backend` | reading, merging, precedence, provenance — the whole read surface |
| `WritableBackend` | `Apply` can route writes to it |
| `WatchableBackend` | it takes part in hot-reload, calling `onChange` on its own schedule |

A read-only Consul layer implements one interface. A backend with a real subscription
implements `WatchableBackend` and gets push-based reload without polling anything. Nothing
forces a source to pretend it can do something it cannot — which is the failure mode that
makes a single `Backend` interface with stub methods so unpleasant to live with.
→ **[Write a custom backend](../how-to/custom-backend.md)** · [Backends & capabilities](../explanation/backends.md)

A file in **another format** needs less again. The file machinery — reading, conflict
detection, atomic writes, symlink handling, watching — is format-agnostic and written once,
so a new format is a `Codec`, not a whole `Backend`: `Decode` to read, and `EditingCodec`
(`Check`/`Apply`/`Empty`) if it can be edited in place. `NewCodecBackend` turns a codec into
a backend, writable exactly when the codec can edit — the same capability split the type
system settles, one level down. YAML is the codec the core ships; every other format is a
sibling `config-<format>` module, so needing JSON never pulls a TOML parser into your graph.

## Validation on both sides, and repairable when it fails

A `Schema` built from `config:` struct tags is checked against the **resolved**
configuration rather than any single layer — a base file may legitimately omit a key its
overlay supplies, and judging layers separately would reject a perfectly good setup.

It runs at load, on every reload, and on every write. The write case is the interesting
one, because the obvious rule is the wrong one:

> `Apply` rejects only the violations **your change introduces**. Violations that were
> already there do not block the write, and are reported separately as pre-existing.

```
config: configuration is not valid: this change would make the configuration invalid:
  log.level: value "verbose" is not allowed (hint: allowed values: debug, info, warn, error)

the configuration was already invalid before this change, and these are unaffected by it:
  server.port: expected type int but got string (hint: ensure server.port has a value of type int)
```

Validating the result outright would lock an already-broken configuration against edits —
including the edit that would fix it. Suppressing the pre-existing violations would hide
from the user that their file is broken in ways their change did not cause. Reporting them
without the disclaimer would send someone off debugging a change that was fine.

The same reasoning runs through `NewStore`, which hands back a usable store alongside
`ErrInvalidConfig` so a configuration that fails validation can still be repaired through
the surface designed to repair it. A configuration tool that cannot open a broken
configuration is of no use precisely when it is needed.
→ [Validate configuration](../how-to/validate-config.md)

## Quieter things that matter

None of these will sell the module on their own. Together they are most of what living
with it feels like.

**Generics all the way through, not bolted on.** `Value[T]`, `UnmarshalSection[T]`,
`ObserveSection[T]`, `ValidateStruct[T]`, `SchemaOf[T]`, `Section[T]`, `SectionChange[T]`
— one consistent shape rather than a generic escape hatch beside a hand-written method per
type. The practical consequence is future-proofing: a type this module has never seen needs
no new API, no new accessor, and no release. It reads through the same `Value[T]` as
everything else, and if it implements `encoding.TextUnmarshaler` it decodes without being
special-cased anywhere.

**A multi-document YAML file is several layers, one per document.** They take part in
precedence and provenance independently, and `Explain` distinguishes them:

```
shared = from-second (from /app.yaml#1); also defined in /app.yaml
```

Worth calling out because the common alternative is worse than unsupported: viper reads the
first document, silently discards the rest, and returns no error — so a key in your second
document simply is not there.

**Every failure is a named error you can branch on.** Seventeen sentinels — `ErrConflict`,
`ErrNoWritableLayer`, `ErrBackendUnsafe`, `ErrAmbiguousEnvKey`, `ErrPartialCommit` and the
rest — all matched with `errors.Is`, so handling a specific failure never means comparing
strings. Validation failures go further and carry a **hint**:

```
server.port: expected type int but got string (hint: ensure server.port has a value of type int)
```

**Context on every operation that does I/O.** `NewStore`, `Reload`, `Apply`, `AddLayer`
and `Watch` all take a `context.Context` and honour cancellation, so configuration work
participates in your shutdown path instead of ignoring it.

**A smaller dependency graph than the thing it replaces**, while doing more. Library
dependencies only, no test-only packages:

| | non-stdlib packages | distinct modules |
|---|---|---|
| viper 1.21.0 | 36 | 13 |
| `config` | **20** | **8** |

**The filesystem is an interface this module defines, not one it imposes.** `config.FS` is
six methods. `config.OS()` is the operating system; `config.Dir(path)` is backed by
`os.Root`, so every operation is confined to that directory and a path resolving outside it
— through `..`, an absolute path, or a symlink pointing away — is refused by the operating
system rather than by a check this module performs. A tool reading configuration from a
directory a user named gets that containment without asking. And a test needs no filesystem
dependency at all: `config.Dir(t.TempDir())` gives real file semantics, watching included.

**Section defaults with your own merge function.** `WithSectionDefaults` takes both the
defaults and the function that combines them with what was configured, so "merge" means
what your type needs rather than what a library guessed. `WithSectionEqual` does the same
for change detection.

**`Snapshot.Version()`** is a monotonic counter, so a component can tell whether the
configuration it is holding has moved on without comparing values. **`Sub`** scopes a view
to a subtree for a component that should not see the rest, and **`With`** pins one snapshot
across a block of reads.

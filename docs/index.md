# config

**Layered configuration for Go that can tell you where a value came from — and write one
back without wrecking the file.**

Assemble settings from embedded defaults, files, the environment and CLI flags with a
precedence you can read off the call site. Read them through immutable snapshots, so a
reload can never hand you half of one configuration and half of another. Ask of any key
which layer supplied it and what shadows it. And when your program saves a change, save it
to the layer that owns it — leaving every comment its author wrote exactly where it was.

```bash
go get gitlab.com/phpboyscout/go/config
```

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
→ [Provenance](explanation/provenance.md)

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
→ [Hot-reload safety](explanation/hot-reload-safety.md)

Beyond that, the ordinary things done deliberately:

- **Precedence is the order you added the sources**, readable off the call site, rather
  than a fixed ranking baked into the library that you have to look up and cannot change.
- **Read any type, including your own.** `Value[T]` reads whatever `T` is — durations, IP
  addresses, URLs, timezones, and anything implementing `encoding.TextUnmarshaler`, so
  your own enums and domain types decode without this module having heard of them.
  → [Read configuration values](how-to/read-values.md)
- **Typed sections that stay current.** `ObserveSection[T]` decodes a subtree onto your
  struct and republishes it across reloads, delivering only when the struct actually
  changed. Each snapshot decodes in one operation, so it can never hold some fields from
  before a reload and some from after. A package consuming those settings declares a
  one-method interface over its own struct and **never imports this module at all**.
  → [Typed sections](how-to/typed-sections.md)
- **No global singleton, and tests that do not fight each other.** There is no package-level
  instance to configure; stores are values. Read the environment from a function instead of
  the process, the filesystem from `afero`, and use the published mocks — so config-dependent
  tests run in parallel without touching global state. → [Test with the mocks](how-to/test-with-mocks.md)
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
    [alignment is not part of the contract](explanation/write-fidelity.md#the-contract),
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
  [Trojan Source](explanation/write-fidelity.md#invisible-characters-are-escaped-on-write)
  matters. → [What survives a write](explanation/write-fidelity.md)
- **A write lands in the layer that owns the key** — so the value you set is the value you
  read back, rather than being immediately shadowed by an overlay above it. Nothing from
  another layer comes along for the ride. → [Write configuration](how-to/write-config.md)
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

## At a glance

```go
store, err := config.NewStore(ctx,
	config.WithReaders(config.NamedSource{Name: "embedded:defaults.yaml", Content: defaults}),
	config.WithFiles(afero.NewOsFs(), "/etc/mytool/config.yaml", userPath),
	config.WithEnv("MYTOOL"),
	config.WithFlags(cmd.Flags()),
)
if err != nil {
	return err
}

// Precedence is the order you added the sources: flags beat env, env beats the
// user file, the user file beats /etc, and all of them beat the embedded defaults.
port := store.View().GetInt("server.port")

// Ask why it is what it is.
fmt.Println(store.View().Explain("server.port"))
// server.port = 9090 (from /home/me/.mytool/config.yaml); also defined in embedded:defaults.yaml

// Write it back to the layer that owns it, comments intact.
if _, err := store.Apply(ctx, config.Set("server.port", 9090)); err != nil {
	return err
}
```

The environment prefix is required, and it is a security control rather than tidiness.
Without one, any variable matching a configuration key could silently reconfigure your
tool — on a shared CI runner or a multi-tenant host, an unrelated process setting
`LOG_LEVEL` would reach every program running there.

## Should you use this?

**Yes, if any of these describe you** — and note that only the first is about writing:

- you have more than two sources and have lost an afternoon to "which one set this?";
- a long-running service reloads configuration, and you need reads that never straddle a
  reload, notifications you can trust, and a broken file that cannot take the service down;
- your program writes configuration back, and users have to live in the file afterwards;
- configuration lands in files that get committed, reviewed, or shared between people;
- you want configuration-dependent tests that run in parallel and do not reach for process
  globals;
- your settings have real types — enums, addresses, URLs — and you are tired of decoding
  them by hand.

**Probably not, if** one file and a couple of environment variables cover you, nothing is
ever written back, and nothing reloads. That is a large share of programs, and it is a
genuinely well-served case: [viper](https://github.com/spf13/viper) is battle-tested by an
enormous number of them, has an ecosystem this module does not, and will be a smaller
dependency in your graph. Reach for the smaller tool when the smaller tool fits — see
[History](about/history.md) for how much of what is here was learned from years of using
it.

## Where next

The documentation follows the [Diátaxis](https://diataxis.fr) quadrants: learn, then do,
then understand.

<div class="grid cards" markdown>

- :material-rocket-launch: **[Getting started](getting-started.md)** — the tutorial.
  Build a store over a file, read values, ask where they came from, write a change back,
  and react to one made outside your process.
- :material-file-tree: **[Load & merge](how-to/load-and-merge.md)** — files, embedded
  defaults, the environment, flags, and the precedence chain.
- :material-magnify: **[Read values](how-to/read-values.md)** — typed accessors, the
  generic `Value[T]`, scoped views and struct decoding.
- :material-content-save-edit: **[Write configuration](how-to/write-config.md)** —
  planning a write, where a change lands, conflicts, and shadowed writes.
- :material-code-braces: **[Typed sections](how-to/typed-sections.md)** — project a
  subtree onto your own struct with `UnmarshalSection` and `ObserveSection`.
- :material-reload: **[Hot-reload](how-to/hot-reload.md)** — `Watch`, observers, and
  reacting to foreign changes.
- :material-shield-check: **[Validate](how-to/validate-config.md)** — `Schema` and
  `ValidateStruct[T]` from `config:` struct tags.
- :material-test-tube: **[Test with the mocks](how-to/test-with-mocks.md)** —
  `MockReader`, `MockBinder` and `MockObserved` in your tests.
- :material-lightbulb-on: **[The Store](explanation/the-store.md)** — why one component
  owns config I/O, and what follows from that rule.
- :material-map-marker-path: **[Provenance](explanation/provenance.md)** — what `Origin`,
  `Shadowed` and `Explain` can and cannot answer.

</div>

## Coming from v0.2.x

v0.2.x had a different architecture and a different API. The
**[migration guide](migrating.md)** covers it step by step: `Containable` becomes
`Reader`, the several constructors become `NewStore`, and the handful of call sites that
are genuine ports rather than renames — writing, flag binding, and the old escape hatch —
each get their own section.

If your reusable packages declare their own one-method interface and take an
`ObservedSection[T]` structurally, they need no change at all. That decoupling boundary is
the reason the module exists, and it held.

For how the module got here, see [History](about/history.md).

## Reference

The Go API reference lives on
**[pkg.go.dev](https://pkg.go.dev/gitlab.com/phpboyscout/go/config)**, which is where a
language API reference belongs — generated from the source it documents, so it cannot
drift from it.

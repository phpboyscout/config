# config

**Layered configuration for Go that can write back.**

Assemble settings from embedded defaults, files, the environment and CLI flags with a
precedence you can read off the call site. Ask of any key where it came from and what
shadows it. Save a change to the layer that owns it — without destroying the file its
author wrote.

```bash
go get gitlab.com/phpboyscout/go/config
```

## The problem this exists to solve

Reading configuration is a solved problem. Every library does it well, and if reading is
all you need, you do not need this one.

It stops being solved the moment your program **writes** configuration back — a settings
screen, a `--set` flag, a first-run wizard, an `auth login` that stores a token. Here is a
file with one setting changed. The user changed `server.port`, and nothing else:

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

## What follows from keeping the record

This module never folds its layers away. A `Store` owns every read, write and watch, and
records provenance **during** the merge rather than trying to reconstruct it afterwards.
The rest is downstream of that one decision:

- **Writes preserve what a human wrote.** `Apply` edits the target document in place, so
  comments stay attached to their keys, and order, quoting and block style survive.
  Repeated writes converge rather than drifting. Invisible bidirectional characters are
  escaped on the way out, because a config file is exactly where
  [Trojan Source](explanation/write-fidelity.md#invisible-characters-are-escaped-on-write)
  matters. → [What survives a write](explanation/write-fidelity.md)
- **A write lands in the layer that owns the key** — so the value you set is the value you
  read back, rather than being immediately shadowed by an overlay above it. Nothing from
  another layer comes along for the ride. → [Write configuration](how-to/write-config.md)
- **"Why is this value 9090?" has an answer.** `Origin` names the layer that supplied it,
  `Shadowed` lists every layer defining it, `Explain` renders the whole chain. "Which file
  do I edit?" and "why is my edit not taking effect?" are the same question from two
  directions. → [Provenance](explanation/provenance.md)
- **A write that cannot take effect says so** instead of appearing to succeed. If an
  environment variable still outranks the file you just wrote, you are told — which is the
  single most common way a settings screen appears broken.
- **`Plan` is a dry run that cannot drift**, because it *is* the routing pass `Apply`
  runs, not a second implementation of it.

And the parts that are simply table stakes done carefully:

- **Hot-reload that fails closed.** A candidate that will not parse, or violates your
  schema, is rejected and the last-known-good configuration stays live — never a mixture
  of before and after. A watcher that cannot function returns an error rather than
  silently doing nothing. Writing from inside an observer is refused outright, so the
  cascade it would cause is unrepresentable rather than something you must remember to
  break. → [Hot-reload safety](explanation/hot-reload-safety.md)
- **Typed sections that stay current.** `ObserveSection[T]` decodes a subtree onto your
  struct and republishes it across reloads, delivering only when the struct actually
  changed. Each snapshot decodes in one operation, so it can never hold some fields from
  before a reload and some from after. → [Typed sections](how-to/typed-sections.md)
- **Read any type, including your own.** `Value[T]` reads whatever `T` is; durations, IP
  addresses, URLs, timezones and anything implementing `encoding.TextUnmarshaler` decode
  from their ordinary written form. → [Read configuration values](how-to/read-values.md)
- **Everything is a layer** — a file, one document within a multi-document file, embedded
  defaults, the environment, the flag set, something computed at runtime. All ordered by
  precedence, with no special case to remember.

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

**Probably not, if** your program only ever reads configuration and never writes it, or a
single file and some environment variables cover you. Viper does that job well, is
battle-tested by an enormous number of programs, and has an ecosystem this module does not
have. Reach for the smaller tool when the smaller tool fits — see
[History](about/history.md) for how much this module owes to viper's lineage.

**Yes, if** any of these describe you:

- your program writes configuration back, and users have to live in the file afterwards;
- you have more than two sources and have lost an afternoon to "which one set this?";
- a long-running service reloads config and you need reload to be safe rather than eager;
- config lands in files that are committed, reviewed, or shared between people.

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

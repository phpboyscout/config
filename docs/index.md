# config

**Layered configuration for Go that can tell you where a value came from — and write one
back without wrecking the file.**

Assemble settings from embedded defaults, files, the environment and CLI flags, with a
precedence you can read off the call site.

```bash
go get gitlab.com/phpboyscout/go/config
```

## The thirty-second version

A user changes one setting. Here is what that does to their file:

=== "Most libraries"

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

=== "`config`"

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

Comments gone, order alphabetised, a default pinned into the file — and a production
password that arrived from an environment variable, now committed to git.

That is not a bug in anything. It is what "serialise the merged view" means, and it
follows from merging eagerly: fold every source into one map and a writer holding that map
has nothing to write *but* the whole of it. **This module never folds its layers away** —
so it changes the key you asked for, and nothing else.

## What you get

| | |
|---|---|
| **Provenance** | `Explain("server.port")` → `9090 (from ~/.app.yaml); also defined in embedded:defaults.yaml`. Recorded during the merge, not reconstructed after. |
| **Coherent reads** | A `View` is pinned to one snapshot, so two related values can never straddle a reload. Measured: **0** mismatched pairs in 11.6M reads, against 1,759 for a library reading live state. |
| **Writes that preserve authorship** | Comments, key order, quoting and anchors survive. The change lands in the layer that *owns* the key, and a write that cannot take effect says so. |
| **Notifications you can trust** | Exactly once per logical change, never for a rejected one, never out of order, pinned to one snapshot for the whole callback. |
| **Fail-closed reload** | A file that will not parse, or fails your schema, is rejected — last-known-good stays live. Never half of one config and half of another. |
| **Any source as a layer** | `WithBackend` takes any three-method `Backend`, so Consul, a secrets manager or an HTTP endpoint gets full precedence, provenance and shadowing. |
| **Typed sections** | `ObserveSection[T]` keeps your struct current across reloads, and the package consuming it never imports this one. |
| **Read any type** | `Value[T]` reads whatever `T` is — durations, IP addresses, URLs, and your own `encoding.TextUnmarshaler` types. |

**[→ The full case, with the reasoning and the measurements](about/features.md)**

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
- :material-puzzle: **[Write a custom backend](how-to/custom-backend.md)** — make Consul,
  a secrets manager or an HTTP endpoint an ordinary layer.
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

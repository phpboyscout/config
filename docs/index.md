# config

**Layered configuration for Go, with one owner.** Assemble settings from embedded
defaults, files, the environment and CLI flags with a precedence you can read off the
call site; ask of any key where it came from and what shadows it; write a change back
to the layer that owns it without destroying the comments its author wrote.

```bash
go get gitlab.com/phpboyscout/go/config
```

Most configuration libraries merge eagerly. They fold every source into one map and
throw away the record of who contributed what, which is why they can tell you a value
is `9090` but never why, never which file to edit to change it, and never how to write
it back without flattening the merged result — secrets from the environment included —
over the top of a user's carefully commented file.

This module keeps that record. A `Store` owns every read, write and watch, and records
provenance during the merge rather than trying to reconstruct it afterwards.

## What it gives you

- **One owner for config I/O.** The `Store` loads, merges, watches and writes; nothing
  else touches a source. There is no protocol between a reader and a writer because
  there is only one of each. See [The Store](explanation/the-store.md).
- **Comment-preserving, layer-correct writes.** `Apply` edits the target document in
  place, so comments stay attached to their keys and key order, quoting and block style
  survive. A change is routed to the layer that already owns the key, so the value you
  set is the value you read back.
- **Per-key provenance.** `Origin` names the layer that supplied a value, `Shadowed`
  lists every layer defining it, `Explain` renders the whole chain. "Which file do I
  edit?" and "why is my edit not taking effect?" are the same question from two
  directions, and both are answerable.
- **Typed sections that stay current.** `ObserveSection[T]` decodes a subtree onto your
  struct and republishes it across reloads, delivering only when the struct actually
  changes. Each snapshot decodes in one operation, so it can never hold some fields from
  before a reload and some from after.
- **Hot-reload that fails closed.** A candidate that will not parse or violates the
  schema is rejected and the last-known-good configuration is retained. A watcher that
  cannot function says so rather than silently doing nothing.
- **Everything is a layer.** A file, a document within a multi-document file, embedded
  defaults, the environment, the flag set, something computed at runtime — all ordered
  by precedence, with no special case to remember.

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

## Where next

The documentation follows the [Diátaxis](https://diataxis.fr) quadrants: learn, then do,
then understand.

<div class="grid cards" markdown>

- :material-rocket-launch: **[Getting started](getting-started.md)** — the tutorial.
  Build a store over a file, read values, ask where they came from, write a change back,
  and react to one made outside your process.
- :material-file-tree: **[Load & merge](how-to/load-and-merge.md)** — files, embedded
  defaults, the environment, flags, and the precedence chain.
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
`ObservedSection[T]` structurally, they need no change at all. That decoupling boundary
is the reason the module exists, and it held.

For how the module got here, see [History](about/history.md).

## Reference

The Go API reference lives on
**[pkg.go.dev](https://pkg.go.dev/gitlab.com/phpboyscout/go/config)**, which is where a
language API reference belongs — generated from the source it documents, so it cannot
drift from it.

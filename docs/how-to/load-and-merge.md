# Load & merge configuration

You need to assemble configuration from more than one source — shipped defaults, a
user's file, an overlay, the environment, a flag — and know which value wins. This
guide covers building a `Store`, the precedence chain, and how to ask where a value
actually came from.

!!! note "Imports used below"
    ```go
    import (
        "context"
        "errors"
        "strings"

        "gitlab.com/phpboyscout/go/config"
    )
    ```

    Snippets use `ctx` (a `context.Context`) and, where flags appear, `cmd` — a
    `*cobra.Command`. Nothing requires cobra: `WithFlags` takes a `*pflag.FlagSet`, so
    any source of one will do. See [Bind CLI flags](bind-cli-flags.md).

## Build a store

A `Store` is the sole owner of configuration I/O: it loads, merges, watches and
writes. You build one with `NewStore`, and it performs its first load as part of
construction — there is no "built but not loaded" state for you to remember to
handle:

```go
store, err := config.NewStore(ctx,
	config.WithReaders(config.NamedSource{Name: "embedded:defaults.yaml", Content: defaults}),
	config.WithFiles(config.OS(), "/etc/mytool/config.yaml", "/home/me/.mytool/config.yaml"),
	config.WithEnv("MYTOOL"),
	config.WithFlags(cmd.Flags()),
)
if err != nil {
	return err
}
```

Read values through a `View`:

```go
port := store.View().GetInt("server.port")
host := store.View().GetString("server.host")
```

A `View` performs no I/O — it resolves against one immutable snapshot — so taking one
is a pointer copy rather than a load.

## Precedence is the order you added the sources

There is no ranking baked into the module. **Backends are read in the order they are
added, and later ones win.** The call above therefore resolves, highest first:

1. changed CLI flags (`WithFlags`)
2. environment variables under `MYTOOL_` (`WithEnv`)
3. `~/.mytool/config.yaml`
4. `/etc/mytool/config.yaml`
5. compiled-in defaults (`WithReaders`)

That ordering is the recommended one, and each placement earns its keep: defaults
belong at the bottom as an ordinary layer rather than a special case; the environment
belongs above files because that is what an operator expects an env var to do; and an
explicit flag is the most deliberate input a user can give, so it goes last.

Merging is per-key and deep. A base file supplying `server.host` and an overlay
supplying `server.port` produce a `server` section holding both — the overlay does not
replace the whole subtree.

The full model is in [Precedence & merge model](../explanation/precedence-and-merge.md).

## Ship compiled-in defaults

Defaults go in `WithReaders` as a `NamedSource`. The name is what appears in
provenance, so give it something a user would recognise — `embedded:defaults.yaml`
rather than `reader1`:

Create `defaults.yaml` next to the Go file that embeds it, then declare the embed at
**package scope** — `//go:embed` only works there, and only when the `embed` package is
imported:

```go
import (
	_ "embed" // required for //go:embed, even though nothing references it

	"gitlab.com/phpboyscout/go/config"
)

//go:embed defaults.yaml
var defaults []byte
```

Then use it like any other source:

```go
store, err := config.NewStore(ctx,
	config.WithReaders(config.NamedSource{Name: "embedded:defaults.yaml", Content: defaults}),
	config.WithFiles(config.OS(), "config.yaml"),
)
if err != nil {
	return err
}
```

Because defaults are a layer like any other, `Origin` can say "this came from the
embedded defaults" rather than leaving a user to guess why a key they never set has a
value.

## Decide which files are optional

A missing file is skipped by default, which is what you want for an overlay: a user
who has not written `~/.mytool/config.yaml` yet is not misconfigured. A missing *base*
file is usually a broken installation, and `RequireFirstSource` says so:

```go
store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/mytool/config.yaml", "/home/me/.mytool/config.yaml"),
	config.RequireFirstSource(),
)
```

Building a store with no backends at all returns `ErrNoSources`.

## Handle an invalid configuration without losing the store

With `WithSchema`, every candidate configuration is validated before it is published.
A configuration that loads and parses but violates the schema is returned *alongside*
`ErrInvalidConfig` rather than discarded:

```go
schema, err := config.SchemaOf[AppConfig]()
if err != nil {
	return err
}

store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "config.yaml"),
	config.WithSchema(schema),
)
if err != nil && !errors.Is(err, config.ErrInvalidConfig) {
	return err // genuinely unusable: no sources, unreadable, or unparseable
}
// store is non-nil here even when the configuration is invalid.
```

The ordinary `if err != nil { return err }` still fails fast, so a service refuses to
start on a bad config exactly as you would want. The distinction only matters to a
tool whose job is to *repair* configuration: returning nil would make one missing key
unfixable through the very surface designed to fix it. Every other failure — no
sources, a source that cannot be read, one that cannot be parsed — returns nil,
because there is no configuration to hand back. See
[Validate configuration](validate-config.md).

## Read the environment

`WithEnv` requires a prefix, and the prefix is a security control rather than
tidiness. Without one, any environment variable matching a configuration key could
silently change behaviour — on a shared CI runner or a multi-tenant host, an unrelated
process setting `LOG_LEVEL` would reconfigure every tool running there. Pass it
without a trailing underscore:

```go
config.WithEnv("MYTOOL") // MYTOOL_SERVER_PORT → server.port
```

A trailing underscore is trimmed for you, and the prefix is upper-cased. An **empty**
prefix contributes nothing at all rather than swallowing the whole environment — so a
prefix accidentally read from an unset variable disables the layer instead of opening
it up.

In tests, read the environment from a function instead of the process, so parallel
tests cannot interfere with each other through global state:

```go
config.WithEnv("MYTOOL", config.WithEnviron(func() []string {
	return []string{"MYTOOL_SERVER_PORT=9090"}
}))
```

Mapping a variable name back to a dotted key is genuinely ambiguous —
`MYTOOL_SERVER_PORT` could mean `server.port` or `server_port` — so the name is
resolved against the keys the layers beneath already define, which is nearly always
what a variable is overriding. A key defined nowhere else falls back to treating every
underscore as a separator. When two existing keys would both be spelled the same way,
the load fails with `ErrAmbiguousEnvKey` naming both candidates, rather than picking
one in map iteration order and behaving differently between runs of the same program.

## Bind flags

`WithFlags` contributes only the flags the user actually changed. See
[Bind CLI flags](bind-cli-flags.md) for why that filtering matters, and for `BindFlag`
when a flag name and its configuration key differ.

## Add a layer you computed at runtime

Some configuration is not read at all — a resolved path, a value negotiated with a
server, a toggle that applies to this run only. `AddLayer` contributes it as an
in-memory layer at the highest precedence:

```go
if err := store.AddLayer(ctx, "computed", strings.NewReader("cache:\n  dir: /var/cache\n")); err != nil {
	return err
}
```

It is an ordinary layer, so precedence, provenance, merging and shadowing all work on
it exactly as they do on a file, and it survives reloads. It is never a write target,
though: an in-memory source has nowhere to persist to, so a later write lands in the
file beneath it and is reported as shadowed rather than letting you believe your edit
took effect. Compiled-in defaults belong in `WithReaders` instead, where they sit at
the bottom of the order rather than the top.

## Find out where a value came from

"Which file do I edit?" and "why is my edit not taking effect?" are the same question
asked from two directions, and both need the whole chain rather than just the winner:

```go
view := store.View()

src, ok := view.Origin("server.port") // the layer that supplied the effective value
all := view.Shadowed("server.port")   // every layer defining it, lowest precedence first

fmt.Println(view.Explain("server.port"))
```

`store.Sources()` lists every backend's identity in precedence order, which is the
quickest answer to "did it even read my file?":

```go
slog.Default().Info("config sources", "sources", store.Sources())
```

Provenance is defined for leaves — scalars, and containers that are empty. A populated
subtree is assembled from however many layers contributed to it, so naming one source
for it would be dishonest: `Origin` reports not-found and `Shadowed` answers what you
actually wanted to know.

## Keep a run of related reads coherent

Individual reads are always coherent, but a *sequence* of them can straddle a reload —
read the host from one snapshot and the port from the next, and you connect to the new
host on the old port. `With` pins one snapshot for a block of reads:

```go
var addr string

err := store.With(func(view *config.View) error {
	addr = fmt.Sprintf("%s:%d", view.GetString("server.host"), view.GetInt("server.port"))

	return nil
})
```

It is scoped to a closure rather than handed out as a handle, because a handle can be
kept indefinitely and would quietly serve values that grew arbitrarily old.

## Store options

| Option | Purpose |
|---|---|
| `WithReaders(...NamedSource)` | in-memory sources, in precedence order — where compiled-in defaults belong |
| `WithFiles(config.FS, ...string)` | one file backend per path; the first is the base and the last wins |
| `WithEnv(string, ...EnvOption)` | environment variables under a required prefix |
| `WithFlags(*pflag.FlagSet, ...FlagOption)` | the flags the user actually changed |
| `WithBackend(Backend)` | append a backend you implemented yourself |
| `WithSchema(*Schema)` | validate every candidate configuration before it is published |
| `RequireFirstSource()` | make the first backend mandatory instead of optional |

## Limits worth knowing before you hit them

- **A YAML document is a layer.** A multi-document file contributes one layer per
  document, and later documents override earlier ones — the same rule as files.
- **An empty map or sequence is a value, not an absence.** It is present to `Has`, it
  appears in `Keys()`, and it survives a write.
- **A source that cannot be safely round-tripped is refused at load** with
  `ErrBackendUnsafe`, naming the location. In practice that means a multi-line flow
  collection with comments inside it: editing such a file would risk corrupting it, so
  it is rejected up front rather than at write time.
- **A source that is not valid YAML** fails with `ErrBackendParse`.

## Related

- [Read configuration values](read-values.md) — the accessors, `Value[T]` and `Sub`
- [Use typed sections](typed-sections.md) — project a subtree onto your own struct
- [React to changes with hot-reload](hot-reload.md)
- [Validate configuration](validate-config.md)
- [Precedence & merge model](../explanation/precedence-and-merge.md)

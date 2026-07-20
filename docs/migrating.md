# Migrating from v0.2.x

v0.2.x was a wrapper around Viper. This release is not: a `Store` owns every read, write
and watch, and **Viper is no longer a dependency** — it is absent from `go.mod`, `go.sum`
and every source file.

Be realistic about the size of this. Most of the surface renames mechanically, but a
handful of call sites are genuine ports rather than substitutions, and every one of them
is a place where the old behaviour was doing something you probably did not intend.
Measured against the main consumer, `gitlab.com/phpboyscout/go-tool-base`: of 160
`config.Containable` references, about 89 are bare parameter declarations that become
`config.Reader` by renaming the type, and only 12 call a method `Reader` does not have.
Those 12 are the work.

## If you use the local-interface pattern, you are unaffected

The typed-section *semantics* are frozen. `ObservedSection[T]`, delivery only when the
decoded struct actually changes, the monotonic `Version()`, and the `WithSection*` options
all behave exactly as they did. What changed is the *parameter types* they are handed.

So a package that declares its own one-method interface and never imports this module —

```go
type SettingsSource interface {
    Current() *ServerSettings
}
```

— needs no change at all. `*config.ObservedSection[T]` still satisfies it structurally.
That decoupling boundary is the reason the module exists, and it held. If your reusable
packages are written this way, the port is confined to the code that constructs
configuration, not the code that consumes it.

## Step 1 — construction

There is one constructor now. Sources are added in precedence order and later ones win,
so the order of the options *is* the precedence chain — there is no separate ranking to
remember.

Before:

```go
cfg, err := config.LoadFilesContainer(fs,
    config.WithConfigFiles(paths...),
    config.WithEnvPrefix("MYTOOL"),
    config.WithSchema(schema),
)
```

After:

```go
store, err := config.NewStore(ctx,
    config.WithReaders(config.NamedSource{Name: "embedded:defaults.yaml", Content: defaults}),
    config.WithFiles(config.OS(), paths...),   // was an afero.Fs — see step 2
    config.WithEnv("MYTOOL"),
    config.WithFlags(flags),
    config.WithSchema(schema),
)
if err != nil && !errors.Is(err, config.ErrInvalidConfig) {
    return err
}

cfg := store.View()
```

Two things to note.

`NewStore` loads immediately; there is no unloaded state to handle. And a configuration
that parses but fails schema validation is returned *alongside* `ErrInvalidConfig` rather
than discarded — the usual `if err != nil { return }` still fails fast, but a tool whose
job is to repair configuration can check for `ErrInvalidConfig` specifically and carry on
with a usable `Store`. Every other failure returns nil.

`RequireFirstSource()` makes the first backend mandatory, which is the closest equivalent
to the old required-base-file behaviour. Note that it applies to whichever backend you
added first — if that is your embedded defaults, it is those that become mandatory.

There is no equivalent of `LoadEnv`. If you relied on a `.env` file being read from the
working directory, load it yourself before calling `NewStore`.

## Step 2 — the filesystem

`WithFiles` no longer takes an `afero.Fs`. It takes `config.FS`, a six-method interface
this module defines, and the module ships two implementations.

For the overwhelmingly common case the change is one call:

```go
config.WithFiles(afero.NewOsFs(), paths...)   // before
config.WithFiles(config.OS(), paths...)       // after
```

If you passed afero only to reach the real filesystem, you can drop the dependency
entirely.

### If you have an `afero.Fs` to pass through

Wrap it. The adapter is six methods, and two optional ones worth including — without
`RealPath` your filesystem is polled rather than watched natively, and without `Readlink`
a write through a symlink replaces the link instead of following it:

```go
type aferoFS struct{ fs afero.Fs }

func (a aferoFS) ReadFile(name string) ([]byte, error) { return afero.ReadFile(a.fs, name) }
func (a aferoFS) Stat(name string) (fs.FileInfo, error) { return a.fs.Stat(name) }
func (a aferoFS) Rename(old, new string) error          { return a.fs.Rename(old, new) }
func (a aferoFS) Remove(name string) error              { return a.fs.Remove(name) }

func (a aferoFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return afero.WriteFile(a.fs, name, data, perm)
}

func (a aferoFS) MkdirAll(path string, perm fs.FileMode) error {
	return a.fs.MkdirAll(path, perm)
}
```

!!! warning "Only implement `RealPath` when the filesystem really has OS paths"
    `RealPather` is how the module decides whether native notification can work at all. An
    adapter that implements it unconditionally — returning `false` from the method rather
    than not having the method — makes every filesystem look watchable, so `fsnotify` is
    selected and the absence of anything to watch is discovered one path at a time.

    Give the method to a type that wraps `*afero.OsFs` or `*afero.BasePathFs`, and use a
    plain type for anything else.

### Testing

If you used `afero.NewMemMapFs()` in tests, `config.Dir(t.TempDir())` replaces it with no
dependency at all:

```go
fsys, err := config.Dir(t.TempDir())
require.NoError(t, err)
require.NoError(t, fsys.WriteFile("config.yaml", []byte(body), 0o600))

store, err := config.NewStore(ctx, config.WithFiles(fsys, "config.yaml"))
```

It is a real directory, so watching works and a write behaves exactly as it does in
production — which an in-memory filesystem cannot exercise.

!!! warning "`Dir` takes paths relative to its root"
    Pass `"config.yaml"`, not `"/tmp/xyz/config.yaml"`. An absolute path is treated as an
    attempt to escape the root and fails with `path escapes from parent` — **not**
    `fs.ErrNotExist`. The distinction matters: a missing optional source is normal and
    skipped, whereas an escape is fatal, so an absolute path turns a merely-absent file
    into a hard failure.

## Step 3 — the mechanical rename

`Containable` is gone. The read surface is `Reader`, and it is deliberately free of any
dependency's types: nothing in it exposes an underlying library, because anything
reachable through such an escape hatch becomes part of your contract.

Before:

```go
func ServerSettingsFromConfig(cfg config.Containable, prefix string) ServerSettings {
    return ServerSettings{Port: cfg.GetInt(prefix + ".port")}
}
```

After:

```go
func ServerSettingsFromConfig(cfg config.Reader, prefix string) ServerSettings {
    return ServerSettings{Port: cfg.GetInt(prefix + ".port")}
}
```

This covers the large majority of references. `Get`, `GetString`, `GetBool`, `GetInt`,
`GetFloat`, `GetDuration`, `GetTime`, `Has`, `IsSet`, `SectionExists`, `Unmarshal` and
`UnmarshalKey` all keep their names and signatures. `GetStringSlice` and `Keys` are new to
the interface, and `Origin`, `Shadowed` and `Explain` are new provenance methods.

One trap worth stating plainly: **`Sub` now returns `*View`, which satisfies `Reader` but
not `Binder`.** An adapter chain that takes a scoped container and passes it down must be
typed `Reader` all the way. If a function in that chain needs to register an observer, it
must take the `*Store` (or a `Binder`) instead, not a scoped view.

In tests, `mocks.MockContainable` becomes `mocks.NewMockReader`, with
`mocks.NewMockBinder` and `mocks.NewMockObserved` alongside it.

## Step 4 — what was removed, and what replaces it

| Removed | Use instead | Note |
|---|---|---|
| `Containable` | `Reader` | Rename; the accessor set is a superset |
| `Containable.GetViper()` | typed accessors, `Store.Snapshot()`, `Store.AddLayer` | See below — the five real uses each have a first-class answer |
| `Containable.Set(key, value)` | `Store.Apply(ctx, config.Set(...))` | Staging and persisting are one step now |
| `Containable.WriteConfigAs(dest)` | `Store.Apply` | The old method wrote the *merged* view, including env-sourced secrets |
| `Containable.BindPFlag(key, flag)` | `WithFlags(flags, BindFlag(...))` at construction | Bindings now survive a reload |
| `Containable.ToJSON()` / `Dump(w)` | `Store.Snapshot().Values()` and marshal it yourself | The module no longer picks an output format for you |
| `Containable.ConfigFiles()` | `Store.Sources()`, or `Snapshot().Layers()` filtered on `Kind == SourceFile` | Layers carry provenance, not just paths |
| `Containable.Sub(key)` | `View.Sub(key) *View` | Still nil when absent, so `if sub != nil` guards behave |
| `Containable.Validate(schema)` | `View.Validate(schema)` | Not on `Reader`; a validating function must take `*View` |
| `Container.Close()` | the `stop` func returned by `Store.Watch` | Watching is now started explicitly, not implicitly |
| `Container.SetSchema(schema)` | `WithSchema(schema)` at construction | |
| `Container.GetObservers()` | `Store.Observers()` | |
| `NewFilesContainer`, `NewReaderContainer` | `NewStore` with `WithFiles` / `WithReaders` | |
| `LoadFilesContainer`, `LoadFilesContainerWithSchema` | `NewStore` with `WithSchema` | |
| `Load`, `LoadEmbed` | `NewStore` with `WithFiles` / `WithReaders` | Embedded assets become `NamedSource` values |
| `LoadEnv` | — | Load `.env` yourself before constructing |
| `NewContainerFromViper` | — | There is no Viper to construct from |
| `WithConfigFiles(files...)` | `WithFiles(fs, paths...)` | Filesystem and paths are now one option |
| `WithConfigReaders(readers...)` | `WithReaders(NamedSource...)` | Each source now carries a name, which appears in provenance |
| `WithEnvPrefix(prefix)` | `WithEnv(prefix)` | Still a security control; still no trailing underscore |
| `WithConfigFormat(format)` | — | YAML only |
| `WithLogger(l)` | `Store.OnReloadError`, `Store.OnObserverError` | The Store reports; it does not log on your behalf |
| `WithReloadDebounce(d)` | `Watch(ctx, WithPollInterval(d))` | `DefaultPollInterval` is 2s |
| `Observable.Run(Containable)` | `Observable.Run(Observed)` | |
| `AddObserverFunc(func(Containable) error)` | `AddObserverFunc(func(Observed) error)` | |
| `Observer` struct | `ObserverFunc` | |
| `ValidateStruct[T](cfg Containable, ...)` | `ValidateStruct[T](cfg *View, ...)` | Takes the concrete view |
| `UnmarshalSection[T](cfg Containable, ...)` | `UnmarshalSection[T](cfg Reader, ...)` | Rename only |
| `WithSectionDefaultFunc(func(Containable) T, merge)` | `WithSectionDefaultFunc(func(Observed) T, merge)` | Rename only |
| `ErrConfigFileNotFound`, `ErrNoFilesFound` | `ErrNoSources`, and `fs.ErrNotExist` from a backend | |
| `DefaultReloadDebounce` | `DefaultPollInterval` | |

## Step 5 — the genuinely breaking cases

### `GetViper()`

Across all repositories this escape hatch was reached for exactly five distinct methods.
Three of them are now first-class:

| Old | New |
|---|---|
| `cfg.GetViper().GetStringSlice(key)` | `cfg.GetStringSlice(key)` |
| `cfg.GetViper().Set(key, value)` | `store.Apply(ctx, config.Set(key, value))` |
| `cfg.GetViper().AllSettings()` | `store.Snapshot().Values()` |
| `cfg.GetViper().ConfigFileUsed()` | `cfg.Origin(path)` — or `store.Sources()` for the whole list |
| `cfg.GetViper().MergeConfig(r)` | `store.AddLayer(ctx, "computed", r)` |

`ConfigFileUsed()` is the interesting one. It answered "the last file read", which for a
merged configuration is misleading. The honest question is per-key, and provenance answers
it:

```go
if src, ok := cfg.Origin("server.port"); ok {
    fmt.Printf("server.port came from %s\n", src)
}

fmt.Println(cfg.Explain("server.port")) // the whole chain, including shadowed layers
```

`AddLayer` is not a like-for-like replacement for `MergeConfig`. It contributes an
in-memory layer at the *highest* precedence and it survives reloads, which is what you want
for values your tool computed at runtime. It is never a write target, so a later write
lands in the file beneath it and is reported as shadowed rather than silently having no
effect. Compiled-in defaults belong in `WithReaders` at construction instead, where they
sit at the bottom of the order.

### `WriteConfigAs` — read this one carefully

This is the most important change in the release, and it is a bug fix as much as a port.

`WriteConfigAs` serialised Viper's `AllSettings()`, which is the **merged** view. That
means it wrote environment-sourced values — including secrets injected as env vars — into a
file on disk, alongside every compiled-in default the user never asked for. It also dropped
empty maps, because `AllSettings()` omits `key: {}`. Consumers worked around this by
constructing a second, throwaway Viper containing only the keys they meant to persist, and
writing that instead.

None of that is needed now. `Apply` routes each change to a layer, in reverse merge order,
picking the first writable match. Env and flags are never targets, so a value that reached
you from the environment cannot be written into a file by accident.

Before:

```go
p.Config.Set("telemetry.enabled", enabled)

v := p.Config.GetViper()

// A fresh Viper holding only the telemetry keys, so the write does not
// persist embedded defaults into the user's config file.
fresh := viper.New()
fresh.SetFs(p.FS)
fresh.SetConfigType("yaml")
fresh.Set("telemetry.enabled", v.GetBool("telemetry.enabled"))

if err := fresh.WriteConfigAs(configFile); err != nil {
    return errors.Wrap(err, "failed to write config")
}
```

After:

```go
if _, err := store.Apply(ctx, config.Set("telemetry.enabled", enabled)); err != nil {
    return errors.Wrap(err, "failed to write config")
}
```

The write preserves comments, key order and block style in the target file, and only the
key you named is touched.

If you need to know where a write will land before committing it — a `--dry-run`, or a
settings screen previewing its own changes — `Plan` returns exactly what `Apply` would
execute:

```go
plan, err := store.Plan(config.Set("server.port", 9090))
if err != nil {
    return err
}

fmt.Println(plan.String())

for _, op := range plan.Operations {
    if !op.Effective() {
        fmt.Printf("%s will be written to %s but %v still wins\n",
            op.Change.Path, op.Target, op.ShadowedBy)
    }
}
```

Reporting that is worth doing. "Written, but the environment still overrides it" is the
difference between a user understanding what happened and concluding your tool is broken.

Writes fail with `ErrNoWritableLayer`, `ErrNotWritable`, `ErrConflict`, `ErrNoChanges`,
`ErrInvalidPath` or `ErrPartialCommit`; branch on them with `errors.Is`.

One rule to know before you port an observer: **writing from inside an observer callback
returns `ErrWriteFromObserver`.** Each such write notifies, which runs the observer again,
which is a cascade with no natural end. Capture what you need, return, and write from
somewhere else.

### `BindPFlag`

Flags are now a backend, declared at construction. This fixes a real defect: under the old
container a reload rebuilt Viper from files and silently discarded every flag binding, so a
`--port` the user passed stopped applying the first time the config file changed.

Only flags the user actually changed contribute — that filtering is now the backend's job,
not yours. Binding a flag left at its default is how a flag default silently masks
configuration, and the user has no way to discover why their file is being ignored.

Before:

```go
for key, flag := range boundFlags {
    if flag == nil || !flag.Changed {
        continue
    }

    if err := cfg.BindPFlag(key, flag); err != nil {
        log.Debug("failed to bind flag to config", "flag", flag.Name, "key", key, "error", err)
    }
}
```

After — at construction, adding the flag backend last so an explicit flag outranks
everything:

```go
store, err := config.NewStore(ctx,
    config.WithFiles(fs, paths...),
    config.WithEnv("MYTOOL"),
    config.WithFlags(flags,
        config.BindFlag("log-level", "logging.level"), // only where name and key differ
    ),
)
```

The hyphen-to-dot convention (`server-port` → `server.port`) is applied for you, so
`BindFlag` is needed only for the exceptions. The local `bindable` interface some consumers
declared for this has nothing left to describe and should be deleted.

### `ToJSON` and `Dump`

The module no longer chooses an output format for you. Take the values and marshal them:

```go
data, err := json.Marshal(store.Snapshot().Values())
if err != nil {
    return err
}

fmt.Fprintln(w, string(data))
```

`Values()` returns a copy, so mutating it cannot affect the snapshot. Note that this is the
merged view — if you are producing output for a user, mask anything whose `Origin` is
`SourceEnv` before printing it.

### `Set`

`Set` was stage-then-persist: it mutated the in-memory Viper and left you to write the
file separately. Every measured downstream use was the first half of that pair, and not one
was a genuine ephemeral override. `Apply` does both in one step:

```go
snap, err := store.Apply(ctx,
    config.Set("github.auth.method", "token"),
    config.Remove("github.auth.password"),
)
```

Batched changes land together. If you genuinely wanted an in-process override that is never
persisted, that is `AddLayer`.

## Step 6 — `ObserveSection`

The semantics are unchanged. What changed is the first parameter: `ObserveSection` now
takes a `Binder`, which is anything with `View() *View` and
`AddObserverFunc(func(Observed) error)`. `*Store` satisfies it, so at the call site this is
usually just passing the store instead of the container.

Before:

```go
func ObserveServerSettings(
    cfg config.Containable,
    prefix string,
) (*config.ObservedSection[ServerSettings], error) {
    return config.ObserveSection[ServerSettings](cfg, prefix,
        config.WithSectionDefaultFunc(func(next config.Containable) ServerSettings {
            if next == nil {
                return ServerSettings{}
            }

            return ServerSettings{Port: next.GetInt("server.port")}
        }, mergeServerSettings),
    )
}
```

After:

```go
func ObserveServerSettings(
    binder config.Binder,
    prefix string,
) (*config.ObservedSection[ServerSettings], error) {
    return config.ObserveSection[ServerSettings](binder, prefix,
        config.WithSectionDefaultFunc(func(next config.Observed) ServerSettings {
            if next == nil {
                return ServerSettings{}
            }

            return ServerSettings{Port: next.GetInt("server.port")}
        }, mergeServerSettings),
    )
}
```

Taking `Binder` rather than `*Store` keeps the function testable: a fixed snapshot in a
test satisfies the interface without dragging in the machinery that loads one, and
`mocks.NewMockBinder` is generated for exactly this.

`Current()`, `Value()`, `Exists()` and `Version()` are untouched, as are
`WithSectionDefaults`, `WithSectionEqual`, `WithSectionValidator` and `WithSectionApply`.

## Step 7 — watching

Watching is explicit now, and it fails loudly when it cannot work. The old container
started an fsnotify watcher during construction, which silently did nothing on a filesystem
the operating system could not see — so hot reload was quietly dead in exactly the tests and
virtual-worktree tools that most needed it.

```go
stop, err := store.Watch(ctx)
if err != nil {
    return err // ErrWatchUnavailable: better than believing you will hear about changes
}

defer stop()
```

Pass `WithPollInterval(d)` to tune polling, or `WithWatcher(w)` to drive change detection
deterministically in tests. `NewWatcher` picks native notification where it works and
polling everywhere else. `Store.Close()` does not exist; the returned `stop` is the whole
lifecycle.

## What you gain

The port buys more than parity.

**Layer-correct writes that preserve comments.** A write lands in one layer, touching one
key, and the file's comments, key order and block style survive it. Repeated writes
converge rather than drifting.

**Per-key provenance.** `Origin` names the layer that supplied a value, `Shadowed` lists
every layer that defines it, and `Explain` renders the chain. "Which file do I edit?" and
"why is my edit not taking effect?" are the same question from two directions, and both are
now answerable.

**Multi-document YAML.** The old stack read the first document of a file and discarded
everything after the first `---` with no error at all. Every document is now a layer, in
order, with later documents overriding earlier ones. Nothing is silently lost.

**Reload no longer discards flag bindings.** Flags are a backend that is re-read like any
other, so a `--port` the user passed keeps applying across reloads.

**Empty containers are values.** `key: {}` survives a write and appears in `Keys()`,
instead of being silently deleted.

## Limits worth knowing before you port

- A **multi-line flow collection with interior comments** cannot be round-tripped safely
  and is refused at load with `ErrBackendUnsafe`, naming the location. Reformat it to block
  style.
- A **map-valued `Set` replaces the whole subtree**; comments and anchors inside it may not
  survive. Set leaves where you can.
- Individual reads are always coherent, but a *sequence* of them can straddle a reload. Use
  `store.With(func(v *config.View) error { ... })` for a block of related reads.
- Serialisation is in-process only. It does not coordinate between processes.
- Invisible and bidirectional characters are escaped on write — a Trojan Source mitigation
  (CVE-2021-42574). The escape is lossless; every visible character survives verbatim.

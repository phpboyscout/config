# Load & merge configuration

You need to assemble configuration from more than one source — shipped defaults, a
user's file, maybe an overlay — and know which value wins. This guide covers the
loaders and the precedence chain.

## Load from files

`LoadFilesContainer` loads the first file and **merges** each subsequent one on top
(later files win for shared keys); missing non-first files are skipped:

```go
cfg, err := config.LoadFilesContainer(afero.NewOsFs(),
	config.WithConfigFiles("/etc/mytool/config.yaml", "~/.mytool/config.yaml"))
```

- The **first** file is required — if it does not exist, `LoadFilesContainer` returns
  an error wrapping [`ErrConfigFileNotFound`](https://pkg.go.dev/gitlab.com/phpboyscout/go/config#pkg-variables),
  so you can branch to defaults on first run.
- Later files are optional overlays; a missing one is skipped, and a malformed one is
  logged as a warning (the load still succeeds with the valid files).

`Load` is the variant that takes an explicit `allowEmptyConfig` — use it when no
config file at all is acceptable:

```go
cfg, err := config.Load([]string{"config.yaml"}, afero.NewOsFs(), true /* allowEmpty */)
```

## Load embedded defaults

Ship defaults compiled into your binary and merge a user file on top. `LoadEmbed`
takes any `fs.FS` (e.g. an `embed.FS`):

```go
//go:embed defaults.yaml
var assets embed.FS

defaults, err := config.LoadEmbed([]string{"defaults.yaml"}, assets)
```

To layer a user file over embedded defaults, load the defaults as readers and add the
file — or merge two containers by reading both. The common pattern is embedded
defaults first, then user files last (last-wins).

## Load from readers

For tests or non-file sources, `NewReaderContainer` reads from any `io.Reader`:

```go
cfg := config.NewReaderContainer(afero.NewMemMapFs(),
	config.WithConfigFormat("yaml"),
	config.WithConfigReaders(strings.NewReader("server:\n  port: 8080\n")))
```

## Precedence

Every accessor resolves through Viper's precedence, highest wins:

1. **Explicit `Set`** (and bound CLI flags — see [Bind CLI flags](bind-cli-flags.md))
2. **Environment variables** (automatic; `server.port` ← `SERVER_PORT`)
3. **File config** (later merged files over earlier)
4. **Embedded defaults**
5. **Struct `default:` tags** (applied during validation/unmarshal)

The full model — including how nested keys merge and how env binding maps dotted keys
— is in [Precedence & merge model](../explanation/precedence-and-merge.md).

## Container options

Every constructor takes `afero.Fs` plus functional options:

| Option | Purpose |
|---|---|
| `WithLogger(*slog.Logger)` | logging seam — **nil-safe**, defaults to quiet (`slog.DiscardHandler`) |
| `WithConfigFiles(...string)` | the ordered file set (first required, rest optional overlays) |
| `WithConfigFormat(string)` | `yaml` / `json` / `toml` — **required** for reader-backed containers |
| `WithConfigReaders(...io.Reader)` | load from readers instead of files |
| `WithEnvPrefix(string)` | scope env lookups to `PREFIX_*` — see [why that matters](../explanation/precedence-and-merge.md#the-env-prefix-is-a-security-control) |
| `WithSchema(*Schema)` | attach validation; also gates hot-reload |
| `WithReloadDebounce(time.Duration)` | coalescing window for file-change bursts (default 250 ms) |

The logger default is deliberate: a library should be **quiet unless the application
wires logging**. Pass your logger to see merge warnings and reload events:

```go
cfg, err := config.LoadFilesContainer(afero.NewOsFs(),
	config.WithLogger(slog.Default()),
	config.WithConfigFiles("config.yaml"))
```

## Close file-backed containers

A file-backed container owns an OS watcher — release it on shutdown. `Close()` is
idempotent and a no-op on non-watching containers:

```go
defer cfg.Close()
```

## Find out which files were used

`ConfigFiles()` returns the files that actually contributed, in merge order — invaluable
when a value isn't what you expected:

```go
slog.Default().Info("config sources", "files", cfg.ConfigFiles())
```

## Related

- [Use typed sections](typed-sections.md)
- [Precedence & merge model](../explanation/precedence-and-merge.md)
- [Why a wrapper over Viper](../explanation/why-a-wrapper.md)

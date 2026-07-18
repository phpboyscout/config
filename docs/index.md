# config

**A hardened [Viper](https://github.com/spf13/viper) configuration container for Go.**
Merge flags, environment, files, and embedded defaults with a clear precedence; watch
files and reload safely; hand out **typed, validated** views — all behind one small
interface so your code depends on `Containable`, not on Viper.

```bash
go get gitlab.com/phpboyscout/go/config
```

`gitlab.com/phpboyscout/go/config` is the configuration layer extracted from
[go-tool-base](https://gitlab.com/phpboyscout/go-tool-base). It wraps Viper so your
packages take a small interface, your tests take a mock, and runtime config changes
are applied atomically — or rejected and rolled back to the last-known-good.

## Why

- **One interface to depend on.** `Containable` exposes typed accessors, section
  access, validation, and observer registration. Your code never imports Viper
  directly; tests use the published [`mocks`](https://pkg.go.dev/gitlab.com/phpboyscout/go/config/mocks)
  package.
- **Safe hot-reload.** A file change rebuilds and **re-validates** the config; a
  candidate that fails to build or validate is rejected and the last-known-good is
  retained. Observers fire only on a successful reload. See
  [hot-reload safety](explanation/hot-reload-safety.md).
- **Typed sections + validation.** `UnmarshalSection[T]` / `ObserveSection[T]`
  project a config subtree onto your struct (with hot-reload for the observed
  variant); `Schema` / `ValidateStruct[T]` check values against `config:` struct
  tags.
- **The Viper module.** The other toolkit modules are deliberately Viper-free — this
  one *is* the Viper layer, so the rest of your code doesn't carry it. It also closes
  several traps in raw Viper usage: the detached-`Sub()` snapshot, the flag
  default-clobber, and the missing env key replacer. See
  [Why a wrapper](explanation/why-a-wrapper.md).

## Where next

<div class="grid cards" markdown>

- :material-rocket-launch: **[Getting started](getting-started.md)** — load a file,
  read typed values, and react to a change in a few lines.
- :material-file-tree: **[Load & merge](how-to/load-and-merge.md)** — files,
  embedded defaults, readers, and the precedence chain.
- :material-code-braces: **[Typed sections](how-to/typed-sections.md)** — project a
  subtree onto a struct with `UnmarshalSection`.
- :material-reload: **[Hot-reload](how-to/hot-reload.md)** — `Observable`,
  `AddObserverFunc`, and `ObserveSection`.
- :material-shield-check: **[Validate](how-to/validate-config.md)** — `Schema` and
  `ValidateStruct` from `config:` tags.
- :material-test-tube: **[Test with the mock](how-to/test-with-mocks.md)** —
  `configmocks.MockContainable` in your tests.
- :material-lightbulb-on: **[Why a wrapper](explanation/why-a-wrapper.md)** — the traps
  in raw Viper this module exists to close.

</div>

## Reference

The Go API reference is on
**[pkg.go.dev](https://pkg.go.dev/gitlab.com/phpboyscout/go/config)**.

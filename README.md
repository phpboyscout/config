<div align="center">

# config

**A hardened Viper configuration container for Go — hierarchical merge, safe hot-reload, typed sections, and struct-tag validation, behind one small interface**

[![Go Reference](https://pkg.go.dev/badge/gitlab.com/phpboyscout/go/config.svg)](https://pkg.go.dev/gitlab.com/phpboyscout/go/config)
[![Pipeline](https://gitlab.com/phpboyscout/go/config/badges/main/pipeline.svg)](https://gitlab.com/phpboyscout/go/config/-/pipelines)
[![Coverage](https://gitlab.com/phpboyscout/go/config/badges/main/coverage.svg)](https://gitlab.com/phpboyscout/go/config/-/graphs/main/charts)
[![phpboyscout Go toolkit](https://img.shields.io/badge/phpboyscout-Go%20toolkit-554488?logo=gitlab&logoColor=white)](https://go.phpboyscout.uk)

<em>Part of the <a href="https://go.phpboyscout.uk">phpboyscout Go toolkit</a> &mdash; small, framework-free Go modules extracted from <a href="https://gitlab.com/phpboyscout/go-tool-base">go-tool-base</a>. Docs: <a href="https://config.go.phpboyscout.uk">config.go.phpboyscout.uk</a></em>

</div>

---

`gitlab.com/phpboyscout/go/config` wraps [Viper](https://github.com/spf13/viper) in a
small, testable interface. A `Container` merges configuration from flags, environment
variables, files, and embedded defaults with a clear precedence, watches files for
changes, and hands out **typed sections** and **struct-validated** views — all behind
the `Containable` interface so your code depends on an interface, not on Viper
directly.

## Design

- **One interface to depend on.** `Containable` exposes typed accessors, section
  access, validation, and observer registration. Your packages take a `Containable`;
  tests pass a mock from the published [`mocks`](https://pkg.go.dev/gitlab.com/phpboyscout/go/config/mocks)
  package.
- **Safe hot-reload.** File watches rebuild the config on change and re-validate it;
  a candidate that fails to build or validate is **rejected**, and the
  last-known-good config is retained. `Observable` observers are notified only on a
  successful reload; `OnReloadError` surfaces the rejected ones.
- **Typed sections + validation.** `Section[T]` / `ObservedSection[T]` unmarshal a
  config subtree into your struct (with hot-reload for the observed variant), and
  `Schema` / `ValidateStruct[T]` validate values against field rules.
- **The Viper wrapper.** Unlike the other toolkit modules — which are deliberately
  Viper-free — this module *is* the Viper layer. It carries the Viper stack so the
  rest of your code does not have to.

## Install

```bash
go get gitlab.com/phpboyscout/go/config
```

## Quick start

```go
package main

import (
	"fmt"

	"github.com/spf13/afero"

	"gitlab.com/phpboyscout/go/config"
)

func main() {
	cfg, err := config.LoadFilesContainer(afero.NewOsFs(),
		config.WithConfigFiles("config.yaml"))
	if err != nil {
		panic(err)
	}

	fmt.Println(cfg.GetString("name"))

	// React to config changes at runtime.
	cfg.AddObserverFunc(func(c config.Containable) error {
		fmt.Println("reloaded:", c.GetString("name"))
		return nil
	})
}
```

## What's inside

- **Container / Containable** — the Viper-backed container and the interface your code
  depends on; `LoadFilesContainer`, `NewContainerFromViper`, `NewReaderContainer`,
  `Load`, `LoadEmbed`, `LoadEnv`.
- **Sections** — `Section[T]` and `ObservedSection[T]` for typed, optionally
  hot-reloaded config subtrees.
- **Validation** — `Schema` / `FieldSchema` / `ValidateStruct[T]` / `ValidationResult`.
- **Hot-reload** — `Observable` / `Observer`, `AddObserver`, `OnReloadError`, and the
  reject-and-retain-last-good reload path.
- **`mocks`** — published testify mocks of `Containable` and `Observable` for
  downstream tests.

## Documentation

Full guides and the precedence/hot-reload model: **[config.go.phpboyscout.uk](https://config.go.phpboyscout.uk)**.
API reference: **[pkg.go.dev](https://pkg.go.dev/gitlab.com/phpboyscout/go/config)**.

## License

See [LICENSE](LICENSE).

<div align="center">

# config

**Layered configuration for Go with a single owner — comment-preserving layer-correct writes, per-key provenance, safe hot-reload, typed sections and struct-tag validation**

[![Go Reference](https://pkg.go.dev/badge/gitlab.com/phpboyscout/go/config.svg)](https://pkg.go.dev/gitlab.com/phpboyscout/go/config)
[![Pipeline](https://gitlab.com/phpboyscout/go/config/badges/main/pipeline.svg)](https://gitlab.com/phpboyscout/go/config/-/pipelines)
[![Coverage](https://gitlab.com/phpboyscout/go/config/badges/main/coverage.svg)](https://gitlab.com/phpboyscout/go/config/-/graphs/main/charts)
[![phpboyscout Go toolkit](https://img.shields.io/badge/phpboyscout-Go%20toolkit-554488?logo=gitlab&logoColor=white)](https://go.phpboyscout.uk)

<em>Part of the <a href="https://go.phpboyscout.uk">phpboyscout Go toolkit</a> &mdash; small, framework-free Go modules extracted from <a href="https://gitlab.com/phpboyscout/go-tool-base">go-tool-base</a>. Docs: <a href="https://config.go.phpboyscout.uk">config.go.phpboyscout.uk</a></em>

</div>

---

`gitlab.com/phpboyscout/go/config` gives configuration a single owner. A `Store` reads
every source — files, compiled-in defaults, environment variables, command-line flags —
merges them in a defined precedence order, records which layer supplied each value, and
is the only thing that writes any of them back. Everything else is a view over the
snapshot it publishes.

That single rule is what makes the rest possible. Because merging happens in one place,
provenance can be recorded while it happens rather than reconstructed afterwards. Because
one component owns the files, a write can be aimed at the layer that actually owns a key
instead of flattening the merged view over it.

## Design

- **Writes preserve what you wrote.** Saving a value changes that value and nothing else:
  comments, key order and block style survive, and the change lands in the file that owns
  the key rather than in a re-encoding of everything the process could see. `Plan` shows
  what a write would do before it does it.
- **Per-key provenance.** `Origin` reports which source supplied a value, `Shadowed` lists
  every layer defining it, and `Explain` renders the chain — so "why is this value what it
  is" has an answer.
- **Safe hot-reload.** A candidate that fails to build or validate is rejected and the
  last-known-good configuration is retained. Observers are told exactly once per logical
  change; rejections travel on `OnReloadError` instead.
- **Typed sections + validation.** `ObservedSection[T]` keeps a struct current across
  reloads, and `Schema` / `ValidateStruct[T]` check values against field rules. A package
  consuming settings declares a one-method interface over its own struct and never imports
  this one.
- **Everything is a layer.** Files, defaults, environment, flags and runtime `AddLayer`
  sources all take part in precedence, provenance and shadowing the same way.

## Install

```bash
go get gitlab.com/phpboyscout/go/config
```

## Quick start

```go
package main

import (
	"context"
	"fmt"

	"github.com/spf13/afero"

	"gitlab.com/phpboyscout/go/config"
)

func main() {
	ctx := context.Background()

	s, err := config.NewStore(ctx,
		config.WithFiles(afero.NewOsFs(), "config.yaml"),
		config.WithEnv("APP"))
	if err != nil {
		panic(err)
	}

	fmt.Println(s.View().GetString("name"))

	// Where did that value come from?
	fmt.Println(s.View().Explain("name"))

	// Persist a change, preserving the file's comments and layout.
	if _, err := s.Apply(ctx, config.Set("name", "updated")); err != nil {
		panic(err)
	}

	// React to changes made by anything other than this process.
	s.AddObserverFunc(func(cfg config.Observed) error {
		fmt.Println("reloaded:", cfg.GetString("name"))

		return nil
	})
}
```

## What's inside

- **`Store`** — the single owner of config I/O: `NewStore`, `View`, `With`, `Plan`,
  `Apply`, `Reload`, `Watch`, `AddLayer`, `Snapshot`.
- **Reading** — `Reader` / `View` typed accessors, `Sub` for a scoped read, `Unmarshal`.
- **Writing** — `Set` / `Remove`, `Plan` as a dry run, `Operation.Effective()` and
  `ShadowedBy` for writes a higher layer still overrides.
- **Provenance** — `Origin`, `Shadowed`, `Explain`.
- **Sections** — `Section[T]` and `ObservedSection[T]` for typed, reload-aware subtrees.
- **Validation** — `Schema` / `FieldSchema` / `ValidateStruct[T]` / `ValidationResult`,
  applied on reload and on write.
- **`mocks`** — published testify mocks for downstream tests.

Comment-preserving YAML editing lives in its own module,
[`yamldoc`](https://yamldoc.go.phpboyscout.uk).

## Migrating from v0.2.x

v0.3 replaced the Viper-backed container with the Store. The typed-section semantics are
unchanged, so a package consuming `ObservedSection[T]` through its own interface needs no
change at all; code holding a `Containable` does. See the
[migration guide](https://config.go.phpboyscout.uk/migrating/) and the
[history](https://config.go.phpboyscout.uk/about/history/) of how the module got here.

## Documentation

Full guides and the precedence/hot-reload model: **[config.go.phpboyscout.uk](https://config.go.phpboyscout.uk)**.
API reference: **[pkg.go.dev](https://pkg.go.dev/gitlab.com/phpboyscout/go/config)**.

## License

See [LICENSE](LICENSE).

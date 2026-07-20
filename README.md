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

## Why

Reading configuration is a solved problem. If reading is all you need, you do not need
this module.

It stops being solved the moment your program **writes** configuration back — a settings
screen, a `--set` flag, a first-run wizard, an `auth login` that stores a token. Here is
what changing one value does when a library serialises its merged view over your file:

<table>
<tr><th>Before</th><th>After changing <code>server.port</code></th></tr>
<tr valign="top">
<td>

```yaml
# Which port the listener binds to.
# Needs a firewall change too.
server:
  host: localhost   # dev only
  port: 8080

# Feature flags. Keep alphabetical.
features:
  beta_ui: false
```

</td>
<td>

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

</td>
</tr>
</table>

Every comment is gone, the order is now alphabetical rather than meaningful, a default the
program never had written down is now pinned into the user's file — and a production
password that arrived from an environment variable and was never in any file is now
sitting in one that is probably in git.

That is not a bug in anything. It is what "serialise the merged view" means, and it
follows from merging eagerly: once every source is folded into one map, a writer holding
that map has nothing to write *but* the whole of it. It cannot leave the environment's
contribution out, because it can no longer tell which contribution was the environment's.

**This module never folds its layers away.** A `Store` owns every read, write and watch,
and records provenance *during* the merge instead of reconstructing it afterwards. Changing
`server.port` above changes `server.port`, and nothing else.

## Design

- **Writes preserve what a human wrote.** Comments stay attached to their keys; order,
  quoting and block style survive; repeated writes converge. The change lands in the layer
  that actually owns the key, so the value you set is the value you read back — and
  nothing from another layer comes along for the ride. `Plan` is a dry run that cannot
  drift from `Apply`, because it *is* the same routing pass.
- **Per-key provenance.** `Origin` reports which source supplied a value, `Shadowed` lists
  every layer defining it, and `Explain` renders the chain — so "why is this value what it
  is" has an answer. A write that a higher layer still shadows says so rather than
  appearing to succeed.
- **Safe hot-reload.** A candidate that fails to build or validate is rejected and the
  last-known-good configuration is retained — never a mixture of before and after.
  Observers are told exactly once per logical change; rejections travel on
  `OnReloadError`. Writing from inside an observer is refused, so the cascade it would
  cause is unrepresentable rather than something to remember to break.
- **Typed sections + validation.** `ObservedSection[T]` keeps a struct current across
  reloads, and `Schema` / `ValidateStruct[T]` check values against field rules. A package
  consuming settings declares a one-method interface over its own struct and never imports
  this one.
- **Read any type, including your own.** `Value[T]` reads whatever `T` is; durations, IP
  addresses, URLs, timezones and anything implementing `encoding.TextUnmarshaler` decode
  from their ordinary written form.
- **Everything is a layer.** Files, defaults, environment, flags and runtime `AddLayer`
  sources all take part in precedence, provenance and shadowing the same way.

## Should you use this?

**Probably not**, if your program only reads configuration, or one file and some
environment variables cover you. [Viper](https://github.com/spf13/viper) does that job
well, is battle-tested by an enormous number of programs, and has an ecosystem this module
does not. Much of what is here was learned from years of using it — see the
[history](https://config.go.phpboyscout.uk/about/history/).

**Yes**, if your program writes configuration back and users live in the file afterwards;
if you have lost an afternoon to "which source set this?"; if a long-running service needs
reload to be safe rather than eager; or if config lands in files that get committed and
reviewed.

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

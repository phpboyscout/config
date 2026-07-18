# Getting started

This tutorial loads a YAML config file, reads typed values through the `Containable`
interface, projects a subtree onto a struct, and reacts to a change at runtime — the
whole shape of the package in about forty lines. It runs on any machine, no external
services.

## Prerequisites

- Go 1.26 or newer.

## 1. Create a module

```bash
mkdir cfgdemo && cd cfgdemo
go mod init cfgdemo
go get gitlab.com/phpboyscout/go/config
```

## 2. Load a config file

Write a `config.yaml`:

```yaml
server:
  host: localhost
  port: 8080
log:
  level: info
```

Then load it. `LoadFilesContainer` takes an [afero](https://github.com/spf13/afero)
filesystem (use `afero.NewOsFs()` for the real disk) and returns a `Containable`:

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

	fmt.Println("host:", cfg.GetString("server.host"))
	fmt.Println("port:", cfg.GetInt("server.port"))
}
```

```bash
go run .
# host: localhost
# port: 8080
```

Environment variables override file values automatically: `SERVER_PORT=3000 go run .`
prints `port: 3000`.

## 3. Project a subtree onto a struct

Instead of stringly-typed keys, unmarshal a subtree into a struct with
`UnmarshalSection` — the `config:` tags map fields to keys **relative to** the
section:

```go
type Server struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

section, err := config.UnmarshalSection[Server](cfg, "server")
if err != nil {
	panic(err)
}
fmt.Printf("%s:%d\n", section.Value.Host, section.Value.Port)
// localhost:8080
```

!!! note "Two different struct tags"
    Sections decode with **`mapstructure:`** (falling back to `json:` then `yaml:`).
    The separate [validation](how-to/validate-config.md) path uses **`config:`** tags.
    They are different mechanisms — don't mix them up.

## 4. React to a change at runtime

Register an observer; when a watched file changes, the container rebuilds, validates,
and — only on success — notifies your callback:

```go
cfg.AddObserverFunc(func(c config.Containable) error {
	fmt.Println("reloaded — port is now", c.GetInt("server.port"))
	return nil
})

select {} // keep running; edit config.yaml and save to see the reload
```

Edit `config.yaml`, change the port, and save — the callback fires with the new
value. If your edit is invalid YAML or fails validation, the change is **rejected**
and the last-known-good config is kept (see [hot-reload safety](explanation/hot-reload-safety.md)).

## Where next

- **[Load & merge configuration](how-to/load-and-merge.md)** — multiple files,
  embedded defaults, and the precedence chain.
- **[Use typed sections](how-to/typed-sections.md)** — `UnmarshalSection` and the
  hot-reloaded `ObserveSection`.
- **[Validate configuration](how-to/validate-config.md)** — catch bad config at load
  time with a `Schema`.
- **[Test with the config mock](how-to/test-with-mocks.md)** — inject a
  `Containable` mock in your tests.

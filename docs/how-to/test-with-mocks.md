# Test with the config mock

Code that reads configuration should take a `Containable`, not a concrete `*Container`
— so tests can pass a mock and assert exactly which keys are read, or drive specific
return values, without building a real config file. The module ships those mocks in
the **`configmock`** package.

## Depend on the interface

```go
// production code takes the interface
func NewServer(cfg config.Containable) *Server {
	return &Server{addr: cfg.GetString("server.host"), port: cfg.GetInt("server.port")}
}
```

## Use the published mock

`configmock.MockContainable` is a [testify](https://github.com/stretchr/testify) mock
of `Containable` (and `MockObservable` of `Observable`). Set expectations with the
generated `EXPECT()` helpers:

```go
import (
	"testing"

	"github.com/stretchr/testify/assert"

	"gitlab.com/phpboyscout/go/config/configmock"
)

func TestNewServer(t *testing.T) {
	cfg := configmock.NewMockContainable(t) // fails the test on unexpected calls
	cfg.EXPECT().GetString("server.host").Return("localhost")
	cfg.EXPECT().GetInt("server.port").Return(8080)

	srv := NewServer(cfg)

	assert.Equal(t, "localhost", srv.addr)
	assert.Equal(t, 8080, srv.port)
}
```

`NewMockContainable(t)` registers cleanup that asserts every expectation was met, so a
missing read fails the test.

### Mocking nested sections

`Sub()` returns another `Containable`, so return a second mock from it:

```go
sub := configmock.NewMockContainable(t)
sub.EXPECT().GetString("host").Return("db.local")

cfg := configmock.NewMockContainable(t)
cfg.EXPECT().Sub("database").Return(sub)
```

## When you want real behaviour

If you're testing the *config* behaviour itself (merge, precedence, unmarshal) rather
than a consumer, don't mock — build a real container from a reader, which needs no
files:

```go
cfg := config.NewReaderContainer(afero.NewMemMapFs(),
	config.WithConfigFormat("yaml"),
	config.WithConfigReaders(strings.NewReader("server:\n  port: 8080\n")))

assert.Equal(t, 8080, cfg.GetInt("server.port"))
```

!!! note "Readers need an explicit format"
    `WithConfigFormat` is **required** for reader-backed containers — there is no
    filename to infer `yaml`/`json`/`toml` from.

Use the mock to test *consumers* of config; use a reader container to test config
*itself*. For file-based tests, `afero.NewMemMapFs()` + `afero.WriteFile` gives you real
merge behaviour with no disk.

## Testing observers

Observers often carry critical logic (restarting services, changing log levels), so test
them — but you don't need a file watcher to do it.

**Unit-test the logic** by calling it directly with a mock:

```go
cfg := configmock.NewMockContainable(t)
cfg.EXPECT().GetString("log.level").Return("debug")

require.NoError(t, (&levelWatcher{}).Run(cfg))
```

**Test registration** with a reader container. Reader containers never watch files, so
run the registered observers yourself via `GetObservers()` — it exists precisely as a
testability affordance:

```go
cfg := config.NewReaderContainer(afero.NewMemMapFs(),
	config.WithConfigFormat("yaml"),
	config.WithConfigReaders(strings.NewReader("log:\n  level: debug\n")))

registerObservers(cfg) // the code under test

for _, o := range cfg.GetObservers() {
	require.NoError(t, o.Run(cfg))
}
```

For genuine end-to-end reload tests see
[hot-reload](hot-reload.md#testing-reload-behaviour) — inject a small debounce and poll.

## Debugging a config surprise

Three affordances for "why is this value what it is?":

```go
cfg.Dump(os.Stdout)                    // every resolved value, as JSON, to a writer
slog.Default().Info("config", "all", cfg.ToJSON())
cfg.GetViper().AllSettings()           // sanctioned escape hatch for deep introspection
cfg.ConfigFiles()                      // which files actually contributed, in merge order
```

When a value surprises you, walk the precedence chain: **flags → env → files (later
override earlier) → embedded → defaults**.

## Related

- [Getting started](../getting-started.md)
- [Use typed sections](typed-sections.md)

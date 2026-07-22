---
title: Test with the config mocks
description: Use MockReader, MockBinder and MockObserved to test code that consumes configuration.
tags: [how-to, testing, mocks]
---

# Test with the config mocks

Code that reads configuration should take a `config.Reader`, not a concrete `*Store` —
so a test can pass a mock, assert exactly which keys are read, and drive specific
return values without building a real configuration file. The module ships generated
[testify](https://github.com/stretchr/testify) mocks in the **`mocks`** package.

## What is in the package

| Mock | Mocks | Use it when |
|---|---|---|
| `MockReader` | `config.Reader` | testing anything that only reads configuration |
| `MockObserved` | `config.Observed` | testing an observer's `Run` in isolation |
| `MockObservable` | `config.Observable` | asserting that your wiring registered an observer, or that it ran |
| `MockBinder` | `config.Binder` | testing a typed section without a real `Store` |
| `MockBackend` | `config.Backend` | testing a component that consumes a custom source |
| `MockWritableBackend` | `config.WritableBackend` | as above, for one that can persist |
| `MockWatchableBackend` | `config.WatchableBackend` | as above, for one that reports its own changes |

`config.Watcher` is deliberately **not** mocked. It is a one-method interface, and a
test usually wants to hold the trigger rather than script a return value — write the
four-line stub shown in
[testing reload behaviour](hot-reload.md#testing-reload-behaviour).

Each has a `NewMockX(t)` constructor that registers cleanup asserting every
expectation was met, so a read your code was supposed to perform and did not fails the
test.

## Depend on the interface

```go
// production code takes the interface, not the store
func NewServer(cfg config.Reader) *Server {
	return &Server{addr: cfg.GetString("server.host"), port: cfg.GetInt("server.port")}
}
```

!!! tip "Do not hand-write a `Reader`"
    `Reader` is broad — thirty methods, covering every accessor, provenance and both
    unmarshal entry points — and it grows as typed accessors are added. A hand-written
    fake has to be updated every time, for no benefit. Use `mocks.MockReader`, or a real
    store over an in-memory filesystem.

    If your component only needs two values, the better fix is to take those two values
    rather than a `Reader` at all.

`*config.View` satisfies `config.Reader`, so the wiring passes `store.View()` and the
test passes a mock.

## Use the published mock

```go
import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"gitlab.com/phpboyscout/go/config"
	"gitlab.com/phpboyscout/go/config/mocks"
)

func TestNewServer(t *testing.T) {
	cfg := mocks.NewMockReader(t) // fails the test on unexpected calls
	cfg.EXPECT().GetString("server.host").Return("localhost")
	cfg.EXPECT().GetInt("server.port").Return(8080)

	srv := NewServer(cfg)

	assert.Equal(t, "localhost", srv.addr)
	assert.Equal(t, 8080, srv.port)
}
```

Reach for this when the *keys read* are the thing under test. It is the only way to
assert that a component reads `server.host` and nothing else, which is a genuine
regression risk when someone adds a stray lookup.

### Mocking a `config.Value[T]` read

`Value[T]` is a function over `Reader`, and it reads through `UnmarshalKey` — so that is
what to expect, writing the result through the pointer it is handed:

```go
cfg.EXPECT().UnmarshalKey("log.level", mock.Anything).
	Run(func(_ string, target any) {
		*(target.(*Severity)) = Debug
	}).
	Return(nil)
```

If the keys read are not what your test is about, a real store over an in-memory file is
less work than this — see [when you want real behaviour](#when-you-want-real-behaviour).

## Test an observer's logic

An observer is handed a `config.Observed`, so `MockObserved` drives it directly — no
watcher, no files, no timing:

```go
cfg := mocks.NewMockObserved(t)
cfg.EXPECT().GetString("log.level").Return("debug")

require.NoError(t, (&levelWatcher{}).Run(cfg))
```

Observers often carry the riskiest logic in a service — restarting listeners, changing
log levels — and this is the cheapest way to test it. For the reload machinery itself,
see [hot-reload](hot-reload.md#testing-reload-behaviour), which drives change detection
with a stub `Watcher`.

`MockObserved.Sub` returns a `*config.View`, not another mock, because `Sub` is
concrete on the read surface. Return `nil` for an absent key, or a real view built
with `config.NewView(snapshot)` when the code under test descends into a subtree.

## Test a typed section without a store

`ObserveSection` takes a `config.Binder`, and `MockBinder` is one. This lets you assert
that a component binds the section it claims to, and capture the observer it registers
so you can fire it yourself:

```go
// The two configurations this test moves between. Real stores over in-memory
// files are less work here than mocking a whole Reader.
source := storeOver(t, "server:\n  port: 8080\n") // what the binding starts with
next := storeOver(t, "server:\n  port: 9090\n")   // what a later reload supplies

var observer func(config.Observed) error

binder := mocks.NewMockBinder(t)
binder.EXPECT().View().Return(source.View())
binder.EXPECT().AddObserverFunc(mock.Anything).
	Run(func(fn func(config.Observed) error) { observer = fn }).
	Return()

settings, err := config.ObserveSection[Server](binder, "server")
require.NoError(t, err)
require.Equal(t, 8080, settings.Value().Port)

// Later: simulate a reload with whatever configuration you like.
require.NoError(t, observer(next.View()))
require.Equal(t, 9090, settings.Value().Port)
```

`storeOver` is a two-line helper, and `Server` is your own settings struct:

```go
type Server struct {
	Port int `mapstructure:"port"`
}

func storeOver(t *testing.T, body string) *config.Store {
	t.Helper()

	fsys, err := config.Dir(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, fsys.WriteFile("/config.yaml", []byte(body), 0o600))

	store, err := config.NewStore(context.Background(), config.WithFiles(fsys, "/config.yaml"))
	require.NoError(t, err)

	return store
}
```

## When you want real behaviour

If what you are testing is the *configuration* behaviour itself — merging, precedence,
provenance, decoding — do not mock. Build a real `Store` from in-memory readers, which
needs no filesystem at all:

```go
store, err := config.NewStore(ctx,
	config.WithReaders(
		config.NamedSource{Name: "defaults", Content: []byte("server:\n  port: 8080\n")},
		config.NamedSource{Name: "overlay", Content: []byte("server:\n  port: 9090\n")},
	),
)
require.NoError(t, err)

assert.Equal(t, 9090, store.View().GetInt("server.port"))
```

For file behaviour — watching, writing, missing overlays — use `config.Dir(t.TempDir())`,
which gives you real merge and write behaviour with nothing on disk:

```go
fsys, err := config.Dir(t.TempDir())
require.NoError(t, err)
require.NoError(t, fsys.WriteFile("/app.yaml", []byte("server:\n  port: 8080\n"), 0o644))

store, err := config.NewStore(ctx, config.WithFiles(fsys, "/app.yaml"))
require.NoError(t, err)
```

Note that `fsnotify` cannot see an in-memory filesystem, so `Watch` falls back to
polling there — which is why a test that needs deterministic change detection should
supply its own `Watcher` rather than rely on either.

Use a mock to test *consumers* of configuration; use a real store to test
configuration *itself*.

## Test the environment without touching the process

Process environment is global state, so mutating it makes parallel tests interfere with
each other. `WithEnviron` supplies the variables instead:

```go
store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/app.yaml"),
	config.WithEnv("MYTOOL", config.WithEnviron(func() []string {
		return []string{"MYTOOL_SERVER_PORT=9090"}
	})),
)
```

## Check which observers were registered

`store.Observers()` returns the registered observers, which exists for exactly this —
asserting that your wiring code registered what it should:

```go
registerObservers(store) // the code under test

assert.Len(t, store.Observers(), 3)
```

## Debugging a config surprise

Three questions cover nearly every "why is this value what it is?":

```go
view := store.View()

fmt.Println(view.Explain("server.port"))    // the whole provenance chain
fmt.Println(view.Shadowed("server.port"))   // every layer defining it, lowest first
fmt.Println(store.Sources())                // every backend, in precedence order
```

`view.Keys()` enumerates every leaf path, and `store.Snapshot().Values()` returns the
merged configuration as a plain map — a copy, so printing or mutating it cannot affect
the store. Remember that precedence is simply the order the sources were added, so
`Sources()` is usually enough to explain a surprise on its own.

## Related

- [Load & merge configuration](load-and-merge.md)
- [Use typed sections](typed-sections.md)
- [React to changes with hot-reload](hot-reload.md)
- [Getting started](../getting-started.md)

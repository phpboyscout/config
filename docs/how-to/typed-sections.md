# Use typed sections

Reading `view.GetString("server.host")` everywhere is stringly-typed and easy to get
wrong. A **section** projects a subtree of the configuration onto one of your structs,
once, so the rest of your code works with ordinary Go data.

This is also the boundary that keeps packages reusable: a package should accept *its
own settings struct*, not a configuration store. The
[last section of this page](#stay-extractable-depend-on-a-tiny-local-interface) shows
how that works.

!!! note "Assumed setup"
    Every snippet below assumes a `store` and a `ctx`:

    ```go
    ctx := context.Background()

    store, err := config.NewStore(ctx,
        config.WithFiles(config.OS(), "/etc/app/config.yaml"),
        config.WithEnv("APP"),
    )
    if err != nil {
        return err
    }
    ```

    See [Load & merge configuration](load-and-merge.md) for the full set of options.

## Unmarshal a section

`UnmarshalSection[T]` decodes the subtree at `key` into a `Section[T]`. It takes a
`config.Reader`, so a `*View` — or a mock — will do. Field tags are **relative to**
the section key:

```go
type ServerSettings struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
	TLS  struct {
		Enabled bool   `mapstructure:"enabled"`
		Cert    string `mapstructure:"cert"`
	} `mapstructure:"tls"`
}

section, err := config.UnmarshalSection[ServerSettings](store.View(), "server")
if err != nil {
	return err
}

srv := section.Value    // ServerSettings — a plain struct field, not a method
if !section.Exists {
	// the "server" key was absent; srv is the zero value (or your defaults)
}
```

`Section[T]` is a small value type: `Value T` and `Exists bool`.

!!! note "Which struct tag?"
    Section decoding treats **`mapstructure:`** as canonical, and accepts **`yaml:`**
    and **`json:`** as aliases — a value spelled under any of the three resolves, with
    `yaml` consulted before `json`. An untagged field matches its own name. Embedded
    structs are flattened, and `,squash` / `,inline` do what they do elsewhere.

    The [validation](validate-config.md) path is a *separate*
    mechanism that reads **`config:`** (plus `validate:` / `enum:` / `default:`). A
    struct can carry both sets of tags.

`MustUnmarshalSection[T]` is the panic-on-error variant — use it **sparingly**, in tests
or package defaults where a panic is acceptable; production code should handle the error.
`view.SectionExists("server")` checks presence without decoding, which lets you
distinguish *absent* from *present-but-empty* (e.g. an optional provider block, or an
"is this configured yet?" check).

!!! warning "One-shot decode leaves long-lived components stale"
    `UnmarshalSection` is a **snapshot**. A long-lived component that decodes once and
    keeps the result will silently ignore every later config change. One-shot decoding is
    for short-lived command paths and tests; anything long-lived should use
    [`ObserveSection`](#keep-a-section-live-with-hot-reload) below.

## Keep a section live with hot-reload

`UnmarshalSection` is a snapshot. `ObserveSection[T]` binds a **live** typed view: it
performs the initial decode, registers a reload observer, validates each fresh
snapshot, **preserves the last valid snapshot if a reload cannot be decoded or
validated**, and optionally invokes an apply callback when the section actually
changed.

!!! warning "Register `OnObserverError` or those failures are silent"
    When a reload cannot be decoded or validated, the binding keeps the last good
    section and returns the error from its observer — which goes to
    `store.OnObserverError`. Without a handler registered there, a section that has
    quietly stopped tracking the file looks exactly like one that has not changed.

    ```go
    store.OnObserverError(func(err error) {
        log.Printf("config: section did not update: %v", err)
    })
    ```

Its first argument is a `config.Binder` — anything with a `View() *View` and an
`AddObserverFunc`. A `*Store` is one, and so is a stub you write for a test:

```go
// srv is your component — whatever holds the listener, pool or client that
// these settings configure. It is not this module's type.
srv := newServer()

settings, err := config.ObserveSection[ServerSettings](store, "server",
	config.WithSectionValidator(func(next ServerSettings) error {
		if next.Port <= 0 {
			return errors.New("port must be positive")
		}

		return nil
	}),
	config.WithSectionApply(func(change config.SectionChange[ServerSettings]) error {
		return srv.Reconfigure(&change.Current.Value)
	}),
)
if err != nil {
	return err
}

log.Printf("serving on port %d", settings.Value().Port)
```

### Configure your component at startup too

The apply callback fires on **change**, not on binding. Nothing calls it for the section
the store already had, so the recipe above reconfigures `srv` on every later reload and
never on the first one.

That is deliberate: `ObserveSection` usually runs while the thing being configured is
still being built, and firing then would run your callback against a half-constructed
world. You say when your dependencies exist:

```go
// Declared before the binding, assigned after it. The callback closes over the
// variable, not over its value, so it sees the real server once it exists —
// and nothing calls it until ApplyInitial does.
var srv *Server

settings, err := config.ObserveSection[ServerSettings](store, "server",
	config.WithSectionApply(func(change config.SectionChange[ServerSettings]) error {
		return srv.Reconfigure(&change.Current.Value)
	}),
)
if err != nil {
	return err
}

srv = newServer(deps...) // your constructor, whatever it takes

// Everything the callback needs now exists, so deliver the initial section.
if err := settings.ApplyInitial(); err != nil {
	return err
}
```

`ApplyInitial` hands the callback the current section with `Initial: true` and
`Previous: nil`. Calling it twice is a no-op, so it is safe in a startup path that may
run more than once, and it does nothing if a reload already beat it — you never get an
older section delivered after a newer one.

If your callback does not care which delivery it is handling, ignore `Initial` and
reconfigure the same way each time. That is usually the right shape.

`ObservedSection[T]` reads (safe from any goroutine):

| Method | Returns |
|---|---|
| `Value() T` | the current value (a copy) — **a method here**, unlike `Section[T].Value` |
| `Current() *T` | pointer to the current snapshot; for anything `ObserveSection` returns this is non-nil even when the section is absent, pointing at the zero value or your defaults |
| `Exists() bool` | whether the latest snapshot came from an explicit section |
| `Version() uint64` | starts at 1 after the initial decode, and bumps **only** when the typed section actually changed |

!!! warning "Snapshots are read-only, and you must re-read them"
    Treat the value behind `Current()` as **immutable** — never mutate it in place. It is
    a published snapshot that other goroutines may be reading. Call `Current()` again
    when you need to observe a later reload; a pointer you captured earlier keeps
    pointing at the older snapshot.

    `Version()` counts **changes, not reloads** — an unrelated reload leaves it alone.

### Whole-snapshot comparison

The binding compares **complete typed snapshots**, not individual keys. An unrelated
config reload — a change somewhere else in the file — does **not** bump `Version()`
and does **not** invoke your apply callback. When the section does change,
`SectionChange[T]` carries both `Previous` and `Current` (plus `Initial`, `Changed`,
`Version`), so a component reconfigures from a whole settings object instead of
diffing individual keys.

### Binding options

| Option | Purpose |
|---|---|
| `WithSectionValidator(func(T) error)` | reject an invalid snapshot; the previous good one is kept |
| `WithSectionApply(func(SectionChange[T]) error)` | run on a real change — reconfigure your component |
| `WithSectionDefaults(defaults T, merge func(defaults, overlay T) T)` | seed defaults and define how an overlay merges over them |
| `WithSectionDefaultFunc(func(Observed) T, merge func(defaults, overlay T) T)` | as above, but the defaults are computed from the configuration snapshot being decoded |
| `WithSectionEqual(func(previous, current Section[T]) bool)` | custom change detection when the default comparison isn't right |

Supplying defaults without a merge function returns `ErrNoMergeFunc`: silently
preferring one over the other would drop half the settings without saying so.

```go
defaults := ServerSettings{Host: "localhost", Port: 8080}

settings, err := config.ObserveSection[ServerSettings](store, "server",
	config.WithSectionDefaults(defaults, func(defaults, overlay ServerSettings) ServerSettings {
		if overlay.Host == "" {
			overlay.Host = defaults.Host
		}
		if overlay.Port == 0 {
			overlay.Port = defaults.Port
		}
		return overlay
	}))
```

## Stay extractable: depend on a tiny local interface

A package that may one day become its own module should **not** import this one just
to read reload-aware settings. Define a minimal interface locally:

```go
// in your reusable package — no config import at all

// ServerSettings is declared here, by the package that consumes it.
type ServerSettings struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

// SettingsSource is the whole dependency: one method.
type SettingsSource interface {
	Current() *ServerSettings
}

type Server struct{ settings SettingsSource }

func NewServer(src SettingsSource) *Server { return &Server{settings: src} }

func (s *Server) addr() string {
	cur := s.settings.Current()

	return fmt.Sprintf("%s:%d", cur.Host, cur.Port)
}
```

`*config.ObservedSection[ServerSettings]` satisfies that shape structurally, so the
wiring code passes it in and your package stays dependency-free. This is exactly how
the phpboyscout toolkit modules were decoupled before extraction — the settings struct
and a one-method interface are the whole contract.

## Related

- [React to changes with hot-reload](hot-reload.md)
- [Validate configuration](validate-config.md)
- [Load & merge configuration](load-and-merge.md)
- [Test with the config mocks](test-with-mocks.md)

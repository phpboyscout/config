# Use typed sections

Reading `view.GetString("server.host")` everywhere is stringly-typed and easy to get
wrong. A **section** projects a subtree of the configuration onto one of your structs,
once, so the rest of your code works with ordinary Go data.

This is also the boundary that keeps packages reusable: a package should accept *its
own settings struct*, not a configuration store. The
[last section of this page](#stay-extractable-depend-on-a-tiny-local-interface) shows
how that works.

## Unmarshal a section

`UnmarshalSection[T]` decodes the subtree at `key` into a `Section[T]`. It takes a
`config.Reader`, so a `*View` — or a mock — will do. Field tags are **relative to**
the section key:

```go
type Server struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
	TLS  struct {
		Enabled bool   `mapstructure:"enabled"`
		Cert    string `mapstructure:"cert"`
	} `mapstructure:"tls"`
}

section, err := config.UnmarshalSection[Server](store.View(), "server")
if err != nil {
	return err
}

srv := section.Value    // Server — a plain struct field, not a method
if !section.Exists {
	// the "server" key was absent; srv is the zero value (or your defaults)
}
```

`Section[T]` is a small value type: `Value T` and `Exists bool`.

!!! note "Which struct tag?"
    Section decoding reads **`mapstructure:`**, falling back to **`json:`** then
    **`yaml:`** if absent. The [validation](validate-config.md) path is a *separate*
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

Its first argument is a `config.Binder` — anything with a `View() *View` and an
`AddObserverFunc`. A `*Store` is one, and so is a stub you write for a test:

```go
settings, err := config.ObserveSection[Server](store, "server",
	config.WithSectionValidator(func(next Server) error {
		if next.Port <= 0 {
			return errors.New("port must be positive")
		}
		return nil
	}),
	config.WithSectionApply(func(change config.SectionChange[Server]) error {
		return server.Reconfigure(&change.Current.Value)
	}),
)
if err != nil {
	return err
}
```

`ObservedSection[T]` reads (safe from any goroutine):

| Method | Returns |
|---|---|
| `Value() T` | the current value (a copy) — **a method here**, unlike `Section[T].Value` |
| `Current() *T` | pointer to the current snapshot; when the section is absent it points at the zero value (or your defaults), not nil |
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
defaults := Server{Host: "localhost", Port: 8080}

settings, err := config.ObserveSection[Server](store, "server",
	config.WithSectionDefaults(defaults, func(defaults, overlay Server) Server {
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
// in your reusable package — no config import
type SettingsSource interface {
	Current() *ServerSettings
}

func NewServer(src SettingsSource) *Server { ... }
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

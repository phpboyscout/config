# React to changes with hot-reload

A long-running service often needs to pick up config changes without a restart. A
file-backed `Container` watches every configured file and — on a **valid** change —
rebuilds the config and notifies observers. This guide shows how to wire that up and the
traps to avoid.

Only file-backed containers watch (`LoadFilesContainer`, `LoadFilesContainerWithSchema`,
`NewFilesContainer`). Reader and embedded containers have no file to watch.

## Observe with a callback

The idiomatic pattern for simple, stateless reconfiguration:

```go
cfg.AddObserverFunc(func(c config.Containable) error {
	applyLogLevel(c.GetString("log.level"))
	return nil
})
```

For stateful reconfigurers, implement `Observable`:

```go
type levelWatcher struct{ logger *slog.Logger }

func (w *levelWatcher) Run(c config.Containable) error {
	w.logger.Info("config reloaded", "level", c.GetString("log.level"))
	return nil
}

cfg.AddObserver(&levelWatcher{logger: slog.Default()})
```

**Register one observer per concern** — log level, rate limit, pool size, cache — rather
than one mega-observer. They run in registration order, and an error from one is logged
without aborting the others or stalling future reloads.

## Release the watcher on shutdown

A file-backed container owns an OS watcher. **Always** close it:

```go
cfg, err := config.LoadFilesContainer(afero.NewOsFs(), config.WithConfigFiles("config.yaml"))
if err != nil {
	return err
}
defer cfg.Close() // idempotent, and a no-op on non-watching containers
```

## Observers do not fire at startup

They fire on *changes* only. If you need the same logic applied at boot and on every
reload, extract it and use it twice:

```go
func reconfigure(c config.Containable) error {
	return applyLogLevel(c.GetString("log.level"))
}

if err := reconfigure(cfg); err != nil { // startup
	return err
}
cfg.AddObserverFunc(reconfigure)          // subsequent changes
```

## Keep handlers fast

Observers run **sequentially on the watch goroutine**, so a slow handler delays every
later observer and the next reload. For expensive work (reconnecting a pool, rebuilding
an exporter), hand off rather than blocking:

```go
reloads := make(chan struct{}, 1)

cfg.AddObserverFunc(func(c config.Containable) error {
	select {
	case reloads <- struct{}{}: // signal the worker
	default:                    // a reload is already pending — drop this one
	}
	return nil
})
```

## Handle rejected reloads

Observers fire **only** on a successful reload. When a candidate fails to build or
validate, the change is rejected, last-known-good is retained, and observers are *not*
called. Register `OnReloadError` to learn about those:

```go
cfg.OnReloadError(func(err error) {
	slog.Default().Warn("config reload rejected; keeping last-known-good", "error", err)
})
```

The container already logs the rejection at `ERROR` — this hook is **additive**, for
alerting, metrics, or surfacing a banner. See
[hot-reload safety](../explanation/hot-reload-safety.md) for the full guarantees.

## Tune the debounce

A single save emits a burst of filesystem events; they are coalesced behind a debounce
window (default `DefaultReloadDebounce`, 250 ms — generous so slow or networked
filesystems still coalesce correctly):

```go
cfg, err := config.LoadFilesContainer(afero.NewOsFs(),
	config.WithConfigFiles("config.yaml"),
	config.WithReloadDebounce(50*time.Millisecond))
```

Values ≤ 0 fall back to the default.

## Track typed settings instead

If what you want is a *typed* view that stays current, prefer
[`ObserveSection`](typed-sections.md#keep-a-section-live-with-hot-reload) over a
hand-written observer — it decodes, validates, keeps the last valid snapshot on failure,
and tells you when the section actually changed.

!!! warning "A version bump is a notification, not an action"
    Observing a change does not apply it. Components holding OS-level state — listeners,
    sockets, exporters — need explicit restart or redial logic before a changed port or
    TLS path affects live traffic.

## Testing reload behaviour

- Unit-test observer *logic* by calling `observer.Run(cfg)` directly — no watcher, no
  files, no timing. A [mock](test-with-mocks.md) container works well here.
- For end-to-end reload tests, inject a small debounce (`WithReloadDebounce(20ms)`) and
  **poll** for the effect (`require.Eventually`) rather than hard-sleeping. These tests
  are inherently filesystem- and timer-sensitive; keep the timeouts generous or they will
  flake on a loaded CI runner.

## Related

- [Hot-reload safety](../explanation/hot-reload-safety.md) — the guarantees and why
- [Use typed sections](typed-sections.md)
- [Validate configuration](validate-config.md) — gate reloads on a schema

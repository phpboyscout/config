# React to changes with hot-reload

A long-running service often needs to pick up configuration changes without a
restart. A `Store` can watch its file-backed sources, re-read them on a change, and —
if the result is valid and actually different — publish a new snapshot and tell your
observers. This guide shows how to wire that up and the traps to avoid.

## Start watching

`Watch` begins reacting to changes made outside this process, and returns a function
that stops it:

```go
stop, err := store.Watch(ctx)
if err != nil {
	return err
}

defer stop()
```

Watching covers every source that can change on its own — which means files, and any
backend of your own implementing `WatchableBackend`. Environment variables and flags
do not change under a running process, and an in-memory layer is changed by the code
that owns it, so none of those are watched.

A store with nothing watchable fails here with `ErrWatchUnavailable` rather than
silently doing nothing: an application that believes it will hear about changes and
never does is worse off than one that knows it must restart.

Native filesystem notification is used where it works, and polling everywhere else —
`fsnotify` operates on real paths, so it cannot see an in-memory filesystem, and a network
mount or a container with its inotify watches exhausted will not support it either.

The choice is made **per path**, not once for the whole set, so one unwatchable file does
not downgrade the others to polling. If native notification stops being trustworthy while
running, the affected paths degrade to polling silently — that is a successful recovery,
not something you need to handle. If polling cannot be established *either*, the watch has
genuinely stopped working, and that is reported through `OnReloadError` rather than
allowed to pass quietly.

Set the polling interval when the default (`DefaultPollInterval`, two seconds) is not what
you want:

```go
stop, err := store.Watch(ctx, config.WithPollInterval(500*time.Millisecond))
```

## Reload on demand

You do not need a watcher to reload. `Reload` re-reads every backend and, on success,
publishes a new snapshot — useful behind a `SIGHUP` handler or an admin endpoint:

```go
if err := store.Reload(ctx); err != nil {
	return err
}
```

Reloading is fail-closed. If any source fails to load or parse, the error is returned
and the previous snapshot stands: a configuration that is partly the old values and
partly the new is worse than either, and worse than refusing.

That guarantee needs a previous snapshot to fall back to, so it applies to reloads and
not to the first load. `NewStore` returns a usable store alongside `ErrInvalidConfig`
when the initial configuration fails its schema — otherwise a bad file would leave you
with no store, and no way to write the fix. See
[an invalid configuration is still repairable](validate-config.md#an-invalid-configuration-is-still-repairable).

## Observe a change

Register a function for simple, stateless reconfiguration:

```go
store.AddObserverFunc(func(cfg config.Observed) error {
	return applyLogLevel(cfg.GetString("log.level"))
})
```

For a stateful reconfigurer, implement `Observable`:

```go
type levelWatcher struct{ logger *slog.Logger }

func (w *levelWatcher) Run(cfg config.Observed) error {
	w.logger.Info("config reloaded", "level", cfg.GetString("log.level"))

	return nil
}

store.AddObserver(&levelWatcher{logger: slog.Default()})
```

The `Observed` handed to an observer is pinned to the snapshot that triggered the
notification, not to whatever is current. Without that, an observer reacting to one
change could read values from a *later* change partway through its own callback and
produce a result that never existed as a coherent configuration. `cfg.Snapshot()`
exposes that exact snapshot if you need its version or its layers.

**Register one observer per concern** — log level, rate limit, pool size, cache —
rather than one mega-observer. They run in registration order, and an error from one
is reported without stopping the others: the configuration has already changed, and
one component being unable to react is no reason to withhold the change from the rest.

## Do not write from inside an observer

Calling `Apply`, `AddLayer` or `Reload` from within an observer returns
`ErrWriteFromObserver`. This is refused outright rather than made to work: each such
write is itself a change, which notifies, which runs the observer again — a cascade
with no natural end, and one the Store cannot break without either dropping
notifications other components need or silently reordering what changed when.

The supported pattern is to take the change *out* of the observation: record what is
needed, return, and perform the write from somewhere else.

```go
writes := make(chan config.Change, 1)

store.AddObserverFunc(func(cfg config.Observed) error {
	// Capture what is needed and return; do not write here.
	select {
	case writes <- config.Set("server.generation", cfg.GetInt("server.generation")+1):
	default: // a write is already pending — this one supersedes nothing
	}

	return nil
})

go func() {
	for change := range writes {
		if _, err := store.Apply(ctx, change); err != nil {
			slog.Default().Error("deferred config write failed", "error", err)
		}
	}
}()
```

The refusal is scoped to the goroutine running the observer, so an unrelated goroutine
writing while observers happen to be executing is unaffected — that is a legitimate
concurrent write, and rejecting it would be a worse bug than the cascade this
prevents. Serialising and de-duplicating deferred writes is yours to decide, because
you are the only party that knows which of them still matter.

## A notification means something really changed

Two guarantees are worth relying on:

- **Exactly one notification per logical change.** One `Apply` touching two files
  notifies once, not twice.
- **A change that changes nothing notifies nobody.** Filesystem events are noisy —
  permission changes, atomic renames, editors writing in several passes — and none of
  them alters configuration. The Store compares the candidate with what it already has
  and stays quiet when they match. Rewriting a file with identical content is not a
  change either.

The Store's own writes never travel through the watcher. `Apply` builds the next
snapshot from the content it just wrote and notifies directly, so a write cannot come
back round through the filesystem and be seen a second time.

!!! info "A foreign change spanning several files is coalesced — up to a point"
    "Exactly once per logical change" means a change the Store performed. An `Apply` is
    one logical change however many files it touches, because the Store knows the batch.

    When **something else** changes two files — a deploy, a config-management run, an
    operator editing two overlays — those are separate filesystem events, and nothing
    tells the Store they were meant as one change. So it waits for the burst to settle
    before reloading: `DefaultSettleInterval`, 250ms, configurable with
    `WithSettleInterval`.

    ```go
    stop, err := store.Watch(ctx, config.WithSettleInterval(500*time.Millisecond))
    ```

    **This reduces the problem rather than removing it.** Writes spaced further apart than
    the window are indistinguishable from two separate changes, because that is what they
    look like — observers run twice, and the first run sees the first file updated and the
    second not yet. Each snapshot is still internally coherent, never a mixture of two
    reads, so you will not see a torn value; you can see a real combination nobody
    intended.

    If it matters to your component, in order of preference:

    - **Keep settings that change together in one file.** A single file is always read
      atomically, which removes the problem rather than shortening the window in which it
      can happen. This is the right answer nearly always.
    - **Swap atomically at the directory level** if the deploy system can — a Kubernetes
      ConfigMap update replaces a symlinked directory in one operation.
    - **Widen the window** to cover your deploy's write span, accepting the reload latency.
    - **Use a typed section.** `ObserveSection[T]` delivers only when *your struct*
      changed, so an intermediate state that does not touch your fields never reaches you.

    Settling applies only to foreign changes. `Apply` never waits, and an injected watcher
    (`WithWatcher`) defaults to no window so tests stay deterministic.

## Observers do not fire at startup

They fire on *changes* only. If the same logic applies at boot and on every reload,
extract it and use it twice:

```go
reconfigure := func(cfg config.Observed) error {
	return applyLogLevel(cfg.GetString("log.level"))
}

if err := reconfigure(store.View()); err != nil { // startup
	return err
}

store.AddObserverFunc(reconfigure) // subsequent changes
```

A `*View` satisfies `Observed`, so one function serves both paths.

## Keep handlers fast

Observers run sequentially on the goroutine performing the reload, so a slow handler
delays every later observer and the reload itself. For expensive work — reconnecting a
pool, rebuilding an exporter — hand off rather than blocking:

```go
reloads := make(chan struct{}, 1)

store.AddObserverFunc(func(_ config.Observed) error {
	select {
	case reloads <- struct{}{}: // signal the worker
	default: // a reload is already pending — drop this one
	}

	return nil
})
```

## Handle rejected reloads and failing observers

The two are different events and travel on different channels, because they mean
different things. A rejected reload means the configuration did **not** change; a
failing observer means it did and one component could not keep up.

```go
store.OnReloadError(func(err error) {
	slog.Default().Warn("config reload rejected; keeping last-known-good", "error", err)
})

store.OnObserverError(func(err error) {
	slog.Default().Error("a component failed to apply new configuration", "error", err)
})
```

Announcing a rejection to observers would be a lie: they would re-read values
identical to the ones they already hold. With a schema attached, a candidate that
fails validation is rejected the same way — the last known good configuration stays
live and observers hear nothing. See
[Validate configuration](validate-config.md) and
[hot-reload safety](../explanation/hot-reload-safety.md).

## Track typed settings instead

If what you want is a *typed* view that stays current, prefer
[`ObserveSection`](typed-sections.md#keep-a-section-live-with-hot-reload) to a
hand-written observer — it decodes, validates, keeps the last valid snapshot on
failure, and tells you when the section actually changed rather than when anything in
the file did.

!!! warning "A notification is not an action"
    Observing a change does not apply it. Components holding OS-level state —
    listeners, sockets, exporters — need explicit restart or redial logic before a
    changed port or TLS path affects live traffic.

## Testing reload behaviour

Unit-test observer *logic* by calling it directly — no watcher, no files, no timing.
A [mock `Observed`](test-with-mocks.md) works well here.

For the reload path itself, drive change detection deterministically with your own
`Watcher` instead of waiting on a real filesystem. It is a one-method interface:

```go
type manualWatcher struct{ fire func() }

func (w *manualWatcher) Watch(_ context.Context, _ []string, onChange func()) (func(), error) {
	w.fire = onChange

	return func() {}, nil
}
```

Pass it with `WithWatcher`, write the file, then trigger:

```go
w := &manualWatcher{}

stop, err := store.Watch(ctx, config.WithWatcher(w))
require.NoError(t, err)

defer stop()

require.NoError(t, afero.WriteFile(fsys, "/app.yaml", []byte("value: second\n"), 0o644))
w.fire() // the store re-reads and notifies, synchronously

assert.Equal(t, "second", store.View().GetString("value"))
```

That gives a test with no sleeps and no polling. Keep `require.Eventually` for the
handful of cases that genuinely exercise a real filesystem watcher.

`WithWatcher` **replaces** the whole watch set rather than adding to it, and the paths
it is handed are the file backends' — a custom backend's sources are not among them.
Both are what you want in a test, where the point is to control exactly when a change
is reported. Neither is what you want in production.

## Watch a source of your own

A backend is watched if it implements `WatchableBackend`, which is the whole of the
contract:

```go
Watch(ctx context.Context, interval time.Duration, onChange func()) (stop func(), err error)
```

Call `onChange` when your sources *may* have changed — you are not expected to know
whether anything that resolves actually moved, and deciding that is the store's job.
It handles the rest: re-reading, merging, validating, and rejecting the result if it
does not hold up, so a spurious `onChange` costs a read and nothing else.

`interval` is the polling interval from `WithPollInterval`; ignore it if you have a
real subscription. Return an error only if you cannot watch at all, and make the
returned stop function safe to call twice.
Register the backend with `WithBackend` as usual. See
[Backends & capabilities](../explanation/backends.md).

## Related

- [Hot-reload safety](../explanation/hot-reload-safety.md) — the guarantees and why
- [Use typed sections](typed-sections.md)
- [Validate configuration](validate-config.md) — gate reloads on a schema
- [Test with the config mocks](test-with-mocks.md)

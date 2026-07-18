# Hot-reload safety

Hot-reloading configuration is dangerous if done naïvely: a fat-fingered edit or a
half-written file could put a running service into a broken state. This module's reload
path is built around one guarantee — **a running service never applies a config it
couldn't also have started with** — and every rule below exists because the naïve
version of it failed in production.

## Why the container owns the watcher

Viper ships `WatchConfig`, and the obvious implementation is to use it. That does not
work for a **merged, multi-file** configuration, and the failure is silent:

Viper's merge is built by repeated `SetConfigFile` + `MergeInConfig`, which leaves its
internal `configFile` pointing at the **last** file. `WatchConfig` therefore watches only
that one file, and on change calls `ReadInConfig()`, which **replaces** the entire
settings map with that single file's contents — silently dropping every earlier layer. A
tweak to your local overrides file would erase your base config.

So the container owns its own `fsnotify` watcher across **all** configured files, and
Viper's watch machinery is not used at all. Everything below follows from that decision.

## The reload cycle: candidate → validate → swap

On a change the container:

1. **Rebuilds a candidate** — re-reads file `[0]` and re-merges files `[1:]` in order,
   *exactly as on first load*, so the merged multi-file view is preserved.
2. **Validates the candidate** — if a schema is attached, it is checked **before**
   anything is swapped.
3. **Swaps atomically** — on success the live config pointer is replaced under the
   container lock, then observers run.
4. **Rejects otherwise** — the candidate is discarded, last-known-good is retained.

The ordering is the point. An earlier implementation validated *after* Viper had already
swapped its map, so a "rejected" reload still served invalid values. **Validate a
candidate; never validate after mutating.**

## Fail-closed, at both levels

A reload is rejected — and last-known-good retained — when:

- any file in the merge fails to parse (file `[0]` valid but file `[2]` malformed still
  rejects the *whole* reload — no half-merged state is ever observable),
- the primary file has gone missing, or
- the candidate fails schema validation.

The same doctrine applies one level down, to
[typed sections](../how-to/typed-sections.md): if a reload cannot be decoded or fails a
section validator, the binding keeps the last valid snapshot — `Current()` continues to
return the previous settings, `Version()` does not increment, and the apply callback is
not invoked.

## Two callback channels, deliberately separate

- **Observers** (`AddObserver` / `AddObserverFunc`) fire **only on success**. They are
  never handed a rejected reload, because nothing changed — calling them would imply a
  change occurred.
- **`OnReloadError`** fires **only on rejection**.

`OnReloadError` exists to fill a specific gap. The observer contract used to be
`Run(Containable, chan error)` — an unbuffered, unread error channel that the documented
usage pattern *sent* on, so the first observer error blocked the reload goroutine and
**permanently stalled all subsequent reloads**. Replacing it with `Run(Containable) error`
deleted that deadlock class outright, but left no way to *push* a reload-time error at
anyone. `OnReloadError` is that path, added deliberately rather than by re-repurposing
observers.

It is **additive to logging**, not a replacement: the container always logs a rejection
at `ERROR` regardless. Use the hook for alerts, metrics, or a UI banner.

## Concurrency rules

Three invariants keep this race-free, and each is a house rule for any future callback
list:

1. **Copy under the lock, invoke outside it.** Observers and `OnReloadError` callbacks
   are stored in mutex-guarded slices, copied under the lock, then invoked outside it.
   Registering a callback concurrently with an active reload is therefore safe under
   `-race`, and a slow callback cannot deadlock a registration.
2. **All reads route through one accessor.** Every read path — `Get*`, `Has`, `IsSet`,
   `Set`, `Sub`, `WriteConfigAs`, `ToJSON`, `Validate` — goes through the live-viper
   accessor, which reads the pointer under lock. A new read method that bypasses it
   reintroduces the race silently.
3. **The watcher starts after construction completes**, so the reload goroutine never
   observes a half-built container.

Callbacks run **sequentially, in registration order, on the watch goroutine**. An
observer that returns an error is logged and does not abort the observers after it or
stall future reloads — but a *slow* observer delays subsequent reloads, so offload
expensive work.

## Debouncing and the atomic-rename trap

A single save typically emits a **burst** of filesystem events (write, rename, chmod).
These are coalesced behind a debounce window — default 250 ms, chosen to tolerate slow or
networked filesystems, configurable with `WithReloadDebounce` (values ≤ 0 fall back to
the default; there is no upper clamp).

Less obvious: many editors save via **atomic rename**, which replaces the file's inode
and thereby invalidates an inode-based watch. The watcher re-establishes its watch on
each path after every event — without that, the *second* save onward is silently missed.

## Candidate and live must be built the same way

The candidate config is constructed with the same internal helper as the live one, so it
inherits the same filesystem, env prefix, `AutomaticEnv`, and key replacer. If the two
ever diverge, environment resolution silently changes after the first reload — config
that worked at startup behaves differently an hour later. There is a dedicated test
pinning env-prefix behaviour across a reload, and it should stay.

More generally: **every new API that reaches into a subtree must be tested for env-prefix
override.** `Sub()` needed a workaround for Viper dropping `AutomaticEnv`, `BindPFlag` had
to route through the qualifying resolver, and section unmarshal was flagged as the
highest-risk case. This is the single most repeated regression risk in the package.

## What it does not do

- **A version bump is a notification, not an action.** When an observed section changes,
  components holding OS-level state — listeners, sockets, exporters, pools — do **not**
  reconfigure themselves. They must implement explicit restart or redial logic. A
  package that cannot genuinely reconfigure live should not pretend otherwise.
- **Observers are not called at startup**, only on subsequent changes. Extract a shared
  `reconfigure(cfg)` function, call it once during startup and register it as the
  observer.
- **It does not unwind your observers' side effects.** If an observer applied a change
  then returned an error, the config is still the new valid one; the error is logged, not
  rolled back. Observers should be idempotent.
- **It does not watch non-file sources.** Reader and embedded containers have no backing
  file, so watching and `Close()` are no-ops — deliberately out of scope, not an
  oversight.

## Release the watcher

A file-backed container owns an OS watcher. Call `Close()` on shutdown to release it.
`Close()` is **idempotent** and safe to call on a non-watching container, so
`defer container.Close()` is always correct.

## Related

- [React to changes with hot-reload](../how-to/hot-reload.md)
- [Use typed sections](../how-to/typed-sections.md)
- [Why a wrapper over Viper](why-a-wrapper.md)

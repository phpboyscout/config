---
title: Hot-reload safety
description: "Why reloading under a running process is safe here: snapshots, fail-closed parsing and coherent reads."
tags: [explanation, hot-reload, safety]
---

# Hot-reload safety

Reloading configuration under a running process is dangerous if done naïvely. A
half-written file, a fat-fingered edit or a merge that lands in the wrong order can put a
healthy service into a state it would never have started in. This module's reload path is
built around one guarantee:

> **A running process never applies a configuration it could not also have started with.**

Everything below follows from that, plus one structural decision that removes most of the
difficulty before the guarantee is even needed.

## The watcher is for other people's changes

The decision worth understanding first is what the watcher is *not* for.

Under the earlier design, a write propagated like this: write the file, `fsnotify` fires,
wait out a debounce window, re-read every file, re-merge, swap, notify. That is a round
trip through the filesystem, gated on a timing heuristic, to learn something the process
already knew, because it had just written it.

`Store.Apply` instead builds the next snapshot **directly from the content it just
wrote**, and notifies from there. The write is self-contained. The watcher's only
remaining job is to detect changes the Store did not make: an operator editing the file, a
deploy tool replacing it, a sibling process writing to it.

That one change removes four problems structurally rather than mitigating them:

- **Notification nondeterminism.** An own write notifies exactly once by construction, no
  matter how many files the batch touched. There is no coalescing heuristic to tune.
- **The write, reload, react feedback loop.** An own write never re-enters through the
  watcher, so that particular cascade is unrepresentable rather than detected and broken.
- **Debounce sensitivity for the process's own saves.** There is no timing window to tune,
  because the Store does not wait for anything.
- **Redundant re-parsing** of files that did not change.

It also closes a genuine failure mode: there is no interval during which the configuration
in memory disagrees with the configuration on disk. `Apply` returns a snapshot that
already reflects what it wrote.

If you are looking for a debounce setting for your own writes, there is none, for the
reason above. Changes made by someone else do still pass through a short settle window,
and the section on exactly-once notification below covers it.

## The reload cycle

A reload, whether triggered by the watcher or by calling `Reload` directly, runs the
same path:

1. **Load every backend** in precedence order, from scratch, exactly as the first load
   did. Not an incremental patch of the previous state.
2. **Validate the candidate** against the schema, if one is attached, before anything is
   published.
3. **Publish** the new snapshot, under the lock.
4. **Compare** it with what was published before, and notify observers only if the
   resolved configuration actually differs.

Step 2 preceding step 3 is not an implementation detail. An earlier implementation
validated *after* the swap, which meant a "rejected" reload had already served invalid
values to anything that read in between. Validate a candidate; never validate after
mutating.

Step 1 matters for a subtler reason. Because the candidate is built by the same code path
as the live configuration, a reload cannot resolve differently from a fresh start. Two
separate construction paths would drift, and the drift would show up as configuration that
worked at startup and behaved differently an hour later.

## Fail-closed, with last-known-good retained

A reload is rejected, and the previous snapshot stands, when any source fails to load or
parse, or when the candidate fails schema validation. `Reload` returns the error;
`OnReloadError` callbacks are told.

The rejection is all-or-nothing across sources. A valid base file and a malformed overlay
reject the whole reload rather than publishing the half that parsed. A configuration that
is partly the old values and partly the new is worse than either of them, and worse than
refusing: it is a state no file on disk describes, so nobody can reason about it or
reproduce it.

Running on last-known-good is better than running on values the application has already
said it cannot use. That is the whole argument, and it is why validation failure is
treated identically to a parse failure.

**The first load is the one exception, and it is deliberate.** A store that has no
last-known-good has nothing to fall back on, so a configuration that parses but fails
validation is published anyway *and* the error is returned. A store with no snapshot could
not be read from or written through, which would make an invalid configuration unfixable
by the very surface designed to fix it. The error still travels, so a caller that wants to
fail fast does; the ordinary `if err != nil` is unchanged. Only a caller that explicitly
checks for `ErrInvalidConfig` proceeds. See
[Validate configuration](../how-to/validate-config.md#an-invalid-configuration-is-still-repairable).

## Exactly one notification, and only for a real change

Two guarantees are worth relying on:

**Exactly once per logical change.** One `Apply` touching two files notifies once, not
twice. Own writes get this by construction. Foreign changes get it because the reload is
a single pass over every backend and a burst of change reports is settled into one
reload first.

**A change that changes nothing notifies nobody.** After publishing, the Store compares
the resolved values with the previous snapshot's and stays silent if they match.

The second is doing more work than it appears to. Filesystem notification is *noisy*: it
delivers events for permission changes, for atomic renames, and for editors that write a
file in several passes. None of those alters configuration. Comparing configurations
answers "did anything change?" directly instead of estimating it from the events, and the
answer does not depend on how fast the filesystem is.

Timing still has a small job, and only for foreign changes. A deploy that replaces two
overlays produces two events, and nothing says they belong together. The Store waits for a
burst to settle before reloading, 250 milliseconds by default (`WithSettleInterval`; zero
disables it), and reloads at most four windows after the first event even if changes keep
arriving. That turns the deploy into one reload and one notification. It is a heuristic:
writes spaced further apart than the window are still seen as separate changes, so a
multi-file change is only atomic if whoever makes it makes it atomically.

The comparison also covers the case no window can: a file genuinely rewritten with
different bytes but the same resolved values (comments reflowed, a key relocated to the
layer that should own it) is not a configuration change, and announcing it would have every
observer re-read values identical to the ones it holds. Without this rule, `Apply` and `Reload`
disagree about what counts as a change, and an idempotent observer loops forever on a write
that keeps being announced as news.

The cost is small: the comparison is a deep equality check over the merged values on every
reload. For configuration-sized data that is nothing, and it is paid only when something
fired.

## Observers see the snapshot that triggered them

An observer is handed an `Observed` pinned to the snapshot that caused the notification,
not a handle onto "whatever is current".

That is a correctness property, not a convenience. An observer reacting to one change
could otherwise read some values before a second reload lands and some after, and produce
a result that never existed as a coherent configuration: a host from one snapshot and a
port from the next. Pinning makes that unrepresentable for the duration of the callback,
and it is why observers never need `With`. `cfg.Snapshot()` exposes the exact snapshot if
you want its version or its layers.

Observers run sequentially, in registration order, on the goroutine performing the reload.
The list is copied under the lock and invoked outside it, so registering an observer
concurrently with a reload is safe and a slow observer cannot deadlock the Store. It does
delay every later observer and the reload itself, though, so expensive work belongs on
your own goroutine. Delivery is also ordered: if a newer snapshot has already gone out, an
older one that arrives late is dropped, because each snapshot is complete in itself.

An observer that returns an error is reported and does not stop the ones after it. The
configuration has already changed; one component being unable to react is no reason to
withhold the change from the rest. It must not pass silently either, which is what
`OnObserverError` is for.

## Two error channels, because they mean different things

`OnReloadError` fires when a reload was **rejected**: nothing changed, and the process is
still on its previous configuration. `OnObserverError` fires when the configuration **did**
change and one component could not keep up. Conflating them would leave a caller unable to
tell "your edit was refused" from "your edit landed and the metrics exporter did not
restart": two situations demanding opposite responses.

Announcing a rejection through the observer path would be worse still: it would tell every
component that configuration changed when it did not.

## Writing from inside an observer is refused

`Apply`, `Reload` and `AddLayer` return `ErrWriteFromObserver` when called from within an
observer callback. The obvious question is why this is not simply made to work.

`Apply`, notify, observer, `Apply`, notify is a loop entirely inside the Store. The
watcher plays no part in it, so the structural fix above does not touch it. Every
mitigation on offer (suppressing the nested notification, coalescing, a depth ceiling) leaves
the program no more correct, so the write is refused instead. [The Store](the-store.md#writing-from-inside-an-observer-is-refused)
has the full argument, including the two defects that had been hiding the loop.

So it is treated as what it is: a breakdown of the ownership model, refused at the
boundary. The supported pattern is to take the write *out* of the observation: capture
what is needed, return, and write from somewhere else:

```go
writes := make(chan config.Change, 1)

store.AddObserverFunc(func(cfg config.Observed) error {
	// Capture and return. Do not write here.
	select {
	case writes <- config.Set("server.generation", cfg.GetInt("server.generation")+1):
	default: // one is already queued
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

Serialising and de-duplicating those deferred writes belongs to you, because you are the
only party that knows which of them still matter.

The check is scoped to the goroutine running the callback, so an unrelated goroutine writing
while observers execute is unaffected. [The Store](the-store.md#writing-from-inside-an-observer-is-refused)
explains why a flag or a marked context would not do.

## Watching is never silently absent

`Watch` fails with `ErrWatchUnavailable` when no source can be watched, rather than
returning successfully and doing nothing. An application that believes it will hear about
changes and never will is worse off than one that knows it must restart, because the
second will at least be restarted.

The predecessor to this module got this wrong in an instructive way. It called
`fsnotify.Add()` regardless of filesystem, which fails on an in-memory one, and swallowed
the error at debug level. Hot-reload was dead on every in-memory worktree and nothing said
so.

Underneath, the mechanism adapts rather than pretending:

- **Native notification where it works.** For a file source, `fsnotify` operates on real operating-system
  paths, so a path is watched natively only after confirming that the file the OS has at
  the resolved location really is the file the store is reading.
- **Polling everywhere else**, every two seconds by default or at an interval you set with
  `WithPollInterval`. A backend that polls something costly can declare a slower default,
  and an explicit `WithPollInterval` still wins. That
  covers in-memory filesystems, wrappers that hide what they contain, network mounts where
  native notification is unreliable, and hosts that have exhausted their watch descriptors.
  A mixed set is handled by watching what can be watched and polling the rest, rather than
  downgrading the whole set.
- **Re-establishing the watch after an atomic save.** Many editors save by writing a
  temporary file and renaming it over the original, which replaces the inode and leaves an
  inode-based watch pointing at a file nothing will ever write to again. The watcher
  re-adds the path on rename and remove events; without that, the *second* save onward
  would be silently missed.
- **Reporting possible change, not actual change.** A watcher's job is to say the paths may
  have moved. Deciding whether anything really changed belongs to the Store, which is the
  only party that can compare configurations. This is also what makes `WithWatcher` a
  clean test seam: drive change detection by hand and you get a reload test with no sleeps
  and no polling.

The polling watcher samples its baseline before `Watch` returns, so a change landing
between the caller being told watching has started and the first tick cannot be absorbed
into the baseline and lost.

## What this does not give you

Each of these is a real edge:

- **A notification is not an action.** Components holding operating-system state
  (listeners, sockets, exporters, connection pools) do not reconfigure themselves when a
  value changes. They need explicit restart or redial logic. A package that cannot
  genuinely reconfigure live should say so rather than pretend.
- **Observers do not fire at startup.** They fire on changes only. If the same logic
  applies at boot and on every reload, extract it and use it twice (a `*View` satisfies
  `Observed`, so one function serves both paths).
- **Side effects are not unwound.** If an observer applied a change and then returned an
  error, the configuration is still the new, valid one. The error is reported, not rolled
  back. Observers should be idempotent.
- **Only watchable sources are watched.** Environment variables and flags do not change
  under a running process, and an in-memory layer is changed by the code that owns it.
  File sources and the remote adapters that implement `WatchableBackend` are watched, by
  native notification or by polling.
- **Cross-process coordination is out of scope.** Watching tells you a foreign change
  happened; it does not stop one landing midway through your own write. Conflict detection
  at commit is the defence there, and it detects rather than prevents. See
  [Write configuration](../how-to/write-config.md#conflicts).
- **The watcher must be stopped.** `Watch` returns a `stop` function that releases the
  underlying watcher and its goroutine. `defer stop()` on shutdown.

## Related

- [React to changes with hot-reload](../how-to/hot-reload.md): wiring it up
- [The Store](the-store.md): why one component owns configuration I/O
- [Validate configuration](../how-to/validate-config.md): gating reloads on a schema
- [Use typed sections](../how-to/typed-sections.md): the same fail-closed doctrine, one level down
- [Write configuration](../how-to/write-config.md): why an own write never reaches the watcher
- [Defaults and limits](../reference/defaults-and-limits.md#watching-and-reload-timing): the intervals this page reasons about

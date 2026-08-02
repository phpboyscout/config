---
title: Defaults and limits
description: Every default interval, file mode and built-in bound — the value, what it governs, and what overrides it.
tags: [reference, defaults, limits]
---

# Defaults and limits

Every number this module chooses on your behalf, in one place.

## Watching and reload timing

| Constant | Value | Governs | Overridden by |
|---|---|---|---|
| `DefaultPollInterval` | `2s` | How often the polling watcher checks for changes when a source cannot notify natively. | `WithPollInterval(d)`, or a backend or filesystem implementing `PollIntervalHinter`. |
| `DefaultSettleInterval` | `250ms` | How long a burst of *foreign* change reports is allowed to settle before the store reloads once. | `WithSettleInterval(d)`. |
| *(unexported)* burst bound | `4 ×` the settle interval | The longest settling may defer a reload, however long a burst runs. | Nothing. |

### How the poll interval is actually chosen

1. If you passed `WithPollInterval(d)` with a positive `d`, that wins outright.
2. Otherwise, if the backend or filesystem implements `PollIntervalHinter` and hints a
   positive duration, the hint is used. This is how a remote object store — where every poll
   is a billed API call — asks for minutes rather than the two seconds a local file wants.
3. Otherwise, `DefaultPollInterval`.

A non-positive interval passed to `NewWatcher` is replaced by the default.

### What the settle window is and is not

`DefaultSettleInterval` exists because a logical configuration change is not always one
filesystem event. A deploy replacing two overlays, a config-management run rewriting a
directory, an operator saving two files — each produces separate events, and nothing in the
filesystem says they were meant as one. Without a window, observers run once per file, and
the first run sees a combination nobody intended.

`WithSettleInterval(0)` disables settling and reloads on each report. Tests driving an
injected watcher usually want that, so the trigger and the reload stay in step without
waiting on a timer.

**Settling does not make a multi-file change atomic.** Writes spaced further apart than the
window are still seen as separate changes. That guarantee belongs to whoever writes the
files, by writing them atomically or by keeping settings that change together in one file.

The burst bound matters for the pathological case: a pure "wait for quiet" window never
fires while changes keep arriving faster than the window, so a file rewritten continuously
would tell observers nothing at all. The bound guarantees a reload within four settle
intervals of the first change in a burst — one second at the default.

## File modes and write staging

| Thing | Value | Notes |
|---|---|---|
| Mode of a config file this module **creates** | `0600` | Owner-only. Configuration routinely holds credentials, so a permissive default would leak them. |
| Mode of a config file that **already exists** | Unchanged | Whatever mode its owner chose is their decision. Staging through a temporary file must not quietly tighten it. |
| Mode of a directory this module creates | `0750` | Created only when writing into a directory that does not exist yet. |
| Staging file name | `<target>.<pid>-<counter>.yamldoc-tmp` | Written beside the target and renamed over it. Unique per write, so two writers in one process cannot stage over each other. |
| Symbolic-link hops followed before writing | `16`, with cycle detection | A configuration file managed by a dotfile tool is routinely a symlink into a tracked repository, and a rename would replace the link rather than write through it. |

Conflict detection compares a **SHA-256 hash of the content**, not the modification time.
Timestamp granularity varies and is coarse on some filesystems, so an edit that preserves a
file's length within one tick — changing `info` to `warn`, say — is invisible to a
stat-based comparison. The polling watcher hashes for the same reason.

## `config.Dir` — paths are relative to the root

`config.Dir(path)` returns a filesystem confined to a directory. Pass `"config.yaml"`, not
`"/etc/app/config.yaml"`.

An absolute path is treated as an attempt to escape and fails with `path escapes from
parent` rather than `fs.ErrNotExist`. The distinction matters to the store, which treats a
missing optional source as normal and anything else as fatal — so an absolute path turns a
file that is merely absent into a hard failure.

Containment is enforced by the operating system through `os.Root`, not by a check this
module performs, so `..`, an absolute path and a symlink pointing away are all refused. The
root is opened per operation rather than held open, which costs one extra `openat` per call
and avoids a file-descriptor leak that surfaced only at scale.

## Sizes

`View.GetSizeInBytes` reads a human-written size. Suffixes are **binary** — each step is a
multiple of 1024, not 1000:

| Written | Bytes |
|---|---|
| `1024` (no suffix) | 1024 |
| `1b` | 1 |
| `1kb` | 1,024 |
| `1mb` | 1,048,576 |
| `1gb` | 1,073,741,824 |
| `1tb` | 1,099,511,627,776 |

Matching is case-insensitive and surrounding whitespace is trimmed, so `"10 MB"` works. A
fractional number is accepted and truncated toward zero, so `"1.5kb"` is `1536`. A negative
number, or anything that cannot be read as a size, returns `0` — the same answer the other
accessors give for a value they cannot convert.

There is no `kib`/`mib` spelling and no decimal (1000-based) mode. `kb` **is** 1024.

## Caching

`SchemaOf[T]()` called **with no options** caches its result per type, so repeated calls do
not re-reflect. A call that passes options — `WithStrictMode()`, say — builds fresh and is
not cached, because an option can change the result.

Nothing else in this module caches. A `View` is a pointer copy over an immutable snapshot,
so taking one is not a load.

## Concurrency

`Store` serialises its own access internally. Concurrent loads and writes cannot interleave,
so a reader never observes a partially applied change and two writers cannot both decide to
write and then overwrite one another.

A `Snapshot` is immutable and a `View` performs no I/O, so both are safe to share across
goroutines without further synchronisation.

## Related

- [React to changes with hot-reload](../how-to/hot-reload.md) — the task guide for watching
- [Hot-reload safety](../explanation/hot-reload-safety.md) — why settling and fail-closed
  reload work as they do
- [What survives a write](../explanation/write-fidelity.md) — what the staged-and-renamed
  commit preserves
- [Limitations](limitations.md) — the bounds these numbers do *not* buy you

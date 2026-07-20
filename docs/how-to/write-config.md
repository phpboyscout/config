# Write configuration

You need to persist a change — a user flipped a setting, a command took a `--set` flag, a
first-run wizard is writing its answers. This guide covers planning a write, applying it,
where the change lands, and what to do when it does not go through.

Everything here goes through the `Store`. It is the only component that writes, which is
what lets a write be routed to the correct layer, validated, and reflected in the next
snapshot without a round trip through the filesystem.

## Plan first: a dry run that cannot drift

`Plan` routes changes without writing anything:

```go
plan, err := store.Plan(
    config.Set("server.port", 9090),
    config.Remove("server.tls.cert"),
)
if err != nil {
    return err
}

fmt.Print(plan)
// set server.port → /etc/app/prod.yaml
// set server.name → /etc/app/prod.yaml (new key)
// remove server.tls.cert → /etc/app/prod.yaml
```

A line carries `(new key)` when the change creates a key rather than replacing one, and
`— shadowed by <source>` when the write will land but not take effect.

`Plan.String()` renders the plan for a `--dry-run` flag or a preview pane. The routing it
shows is not an approximation of what `Apply` would do — it is the same routing pass,
which is why a dry run here cannot drift from the real thing the way a separate preview
implementation would.

For anything more than printing, walk the operations:

```go
for _, op := range plan.Operations {
    fmt.Println(op.Change.Path, "→", op.Target)

    if op.Creates {
        fmt.Println("  this key is new to that layer")
    }

    if !op.Effective() {
        fmt.Println("  will be shadowed by", op.ShadowedBy)
    }
}
```

`plan.Targets()` gives the distinct layers a plan will touch, and `plan.Effective()`
reports whether every operation will be visible once written.

Producing a plan is the expensive part of an apply; executing it is not. Planning before
applying costs you very little.

## Apply

```go
snap, err := store.Apply(ctx,
    config.Set("server.port", 9090),
    config.Remove("server.tls.cert"),
)
if err != nil {
    return err
}

fmt.Println(config.NewView(snap).GetInt("server.port")) // 9090
```

`Apply` takes a batch. Pass every change you want to land together in one call: routing
runs once, each affected document is parsed once, and each target file is written once. A
settings screen saving fifty fields should make one `Apply` call, not fifty.

The snapshot you get back is built **from the content just written**, not by re-reading
the files afterwards. There is no window in which the configuration in memory disagrees
with the configuration on disk, and no waiting on a watcher to notice. Observers are
notified exactly once for the whole batch — and not at all if the write left the
resolved configuration exactly as it was. The file may still have been rewritten, but
observers react to values, and none of them moved.

`Set` and `Remove` are one type — `Change` — rather than two methods, because they route
identically and often need to land together. Replacing a subtree by removing some keys and
setting others only works if both halves commit as one batch.

## Where a change lands

Routing walks the layers in **reverse merge order** and takes the **first writable
match**:

- a key some writable layer already defines routes to the **highest-precedence writable
  layer that defines it**;
- a key nothing defines routes to the **highest-precedence writable layer**, full stop.

The reason is visibility. Write to the layer that already wins and the value you set is
the value you read back. Write to the base instead and your edit is immediately shadowed
by the overlay above it, which looks to the user exactly like the write silently failed.

**Environment variables and flags are never write targets.** They are readable layers like
any other, but they have nowhere to persist to, so routing skips them. The same is true of
a layer added at runtime with `AddLayer`. If everything that could hold a change is
read-only, `Apply` returns `ErrNoWritableLayer` rather than picking something arbitrary.

You can override routing when you genuinely know better, by pinning a change to a layer:

```go
// Only Name and Document are matched, and only against layers that are already
// writable — the other fields are ignored, so setting them proves nothing.
target := config.Source{Name: "/etc/app/base.yaml"}

change := config.Set("server.port", 9090)
change.Target = &target

plan, err := store.Plan(change)
```

The target must name a real, writable layer — and for a multi-document file, its
`Document` index too, which is how you address the second document of one file. A name
that matches nothing returns `ErrInvalidTarget` rather than falling back to routing.

Reach for this sparingly: routing's default is right far more often than a hand-picked
target, and pinning is how an edit ends up written somewhere nobody reads.

## "Written, but it still does not take effect"

A write can succeed and change nothing about the resolved configuration, because a
higher-precedence layer still defines the key. This is not an error — writing the file is
what you asked for — but a caller who cannot say so will leave the user staring at a
setting that refuses to move.

`Operation.Effective()` reports whether the change will be visible once written, and
`Operation.ShadowedBy` names the layers still winning, lowest first:

```go
plan, err := store.Plan(config.Set("db.host", "prod-db"))
if err != nil {
    return err
}

for _, op := range plan.Operations {
    if !op.Effective() {
        winner := op.ShadowedBy[len(op.ShadowedBy)-1]

        fmt.Printf("%s will be written to %s but %s still wins\n",
            op.Change.Path, op.Target, winner)
    }
}
// db.host will be written to /etc/app/prod.yaml but env:APP_DB_HOST still wins
```

Surface this. The commonest cause is an environment variable set in a shell or a container
spec, and it is invisible to anyone reading the config file.

## Creating a file that does not exist yet

A configured file that is not on disk is still a routing candidate. That matters: on a
fresh install the highest-precedence writable layer is usually a user overlay nobody has
created yet, and leaving it out would make routing depend on whether a file happened to
exist.

```go
store, err := config.NewStore(ctx,
    config.WithFiles(afero.NewOsFs(), "/etc/app/base.yaml", "/home/u/.app.yaml"))
if err != nil {
    return err
}

_, err = store.Apply(ctx, config.Set("server.port", 9090))
```

If `~/.app.yaml` does not exist, `Apply` creates it, containing an ordinary block-style
document with the intermediate keys synthesised:

```yaml
server:
  port: 9090
```

## Comments survive

Writing does not serialise the merged view over the top of your file. Each target document
is edited in place, so **comments stay attached to the keys they describe**, and key
order, quoting, block scalars, anchors, aliases and merge keys are preserved. Repeated
writes converge rather than drifting.

Blank lines, indentation, comment alignment and byte-for-byte identity are **not**
guaranteed — comment *retention* is promised, comment *style* is yours. Two things follow
that are worth knowing before they surprise you:

- **Some documents are refused at load**, with `ErrBackendUnsafe`, because they cannot be
  round-tripped safely — a multi-line flow collection with interior comments is the one
  you will meet. Reformat it onto one line, or move the comments out.
- **Invisible characters are escaped on write.** Everything a reader can see survives
  verbatim; bidirectional controls and zero-width characters become escapes, because they
  make a document render one way and parse another.

The full contract, and the reasoning behind both, is in
[what survives a write](../explanation/write-fidelity.md).

## Setting a map replaces the whole subtree

A map-valued `Set` **replaces** the node it addresses. Keys absent from your map are
removed:

```go
_, err := store.Apply(ctx, config.Set("avatars.default", map[string]any{
    "url":  "https://example.invalid/a.png",
    "size": 64,
}))
```

Anything previously under `avatars.default` and not in that map is gone. This is
deliberate — deep-merging would make "this subtree is now exactly this" inexpressible, and
that is precisely what a consumer replacing a catalogue needs.

**It is a sharp edge.** By supplying a map you assert ownership of that subtree and accept
that comments, anchors and block styles *within* it may not survive — see
[why replacing a map is different](../explanation/write-fidelity.md#why-replacing-a-map-is-different).

If you know what changed, say so precisely. Diff your before and after, and issue targeted
`Set` and `Remove` calls for the differences. `Remove` is what makes that possible, and it
is the reason whole-subtree replacement existed at all.

Finally, an empty map is a value, not a delete. `Set("section", map[string]any{})` sets the
subtree to `{}`; `Remove` is the only way to remove a key. By the same rule, removing the
last key of a mapping leaves `section: {}` behind rather than deleting the parent, and
removing the last element of a sequence leaves `[]`.

## Conflicts

Execution is prepare → verify → commit. Everything expensive or likely to fail happens
while nothing is visible; the commit itself is a sequence of atomic renames.

The verify step re-checks every target against a fingerprint of what was read. If anything
changed underneath — another process, a text editor, a deploy tool — the whole batch is
abandoned with `ErrConflict` and **no target is modified**. Committing over it would
silently discard someone else's work.

The recovery is to reload and try again, ideally after telling the user what happened:

```go
_, err := store.Apply(ctx, changes...)
if errors.Is(err, config.ErrConflict) {
    if err := store.Reload(ctx); err != nil {
        return err
    }

    // Re-plan against the new state: routing may now choose differently, and a
    // shadowed-write report computed against the old state is stale.
    _, err = store.Apply(ctx, changes...)
}
```

Do not loop on this blindly. If two processes are both writing the same file, retrying
just means the last one wins more slowly. Where the conflict represents a genuine
disagreement, put it in front of a person.

Be clear about the limit: **serialisation is in-process only**. Concurrent `Apply` calls
within one process cannot interleave, so two writers can never both decide to write and
then overwrite each other. Across processes there is no lock — portable cross-process
locking needs lock files and is out of scope — and conflict detection is the whole defence.
It is a good defence, but it is detection, not prevention.

## Validation

If the Store carries a schema, a write is validated by building the candidate snapshot it
would produce — including the env and flag layers — and validating **that**. It is the same
construction path a reload uses, so pre-write validation and the reload it causes cannot
disagree.

Two rules follow, and both matter:

- **The resolved configuration is validated, never a single layer.** A base file missing a
  key that its overlay supplies is legitimate, and layer-scoped validation would reject a
  perfectly correct write.
- **Only violations your change introduces are rejected.** A configuration that is already
  invalid stays editable — otherwise one missing key makes the config unfixable through the
  very surface designed to fix it. The error distinguishes the two, reporting pre-existing
  violations separately and marking them as unaffected by your change.

A rejected write modifies nothing. The failure wraps `ErrInvalidConfig`.

## Never write from inside an observer

`Apply`, `Reload` and `AddLayer` return `ErrWriteFromObserver` when called from within
an observer callback. This is refused outright rather than made to work: each such write is itself a
change, which notifies, which runs the observer again — a cascade with no natural end.

If an observer needs to change configuration, take the write *out* of the observation:

```go
store.AddObserverFunc(func(cfg config.Observed) error {
    want := derive(cfg.GetString("region"))

    select {
    case pending <- want: // hand it to a worker and return
    default:
    }

    return nil
})
```

The worker goroutine then calls `Apply` normally. Serialising and de-duplicating those
deferred writes is yours to decide, because you are the only one who knows which of them
still matter.

The check is scoped to the goroutine running the callback, so an unrelated goroutine
writing while observers happen to be executing is fine — that write is legitimate and is
not refused.

## Errors worth branching on

Match with `errors.Is`.

| Error | Meaning |
|---|---|
| `ErrConflict` | a target changed since it was read; nothing was written |
| `ErrNoWritableLayer` | every layer that could hold the change is read-only |
| `ErrNotWritable` | a change was routed at a backend that cannot persist |
| `ErrNoChanges` | `Apply` or `Plan` was called with nothing to do |
| `ErrInvalidPath` | the dotted path is malformed |
| `ErrInvalidTarget` | a pinned target names no writable layer, or a new file was pinned to a document other than the first |
| `ErrInvalidConfig` | the change would make the resolved configuration invalid |
| `ErrBackendUnsafe` | a source contains a construct that cannot be safely round-tripped |
| `ErrWriteFromObserver` | a write was attempted from inside an observer callback |
| `ErrPartialCommit` | a multi-target commit failed partway and could not be fully rolled back |

`ErrPartialCommit` is the one that should never happen and must not be glossed over. On a
partial failure the Store restores the targets it has already committed; if restoration
also fails, the error names precisely which targets are in which state. A caller is never
left guessing, and neither should the user be.

## Related

- [What survives a write](../explanation/write-fidelity.md) — the fidelity contract in full
- [Provenance](../explanation/provenance.md) — who supplied a value and what shadows it
- [The Store](../explanation/the-store.md) — why one component owns config I/O
- [Hot-reload](hot-reload.md) — reacting to changes made outside this process

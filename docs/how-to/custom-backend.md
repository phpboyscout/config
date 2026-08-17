---
title: Write a custom backend
description: Make any remote system an ordinary layer by implementing Backend — reads, watch and compare-and-swap writes.
tags: [how-to, backends, extending]
---

# Write a custom backend

You want configuration from somewhere this module has never heard of — Consul, a secrets
manager, an HTTP endpoint, a database table, a device's NVRAM — and you want it to behave
like every other layer: precedence, provenance, shadowing, hot-reload, the lot.

That is what `Backend` is for. This guide builds one end to end: reading first, then
watching, then writing. Each stage works on its own, so stop wherever your source stops.

!!! note "Reading a file in another format? You need a `Codec`, not a whole `Backend`."
    If your source is a *file* in a format the core does not know — JSON, TOML, INI — the file
    machinery is already written: reading, conflict detection, atomic writes, symlink handling
    and watching are all format-agnostic and live in the core. You supply only the
    format-specific part, a `Codec`, and pass it to `config.NewCodecBackend(fsys, path, codec)`.
    A codec that also implements `EditingCodec` yields a writable backend automatically. This
    is how the sibling `config-<format>` modules are built; see the
    [non-YAML format adapters spec](https://gitlab.com/phpboyscout/go/config/-/wikis/specs/0004-non-yaml-format-adapters).
    Reach for a full `Backend`, below, when your source is *not* a file on a filesystem.

!!! tip "The code here is compiled"
    Every snippet is taken from
    [`custombackend_test.go`](https://gitlab.com/phpboyscout/go/config/-/blob/main/custombackend_test.go)
    in this module's own suite, where it is exercised against a fake remote — including the
    write path and the conflict case. It cannot drift from an API that still works.

## What we are building

A backend over a remote key-value store with compare-and-swap semantics — the shape Consul,
etcd and most parameter stores have. Keeping the service behind an interface is what lets
the backend be tested without it:

```go
// remoteStore is whatever you actually talk to.
type remoteStore interface {
	// Fetch returns the current values and a version identifying them.
	Fetch(ctx context.Context) (map[string]any, uint64, error)
	// Put writes values only if the remote is still at ifVersion.
	Put(ctx context.Context, values map[string]any, ifVersion uint64) error
}
```

The backend itself, and its constructor:

```go
package myapp

import (
	"context"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"gitlab.com/phpboyscout/go/config"
)

type remoteBackend struct {
	store  remoteStore
	prefix string

	// version is what Load last saw. The write path compares against it, so a
	// change landing since the load is detected rather than overwritten.
	mu      sync.Mutex
	version uint64
}

func newRemoteBackend(store remoteStore, prefix string) *remoteBackend {
	return &remoteBackend{store: store, prefix: prefix}
}
```

## Read: the three methods you must implement

```go
type Backend interface {
	ID() string
	Load(ctx context.Context, below []Layer) ([]Layer, error)
	Capabilities() Capabilities
}
```

That is the whole read contract.

```go
func (b *remoteBackend) ID() string { return b.prefix }

func (b *remoteBackend) Load(ctx context.Context, _ []config.Layer) ([]config.Layer, error) {
	values, version, err := b.store.Fetch(ctx)
	if err != nil {
		return nil, err
	}

	if values == nil {
		// Absent is not an error. The Store decides whether a missing source is
		// fatal — see RequireFirstSource — so say "not there" and let it choose.
		return nil, fs.ErrNotExist
	}

	b.mu.Lock()
	b.version = version
	b.mu.Unlock()

	return []config.Layer{{
		Source: config.Source{
			Kind:     config.SourceKind("remote"),
			Name:     b.prefix,
			Writable: true, // only if you implement WritableBackend — see below
		},
		Values: values,
	}}, nil
}
```

!!! warning "`ID()` and `Source.Name` must agree"
    Write routing finds your backend again by matching a layer's `Source.Name` against
    `ID()`. If they differ, reads work perfectly and **writes fail** with an error saying no
    backend answers to that name.

    It is an easy mistake — an `ID()` of `"remote:" + prefix` reads better in a log — and it
    fails nowhere near where you made it. Pick one string and use it for both.

Register it like any other source. Precedence is the order you add them:

```go
store, err := config.NewStore(ctx,
	config.WithReaders(config.NamedSource{Name: "embedded:defaults.yaml", Content: defaults}),
	config.WithBackend(newRemoteBackend(client, "app/")),   // ← beats the defaults
	config.WithEnv("APP"),                                  // ← beats the remote
)
```

And it is now indistinguishable from a built-in layer:

```go
view.GetInt("server.port")           // 9090, from the remote
view.GetString("server.host")        // still "localhost" — merging is per-key
view.Origin("server.port").String()  // "remote:app/"
view.Shadowed("server.port")         // every layer defining it, lowest first
```

### Getting `Layer` right

Four things decide how your layer behaves:

| Field | What to set it to |
|---|---|
| `Source.Kind` | Your own `config.SourceKind("consul")`. It is a string type, so you are not limited to the built-in kinds. |
| `Source.Name` | What a *user* would recognise — the key prefix, the endpoint, the table. It is what `Explain` prints, **and it must equal `ID()`**. |
| `Source.Writable` | `true` only if you also implement `WritableBackend`. Routing offers writable layers as targets, so claiming it without implementing it makes writes fail late. |
| `Values` | A nested `map[string]any`. `{"server": {"port": 9090}}` — **not** a flat `{"server.port": 9090}`, which would create a key with a literal dot in its name. |

`Values` must be a tree because that is what merging operates on. If your source is
genuinely flat — most key-value stores are — split each key on your separator and nest it
before returning.

You may return **more than one layer**. A multi-document YAML file returns one per
document, and they take part in precedence independently. Use `Source.Document` to
distinguish them.

### What `below` is for

Most backends ignore the `below` argument. It carries every layer the lower-precedence
backends contributed, in order, and exists for the case where reading your own input
depends on what is already defined.

The environment backend is the real example: mapping `APP_SERVER_PORT` back to a dotted key
is ambiguous — `server.port` or `server_port`? — so it resolves the name against the keys
already defined beneath it. It is an argument rather than a separate call precisely so it
cannot be forgotten.

## Declare what you can and cannot do

```go
func (b *remoteBackend) Capabilities() config.Capabilities {
	return config.Capabilities{
		PreservesComments: false, // a key-value store has nowhere to put a comment
		AtomicMultiKey:    true,  // the compare-and-swap covers the whole set
		NativeWatch:       true,  // a real subscription, not polling
		Sensitive:         false, // set true for a secrets store
	}
}
```

Be honest here rather than generous. These describe your source so a heterogeneous set of
backends can coexist without the weakest one setting the contract for everybody.

**`Sensitive` is the one to think hardest about.** It marks a backend as holding secret
material, and a value from a sensitive source must never be written into a layer that is
not. That is the [environment-secret leak](../about/index.md#writing-where-it-gets-genuinely-hard)
wearing a different costume: a secrets-manager value flattened into a config file is the
same incident by another route. Declaring `Sensitive: true` makes the core enforce it — a
write that would land a key your backend owns into a non-sensitive layer beneath is refused
with `config.ErrSensitiveLeak`, rather than silently written to the plain file.

Note what is *not* here: nothing says whether you can be written to or watched. Those are
answered by implementing the interfaces below, so the compiler checks them once instead of
every caller checking a flag — and so a backend cannot claim one thing and do another.

## Watch: take part in hot-reload

Implement `WatchableBackend` — one more method — and your source joins hot-reload. This
example assumes the remote offers a subscription:

```go
// Subscribe returns a channel signalled whenever the remote data changes.
type subscriber interface{ Subscribe() <-chan struct{} }

func (b *remoteBackend) Watch(
	ctx context.Context,
	interval time.Duration,
	onChange func(),
) (func(), error) {
	sub, ok := b.store.(subscriber)
	if !ok {
		// No subscription available. Returning a no-op stop and no error says
		// "nothing to watch here" without failing the whole watch set; return
		// an error instead if being unwatchable is a real problem.
		return func() {}, nil
	}

	done := make(chan struct{})

	go func() {
		events := sub.Subscribe()

		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-events:
				onChange()
			}
		}
	}()

	var once sync.Once

	return func() { once.Do(func() { close(done) }) }, nil
}
```

Four things to get right:

- **Call `onChange` on *possible* change.** You are not expected to know whether anything
  that resolves actually moved — the Store re-reads, merges, validates and stays quiet if
  the result is identical. A spurious call costs a read and nothing else.
- **`interval` is the polling interval** from `WithPollInterval`. Ignore it if you have a
  real subscription; use it if you must poll.
- **The stop function must be safe to call twice.** Hence the `sync.Once`.
- **Respect `ctx`.** Watching should end when the context does, without leaking the
  goroutine.

Set `NativeWatch: true` in your capabilities when you have a genuine subscription, so
callers know foreign-change latency is push rather than poll.

!!! note "Your backend does not need to debounce"
    The Store coalesces a burst of change reports behind a settle window before reloading —
    see [hot-reload](hot-reload.md#a-notification-means-something-really-changed). Report
    changes as you see them and let the Store decide.

## Write: routing changes to your source

Writing is a separate interface because it carries ownership, atomicity, conflict detection
and failure modes that differ enormously between a local file and a remote store. One
interface pretending otherwise would either lie about those differences or degrade to the
weakest member.

```go
type WritableBackend interface {
	Backend

	// Prepare stages edits without making them visible.
	Prepare(ctx context.Context, edits []Edit) (Pending, error)
}

type Edit struct {
	Document int    // index within a multi-document source; 0 for most backends
	Path     string // the dotted key
	Value    any    // the new value
	Remove   bool   // true to delete the key rather than set it
}
```

`Prepare` returns a `Pending` — staged work that is not yet visible:

```go
type Pending interface {
	Layers() []Layer                    // what you will contribute once committed
	Verify(ctx context.Context) error    // is the source still as it was at Load?
	Commit(ctx context.Context) error    // make it visible
	Rollback(ctx context.Context) error  // undo a commit, best effort
	Discard(ctx context.Context) error   // abandon work never committed
}
```

### Staging

`Prepare` must not modify the source. The Store may abandon this batch because some *other*
backend's `Verify` failed, and that has to cost nothing:

```go
func (b *remoteBackend) Prepare(ctx context.Context, edits []config.Edit) (config.Pending, error) {
	current, _, err := b.store.Fetch(ctx)
	if err != nil {
		return nil, err
	}

	if current == nil {
		current = map[string]any{}
	}

	// The version recorded at Load, NOT one fetched here. See the warning below.
	b.mu.Lock()
	version := b.version
	b.mu.Unlock()

	// Work on a copy, so nothing staged is visible to a concurrent Load.
	next := make(map[string]any, len(current))
	for k, v := range current {
		next[k] = v
	}

	for _, edit := range edits {
		if edit.Remove {
			delete(next, edit.Path)

			continue
		}

		next[edit.Path] = edit.Value
	}

	return &remotePending{backend: b, next: next, previous: current, version: version}, nil
}
```

### The pending write

```go
type remotePending struct {
	backend   *remoteBackend
	next      map[string]any
	previous  map[string]any
	version   uint64
	committed bool
}

// Layers is what this backend will contribute once committed — the
// post-commit state, which is what lets the Store build the next snapshot
// from what was just written instead of re-reading and hoping.
func (p *remotePending) Layers() []config.Layer {
	return []config.Layer{{
		Source: config.Source{
			Kind:     config.SourceKind("remote"),
			Name:     p.backend.prefix,
			Writable: true,
		},
		Values: p.next,
	}}
}

func (p *remotePending) Verify(ctx context.Context) error {
	_, version, err := p.backend.store.Fetch(ctx)
	if err != nil {
		return err
	}

	if version != p.version {
		return fmt.Errorf("%w: remote moved from version %d to %d",
			config.ErrConflict, p.version, version)
	}

	return nil
}

func (p *remotePending) Commit(ctx context.Context) error {
	if err := p.backend.store.Put(ctx, p.next, p.version); err != nil {
		return err
	}

	p.committed = true

	return nil
}

// Rollback restores what was there before, best effort, for when a later
// commit in the same batch failed.
func (p *remotePending) Rollback(ctx context.Context) error {
	if !p.committed {
		return nil
	}

	_, version, err := p.backend.store.Fetch(ctx)
	if err != nil {
		return err
	}

	return p.backend.store.Put(ctx, p.previous, version)
}

// Discard abandons staged work. Nothing was written, so there is nothing to
// undo — which is the point of staging.
func (p *remotePending) Discard(context.Context) error { return nil }
```

### How the Store drives it

1. `Prepare` on every affected backend. Nothing is visible yet.
2. `Verify` on every one of them. Any failure and the whole batch is abandoned with
   `Discard`, so a conflict costs nothing.
3. `Commit` each in turn. If one fails partway, the already-committed ones are `Rollback`ed
   and the caller gets `ErrPartialCommit` naming exactly what is in which state.

!!! warning "`Verify` must compare against what `Load` read, not what `Prepare` read"
    This is the single easiest thing to get wrong, and it fails silently — every write
    succeeds, and conflict detection simply never fires.

    Routing decided where this write goes based on what `Load` saw. A change that landed
    since then invalidates that decision. If `Prepare` fetches a fresh version and `Verify`
    compares against *that*, you are comparing the intruder's data with itself and it will
    always match.

    Keep the version from `Load` — as `remoteBackend.version` does above — and the
    conflict is caught. There is a test for exactly this case in the suite, because the
    first draft of this guide got it wrong.

If your source cannot do any of this safely, **do not implement `WritableBackend`.** A
read-only layer is a perfectly good citizen: routing skips it, `Apply` lands in the next
writable layer down, and a write that would have gone to it is reported as shadowed rather
than silently failing.

## If your backend resolves its own connection

Everything above assumes the client is handed to you. If your backend instead
resolves its own — from an ambient credential chain, say — it takes on three
further obligations, and the
[conformance suite](https://gitlab.com/phpboyscout/go/config/-/tree/main/backendconformance)
checks all three once you supply `Suite.NewUnreachable`.

```go
backendconformance.Run(t, backendconformance.Suite{
	NewBackend: /* … as above … */,
	Seed:       /* … */,
	Defines:    /* … */,

	// A backend whose connection cannot be established, plus a heal that makes
	// it establishable.
	NewUnreachable: newUnreachableBackend,
})
```

**`ID` and `Capabilities` must answer without a connection.** The Store calls
both while ordering layers, routing writes and deciding what to watch — long
before the first `Load`. A backend that connected to answer either would turn
`NewStore` into a network operation.

**A connection failure is an ordinary error from `Load`** — never a panic, and
never `fs.ErrNotExist`. That sentinel means the source is legitimately absent,
which the Store may tolerate, so reporting an unreachable connection with it
turns a hard failure into a silently missing layer. Absent and unreachable are
different answers.

**A failed connection must not be remembered.** A Store is long-lived and
reloads; a credential chain that was not ready at startup must be picked up by the
next `Load` rather than requiring a restart. This rules out `sync.OnceValues`,
which caches the first error for the life of the process. If you want the
memoise-success-but-never-failure behaviour without writing it, take
[`go/clientlifecycle`](https://gitlab.com/phpboyscout/go/clientlifecycle) — it is
that state machine and nothing else, with no dependencies.

### Writing the `heal` correctly

There is one way to get the fixture wrong that makes the third check pass against
the exact defect it exists to catch.

**`heal` must change the world, never your backend.** Flip a flag the *client*
reads, or make the fake server start answering. If `heal` hands back a new backend
— or swaps the one you gave it — it erases any failure the backend had cached, and
a backend that caches its failure will sail through.

```go
// WRONG: replaces the thing under test, hiding the bug
heal := func() { backend.dial = workingDial }

// RIGHT: changes the world the backend reads
var reachable atomic.Bool
dial := func() (*client, error) {
	if !reachable.Load() { return nil, errUnreachable }
	return realClient, nil
}
heal := func() { reachable.Store(true) }
```

Serving real data after healing rather than an empty source is worth the extra
lines where you can afford it: an empty source proves only that the connection was
re-made, whereas a value proves the healed path *read* it.

## Shipping read-only first, and adding writes later

Shipping a backend that reads before it writes is a normal and supported path — often the
right one, when the format deserves a structure-preserving writer that does not exist yet.
Reading is most of the value and arrives far sooner.

Four things make the later promotion uneventful:

1. **Fix your constructor and codec type in the first release.** Gaining write support means
   adding `Prepare` to a type that already exists. A consumer's call site never changes.
2. **Say so in your package documentation.** A consumer choosing your module should know
   which way it is going to move.
3. **Assert the current capability in your tests.** While read-only, assert that routing
   skips your layer and a write lands beneath it. When you add writing, that assertion flips
   — the transition shows up as a test change you make deliberately, not as behaviour you
   discover later.
4. **Release it as a minor version with a release note** stating what changed, in the terms
   below.

### What promotion changes for a consumer

Precisely two things, and only one is a surprise:

**Writes to keys your layer defines start working.** Before promotion they routed to the
layer beneath yours and were reported as ineffective — `Operation.Effective()` false, with
your layer named in `ShadowedBy`. The module was already saying the write would not take
effect. Afterwards it does.

**Writes to keys nothing defines yet move to your layer.** Before, they landed in the
highest-precedence *writable* layer, which was somewhere beneath you. After, they land in
yours. Both work; the file differs. This is the part worth putting in a release note.

### Pinning routing, if it matters to you

A consumer who depends on writes landing in a particular file asserts it with `Plan`, which
is the tool for exactly this question:

```go
plan, err := store.Plan(config.Set("server.name", "prod"))
require.NoError(t, err)

require.Equal(t, "/etc/app/base.yaml", plan.Targets()[0].Name)
```

That fails loudly when a dependency changes routing, rather than the write quietly going
somewhere else.

## Test it

The interface is small enough to test against a fake rather than the real service. A
minimal one, with the compare-and-swap the backend depends on:

```go
type fakeRemote struct {
	mu      sync.Mutex
	values  map[string]any
	version uint64
	events  chan struct{}
}

func newFakeRemote(values map[string]any) *fakeRemote {
	return &fakeRemote{values: values, version: 1, events: make(chan struct{}, 8)}
}

func (f *fakeRemote) Fetch(context.Context) (map[string]any, uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.values == nil {
		return nil, 0, nil
	}

	out := make(map[string]any, len(f.values))
	for k, v := range f.values {
		out[k] = v
	}

	return out, f.version, nil
}

func (f *fakeRemote) Put(_ context.Context, values map[string]any, ifVersion uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.version != ifVersion {
		return errors.New("version conflict")
	}

	f.values, f.version = values, f.version+1

	return nil
}

func (f *fakeRemote) Subscribe() <-chan struct{} { return f.events }
```

Then assert the behaviours a backend can be subtly wrong about:

```go
func TestRemoteBackend(t *testing.T) {
	remote := newFakeRemote(map[string]any{
		"server": map[string]any{"port": 9090},
	})

	store, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{
			Name:    "embedded:defaults.yaml",
			Content: []byte("server:\n  port: 8080\n  host: localhost\n"),
		}),
		config.WithBackend(newRemoteBackend(remote, "app/")),
	)
	require.NoError(t, err)

	view := store.View()

	assert.Equal(t, 9090, view.GetInt("server.port"))           // the remote wins
	assert.Equal(t, "localhost", view.GetString("server.host")) // merging is per-key

	src, _ := view.Origin("server.port")
	assert.Equal(t, "remote:app/", src.String())
}
```

Worth covering, because each is a way a backend can be subtly wrong:

- **precedence** — that being added later actually wins;
- **per-key merging** — that a key you do not supply still comes from below;
- **provenance** — that `Origin` names you, with the name a user would recognise;
- **absence** — that `fs.ErrNotExist` is tolerated rather than fatal;
- **hot-reload** — that a change reaches observers, if you implement `Watch`;
- **writes** — that `Apply` reaches the source *and* that the Store's view agrees;
- **conflicts** — that a change landing after `Load` is refused with `ErrConflict`.

The last two matter most, because a write path that never detects a conflict passes every
happy-path test you will write.

`mocks.MockBackend`, `MockWritableBackend` and `MockWatchableBackend` are published for
testing code that *consumes* a backend. For testing a backend you wrote, a hand-written
fake of the service behind it is usually clearer.

## Related

- [Backends & capabilities](../explanation/backends.md) — why the interfaces are split
- [Load & merge configuration](load-and-merge.md) — how layers combine
- [React to changes with hot-reload](hot-reload.md) — the observer contract your `Watch` feeds
- [Write configuration](write-config.md) — routing, conflicts and `ErrPartialCommit`

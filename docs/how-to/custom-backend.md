# Write a custom backend

You want configuration from somewhere this module has never heard of — Consul, a secrets
manager, an HTTP endpoint, a database table, a device's NVRAM — and you want it to behave
like every other layer: precedence, provenance, shadowing, hot-reload, the lot.

That is what `Backend` is for. This guide builds one, then adds watching and writing.

!!! tip "The code here is compiled"
    Every snippet below comes from
    [`custombackend_test.go`](https://gitlab.com/phpboyscout/go/config/-/blob/main/custombackend_test.go)
    in the module's own suite, so it cannot drift from an API that still works.

## Read: the three methods you must implement

```go
type Backend interface {
	ID() string
	Load(ctx context.Context, below []Layer) ([]Layer, error)
	Capabilities() Capabilities
}
```

That is the whole read contract. Here is one over a remote key-value store:

```go
type remoteBackend struct {
	store  remoteStore // whatever you actually talk to
	prefix string
}

func (b *remoteBackend) ID() string { return "remote:" + b.prefix }

func (b *remoteBackend) Load(ctx context.Context, _ []config.Layer) ([]config.Layer, error) {
	values, _, err := b.store.Fetch(ctx)
	if err != nil {
		return nil, err
	}

	if values == nil {
		// Absent is not an error. The Store decides whether a missing source is
		// fatal — see RequireFirstSource — so say "not there" and let it choose.
		return nil, fs.ErrNotExist
	}

	return []config.Layer{{
		Source: config.Source{
			Kind:     config.SourceKind("remote"),
			Name:     b.prefix,
			Writable: true,
		},
		Values: values,
	}}, nil
}
```

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
| `Source.Name` | What a *user* would recognise — the key prefix, the endpoint, the table. It is what `Explain` prints. |
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
not. That is the environment-secret leak on the
[front page](../index.md#writing-where-it-gets-genuinely-hard) wearing a different costume:
a secrets-manager value flattened into a config file is the same incident by another route.

Note what is *not* here: nothing says whether you can be written to or watched. Those are
answered by implementing the interfaces below, so the compiler checks them once instead of
every caller checking a flag — and so a backend cannot claim one thing and do another.

## Watch: take part in hot-reload

Implement one more method and your source joins hot-reload:

```go
func (b *remoteBackend) Watch(
	ctx context.Context,
	interval time.Duration,
	onChange func(),
) (func(), error) {
	done := make(chan struct{})

	go func() {
		events := b.store.Subscribe()

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

	// Prepare stages edits without making them visible. It must not modify
	// the source: everything it does has to be abandonable.
	Prepare(ctx context.Context, edits []Edit) (Pending, error)
}
```

`Prepare` returns a `Pending` — staged work that is not yet visible:

```go
type Pending interface {
	Layers() []Layer                  // what you will contribute once committed
	Verify(ctx context.Context) error  // is the source still as it was at Prepare?
	Commit(ctx context.Context) error  // make it visible
	Rollback(ctx context.Context) error // undo a commit, best effort
	Discard(ctx context.Context) error  // abandon work never committed
}
```

The three-phase shape — **prepare, verify, commit** — exists so the expensive and
failure-prone part happens while nothing is visible, and the window in which a
partially-applied batch could be observed is as short as you can make it.

The Store drives it like this:

1. `Prepare` on every affected backend. Nothing is visible yet.
2. `Verify` on every one of them. Any failure and the whole batch is abandoned with
   `Discard`, so a conflict costs nothing.
3. `Commit` each in turn. If one fails partway, the already-committed ones are `Rollback`ed
   and the caller gets `ErrPartialCommit` naming exactly what is in which state.

Two obligations that are easy to miss:

**`Layers()` must return what you will contribute *after* the commit.** This is what lets
the Store build the next snapshot from the content just written rather than re-reading and
hoping for the same answer — which is why there is no window where memory and your source
disagree.

**`Verify` must compare against what was read at `Load`, not at `Prepare`.** Routing
decisions were made against the loaded content, so a change that landed since then
invalidates them. A fingerprint taken at write time compares the intruder's data with
itself and finds nothing wrong. This is exactly how a compare-and-swap version works — keep
the version from `Load`, and `Verify` fails if it has moved.

If your source cannot do any of this safely, **do not implement `WritableBackend`.** A
read-only layer is a perfectly good citizen: routing skips it, `Apply` lands in the next
writable layer down, and a write that would have gone to it is reported as shadowed rather
than silently failing.

## Test it

The interface is small enough to test against a fake rather than the real service:

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

	assert.Equal(t, 9090, view.GetInt("server.port"))      // the remote wins
	assert.Equal(t, "localhost", view.GetString("server.host")) // merging is per-key

	src, _ := view.Origin("server.port")
	assert.Equal(t, "remote:app/", src.String())
}
```

Worth asserting specifically, because each is a way a backend can be subtly wrong:

- **precedence** — that being added later actually wins;
- **per-key merging** — that a key you do not supply still comes from below;
- **provenance** — that `Origin` names you, with the name a user would recognise;
- **absence** — that `fs.ErrNotExist` is tolerated rather than fatal;
- **hot-reload** — that a change reaches observers, if you implement `Watch`.

`mocks.MockBackend`, `MockWritableBackend` and `MockWatchableBackend` are published for
testing code that *consumes* a backend. For testing a backend you wrote, a hand-written
fake of the service behind it is usually clearer.

## Related

- [Backends & capabilities](../explanation/backends.md) — why the interfaces are split
- [Load & merge configuration](load-and-merge.md) — how layers combine
- [React to changes with hot-reload](hot-reload.md) — the observer contract your `Watch` feeds
- [Write configuration](write-config.md) — routing, conflicts and `ErrPartialCommit`

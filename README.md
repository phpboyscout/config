<div align="center">

# config

**Layered configuration for Go that can tell you where a value came from — and write one back without wrecking the file**

[![Go Reference](https://pkg.go.dev/badge/gitlab.com/phpboyscout/go/config.svg)](https://pkg.go.dev/gitlab.com/phpboyscout/go/config)
[![Pipeline](https://gitlab.com/phpboyscout/go/config/badges/main/pipeline.svg)](https://gitlab.com/phpboyscout/go/config/-/pipelines)
[![Coverage](https://gitlab.com/phpboyscout/go/config/badges/main/coverage.svg)](https://gitlab.com/phpboyscout/go/config/-/graphs/main/charts)
[![phpboyscout Go toolkit](https://img.shields.io/badge/phpboyscout-Go%20toolkit-554488?logo=gitlab&logoColor=white)](https://go.phpboyscout.uk)

<em>Part of the <a href="https://go.phpboyscout.uk">phpboyscout Go toolkit</a> &mdash; small, framework-free Go modules extracted from <a href="https://gitlab.com/phpboyscout/go-tool-base">go-tool-base</a>. Docs: <a href="https://config.go.phpboyscout.uk">config.go.phpboyscout.uk</a></em>

</div>

---

`gitlab.com/phpboyscout/go/config` gives configuration a single owner. A `Store` reads
every source — files, compiled-in defaults, environment variables, command-line flags —
merges them in a precedence order you can read off the call site, records which layer
supplied each value, and is the only thing that writes any of them back. Everything else
is a view over the immutable snapshot it publishes.

That one rule is what makes the rest possible.

## Reading: answers, not just values

Any library can hand you a value. Three things are harder:

**"Why is this value 9090?"** Provenance is recorded *during* the merge rather than
reconstructed afterwards, so the question has an answer. `Explain` renders the whole chain,
`Origin` names the layer that supplied a value, `Shadowed` lists every layer defining it.

```go
fmt.Println(view.Explain("server.port"))
// server.port = 9090 (from /home/me/.mytool/config.yaml); also defined in embedded:defaults.yaml
```

**"Do these two values agree with each other?"** A `View` is pinned to one immutable
snapshot, so a sequence of reads cannot land either side of a reload and return a
configuration that never existed. That is structural rather than a matter of care, and it
is measured — two related keys read while the file changes underneath:

| | reads | mismatched pairs |
|---|---|---|
| a library reading live mutable state | 6,764,880 | **1,759** |
| `config` | 11,639,791 | **0** |

A mismatched pair is `host` from before a reload and `port` from after — a host and port
never configured together, about to be dialled. [`coherence_test.go`](coherence_test.go)
produces the second row and fails the build if it stops being true.

**"What happens when the new config is broken?"** Reload is fail-closed: a candidate that
will not parse, or violates your schema, is rejected and the last-known-good configuration
stays live. You never read a mixture of before and after, and a watcher that cannot
function returns an error rather than silently doing nothing.

Plus the ordinary things done deliberately — precedence is the order you added the sources
rather than a fixed ranking you cannot change; `Value[T]` reads any type including your own
`encoding.TextUnmarshaler` implementations; there is no global singleton, so
configuration-dependent tests run in parallel without touching process state; and the
environment prefix is required as a security control.

## Reacting to change: told once, told in order, told the truth

The usual shape for hot-reload is a callback handed a **filesystem event** — which says a
file was touched, not what changed, not whether the result was usable, and not whether it
is still current by the time you read it. An observer here is handed a **configuration**,
and the guarantees around that delivery are the point:

| Guarantee | What it means |
|---|---|
| **Exactly once per logical change** | One `Apply` touching four keys across three files is *one* notification — not four, not three, not once per filesystem event. |
| **Never for a rejected change** | A file that will not parse, or fails your schema, notifies nobody; announcing it would be a lie. Rejections travel on `OnReloadError`. |
| **Never for a change that changed nothing** | A write leaving the resolved configuration identical does not notify, even though the file was rewritten. |
| **In order, always** | Observers are never handed an older snapshot after a newer one. Under concurrent writes a superseded snapshot is dropped, not delivered late. |
| **Pinned for the whole callback** | One immutable snapshot per delivery, so an observer cannot read half of one configuration and half of the next. |
| **One failure does not silence the rest** | Observer errors route to `OnObserverError`; the remaining observers still run. |
| **No writing from inside an observer** | `ErrWriteFromObserver`, rather than a cascade with no natural end. |

Ordering and exactly-once are structural — delivery is serialised and version-checked in
one place — rather than something each observer must defend itself against. It cannot: by
the time an older snapshot arrives, an observer has no way to know a newer one already did.

**The limit worth knowing:** exactly-once is per logical change *as the Store understands
it*. An `Apply` is one change however many files it touches, because the Store performs it
and knows the batch. Two files changed by something else are separate events — nothing in
the filesystem says they were one intended change — so foreign changes are coalesced behind
a settle window (250ms by default, `WithSettleInterval`) and reload once.

That reduces the problem rather than removing it: writes spaced wider than the window look
exactly like two changes, so observers run twice and the first run sees a combination nobody
meant. Each snapshot is still internally coherent, never a mixture of two reads. A change
spanning files is atomic only if the writer makes it atomic — keep settings that change
together in one file, or swap the directory at once as a Kubernetes ConfigMap update does.
Both the coalescing and the residual case are pinned by tests.

Change detection is hybrid and per-path: native OS notification where it genuinely works,
polling where it does not — in-memory filesystems, network mounts, containers with inotify
watches exhausted. One unwatchable path does not downgrade the rest, and if native
notification stops being trustworthy mid-run the affected paths fall back to polling and
carry on. Only if polling cannot be established either has the watch genuinely stopped —
and then you are told. A watcher silently gone quiet is the one failure this design refuses.

For comparison, [viper](https://github.com/spf13/viper) v1.21.0 calls `OnConfigChange`
with the `fsnotify.Event`, and its watch loop invokes the callback **after a failed re-read
as well as a successful one** — the parse error goes to viper's internal logger and the
callback is told "changed" regardless. It retains the last good values, which is right; but
an observer that rebuilds a pool or reopens a listener whenever it is told something
changed will do that work for a change that did not happen.

All of the above is asserted in [`observer_contract_test.go`](observer_contract_test.go).

## Writing: where it gets genuinely hard

Everything above is a better answer to a problem other libraries also address. This is one
most cannot address at all, because their architecture forecloses it.

The moment your program **writes** configuration back — a settings screen, a `--set` flag,
a first-run wizard, an `auth login` that stores a token — the file stops being an input and
becomes something you are responsible for. Here is what changing one value does when a
library serialises its merged view over your file:

<table>
<tr><th>Before</th><th>After changing <code>server.port</code></th></tr>
<tr valign="top">
<td>

```yaml
# Which port the listener binds to.
# Needs a firewall change too.
server:
  host: localhost   # dev only
  port: 8080

# Feature flags. Keep alphabetical.
features:
  beta_ui: false
```

</td>
<td>

```yaml
db:
    password: hunter2-prod-secret
features:
    beta_ui: false
log:
    level: info
server:
    host: localhost
    port: 9090
```

</td>
</tr>
</table>

Every comment is gone, the order is now alphabetical rather than meaningful, a default the
program never had written down is now pinned into the user's file — and a production
password that arrived from an environment variable and was never in any file is now
sitting in one that is probably in git.

That is not a bug in anything. It is what "serialise the merged view" means, and it
follows from merging eagerly: once every source is folded into one map, a writer holding
that map has nothing to write *but* the whole of it. It cannot leave the environment's
contribution out, because it can no longer tell which contribution was the environment's.

**This module never folds its layers away.** A `Store` owns every read, write and watch,
and records provenance *during* the merge instead of reconstructing it afterwards. Changing
`server.port` above changes `server.port`, and nothing else.

## Any source can be a layer

Layers are not limited to files and readers. `WithBackend` takes anything satisfying a
three-method `Backend`, so a source this module has never heard of — Consul, a secrets
manager, an HTTP endpoint, a database table, a device's NVRAM — becomes an ordinary layer:

```go
store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),
	config.WithBackend(consulBackend{client: c, prefix: "app/"}),  // ← outranks the file
	config.WithEnv("APP"),
)
```

It takes part in precedence, provenance and shadowing exactly as a file does, and `Explain`
names it. Capability beyond reading is **opt-in through interfaces you may simply not
implement**:

| Implement | To get |
|---|---|
| `Backend` | reading, merging, precedence, provenance — the whole read surface |
| `WritableBackend` | `Apply` can route writes to it |
| `WatchableBackend` | it takes part in hot-reload, on its own schedule |

A read-only Consul layer implements one interface. A backend with a real subscription
implements `WatchableBackend` and gets push-based reload without polling anything. Nothing
forces a source to pretend it can do something it cannot — which is what makes a single
fat interface with stubbed methods so unpleasant to live with.
See **[Write a custom backend](https://config.go.phpboyscout.uk/how-to/custom-backend/)**.

## Validation on both sides, and repairable when it fails

A `Schema` from `config:` struct tags is checked against the **resolved** configuration
rather than any single layer — a base file may legitimately omit a key its overlay
supplies. It runs at load, on every reload, and on every write, where the obvious rule is
the wrong one:

> `Apply` rejects only the violations **your change introduces**. Pre-existing violations
> do not block the write, and are reported separately.

Validating the result outright would lock an already-broken configuration against edits,
including the edit that would fix it. `NewStore` follows the same reasoning, handing back a
usable store alongside `ErrInvalidConfig` — a configuration tool that cannot open a broken
configuration is of no use precisely when it is needed.

## Quieter things that matter

None of these sells the module on its own. Together they are most of what living with it
feels like.

- **Generics all the way through, not bolted on.** `Value[T]`, `UnmarshalSection[T]`,
  `ObserveSection[T]`, `ValidateStruct[T]`, `SchemaOf[T]` — one consistent shape rather
  than a generic escape hatch beside a hand-written method per type. A type this module has
  never seen needs no new API and no release: it reads through the same `Value[T]`, and if
  it implements `encoding.TextUnmarshaler` it decodes without being special-cased.
- **A multi-document YAML file is several layers**, one per document, each taking part in
  precedence and provenance independently — `Explain` reports `/app.yaml#1`. The common
  alternative is worse than unsupported: viper reads the first document, silently discards
  the rest, and returns no error.
- **Every failure is a named error you can branch on** — seventeen sentinels matched with
  `errors.Is`, so handling a specific failure never means comparing strings. Validation
  errors carry a fix hint alongside the message.
- **Context on every operation that does I/O**, so configuration work participates in your
  shutdown path instead of ignoring it.
- **A smaller dependency graph than the thing it replaces**, while doing more — 20
  non-stdlib packages across 8 modules, against viper's 36 across 13 (library only, no test
  dependencies). The filesystem is a six-method interface this module defines, so you are
  not made to import one.
- **Typed sections + validation.** `ObservedSection[T]` keeps a struct current across
  reloads — decoded in one operation, so it never holds some fields from before a reload
  and some from after, and republished only when the struct actually changed. Defaults take
  *your* merge function, so "merge" means what your type needs. `Schema` /
  `ValidateStruct[T]` check field rules on both reload and write.
- **Everything is a layer.** Files, one document within a multi-document file, defaults,
  environment, flags and runtime `AddLayer` sources all take part in precedence, provenance
  and shadowing the same way.
- **`Snapshot.Version()`** tells a component whether the configuration it holds has moved
  on without comparing values; **`Sub`** scopes a view to a subtree; **`With`** pins one
  snapshot across a block of reads.
- **`Plan` is a dry run that cannot drift** from `Apply`, because it *is* the same routing
  pass rather than a second implementation.
- **Published testify mocks** for downstream tests, and a six-method `config.FS`
  interface for the filesystem — `config.OS()`, `config.Dir(path)`, or your own.

## Should you use this?

**Yes**, if you have lost an afternoon to "which source set this?"; if a long-running
service reloads configuration and reads straddling a reload are unacceptable; if your
program writes configuration back and users live in the file afterwards; if config lands in
files that get committed and reviewed; if you want config-dependent tests that run in
parallel without process globals; or if your settings have real types you are tired of
decoding by hand.

**Probably not**, if one file and a couple of environment variables cover you, nothing is
written back and nothing reloads. That is a large share of programs and a well-served case:
[viper](https://github.com/spf13/viper) is battle-tested by an enormous number of them, has
an ecosystem this module does not, and is a smaller dependency. Much of what is here was
learned from years of using it — see the
[history](https://config.go.phpboyscout.uk/about/history/).

## Install

```bash
go get gitlab.com/phpboyscout/go/config
```

## Quick start

```go
package main

import (
	"context"
	"fmt"

	"gitlab.com/phpboyscout/go/config"
)

func main() {
	ctx := context.Background()

	s, err := config.NewStore(ctx,
		config.WithFiles(config.OS(), "config.yaml"),
		config.WithEnv("APP"))
	if err != nil {
		panic(err)
	}

	fmt.Println(s.View().GetString("name"))

	// Where did that value come from?
	fmt.Println(s.View().Explain("name"))

	// Persist a change, preserving the file's comments and layout.
	if _, err := s.Apply(ctx, config.Set("name", "updated")); err != nil {
		panic(err)
	}

	// React to changes made by anything other than this process.
	s.AddObserverFunc(func(cfg config.Observed) error {
		fmt.Println("reloaded:", cfg.GetString("name"))

		return nil
	})
}
```

## What's inside

- **`Store`** — the single owner of config I/O: `NewStore`, `View`, `With`, `Plan`,
  `Apply`, `Reload`, `Watch`, `AddLayer`, `Snapshot`.
- **Reading** — `Reader` / `View` typed accessors, `Sub` for a scoped read, `Unmarshal`.
- **Writing** — `Set` / `Remove`, `Plan` as a dry run, `Operation.Effective()` and
  `ShadowedBy` for writes a higher layer still overrides.
- **Provenance** — `Origin`, `Shadowed`, `Explain`.
- **Sections** — `Section[T]` and `ObservedSection[T]` for typed, reload-aware subtrees.
- **Validation** — `Schema` / `FieldSchema` / `ValidateStruct[T]` / `ValidationResult`,
  applied on reload and on write.
- **`mocks`** — published testify mocks for downstream tests.

Comment-preserving YAML editing lives in its own module,
[`yamldoc`](https://yamldoc.go.phpboyscout.uk).

## Migrating from v0.2.x

v0.3.0 is a breaking release: the Viper-backed container became the `Store`, and `afero.Fs`
became a six-method `config.FS` the module defines itself. The typed-section semantics are
unchanged, so a package consuming `ObservedSection[T]` through its own interface needs no
change at all; code holding a `Containable` does.

Two traps have no compiler error. **Watching is explicit now** — v0.2.x watched from inside
every constructor, so skipping `Store.Watch(ctx)` means configuration silently stops
reloading. And **`ApplyInitial` is not a required fix** — delivery has always been
change-only.

See the [migration guide](https://config.go.phpboyscout.uk/migrating/) and the
[history](https://config.go.phpboyscout.uk/about/history/) of how the module got here.

## Documentation

Full guides and the precedence/hot-reload model: **[config.go.phpboyscout.uk](https://config.go.phpboyscout.uk)**.
API reference: **[pkg.go.dev](https://pkg.go.dev/gitlab.com/phpboyscout/go/config)**.

## License

See [LICENSE](LICENSE).

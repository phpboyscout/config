# Who owns the connection

Every adapter that reads configuration from somewhere remote — Parameter Store,
Key Vault, Consul, an S3 bucket — needs a client to talk to it. This page is
about who builds that client, and why the answer is "you, by default, but not
necessarily".

## The ladder

An adapter that owns a connection offers up to four ways to get one. They are
rungs, not alternatives: each is a thinner layer over the one below.

| Rung | What you supply | When you want it |
|---|---|---|
| **1 — narrow interface** | a value satisfying the adapter's own small interface | tests, and fakes; this is the seam the unit suites use |
| **2 — SDK client** | a built `*ssm.Client`, `*azsecrets.Client`, `*capi.Client` … | you already build clients for other reasons |
| **3 — native config** | the provider's own config or credential | you want **one** credential resolution feeding several clients |
| **4 — ambient** | nothing | you just want it to work |

```go
// 1 — the testing seam
configawsssm.New(fakeParamStore{}, "/app")

// 2 — you built the client
configawsssm.FromClient(ssm.NewFromConfig(cfg), "/app")

// 3 — you resolved the config; the adapter builds its own client
configawsssm.FromConfig(cfg, "/app")

// 4 — you supplied nothing
ssmambient.Default("/app")
```

## Injection is the default, and stays that way

Rungs 1 and 2 are the ones every adapter has always had, and they remain the
recommended path for anything long-lived. Three things follow from the consumer
owning the client, and all three are worth keeping:

- **The adapter is credential-agnostic.** Every cloud does authentication
  differently and you have already decided how; the adapter has no business
  re-deciding it.
- **The adapter is testable without the cloud.** A fake satisfying the narrow
  interface drives the whole unit suite — no account, no network.
- **The dependency is honest.** The adapter pulls only the service package it
  needs. The credential-resolution graph, which is much larger, is yours only if
  you ask for it.

That last point is the one the ambient rung has to work around.

## Why the ambient rung sometimes lives in a subpackage

Resolving an ambient credential chain is not free. Measured against the library
graph each adapter actually hands a consumer:

| Family | Rung 3 | Rung 4 |
|---|---|---|
| AWS | free | **+7 to +10 modules** |
| Azure | free | **+7 modules** |
| GCP | free | free |
| Vault, Consul, etcd | free | free |

Where the ambient rung is free, it sits in the adapter's main package and you
call `Default(…)`. Where it is not, it sits in a subpackage:

```go
import ssmambient "gitlab.com/phpboyscout/go/config-aws-ssm/ambient"

b, err := ssmambient.Default("/app")
```

One extra import, and in exchange the adapter's own dependency footprint is
exactly what it always was. If you never want ambient credentials, you never pay
for them — which is the whole reason each adapter states its cost in a test.

## Sharing a connection is deliberate, never automatic

Two adapters that each take their ambient default resolve the provider chain
**twice**. That is intended, not an oversight.

A hidden process-wide cache would be worse than the duplication it saves: it
would silently share credentials between components that may deliberately differ
— a different profile, an assumed role, a distinct endpoint — and it would make
one component's transient failure everybody's, invisibly.

If you want one resolution feeding several adapters, say so. That is what rung 3
is for:

```go
src := awsclient.Ambient()   // resolved once, lazily, on first use

store, err := config.NewStore(ctx,
    config.WithBackend(configawsssm.FromSource(src, "/app")),
    config.WithFiles(configawss3.FSFromSource(src, "my-bucket"), "config.yaml"),
)
```

The same source can feed `go/signing` and `go/encryption` too — it is an estate
module, not a config one.

## What a self-connecting backend guarantees

A backend that resolves its own connection still behaves like any other layer,
and the [backend conformance suite](https://gitlab.com/phpboyscout/go/config/-/tree/main/backendconformance)
checks it:

- **It can say what it is before it connects.** `ID()` and `Capabilities()`
  answer without a network call, so building a `Store` never becomes a network
  operation.
- **A connection failure is an ordinary error from `Load`** — not a panic, and
  not "source not found". Those are different answers: a missing source may be
  perfectly fine, an unreachable one is not, and conflating them leaves your
  configuration silently short of a layer.
- **A failure is never remembered.** A credential chain that was not ready when
  your tool started is picked up by the next reload rather than needing a
  restart. This is why none of these adapters use `sync.OnceValues` — it caches
  the first error for the life of the process.

## Two adapters that do not have rung 4

**`config-sftp`** stops at rung 3, deliberately. An ambient SSH default would
have to choose a host-key verification policy on your behalf, and every option is
wrong for somebody: trust-on-first-use silently accepts a man in the middle,
requiring `known_hosts` fails exactly the machines most likely to want zero
configuration, and skipping verification is not something a configuration library
should ever ship. Build the `*ssh.Client` yourself.

**`config-keychain`** already has rung 4 under a different name.
`configkeychain.Registered()` resolves through the credentials registry, so what
you get depends on whether you blank-imported the keychain backend — and if you
did not, it reports unavailable rather than pretending. That name says more than
`Default()` would, so it keeps it.

## The format and filesystem adapters have no connection at all

`config-json`, `config-toml`, `config-hcl`, `config-ini`, `config-xml`,
`config-properties`, `config-dotenv`, `config-afero`, `config-billy`,
`config-iofs`, `config-filekv` and `config-schema` read through a `config.FS` or
operate on bytes you already have. There is no client to build, so the ladder
does not apply to them.

## Where this is specified

[config spec 0012](https://gitlab.com/phpboyscout/go/config/-/wikis/specs/0012-adapter-connection-ladder)
decides the config side. It implements
[org spec 0003](https://gitlab.com/phpboyscout/org/-/wikis/specs/0003-provider-client-modules),
which defines the provider client modules, over
[org spec 0002](https://gitlab.com/phpboyscout/org/-/wikis/specs/0002-adapter-connection-lifecycle),
which settles the connection lifecycle for the whole toolkit.

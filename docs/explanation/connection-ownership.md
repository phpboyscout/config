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
| **1 — narrow interface** | a value satisfying the adapter's own small interface | tests and fakes; this is the seam the unit suites use |
| **2 — SDK client** | a built `*ssm.Client`, `*azsecrets.Client`, `*capi.Client` … | you already build clients for other reasons |
| **3 — native config** | the provider's own config, credential or options | you want **one** credential resolution feeding several clients |
| **4 — ambient** | nothing but the target | you just want it to work |

```go
// 1 — the testing seam
configawsssm.New(fakeParamStore{}, "/app")

// 2 — you built the client
configawsssm.FromClient(ssm.NewFromConfig(cfg), "/app")

// 3 — you resolved the config; the adapter builds its own client
b, err := configawsssm.FromConfig(cfg, "/app")

// 4 — you supplied nothing but the prefix
b, err := ssmambient.Default(ctx, "/app")
```

Rungs 3 and 4 return an `error` where rungs 1 and 2 do not. That is not
inconsistency: those rungs *validate* something, and failing at construction is
better than failing at the first read.

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
| Vault, Consul | free | free |

Where the ambient rung is free, it sits in the adapter's main package and you
call `Default(…)`. Where it is not, it sits in a subpackage:

```go
import ssmambient "gitlab.com/phpboyscout/go/config-aws-ssm/ambient"

b, err := ssmambient.Default(ctx, "/app")
```

One extra import, and in exchange the adapter's own dependency footprint is
exactly what it always was. Each of those subpackages carries a test asserting
its parent's graph has not grown — so if the split ever leaks, the build says so.

AWS costs 7 on `config-aws-s3` and 10 on the other two, because S3's larger
service graph already carries three of the credential modules. Each adapter
measures its own rather than quoting a sibling's.

## Sharing a connection is deliberate, never automatic

Two adapters that each take their ambient default resolve the provider chain
**twice**. That is intended, not an oversight.

A hidden process-wide cache would be worse than the duplication it saves: it
would silently share credentials between components that may deliberately differ
— a different profile, an assumed role, a distinct tenant — and it would make one
component's transient failure everybody's, invisibly.

If you want one resolution feeding several adapters, say so:

```go
src := awsclient.Ambient(awsclient.WithRegion("eu-west-2"))

ssmBackend, err := ssmambient.FromSource(ctx, src, "/app")
s3fs, err       := s3ambient.FSFromSource(ctx, src, "my-bucket")
```

The same source can feed [`go/signing`](https://gitlab.com/phpboyscout/go/signing)
and [`go/encryption`](https://gitlab.com/phpboyscout/go/encryption) too — it is an
estate module, not a config one.

### The provider client modules

There is one per provider, deliberately. A single combined module would drag
every cloud SDK into one dependency graph and destroy the segregation this family
is built on, so each is imported only by consumers who want that provider.

| Module | Yields | Notes |
|---|---|---|
| [`go/awsclient`](https://awsclient.go.phpboyscout.uk) | `aws.Config` | refuses to guess a region |
| [`go/azureclient`](https://azureclient.go.phpboyscout.uk) | `azcore.TokenCredential` | guards the typed-nil interface case |
| [`go/gcpclient`](https://gcpclient.go.phpboyscout.uk) | `[]option.ClientOption` | options, not a client — three GCP adapters need three client types |
| [`go/vaultclient`](https://vaultclient.go.phpboyscout.uk) | `*vaultapi.Client` | for Vault the client *is* the prerequisite |

Each offers the same shape: inject what you have, or take the ambient default —
plus a non-memoising `PerCall` rung, because how long a component holds a
credential is a security posture belonging to that component rather than to the
module. All of them share one state machine,
[`go/clientlifecycle`](https://clientlifecycle.go.phpboyscout.uk), which
has no dependencies of its own and never caches a failure.

Two of the five have no consumer in this family: the GCP adapters take
`option.ClientOption` directly and `config-vault` uses `vaultapi.DefaultConfig()`.
They exist for the estate — `go/vaultclient` in particular for the signing and
encryption adapters that follow.

## An ambient rung supplies credentials, never the target

This is the rule that holds across every adapter, and it is worth stating because
it explains a lot of `error` returns.

The ambient rung answers **who am I**. It never answers **what am I talking to**.
Every adapter refuses when the target is missing, rather than guessing:

| Refused | Adapters |
|---|---|
| region | `aws-ssm`, `aws-secrets`, `aws-s3` |
| project | `gcp-secret`, `gcp-parameter` |
| location | `gcp-parameter` |
| bucket, container | `gcp-gcs`, `aws-s3`, `azure-blob` |
| vault URL, endpoint, service URL | `azure-keyvault`, `azure-appconfig`, `azure-blob` |
| address | `vault` |

There is one deliberate exception in the other direction. `config-vault`'s
`Default` inherits Vault's own documented `127.0.0.1:8200`, because **adopting a
provider's documented default is not the same act as inventing one**. AWS
documents no default region, so `config-aws-ssm` refuses instead.

## Some rungs hand you something to close

Where a rung **builds** the client, it owns it — and if that client holds a
connection, the rung returns a concrete type carrying `Close` rather than the
bare interface:

```go
b, err := configgcpsecret.Default(ctx, "my-project", "")
if err != nil { return err }
defer b.Close()          // a service closes it; a CLI simply does not

store, err := config.NewStore(ctx, config.WithBackend(b))
```

The obligation follows **who built the client** *and* **whether the SDK gives you
anything to release**:

| Adapter | Client | Returns |
|---|---|---|
| `config-gcp-secret`, `config-gcp-parameter` | gRPC — *"must be Closed"* | `*OwnedBackend` |
| `config-gcp-gcs` | HTTP — *"need not be called at program exit"* | `*OwnedFS` |
| `config-sftp` | a subsystem channel | `*OwnedFS` — closes the subsystem, **never your SSH connection** |
| `config-azure-blob` | HTTP — no `Close` at all | plain `config.FS` |
| everything else | nothing to release | plain `config.Backend` |

Rungs 1 and 2 never return an owned type: there, you built the client and still
own it.

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

## A credential, or a way to get one

That last guarantee is about a failure the adapter can retry. There is one it
cannot, and which rung 4 makes easier to meet — because rung 4 hands the adapter
a credential you never see.

The property that decides it:

> **Does the object the adapter holds know how to obtain a fresh credential, or
> is it already a credential?**

Where it is a *means*, the SDK renews underneath you and a process can run for
weeks:

| Provider | What is held | Renews? |
|---|---|---|
| AWS | a provider chain behind `aws.NewCredentialsCache` | yes, on expiry |
| Azure | a `TokenCredential` the pipeline re-calls | yes, before expiry |
| GCP | a cached token provider | yes, once stale |
| etcd | a username and password | yes — it re-authenticates |
| keychain | nothing at all; every call re-resolves | not applicable |

Where it is an already-minted **token**, nothing renews it:

| Provider | What is held | Renews? |
|---|---|---|
| Vault | the token `VAULT_TOKEN` carried | **no** |
| Consul | the resolved ACL token | **no** |

Both HashiCorp SDKs model their client as *configured with a token* rather than
*configured with a way to get a token*, and neither re-reads its environment.
Consul additionally reads `CONSUL_HTTP_TOKEN_FILE` once, at construction, though
a token file is precisely the mechanism you would rotate.

The consequence only bites a **long-lived process reloading configuration**: once
the token's lifetime passes, every `Load` fails and nothing recovers, because a
backend cannot tell "my credential expired" from "I am not allowed" — and those
want opposite responses. A command that runs for a second and exits never notices.

**The ladder did not cause this.** `FromClient` always behaved this way. What
rung 4 changes is *visibility*: when you built the client, the token was in your
hands; when the adapter builds it, nobody confronts it. So the two adapters where
it is true say so on `Default` itself.

If your process outlives its token, build the client, renew or rebuild it, and
hand it to `FromClient` — rung 2 doing exactly the job rung 2 exists for.

For completeness: a connection string is a static secret, and Azure's may carry a
SAS with an expiry. That rung is injected rather than ambient, so the credential
is already in your hands — the same hazard, but not a hidden one.

## Three adapters that stop short, and why

**`config-etcd` has no ambient rung, permanently.** `clientv3` has neither an
ambient credential chain nor endpoint discovery — unlike every other provider
here, *both* halves would have to be invented. A `Default()` could only be
`FromConfig` with made-up environment parsing bolted on, and everything of
substance there is already `FromConfig`. Use `FromConfig` with the endpoints you
know.

**`config-sftp` stops at rung 3.** An ambient SSH default would have to choose a
host-key verification policy on your behalf, and every option is wrong for
somebody: trust-on-first-use silently accepts a man in the middle, requiring
`known_hosts` fails exactly the machines most likely to want zero configuration,
and skipping verification is not something a configuration library should ever
ship. Dial the `*ssh.Client` yourself and hand it to `FromSSH`.

**`config-keychain` already has rung 4 under a different name.**
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

---
title: The adapter ecosystem
description: The config adapter family — file/format adapters and dynamic remote backends, each an ordinary layer.
tags: [adapters, backends, ecosystem]
---

# The adapter ecosystem

**One store, many sources.** YAML files are the default `config` reads out of the box, but
they are not the boundary. A file in another format, or configuration that lives in a remote
system rather than a file at all, joins the store as an **ordinary layer** — with the same
precedence, the same per-key provenance, the same coherent snapshots and the same fail-closed
reload every other layer gets.

That is the difference that matters. A library that treats "remote config" as a bolt-on
special case gives you a value and little else; here there is no special case. Consul, a
parameter store or a secrets manager takes part in the merge exactly as `/etc/app.yaml` does,
and `Explain("server.port")` will name it as the source. This is what the previous
Viper-shaped world could not do.

Every adapter is its own **sibling module**, depended on only by the consumers who use it. Your
dependency graph carries the one integration you reached for and nothing else: a consumer
reading TOML never compiles the XML parser, and a consumer configuring from Consul never pulls
a cloud SDK it does not touch.

## What every adapter inherits

An adapter only teaches the store how to *read* (and, where it makes sense, *write* and
*watch*) one kind of source. Everything else is the core's job, so it is identical across the
whole family:

- **Precedence** — the adapter's layer sits wherever you place it in the source order.
- **Provenance** — `Explain` and `Origin` name the adapter as the source of a value, and flag
  where it shadows or is shadowed.
- **Coherent reads** — a `View` is pinned to one snapshot regardless of which adapters fed it.
- **Write fidelity** — where an adapter supports writes, the change lands in the layer that
  owns the key, and structure (comments, order, quoting) is preserved.
- **Fail-closed reload** — a source that will not parse or fails your schema is rejected;
  last-known-good stays live.

## File & format adapters

Available now, each published and versioned. The adapter name links to its how-to guide.

| Adapter | Handles | Reads | Writes | Source |
|---|---|:---:|:---:|---|
| [`config-json`](../how-to/json.md) | JSON &amp; JSON Lines | ✓ | ✓ *(structure-preserving)* | [repo](https://gitlab.com/phpboyscout/go/config-json) · [API](https://pkg.go.dev/gitlab.com/phpboyscout/go/config-json) |
| [`config-toml`](../how-to/toml.md) | TOML | ✓ | ✓ *(structure-preserving)* | [repo](https://gitlab.com/phpboyscout/go/config-toml) · [API](https://pkg.go.dev/gitlab.com/phpboyscout/go/config-toml) |
| [`config-hcl`](../how-to/hcl.md) | HCL *(as a config format, not Terraform)* | ✓ | ✓ | [repo](https://gitlab.com/phpboyscout/go/config-hcl) · [API](https://pkg.go.dev/gitlab.com/phpboyscout/go/config-hcl) |
| [`config-xml`](../how-to/xml.md) | XML | ✓ | — | [repo](https://gitlab.com/phpboyscout/go/config-xml) · [API](https://pkg.go.dev/gitlab.com/phpboyscout/go/config-xml) |
| [`config-dotenv`](../how-to/dotenv.md) | dotenv (`.env`) | ✓ | — | [repo](https://gitlab.com/phpboyscout/go/config-dotenv) · [API](https://pkg.go.dev/gitlab.com/phpboyscout/go/config-dotenv) |
| [`config-ini`](../how-to/ini.md) | INI | ✓ | — | [repo](https://gitlab.com/phpboyscout/go/config-ini) · [API](https://pkg.go.dev/gitlab.com/phpboyscout/go/config-ini) |
| [`config-properties`](../how-to/properties.md) | Java `.properties` | ✓ | — | [repo](https://gitlab.com/phpboyscout/go/config-properties) · [API](https://pkg.go.dev/gitlab.com/phpboyscout/go/config-properties) |

The read-only format adapters (`dotenv`, `ini`, `properties`, `xml`) add **no third-party
dependency** — they parse their format in-module. And if the format you need is not here,
[write a format adapter](../how-to/format-adapter.md): a codec is a `Decode`/`Encode` pair, and
the store handles the rest.

## Filesystem adapters

A format adapter decides *how* a config file is parsed; a **filesystem adapter** decides *where* it
lives. The two compose — you can read TOML out of an S3 bucket — because `config` imposes no
filesystem of its own: [`config.FS`](filesystem-adapters.md) is six methods you can satisfy over the
real disk (`config.OS()`), a rooted directory (`config.Dir`), an embedded filesystem, a remote host
or a cloud object store. A filesystem adapter reads a configuration *file* that happens to live
somewhere other than local disk — which is what distinguishes it from a [dynamic backend](#dynamic-backends),
that maps a remote key–value namespace instead.

Two implementations ship in the core — `config.OS()` and `config.Dir(path)` — so you only reach for
an adapter when the file lives somewhere neither covers.

| Adapter | Where the file lives | Reads | Writes | Source |
|---|---|:---:|:---:|---|
| [`config-afero`](../how-to/afero.md) | an existing [afero](https://github.com/spf13/afero) filesystem | ✓ | ✓ | [repo](https://gitlab.com/phpboyscout/go/config-afero) · [API](https://pkg.go.dev/gitlab.com/phpboyscout/go/config-afero) |
| [`config-iofs`](../how-to/iofs.md) | any [`io/fs.FS`](https://pkg.go.dev/io/fs#FS) — `embed.FS`, zip, tar | ✓ | — | [repo](https://gitlab.com/phpboyscout/go/config-iofs) · [API](https://pkg.go.dev/gitlab.com/phpboyscout/go/config-iofs) |
| [`config-billy`](../how-to/billy.md) | a [go-billy](https://github.com/go-git/go-billy) filesystem (go-git) | ✓ | ✓ | [repo](https://gitlab.com/phpboyscout/go/config-billy) · [API](https://pkg.go.dev/gitlab.com/phpboyscout/go/config-billy) |
| [`config-sftp`](../how-to/sftp.md) | a remote host over SSH | ✓ | ✓ | [repo](https://gitlab.com/phpboyscout/go/config-sftp) · [API](https://pkg.go.dev/gitlab.com/phpboyscout/go/config-sftp) |

`config-iofs` is read-only because `io/fs` is read-only by design, and adds **no third-party
dependency**. How reload, read-only capability and the object-store commit work across the family is
[How filesystem adapters work](filesystem-adapters.md); the
[filesystem adapters spec](../development/specs/2026-07-22-filesystem-adapters.md) is the umbrella
that governs it.

### Cloud object stores

The three cloud object stores share one model — a config file that lives in a bucket, watched by
polling, committed with the store's best atomic primitive — and differ only where the services do
(GCS has a native atomic move; S3 and Azure Blob copy-then-delete). Each is its own approved spec,
built and released independently.

| Adapter | Store | Commit | Status | Source |
|---|---|---|---|---|
| [`config-aws-s3`](../how-to/aws-s3.md) | AWS S3 | copy-then-delete | **v0.1.0** | [repo](https://gitlab.com/phpboyscout/go/config-aws-s3) · [API](https://pkg.go.dev/gitlab.com/phpboyscout/go/config-aws-s3) |
| [`config-gcp-gcs`](../how-to/gcp-gcs.md) | GCP Cloud Storage | native `Move` | **v0.1.0** | [repo](https://gitlab.com/phpboyscout/go/config-gcp-gcs) · [API](https://pkg.go.dev/gitlab.com/phpboyscout/go/config-gcp-gcs) |
| [`config-azure-blob`](../how-to/azure-blob.md) | Azure Blob Storage | copy-then-delete | **v0.1.0** | [repo](https://gitlab.com/phpboyscout/go/config-azure-blob) · [API](https://pkg.go.dev/gitlab.com/phpboyscout/go/config-azure-blob) |

All three are read+write, poll at a 60-second default (a poll is a billed object read;
`WithPollInterval` overrides), and are proven against a real emulator — LocalStack, fake-gcs-server
and Azurite — in a Docker-in-Docker job.

## Dynamic backends

The next chapter, and the one that pulls furthest ahead of a file-only tool: configuration
fetched **at runtime from a remote system**, given full precedence, provenance and hot-reload
exactly as a file is. The seam already exists and is proven —
[`WithBackend`](../how-to/custom-backend.md) takes anything satisfying a three-method `Backend`,
with writes and native watch as opt-in capabilities. The
[dynamic backend adapters spec](../development/specs/2026-07-21-dynamic-backend-adapters.md) is the
umbrella that governs the whole family.

### config-consul — the first

[**`config-consul`**](https://gitlab.com/phpboyscout/go/config-consul) — released at
**v0.1.0** ([API](https://pkg.go.dev/gitlab.com/phpboyscout/go/config-consul)) — reads and writes
configuration from [HashiCorp Consul](https://www.consul.io/) through `config`. You build and
configure the Consul client — every address, token, TLS and datacenter decision stays yours —
and hand it in with a prefix that scopes and is stripped from the keys:

```go
import (
	capi "github.com/hashicorp/consul/api"
	"gitlab.com/phpboyscout/go/config"
	configconsul "gitlab.com/phpboyscout/go/config-consul"
)

client, _ := capi.NewClient(capi.DefaultConfig())

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                     // YAML defaults
	config.WithBackend(configconsul.FromClient(client, "app/")), // Consul outranks them
)
```

A Consul layer takes part in precedence, per-key merge, provenance and hot-reload exactly as a
file does — and `Explain` will tell you when a value came from Consul rather than the file
beneath it. It is the reference implementation for everything that follows. Learn it by building
one in the [Configure from Consul](../tutorials/consul.md) tutorial, reach for a specific operation in
the [Read &amp; write Consul](../how-to/consul.md) how-to, or read [How the Consul backend
works](consul-backend.md) for the data, conflict and watch models behind it.

### Parameter stores — released

Consul's siblings, the cloud parameter stores, are all released at **v0.1.0**. They share Consul's
shape — injected client, prefix-scoped nested tree, values decoded through an injected codec — and
differ where the systems do, which is what [How dynamic backends work](dynamic-backends.md)
explains. In short: they **poll** rather than watch natively, and they split on compare-and-swap.

- [**`config-aws-ssm`**](../how-to/aws-ssm.md) — AWS SSM Parameter Store. Read-only (SSM has no
  compare-and-swap); `SecureString` values read decrypted and mark the layer sensitive so the leak
  guard protects them.
- [**`config-azure-appconfig`**](../how-to/azure-appconfig.md) — Azure App Configuration. Read
  **and** write on per-key ETag compare-and-swap; scoped by a label.
- [**`config-gcp-parameter`**](../how-to/gcp-parameter.md) — GCP Parameter Manager. Read-only, in
  two shapes: one parameter as a whole document, or a prefix of many parameters.

### Roadmap

Each adapter below carries its own SDK, its own authentication and its own consistency and
watch semantics, so — by the umbrella's headline rule — **each gets its own approved spec
before it is built**. The grouping is a planned order, not a commitment date.

| Adapter | System | Phase | Status |
|---|---|---|---|
| [`config-consul`](../how-to/consul.md) | HashiCorp Consul | A — reference &amp; parameter stores | **Released · v0.1.0** |
| [`config-aws-ssm`](../how-to/aws-ssm.md) | AWS SSM Parameter Store | A | **Released · v0.1.0** *(read-only)* |
| [`config-azure-appconfig`](../how-to/azure-appconfig.md) | Azure App Configuration | A | **Released · v0.1.0** |
| [`config-gcp-parameter`](../how-to/gcp-parameter.md) | GCP Parameter Manager | A | **Released · v0.1.0** *(read-only)* |
| `config-vault` | HashiCorp Vault | B — secrets managers | Planned *(read-only by default)* |
| `config-aws-secrets` | AWS Secrets Manager | B | Planned *(read-only by default)* |
| `config-azure-keyvault` | Azure Key Vault | B | Planned *(read-only by default)* |
| `config-gcp-secret` | GCP Secret Manager | B | Planned *(read-only by default)* |
| `config-etcd` | etcd | C — cloud-native key–value | Planned *(native watch)* |
| `config-k8s` | Kubernetes ConfigMaps | C | Planned *(native watch)* |

Secrets managers ship **read-only by default** — a config tool writing a secret is a rarer and
riskier thing than reading one, so write support for those is opt-in and specified per adapter.
Feature-flag systems are deliberately out of scope.

## Build your own

Nothing here is a closed set. The same two seams the family is built on are yours to use:

- [**Write a custom backend**](../how-to/custom-backend.md) — make any remote system (a secrets
  manager, an HTTP endpoint, an internal service) an ordinary layer, walked end to end against
  a Consul-shaped example.
- [**Support a new file format**](../how-to/format-adapter.md) — a codec is a `Decode`/`Encode`
  pair; add one and every store feature comes with it for free.

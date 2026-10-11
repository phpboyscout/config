---
title: How-to guides
description: One page per job (load and merge, read, write, validate, hot-reload, bind flags, compose and test), plus one for every format, filesystem and backend adapter in the family.
tags: [how-to]
---

# How-to guides

Each page here solves one problem for someone who already has a store. They assume you have
been through a [tutorial](../tutorials/index.md). If you have not, start there: a how-to
shows you the shortest route, not the shape of the thing.

## Working with a store

The thirteen jobs that come up whatever your configuration lives in.

| Guide | Answers |
|---|---|
| [Load and merge](load-and-merge.md) | How to declare sources, in what order, and what merging does per key. |
| [Read values](read-values.md) | Getting typed values out, scoping a view, and asking where one came from. |
| [Write configuration](write-config.md) | Planning and applying a change, where routing sends it, and how to pin it elsewhere. |
| [Validate configuration](validate-config.md) | Schemas and struct-tag validation, at load and before a write lands. |
| [Compose schemas](compose-schemas.md) | Mounting each component's schema where it lives, bounding what a source may supply, and using `config-schema` for JSON Schema documents. |
| [Hot-reload](hot-reload.md) | Watching sources, observing changes, and what happens when a reload fails. |
| [Typed sections](typed-sections.md) | Decoding a subtree into a struct, once or on every change. |
| [Bind CLI flags](bind-cli-flags.md) | Making a `pflag` set a layer, and remapping a flag name to a key. |
| [Compose stores](compose-stores.md) | Carrying one store's layers inside another, and promoting a value between them. |
| [Filter a backend](filter-a-backend.md) | Bounding which keys a backend contributes, and accepts writes for. |
| [Test with mocks](test-with-mocks.md) | Faking a backend, a filesystem or a clock without a real service. |
| [Write a custom backend](custom-backend.md) | Implementing `Backend`, and opting into writes and native watch. |
| [Write a format adapter](format-adapter.md) | Implementing a codec, and proving it against the conformance suite. |

## Reading a different file format

The core reads YAML and, because YAML 1.2 is a superset of JSON, a JSON document too.
Each of these is a sibling module that teaches it one more format, and your dependency graph
carries only the ones you import. Four of the seven add **no third-party dependency at
all**.

| Guide | Format | Writes |
|---|---|:---:|
| [JSON](json.md) | JSON and JSON Lines | yes |
| [TOML](toml.md) | TOML | yes |
| [HCL](hcl.md) | HCL, as a config format | yes |
| [XML](xml.md) | XML | no |
| [dotenv](dotenv.md) | `.env` files | no |
| [INI](ini.md) | INI | no |
| [Java properties](properties.md) | `.properties` | no |

A format adapter decides *how* a file is parsed. Where it lives is the next section's job,
and the two compose: you can read TOML out of an S3 bucket.

## Reading from a different filesystem

The core ships `config.OS()` and `config.Dir(path)`. Reach for an adapter when the file
lives somewhere neither covers.

| Guide | Where the file lives | Writes |
|---|---|:---:|
| [afero](afero.md) | an existing afero filesystem | yes |
| [io/fs](iofs.md) | any `io/fs.FS`, such as `embed.FS`, zip or tar | no |
| [go-billy](billy.md) | a go-billy filesystem, as used by go-git | yes |
| [SFTP](sftp.md) | a remote host over SSH | yes |
| [AWS S3](aws-s3.md) | an S3 bucket | yes |
| [GCP Cloud Storage](gcp-gcs.md) | a GCS bucket | yes |
| [Azure Blob](azure-blob.md) | an Azure Blob container | yes |

## Reading from a remote system

Configuration that is not a file at all. It is fetched at runtime and takes part in
precedence, provenance and hot-reload exactly as a file does.

| Guide | System | Writes | Sensitive |
|---|---|:---:|:---:|
| [Consul](consul.md) | HashiCorp Consul KV | yes | no |
| [etcd](etcd.md) | etcd v3 | yes | no |
| [AWS SSM](aws-ssm.md) | AWS Parameter Store | no | per-value |
| [Azure App Configuration](azure-appconfig.md) | Azure App Configuration | yes | no |
| [GCP Parameter Manager](gcp-parameter.md) | GCP Parameter Manager | no | no |
| [Vault](vault.md) | HashiCorp Vault KV v2 | no | yes |
| [AWS Secrets Manager](aws-secrets.md) | AWS Secrets Manager | no | yes |
| [Azure Key Vault](azure-keyvault.md) | Azure Key Vault | no | yes |
| [GCP Secret Manager](gcp-secret.md) | GCP Secret Manager | no | yes |
| [OS keychain](keychain.md) | macOS / Windows / Secret Service | yes | yes |
| [filekv](filekv.md) | a directory of single-value files | opt-in | opt-in |

A **sensitive** layer is one the core will refuse to write down into a plainer layer
beneath it, so a secret cannot be copied into a config file by an ordinary save. The
keychain is the one writable secrets backend. Read [why it is the
exception](../explanation/dynamic-backends.md) before you use it.

## Getting a client for a remote adapter

Every guide below that talks to a remote system has a **Getting a client** section
listing the ways to obtain one: inject the client you built, hand over the
provider's native config, or (for most of them) take the ambient credential
chain and supply nothing but the target.

Injection is the default and stays recommended. The reasoning, the cost of each
ambient rung, and the adapters that deliberately stop short are in
[who owns the connection](../explanation/connection-ownership.md).

## Elsewhere

- **[The adapter ecosystem](../explanation/adapters.md)**: every adapter with its
  dependency footprint, its status and what it costs.
- **[Reference](../reference/index.md)**: key syntax, environment-variable rules, struct
  tags, every error value and every default.
- **[Explanation](../explanation/index.md)**: why the module is built this way.

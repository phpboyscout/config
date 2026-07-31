---
title: config
description: Layered configuration for Go that tells you where a value came from, and writes one back without wrecking the file.
tags: [config, go, overview]
hide:
  - navigation
---

<div class="cfg-hero">
  <div class="cfg-hero__body">
    <p class="cfg-hero__eyebrow">phpboyscout · go toolkit</p>
    <h1 class="cfg-hero__title">Configuration that <span class="amber">shows its work</span></h1>
    <p class="cfg-hero__lede">Assemble settings from defaults, files, the environment, flags and remote systems like Consul — with a precedence you read off the call site, every value able to say where it came from, and writes that never wreck the file.</p>
    <div class="cfg-hero__cta">
      <a class="cfg-btn cfg-btn--primary" href="getting-started/">Get started</a>
      <a class="cfg-btn cfg-btn--ghost" href="explanation/adapters/">Explore the ecosystem</a>
    </div>
    <code class="cfg-hero__install">go get gitlab.com/phpboyscout/go/config</code>
  </div>
  <div class="cfg-hero__art">
    <div class="cfg-hero__tile"><img src="images/branding/logo_transparent.svg" alt="config — layered plates resolving into one container"></div>
  </div>
</div>

<div class="cfg-eco">
  <span class="cfg-eco__label">Formats</span>
  <a class="cfg-pill" href="getting-started/">YAML</a>
  <a class="cfg-pill" href="how-to/json/">JSON</a>
  <a class="cfg-pill" href="how-to/toml/">TOML</a>
  <a class="cfg-pill" href="how-to/hcl/">HCL</a>
  <a class="cfg-pill" href="how-to/xml/">XML</a>
  <a class="cfg-pill" href="how-to/dotenv/">dotenv</a>
  <a class="cfg-pill" href="how-to/ini/">INI</a>
  <a class="cfg-pill" href="how-to/properties/">properties</a>

  <span class="cfg-eco__label">Filesystems</span>
  <a class="cfg-pill" href="explanation/filesystem-adapters/#the-built-ins-and-no-hidden-default">local disk</a>
  <a class="cfg-pill" href="how-to/afero/">afero</a>
  <a class="cfg-pill" href="how-to/iofs/">io/fs</a>
  <a class="cfg-pill" href="how-to/billy/">go-billy</a>
  <a class="cfg-pill" href="how-to/sftp/">SFTP</a>
  <a class="cfg-pill" href="how-to/aws-s3/">S3</a>
  <a class="cfg-pill" href="how-to/gcp-gcs/">GCS</a>
  <a class="cfg-pill" href="how-to/azure-blob/">Azure Blob</a>

  <span class="cfg-eco__label">Backends</span>
  <a class="cfg-pill" href="how-to/consul/">Consul</a>
  <a class="cfg-pill" href="how-to/aws-ssm/">AWS SSM</a>
  <a class="cfg-pill" href="how-to/azure-appconfig/">Azure App Config</a>
  <a class="cfg-pill" href="how-to/gcp-parameter/">GCP Parameter</a>
  <a class="cfg-pill" href="how-to/vault/">Vault</a>
  <a class="cfg-pill" href="how-to/aws-secrets/">AWS Secrets</a>
  <a class="cfg-pill" href="how-to/azure-keyvault/">Azure Key Vault</a>
  <a class="cfg-pill" href="how-to/gcp-secret/">GCP Secret</a>
  <a class="cfg-pill" href="how-to/keychain/">OS keychain</a>
  <a class="cfg-pill" href="how-to/filekv/">file-per-key dir</a>
  <a class="cfg-pill cfg-pill--more" href="explanation/adapters/#roadmap">+ roadmap →</a>
</div>

## The thirty-second version

A user changes one setting. Here is what that does to their file:

=== "Most libraries"

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

=== "`config`"

    ```yaml
    # Which port the public listener binds to.
    # Changing this needs a firewall change too — talk to platform first.
    server:
      host: localhost # loopback only in dev
      port: 9090

    # Feature flags. Keep alphabetical.
    features:
      beta_ui: false
    ```

Comments gone, order alphabetised, a default pinned into the file — and a production
password that arrived from an environment variable, now committed to git.

That is not a bug in anything. It is what "serialise the merged view" means, and it
follows from merging eagerly: fold every source into one map and a writer holding that map
has nothing to write *but* the whole of it. **This module never folds its layers away** —
so it changes the key you asked for, and nothing else.

## What you get

| | |
|---|---|
| **Provenance** | `Explain("server.port")` → `9090 (from ~/.app.yaml); also defined in embedded:defaults.yaml`. Recorded during the merge, not reconstructed after. |
| **Coherent reads** | A `View` is pinned to one snapshot, so two related values can never straddle a reload. Measured: **0** mismatched pairs in 11.6M reads, against 1,759 for a library reading live state. |
| **Writes that preserve authorship** | Comments, key order, quoting and anchors survive. The change lands in the layer that *owns* the key, and a write that cannot take effect says so. |
| **Notifications you can trust** | Exactly once per logical change, never for a rejected one, never out of order, pinned to one snapshot for the whole callback. |
| **Fail-closed reload** | A file that will not parse, or fails your schema, is rejected — last-known-good stays live. Never half of one config and half of another. |
| **Any source as a layer** | `WithBackend` takes any three-method `Backend`, so Consul, a secrets manager or an HTTP endpoint gets full precedence, provenance and shadowing. |
| **Typed sections** | `ObserveSection[T]` keeps your struct current across reloads, and the package consuming it never imports this one. |
| **Read any type** | `Value[T]` reads whatever `T` is — durations, IP addresses, URLs, and your own `encoding.TextUnmarshaler` types. |
| **No filesystem imposed** | You name the filesystem — `config.OS()` for the real disk (the default choice), `config.Dir(path)` for a directory nothing can escape, or [an adapter](explanation/filesystem-adapters.md) for elsewhere. `config.FS` is six methods, so your own works too. |

**[→ The full case, with the reasoning and the measurements](about/index.md)**

## At a glance

```go
store, err := config.NewStore(ctx,
	config.WithReaders(config.NamedSource{Name: "embedded:defaults.yaml", Content: defaults}),
	config.WithFiles(config.OS(), "/etc/mytool/config.yaml", userPath),
	config.WithEnv("MYTOOL"),
	config.WithFlags(cmd.Flags()),
)
if err != nil {
	return err
}

// Precedence is the order you added the sources: flags beat env, env beats the
// user file, the user file beats /etc, and all of them beat the embedded defaults.
port := store.View().GetInt("server.port")

// Ask why it is what it is.
fmt.Println(store.View().Explain("server.port"))
// server.port = 9090 (from /home/me/.mytool/config.yaml); also defined in embedded:defaults.yaml

// Write it back to the layer that owns it, comments intact.
if _, err := store.Apply(ctx, config.Set("server.port", 9090)); err != nil {
	return err
}
```

## Any format, any system — the same layered store

YAML is the default, not the ceiling. A file in another format, or configuration that lives in
a remote system rather than a file at all, joins the store as an **ordinary layer**: same
precedence, the same per-key provenance, the same coherent snapshots and fail-closed reload.
This is where the model pulls decisively ahead of a library that bolts remote sources on as a
special case — here there is no special case, and `Explain` will name Consul or a parameter
store as the source exactly as it names a file.

Every adapter is its own sibling module, so your dependency graph carries only what you use: a
consumer reading TOML never compiles the XML parser, and a consumer configuring from Consul
never pulls a cloud SDK it does not touch.

### File & format adapters — available now

<div class="grid cards" markdown>

- :material-code-json: **[JSON](how-to/json.md)** — JSON and JSON Lines, read and write, structure-preserving.
- :material-file-document: **[TOML](how-to/toml.md)** — read and write TOML, structure-preserving.
- :material-hexagon-outline: **[HCL](how-to/hcl.md)** — HCL as a config format, read and write.
- :material-xml: **[XML](how-to/xml.md)** — read XML.
- :material-dots-horizontal: **[dotenv](how-to/dotenv.md)** — read `.env`, no added dependency.
- :material-cog: **[INI](how-to/ini.md)** — read INI, no added dependency.
- :material-language-java: **[Java properties](how-to/properties.md)** — read `.properties`.

</div>

### Filesystem adapters — where the file lives

A format adapter parses the file; a filesystem adapter decides *where* it lives, over the six-method
`config.FS`. The core covers local disk (`config.OS()`, `config.Dir`) — reach for one of these when
the file lives somewhere else. They compose with any format adapter.

<div class="grid cards" markdown>

- :material-folder-cog: **[afero](how-to/afero.md)** — bridge an existing afero filesystem.
- :material-package-variant-closed: **[io/fs](how-to/iofs.md)** — an `embed.FS`, zip or tar, read-only.
- :material-source-branch: **[go-billy](how-to/billy.md)** — a go-git / go-billy filesystem, read and write.
- :material-server-network: **[SFTP](how-to/sftp.md)** — a config file on a remote host over SSH.
- :material-cloud-outline: **cloud object stores** — [S3](how-to/aws-s3.md), [GCS](how-to/gcp-gcs.md) &amp; [Azure Blob](how-to/azure-blob.md), read and write.

</div>

### Dynamic backends — remote systems as layers

Fetch configuration at runtime from a remote system and give it full precedence, provenance and
hot-reload, exactly as a file gets. The reference — [**config-consul**](how-to/consul.md) — and the
cloud **parameter stores** are released:

- **Parameter stores** — [AWS SSM](how-to/aws-ssm.md), [Azure App Configuration](how-to/azure-appconfig.md)
  and [GCP Parameter Manager](how-to/gcp-parameter.md), Consul's siblings.
- **Secrets managers** — [Vault](how-to/vault.md) and [AWS Secrets
  Manager](how-to/aws-secrets.md) are released and read-only; a value either provides can never be
  written into a plainer layer beneath, because the core refuses that. [Azure Key
  Vault](how-to/azure-keyvault.md) and [GCP Secret Manager](how-to/gcp-secret.md) are built and
  awaiting verification against the real services.
- **The OS keychain** — [config-keychain](how-to/keychain.md) makes macOS Keychain, Windows
  Credential Manager or Secret Service a layer, and is the one secrets backend that *writes*: a
  token this application just obtained belongs there rather than in the config file.
- **A directory of single-value files** — [config-filekv](how-to/filekv.md) reads a mounted
  Kubernetes ConfigMap, Docker secrets or systemd credentials, where each filename is a key. It
  adds no dependency at all.
- **Cloud-native key–value** *(roadmap)* — etcd and Kubernetes ConfigMaps, with native change-watch.

**[→ The full adapter ecosystem, with status and roadmap](explanation/adapters.md)**

## Should you use this?

**Yes, if any of these describe you** — and note that only the first is about writing:

- you have more than two sources and have lost an afternoon to "which one set this?";
- a long-running service reloads configuration, and you need reads that never straddle a
  reload, notifications you can trust, and a broken file that cannot take the service down;
- your program writes configuration back, and users have to live in the file afterwards;
- configuration lands in files that get committed, reviewed, or shared between people;
- you want configuration-dependent tests that run in parallel and do not reach for process
  globals;
- your settings have real types — enums, addresses, URLs — and you are tired of decoding
  them by hand.

**Probably not, if** one file and a couple of environment variables cover you, nothing is
ever written back, and nothing reloads. That is a large share of programs, and it is a
genuinely well-served case: [viper](https://github.com/spf13/viper) is battle-tested by an
enormous number of them, has an ecosystem this module does not, and will be a smaller
dependency in your graph. Reach for the smaller tool when the smaller tool fits — see
[History](about/history.md) for how much of what is here was learned from years of using
it.

## Where next

The documentation follows the [Diátaxis](https://diataxis.fr) quadrants: learn, then do,
then understand.

<div class="grid cards" markdown>

- :material-rocket-launch: **[Getting started](getting-started.md)** — the tutorial.
  Build a store over a file, read values, ask where they came from, write a change back,
  and react to one made outside your process.
- :material-file-tree: **[Load & merge](how-to/load-and-merge.md)** — files, embedded
  defaults, the environment, flags, and the precedence chain.
- :material-magnify: **[Read values](how-to/read-values.md)** — typed accessors, the
  generic `Value[T]`, scoped views and struct decoding.
- :material-content-save-edit: **[Write configuration](how-to/write-config.md)** —
  planning a write, where a change lands, conflicts, and shadowed writes.
- :material-code-braces: **[Typed sections](how-to/typed-sections.md)** — project a
  subtree onto your own struct with `UnmarshalSection` and `ObserveSection`.
- :material-reload: **[Hot-reload](how-to/hot-reload.md)** — `Watch`, observers, and
  reacting to foreign changes.
- :material-shield-check: **[Validate](how-to/validate-config.md)** — `Schema` and
  `ValidateStruct[T]` from `config:` struct tags.
- :material-puzzle: **[Write a custom backend](how-to/custom-backend.md)** — make Consul,
  a secrets manager or an HTTP endpoint an ordinary layer.
- :material-test-tube: **[Test with the mocks](how-to/test-with-mocks.md)** —
  `MockReader`, `MockBinder` and `MockObserved` in your tests.
- :material-lightbulb-on: **[The Store](explanation/the-store.md)** — why one component
  owns config I/O, and what follows from that rule.
- :material-map-marker-path: **[Provenance](explanation/provenance.md)** — what `Origin`,
  `Shadowed` and `Explain` can and cannot answer.

</div>

## Coming from v0.2.x

v0.3.0 is a breaking release. Two things changed: the Viper-backed container became the
`Store`, and `afero.Fs` became a six-method `config.FS` the module defines itself.

The **[migration guide](about/migrating.md)** covers both step by step — `Containable` becomes
`Reader`, the several constructors become `NewStore`, `afero.NewOsFs()` becomes
`config.OS()`, and the handful of call sites that are genuine ports rather than renames
each get their own section.

!!! warning "Two traps with no compiler error"
    **Watching is explicit now.** v0.2.x started a watcher inside every constructor, so you
    got hot-reload without a call site to port. Skip `Store.Watch(ctx)` and the code
    compiles, the tests pass, and configuration silently stops reloading.

    **`ApplyInitial` is not a required fix.** Section delivery has always been change-only,
    so nothing needs accommodating. Add it only if you *want* startup delivery, which
    v0.2.x could not do at all.

If your reusable packages declare their own one-method interface and take an
`ObservedSection[T]` structurally, they need no change at all. That decoupling boundary is
the reason the module exists, and it held.

For how the module got here, see [History](about/history.md).

## Reference

The Go API reference lives on
**[pkg.go.dev](https://pkg.go.dev/gitlab.com/phpboyscout/go/config)**, which is where a
language API reference belongs — generated from the source it documents, so it cannot
drift from it.

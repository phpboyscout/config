# Why a wrapper over Viper

[Viper](https://github.com/spf13/viper) is capable and ubiquitous, so "why not just use
Viper directly?" is the right question to ask of this module. This page is the answer:
the specific problems that pushed a wrapper into existence, and the traps it closes.
Every item here was paid for in production.

## Testability — the original reason

A bare `*viper.Viper` is a concrete struct. Code that takes one cannot be given a test
double, so tests are pushed toward writing real files, mutating process environment, or
threading globals — all of which make tests slow, order-dependent, and flaky.

`Containable` is an **interface**. Your packages take the interface; production passes a
real `*Container`; tests pass either a
[mock](../how-to/test-with-mocks.md) or a reader-backed container built from an
in-memory string. That single change is what makes config-dependent code ordinarily
testable.

The [`mocks`](../how-to/test-with-mocks.md) package is generated from the real
interfaces, so the doubles cannot drift from the contract and they verify expectations
on cleanup.

## Consistency across tools

Factory functions like `LoadFilesContainer` mean every tool follows the *same* rules for
finding, ordering, and merging config files, rather than each one re-implementing
discovery-and-merge slightly differently. The precedence chain is a property of the
module, not of each application.

## Filesystem abstraction

Every constructor takes an [afero](https://github.com/spf13/afero) `Fs`. The same code
loads from the real OS filesystem in production, an in-memory filesystem in tests, and
embedded assets for shipped defaults — through one interface, with no branching on
"am I under test?".

## Automatic environment mapping

Viper's `AutomaticEnv` alone does **not** map a dotted config key to a conventional
environment variable name — it ships no default key replacer, so `server.port` does not
find `SERVER_PORT` until you install one. This module wires
`AutomaticEnv` **and** the `.`→`_` replacer for you, so the mapping every user expects
just works.

`WithEnvPrefix` then scopes those lookups. Its purpose is collision avoidance: when
several tools share one host, generic names like `LOG_LEVEL` or `AI_PROVIDER` would
otherwise leak between them. With a prefix, `ai.provider` resolves from
`MYTOOL_AI_PROVIDER` instead of the shared `AI_PROVIDER`.

## The `Sub()` trap

This one is subtle and bites hard. Viper's native `Sub()` returns a **detached snapshot**
of a subtree: a new `*viper.Viper` holding a point-in-time copy of that data. Later
root-level `Set` calls, file reloads, and the wider precedence chain do not propagate
into it, and a write-back targets the copy rather than the live configuration. Code that
passes `cfg.Sub("database")` into a resolver is therefore quietly reading stale, partially
resolved values.

`Container.Sub()` avoids the trap. The returned view:

1. keeps a **structural view** of the subtree — used by `WriteConfigAs`, `Dump`,
   `ToJSON`, and `Validate` so those stay scoped to the sub-path; and
2. tracks the **root container** plus an accumulated dot-prefix, routing every `Get*`,
   `Set`, `Has`, and `IsSet` through the root with a fully-qualified key.

So root-level writes, hot reloads, and the full precedence chain — including
`AutomaticEnv` and prefix binding — stay live no matter how many `Sub()` layers you walk:

```go
github := cfg.Sub("github")
github.GetString("auth.value")   // resolves MYTOOL_GITHUB_AUTH_VALUE via AutomaticEnv

bitbucket := cfg.Sub("bitbucket")
auth := bitbucket.Sub("auth")
auth.GetString("token")          // qualifies to "bitbucket.auth.token"
```

`Sub()` still returns `nil` when the key is absent from the whole hierarchy, so
`if sub != nil` guards behave as expected.

## The flag default-clobber footgun

Binding a CLI flag to a config key with Viper's `BindPFlag` binds it *whether or not the
user set it*. A flag sitting at its default therefore silently **masks** file and
environment values — the config file says `port: 9090`, nobody passed `--port`, and the
effective value is the flag default `8080`.

The rule this module teaches is: **bind only flags the user actually changed**
(`flag.Changed`). See [Bind CLI flags](../how-to/bind-cli-flags.md). If you call
`BindPFlag` directly, that filtering is *your* responsibility.

## Typed sections are a decoupling boundary

The wrapper's most architectural feature is the one that lets packages *not* depend on
it. A reusable package should own a settings struct and accept it — or accept a tiny,
locally-declared interface — rather than accept a config container:

```go
// in the reusable package: no config import at all
type SettingsSource interface {
	Current() *ServerSettings
}
```

`*config.ObservedSection[ServerSettings]` satisfies that shape structurally, so wiring
code binds the section and hands it over while the package stays dependency-free. The
companion convention is to keep all container coupling in a single adapter file per
package, so the package core never imports config at all.

This is not theoretical: it is how the phpboyscout Go toolkit modules were decoupled
*before* they were extracted into standalone modules. See
[Use typed sections](../how-to/typed-sections.md#stay-extractable-depend-on-a-tiny-local-interface).

## Decentralised validation

There is deliberately **no global schema**. Each package validates its own slice of the
config with its own struct tags. A single central schema would have to know which
features are active in a given build and would couple otherwise-independent packages to
one another. See [Validate configuration](../how-to/validate-config.md).

## The escape hatch is always open

The wrapper narrows Viper's surface to what everyday code needs; it does not imprison
you. `GetViper()` returns the underlying `*viper.Viper` for the operations the interface
does not cover (custom flag sets, `BindEnv`, `AllSettings` introspection). Reaching for
it is sanctioned — it just shouldn't be the default path.

## Related

- [Precedence & merge model](precedence-and-merge.md)
- [Hot-reload safety](hot-reload-safety.md)
- [Test with the config mock](../how-to/test-with-mocks.md)

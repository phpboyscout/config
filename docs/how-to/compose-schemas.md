---
title: Compose schemas from several components
description: Mount each component's schema where it lives, validate the merged result against all of them at once, and bound what a source may contribute.
tags: [how-to, validation, schema, secrets]
---

# Compose schemas from several components

[Validate configuration](validate-config.md) covers one schema for one application. This
covers the other shape: several components — plugins, subsystems, libraries — that each
know the configuration *they* need and none of which knows the whole.

Two surfaces do it, and they answer different questions:

| Surface | Question | Reported as |
|---|---|---|
| `WithSchemaAt` | Is the merged configuration valid? | An aggregate result, one entry per violation |
| `Constrained` | Is this *source* allowed to supply this? | The same result, attributed to the source |

## Mount a component's schema where it lives

A component declares its configuration in its own terms — `enabled`, `ttl` — and the code
assembling the store decides where that lives:

```go
store, err := config.NewStore(ctx,
    config.WithFiles(config.OS(), "/etc/mytool/config.yaml"),
    config.WithEnv("MYTOOL"),
    config.WithSchemaAt("plugins.cache", cacheSchema),
    config.WithSchemaAt("server", serverSchema, config.Required),
)
```

The component never hard-codes its own location, so the same schema can be mounted twice
at different points:

```go
config.WithSchemaAt("plugins.primary", cacheSchema),
config.WithSchemaAt("plugins.secondary", cacheSchema),
```

Failures come back at the full path — `plugins.secondary.ttl`, not `ttl` — because a
relative key is not one anybody could go and edit.

An empty prefix mounts against the whole configuration, which is what you want for a
schema whose paths are already absolute.

## Mark a section required, or it says nothing when it is missing

A schema constrains a key only when that key is *present*. So a component mounted at
`plugins.cache` with no `plugins.cache` section at all raises nothing — its required
keys never fire, because there is no object for them to be required in.

That is exactly right for a plugin nobody enabled, and exactly wrong for one that is
mandatory. `config.Required` says which:

```go
config.WithSchemaAt("server", serverSchema, config.Required)
```

```text
server    required configuration section is missing    [server-component]
```

It is the contribution's choice rather than the composer's, because the component is what
knows whether it can run without configuration.

## Ask what is wrong, whenever you like

Validation runs at load and is fail-closed — a configuration violating a mounted schema
never becomes live. `Store.Validate` asks the same question again on demand:

```go
res := store.Validate()
for _, e := range res.Errors {
    fmt.Printf("%s: %s [%s]\n", e.Key, e.Message, e.Contributor)
}
```

```text
server.port: maximum: got 99999, want 65535 [server-component]
plugins.cache.ttl: minimum: got 0, want 1 [cache-plugin]
telemetry.level: value must be one of 'off', 'basic', 'full' [telemetry-component]
```

Pass paths to scope it, so a component can ask about its own branch without hearing about
anybody else's:

```go
res := store.Validate("plugins.cache")
```

### Every failure names who raised it

`Contributor` is what makes an aggregate report actionable — three components objecting at
once is noise until you can see whose expectation each one violated.

A schema implementing `Name() string` is used for it. One that does not is attributed to
its mount point, which is still something a reader can act on. Either way you never get an
anonymous complaint, and an implementation that sets `Contributor` itself keeps what it
set — a schema composing several documents knows which one objected, and the mount does
not.

## Bound what a source may contribute

`Constrained` is about *provenance* rather than shape: this value may be fine, but not
from here.

```go
config.WithBackend(config.Constrained(
    config.NewFileBackend(fsys, "app.yaml"),
    config.Forbid("credentials.*"),
    config.ConstraintName("project-config-file"),
))
```

```text
credentials.token: supplied by a source that is forbidden to carry it [project-config-file]
```

Patterns are the same glob shape [`Deny`](filter-a-backend.md#what-the-patterns-mean)
uses, so there is no second syntax to learn.

!!! warning "This is not `Filtered`, and reaching for the wrong one costs you the case that matters most"

    [`Filtered`](filter-a-backend.md) bounds **visibility**: a denied key is simply
    absent, reads fall through to a lower layer, and nothing is said.

    `Constrained` bounds **policy**: a forbidden key stays visible and is *reported*.

    A literal credential checked into a file, sitting beneath a working environment
    reference, is a leak. Filtering it out removes it from the store and the leak is
    never reported by anything — the configuration looks fine because the evidence was
    discarded. Filtering hides; constraining tells.

### Writes to a forbidden key are refused

```go
_, err := store.Apply(ctx, config.Set("credentials.token", secret))
// config: forbidden key: "credentials.token" may not be written to source "app.yaml"
```

Match it with `errors.Is(err, config.ErrForbiddenKey)`. This is deliberately the opposite
of `Filtered`, where a write to a denied key routes *past* the backend and lands
somewhere else: a forbidden credential that quietly rerouted would be the same leak in a
different file, with nothing said about it.

### Check the shape of what a source hands back

`MustMatch` validates only the keys the source actually carries:

```go
config.WithBackend(config.Constrained(remoteStore,
    config.MustMatch(shapeSchema)))
```

A layer is never required to be complete — a base file legitimately omits what an overlay
supplies — so a schema's `required` is *not* enforced here, and a key this source does not
carry is somebody else's problem. What it catches is a source supplying a value of the
wrong shape, at the layer that produced it, before the merge has hidden which one that
was.

## Where the schemas come from

`config` defines what validating means and takes no position on how a schema is written.
`config.NewSchema` builds one from struct tags; for JSON Schema, add
[`config-schema`](https://gitlab.com/phpboyscout/go/config-schema):

```go
cacheSchema, err := configschema.FromJSON("cache-plugin", cacheDoc)
```

It stays a separate module on purpose. Twenty-five adapters depend on `config` and each
pins its dependency footprint, so a JSON Schema library linked into the core would widen
every one of them for a capability most do not use.

Both satisfy the same `config.Schema` interface, so they mount identically and mix freely
in one store.

## Related

- [Validate configuration](validate-config.md) — one schema, struct tags, and write-time validation
- [Bound what a backend contributes](filter-a-backend.md) — `Filtered`, and when to reach for it instead
- [Errors](../reference/errors.md#errforbiddenkey) — `ErrForbiddenKey` and `ErrInvalidConfig` in full
- [Hot-reload safety](../explanation/hot-reload-safety.md) — why a rejected reload changes nothing

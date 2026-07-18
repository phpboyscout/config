# Precedence & merge model

This page explains *how a value is resolved* — which source wins when several define
the same key, and how nested structures merge. Understanding it means you can predict
what `cfg.GetString("server.host")` returns without tracing the code.

## The precedence chain

Every accessor resolves through Viper's precedence. Highest wins:

| Rank | Source | Set by |
|------|--------|--------|
| 1 | **Explicit `Set` / bound CLI flag** | `cfg.Set(...)`, `cfg.BindPFlag(...)` on a changed flag |
| 2 | **Environment variable** | automatic env binding (`server.port` ← `SERVER_PORT`) |
| 3 | **File config** | the merged config files |
| 4 | **Embedded defaults** | `LoadEmbed` readers |
| 5 | **Struct `default:` tag** | applied during unmarshal/validation |

So a `--port` flag beats `SERVER_PORT`, which beats the file, which beats the
compiled-in default. This ordering is deliberate: the more explicit and
closer-to-invocation a source is, the higher it sits.

## Environment binding

Env binding is automatic and mechanical: a dotted config key maps to an
`UPPER_SNAKE` environment variable. `server.port` reads `SERVER_PORT`;
`log.level` reads `LOG_LEVEL`. This is why the same setup works unchanged in local
dev and in CI/containers — the platform injects env vars and they override the file
without touching it.

Viper does not do this on its own: `AutomaticEnv` ships **no default key replacer**, so
`server.port` finds nothing until one is installed. The container wires `AutomaticEnv`
*and* the `.`→`_` replacer for you.

### The env prefix is a security control

`WithEnvPrefix("MYTOOL")` makes `server.port` resolve from `MYTOOL_SERVER_PORT` instead
of the bare `SERVER_PORT`. This is not merely tidiness — it closes a **config-pollution**
hole. With unprefixed automatic binding, *any* environment variable matching a config key
can silently override configuration. On a shared CI runner, a container host, or a
multi-tenant box, an unrelated process setting `AI_PROVIDER` or `LOG_LEVEL` would change
the behaviour of every tool running there.

The guarantee: **when a prefix is set, unprefixed environment variables never override
config.** An empty prefix means no prefix, preserving the default behaviour.

Pass the prefix *without* a trailing underscore (`"MYTOOL"`, not `"MYTOOL_"`) — one is
added for you; passing your own produces a double underscore. The prefix is applied
before the key replacer, so `ai.provider` becomes `MYTOOL_AI_PROVIDER`.

The container is deliberately permissive about the prefix string: **format policy belongs
to the caller**, validated at input time, not baked into the container.

!!! note "Never log a config value"
    When recording that an env var overrode a key, log the **key** and the **env-var
    name** — never the value. That rule applies to every key, not just ones that look
    like credentials: defence in depth.

### `.env` for local development

Every container also loads a `.env` file from the working directory if one is present, so
local API keys and tokens can live outside your config file without any extra wiring.

## Nested access with `Sub()`

`Sub("database")` returns a `Containable` scoped to that subtree, and nesting accumulates
(`cfg.Sub("bitbucket").Sub("auth")` qualifies keys to `bitbucket.auth.*`). Crucially — and
unlike Viper's own `Sub` — the returned view stays **live**: root-level writes, hot
reloads, and the full precedence chain including env binding all keep working through it.
That distinction is explained in [Why a wrapper](why-a-wrapper.md#the-sub-trap).

`Sub()` returns `nil` when the key is absent from the whole hierarchy, so `if sub != nil`
guards behave as you'd expect.

## Which files actually contributed?

`ConfigFiles()` returns the ordered list of files that fed the live configuration, lowest
to highest precedence. It exists because Viper's own `ConfigFileUsed()` reports only the
**last** file read, which is misleading for a merged configuration — and "where does this
value actually come from?" is the first question anyone debugging config asks. The slice
is a copy; reader/embedded containers return an empty one, and a sub-container reports the
root's files (the file set is a property of the configuration as a whole).

## File merge (not replace)

When you load several files, they **deep-merge** rather than replace. Later files win
for the keys they set; keys they don't mention are inherited from earlier files:

```yaml
# base.yaml
server:
  http:  { port: 8080, tls: { enabled: false } }
  grpc:  { port: 50051 }
```
```yaml
# overlay.yaml
server:
  http:  { tls: { enabled: true, cert: /etc/cert.pem } }
```

The merged result keeps `server.http.port: 8080` and `server.grpc.port: 50051` from
the base while applying `server.http.tls.enabled: true` and the new `cert` from the
overlay. Merging is per-leaf-key, deep into nested maps — an overlay never has to
restate the whole subtree to change one value.

The **first** file is the base and is required; later files are optional overlays.
A missing overlay is skipped; a malformed overlay is logged as a warning and skipped,
so one bad optional file doesn't sink a valid base.

## Why an interface over raw Viper

Viper is powerful but its full surface is large and easy to misuse, and depending on
it directly couples every package to it. `Containable` exposes the small, typed subset
that everyday code needs — accessors, sections, validation, observers — so your code
is easy to mock and hard to misuse. When you genuinely need Viper's full API,
`GetViper()` is the deliberate escape hatch.

## Related

- [Load & merge configuration](../how-to/load-and-merge.md)
- [Bind CLI flags](../how-to/bind-cli-flags.md)
- [Hot-reload safety](hot-reload-safety.md)

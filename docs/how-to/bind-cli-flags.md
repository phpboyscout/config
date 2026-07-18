# Bind CLI flags

A command-line flag should be able to override a configured value — but only when the
user actually set it. `Containable.BindPFlag` wires a [pflag](https://github.com/spf13/pflag)
into the container's precedence chain, above env vars and file config.

!!! danger "The default-clobber footgun"
    Viper's `BindPFlag` binds a flag **whether or not the user set it**. A flag sitting
    at its default therefore silently **masks** your file and environment values: the
    config says `port: 9090`, nobody passed `--port`, and the effective value is the
    flag's default `8080`.

    **Bind only flags the user actually changed** (`flag.Changed`). When you call
    `BindPFlag` directly, that filtering is *your* responsibility — the rule below is
    not optional.

## Bind a changed flag

Bind the flags the user explicitly changed, so a flag left at its default does not
clobber a configured value:

```go
func run(cmd *cobra.Command, cfg config.Containable) error {
	// Bind only flags the user set on the command line.
	cmd.Flags().Visit(func(f *pflag.Flag) {
		_ = cfg.BindPFlag(configKeyFor(f.Name), f)
	})

	// Now the flag value wins over env and file config for that key.
	port := cfg.GetInt("server.port")
	_ = port
	return nil
}
```

`Flags().Visit` iterates only the flags that were changed (unlike `VisitAll`), which
is exactly the set you want to bind — this is the recommended pattern.

## Bind a single flag

For a one-off:

```go
if f := cmd.Flags().Lookup("port"); f != nil && f.Changed {
	_ = cfg.BindPFlag("server.port", f)
}
```

## Where flags sit in precedence

A bound, changed flag sits at the **top** of the precedence chain:

1. **Bound CLI flag** (this page) / explicit `Set`
2. environment variable
3. file config
4. embedded defaults
5. struct `default:` tags

So `--port 9090` overrides `SERVER_PORT`, which overrides the file — see
[Precedence & merge model](../explanation/precedence-and-merge.md).

## Binding works on sub-containers

`BindPFlag` routes through the same qualifying resolver as the typed accessors, so
binding onto a `Sub()` view qualifies the key correctly and keeps env-aware delegation
intact.

## Naming convention

A common convention maps a flag name to a config key by replacing hyphens with dots
(`--server-port` → `server.port`). Avoid dots *in* flag names — a flag literally named
`--a.b` maps verbatim to the key `a.b`, which is rarely what anyone intends.

Treat a failed bind as non-fatal: log it and continue, so one bad flag never aborts
startup.

## Escape hatch

For advanced Viper operations not on the `Containable` interface (custom flag sets,
`BindEnv`, and the like), `cfg.GetViper()` returns the underlying `*viper.Viper`.
Prefer the typed accessors and `BindPFlag` for everyday use.

## Related

- [Load & merge configuration](load-and-merge.md)
- [Precedence & merge model](../explanation/precedence-and-merge.md)

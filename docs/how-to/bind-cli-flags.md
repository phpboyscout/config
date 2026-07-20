# Bind CLI flags

A command-line flag should be able to override a configured value — but only when the
user actually set it. `WithFlags` contributes a [pflag](https://github.com/spf13/pflag)
flag set to the `Store` as an ordinary layer, and it contributes **only the flags the
user changed**.

!!! note "Imports used below"
    ```go
    import (
        "context"
        "testing"

        "github.com/spf13/pflag"
        "github.com/stretchr/testify/assert"
        "github.com/stretchr/testify/require"

        "gitlab.com/phpboyscout/go/config"
    )
    ```

## Add the flag set

Add it last. Precedence is the order sources are added, and an explicit flag is the most
deliberate input a user can give:

```go
// cmd is a *cobra.Command here, but nothing requires cobra: WithFlags takes a
// *pflag.FlagSet, so any source of one will do — see the testing recipe below.
store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/mytool/config.yaml"),
	config.WithEnv("MYTOOL"),
	config.WithFlags(cmd.Flags()),
)
if err != nil {
	return err
}
```

Parse the flags before the store loads them. A flag set that has not been parsed has no
changed flags, so the layer contributes nothing.

## A flag left at its default cannot clobber anything

This is the point of the whole layer, so it is worth stating plainly.

The flag backend walks the set with `pflag.FlagSet.Visit`, which iterates only the flags
that were actually changed on the command line. A flag sitting at its compiled-in default
therefore contributes **no value at all** — it does not appear in the layer, it does not
appear in provenance, and it cannot outrank your file.

The alternative, binding every flag regardless, is a well-known trap: the file says
`port: 9090`, nobody passed `--port`, and the effective value is the flag default of
`8080`. The user has no way to discover why their configuration is being ignored, because
nothing in the file or the environment explains it.

Under the previous design that filtering was the caller's discipline — you were expected
to call `Visit` yourself and bind what it gave you, and forgetting was silent. It is now
structural: there is no way to ask the store to contribute an unchanged flag.

## Map a flag name to a configuration key

By default a flag name becomes a configuration key by replacing dashes with dots, so
`--log-level` supplies `log.level` and `--server-port` supplies `server.port`. When the
two genuinely differ, name the mapping with `BindFlag`:

```go
store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "config.yaml"),
	config.WithFlags(cmd.Flags(),
		config.BindFlag("port", "server.port"),
		config.BindFlag("verbose", "log.level"),
	),
)
```

`BindFlag` takes the flag name **without** its leading dashes. Flags you do not mention
still contribute under the derived key; `BindFlag` is an override, not an allow-list.

To keep a flag out of configuration altogether — an operational switch like `--verbose`
or `--output` that is nobody's setting — bind it to the empty key:

```go
config.BindFlag("output", "")   // parsed as a flag, never a configuration value
```

Avoid dots inside flag names. A flag literally named `--a.b` maps verbatim to the key
`a.b`, which is rarely what anyone intends and is impossible to tell apart from the
derived form afterwards. Configuration keys are lower-cased, so a flag name that differs
only in case will not give you a distinct key.

## Check that a flag really is winning

A flag is a layer like any other, so provenance names it:

```go
view := store.View()

if src, ok := view.Origin("server.port"); ok && src.Kind == config.SourceFlag {
	fmt.Println("server.port was set by", src.Name) // --port
}

fmt.Println(view.Explain("server.port"))
// server.port = 9090 (from flag:--port); also defined in /etc/mytool/config.yaml
```

That is the quickest answer to "did my flag take effect?", and — more usefully — to "why
is my config file being ignored?". See [Provenance](../explanation/provenance.md).

## Flag values arrive as strings

The flag layer contributes each changed flag's string form, whatever the flag's Go type.
For reading that is invisible: `GetInt`, `GetBool`, `GetDuration` and friends coerce, so
`--port 9090` reads back as the integer `9090`.

Repeatable flags are the exception, and deliberately so. A `StringSlice` flag renders as
`[a,b]` in string form, and storing that would give one garbage value rather than the two
the user passed — so slice flags contribute a list, and `--tag a --tag b` reads back
through `GetStringSlice` as `["a", "b"]`.

It is visible to **schema validation**, which checks the resolved value's actual type. A
field declared `int` in a schema struct will fail its type check when the winning value
came from a scalar flag or an environment variable, because both hold strings:

```
server.port: expected type int but got string (hint: ensure server.port has a value of type int)
```

If a key is routinely supplied by flag or environment, either leave it out of the typed
schema or declare it as a string. See [Validate configuration](validate-config.md).

## Flags are never a write target

Flags are readable but not writable — a flag lives for one invocation, so persisting to
one is meaningless. Write routing skips the flag layer entirely and lands the change in
the highest-precedence writable layer instead.

That has a consequence worth surfacing to users: a write to a key the user also passed on
the command line succeeds, writes the file, and changes nothing about the resolved
configuration, because the flag still wins. `Operation.Effective()` reports exactly that:

```go
plan, err := store.Plan(config.Set("server.port", 9090))
if err != nil {
	return err
}

for _, op := range plan.Operations {
	if !op.Effective() {
		fmt.Printf("%s written to %s, but %s still wins for this run\n",
			op.Change.Path, op.Target, op.ShadowedBy[len(op.ShadowedBy)-1])
	}
}
```

See [Write configuration](write-config.md) for the full routing story.

## Testing without a command

You do not need cobra, or a real command line, to test flag precedence. Build a
`pflag.FlagSet`, parse an argument slice into it, and hand it to the store:

```go
func TestFlagBeatsFile(t *testing.T) {
	fsys, err := config.Dir(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, fsys.WriteFile("/config.yaml",
		[]byte("server:\n  port: 8080\n"), 0o600))

	flags := pflag.NewFlagSet("mytool", pflag.ContinueOnError)
	flags.Int("port", 8080, "listen port")

	require.NoError(t, flags.Parse([]string{"--port", "9090"}))

	store, err := config.NewStore(context.Background(),
		config.WithFiles(fsys, "/config.yaml"),
		config.WithFlags(flags, config.BindFlag("port", "server.port")),
	)
	require.NoError(t, err)

	assert.Equal(t, 9090, store.View().GetInt("server.port"))
}
```

Parse nothing and the assertion flips: the file's value stands, because an unchanged flag
contributes nothing.

## Related

- [Load & merge configuration](load-and-merge.md) — building a store and ordering sources
- [Precedence & merge model](../explanation/precedence-and-merge.md) — how layers fold together
- [Provenance](../explanation/provenance.md) — asking which layer supplied a value
- [Write configuration](write-config.md) — why a flag can shadow a successful write

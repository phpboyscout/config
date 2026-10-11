---
title: Bind CLI flags
description: Let a CLI flag override a configured value, but only when the flag was actually set.
tags: [how-to, flags, cli]
---

# Bind CLI flags

A command-line flag should override a configured value, but only when the user actually
set it. `WithFlags` contributes a [pflag](https://github.com/spf13/pflag) flag set to the
`Store` as an ordinary layer, and it contributes **only the flags the user changed**.

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
// *pflag.FlagSet, so any source of one will do. See the testing recipe below.
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

The flag backend walks the set with `pflag.FlagSet.Visit`, which iterates only the flags
that were actually changed on the command line. A flag sitting at its compiled-in default
therefore contributes **no value at all**. It does not appear in the layer, it does not
appear in provenance, and it cannot outrank your file.

Binding every flag regardless is a well-known trap: the file says `port: 9090`, nobody
passed `--port`, and the effective value is the flag default of `8080`. The user has no
way to discover why their configuration is being ignored, because nothing in the file or
the environment explains it. Here there is no way to ask the store to contribute an
unchanged flag.

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

To keep a flag out of configuration altogether, such as an operational switch like
`--output` that is nobody's setting, bind it to the empty key:

```go
config.BindFlag("output", "")   // parsed as a flag, never a configuration value
```

Avoid dots inside flag names. A flag literally named `--a.b` maps verbatim to the key
`a.b`, which is rarely what anyone intends and cannot be told apart from the derived form
afterwards. Configuration keys are lower-cased, so a flag name that differs only in case
will not give you a distinct key.

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

That is the quickest answer to "did my flag take effect?" and to "why is my config file
being ignored?". See [Provenance](../explanation/provenance.md).

## Flag values arrive as strings

The flag layer contributes each changed flag's string form, whatever the flag's Go type.
For reading that is invisible: `GetInt`, `GetBool`, `GetDuration` and friends coerce, so
`--port 9090` reads back as the integer `9090`.

Repeatable flags are the exception. A `StringSlice` flag renders as `[a,b]` in string
form, and storing that would give one garbage value rather than the two the user passed.
Slice flags therefore contribute a list, and `--tag a --tag b` reads back through
`GetStringSlice` as `["a", "b"]`.

**Schema validation** accepts it for the same reason. A field declared `int`, `float64`, `bool`
or `duration` passes when the winning value is text that reads as that type, so `--port 9090`
satisfies an `int` field. Text that does not, such as `--port abc`, fails:

```
server.port: expected type int but got string (hint: ensure server.port has a value of type int)
```

A field declared `string` accepts only a string. See
[Validate configuration](validate-config.md#the-type-check-reads-text-the-way-the-accessors-do).

## Flags are never a write target

Flags are readable but not writable, since a flag lives for one invocation. Write routing
skips the flag layer and lands the change in the highest-precedence writable layer
instead.

So a write to a key the user also passed on the command line succeeds, writes the file,
and changes nothing about the resolved configuration, because the flag still wins.
`Operation.Effective()` reports exactly that:

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
	require.NoError(t, fsys.WriteFile("config.yaml",
		[]byte("server:\n  port: 8080\n"), 0o600))

	flags := pflag.NewFlagSet("mytool", pflag.ContinueOnError)
	flags.Int("port", 8080, "listen port")

	require.NoError(t, flags.Parse([]string{"--port", "9090"}))

	store, err := config.NewStore(context.Background(),
		config.WithFiles(fsys, "config.yaml"),
		config.WithFlags(flags, config.BindFlag("port", "server.port")),
	)
	require.NoError(t, err)

	assert.Equal(t, 9090, store.View().GetInt("server.port"))
}
```

Parse nothing and the assertion flips: the file's value stands, because an unchanged flag
contributes nothing.

## Related

- [Load & merge configuration](load-and-merge.md): building a store and ordering sources
- [Precedence & merge model](../explanation/precedence-and-merge.md): how layers fold together
- [Provenance](../explanation/provenance.md): asking which layer supplied a value
- [Write configuration](write-config.md): why a flag can shadow a successful write

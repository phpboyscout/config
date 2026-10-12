---
title: Read and write HCL
description: Read and write HCL configuration as a layer through the config-hcl sibling module.
tags: [how-to, formats, hcl]
---

# Read and write HCL

HCL files become a config layer through [`config-hcl`](https://gitlab.com/phpboyscout/go/config-hcl),
a sibling module: the core's own codec is YAML, so you take this one only if you need HCL. Writes
edit the file in place and keep its comments.

```bash
go get gitlab.com/phpboyscout/go/config-hcl
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	confighcl "gitlab.com/phpboyscout/go/config-hcl"
)

store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/app.yaml"),               // real files outrank them
	config.WithBackend(confighcl.New(config.OS(), "/etc/app.hcl")), // outranks it
)
if err != nil {
	return err
}

host := store.View().GetString("server.host") // "localhost"
port := store.View().GetInt("server.port")    // 8080
```

with `/etc/app.hcl` containing:

```hcl
server {
  host = "localhost"
  port = 8080
}
```

## Map HCL to keys

A block's type and labels become dotted-key segments, and its attributes nest beneath them:

```hcl
migration "multi_state" "move_redis" {
  enabled = true
}
```

```go
enabled := store.View().GetBool("migration.multi_state.move_redis.enabled")
```

Two blocks sharing a type and every label collide, which is an error rather than
last-one-wins. Repeated unlabelled blocks of one type are refused as well, since they have no
dotted-key representation, and so are a name used for both an attribute and a block type, and
labelled and unlabelled blocks of the same type. All fail with `config.ErrBackendUnsafe`.

Numbers decode as `float64`, and the read path casts them, so `GetInt` returns `8080`.

### HCL as a configuration format, not Terraform

This module serves HCL the way Nomad, Consul, Vault, Packer and purpose-built HCL DSLs use it,
and **deliberately not Terraform**. A document that reaches outside itself for a value (a
`var.*` reference, `${local.x}` interpolation, or a function call) is **refused at load** with
`config.ErrBackendUnsafe`, which names the file and the offending attribute:

```hcl
port = var.base_port + 1   # refused: no correct answer without a variable context
```

There is no correct answer without a variable context that a configuration library does not
have and should not invent. A value computed from a half-context is worse than an error because
it looks right. Point the adapter at a `.tf` file and it says so, rather than silently dropping
the configuration it could not understand. JSON-encoded HCL (`.hcl.json`) is refused too, since
the adapter reads native HCL syntax only.

## Write back to the file

Because every accepted document is expression-free, HCL is exactly as safe to edit as YAML.
Writing uses HashiCorp's `hclwrite` for a targeted edit that preserves comments, block order
and formatting, changing only the value asked for. The backend reports this through
`PreservesComments` in its `Capabilities`. `Set("server.port", 9090)` on

```hcl
# public listener port, needs a firewall change
server {
  host = "localhost" # dev only
  port = 8080
}
```

changes `port` to `9090` and leaves every comment and the block untouched.

What you can do:

- Set or remove a top-level attribute, or an attribute in a block that already exists. A new
  attribute is appended to its block.
- Set a string, bool, integer, float, `[]string` or nil.

Writing into a sub-path of an object-valued attribute, or creating a new nested block from a
dotted path, is refused with `config.ErrInvalidTarget` rather than guessed. Which layer a change
lands in is the core's decision, not this adapter's. See
[where a change lands](write-config.md#where-a-change-lands).

## Watch for changes

An HCL file is watched like any file-backed source: native filesystem notification where it works,
polling where it does not, at the interval you pass to `Store.Watch` with
`config.WithPollInterval` (two seconds by default). See [hot-reload](hot-reload.md).

## Test with a string

`Codec` is exported, so decoding or editing a string needs no store and no filesystem:

```go
docs, err := confighcl.Codec{}.Decode("app.hcl", []byte("server {\n  port = 8080\n}\n"))
// docs[0]["server"].(map[string]any)["port"] == float64(8080)
```

To test through a store, point `config.Dir(t.TempDir())` at a file you write first.
[Support a new file format](format-adapter.md) covers the conformance suite the adapter itself runs.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `config.ErrBackendParse` | The file is not valid HCL syntax. It names the file. | Fix the file. |
| `config.ErrBackendUnsafe` | At load: an attribute needs a variable, function or interpolation; the file is JSON-encoded HCL; or blocks collide as described under [Map HCL to keys](#map-hcl-to-keys). | Write the value literally, or rename the colliding block. |
| `config.ErrBackendUnsafe` | On a write: the value is not a string, bool, integer, float, `[]string` or nil, a map for example. | Set the leaf keys one by one. |
| `config.ErrInvalidTarget` | The write is into an object-valued attribute, or needs a block that does not exist yet. | Write the object as a block (`x { a = 1 }`), whose attributes can be set, or add the block to the file first. |

## When you don't need this

If you pick the format, YAML costs nothing extra and the core reads it.

Reach for HCL when the file is shared with tooling that already speaks it, so your
configuration sits alongside the rest of a stack rather than translating it.

## What it costs

| | |
|---|---|
| Adds | hclparse, hclwrite and cty, on top of the `config` graph |

This is the heaviest format adapter. It adds HashiCorp's HCL toolchain (`hashicorp/hcl/v2`,
`zclconf/go-cty`) and what those bring to the config graph. An allowlist test in the module
states the full set. There is no filesystem library, because you supply the `config.FS`.

## Related

- [Support a new file format](format-adapter.md): how an adapter like this is built and tested
- [What survives a write](../explanation/write-fidelity.md): the preservation contract HCL is
  held to

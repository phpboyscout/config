---
title: Read and write HCL
description: Read and write HCL configuration as a layer through the config-hcl sibling module.
tags: [how-to, formats, hcl]
---

# Read and write HCL

The core's own codec is YAML. HCL comes from a sibling module,
[`config-hcl`](https://gitlab.com/phpboyscout/go/config-hcl).

```bash
go get gitlab.com/phpboyscout/go/config-hcl
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	confighcl "gitlab.com/phpboyscout/go/config-hcl"
)

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                    // YAML, the default
	config.WithBackend(confighcl.New(fsys, "/etc/app.hcl")),    // outranks it
)
```

## When you do *not* need this

If you pick the format, YAML costs nothing extra and the core reads it.

Reach for HCL when the file is shared with tooling that already speaks it, so your
configuration sits alongside the rest of a stack rather than translating it.

## HCL as a configuration format, not Terraform

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

## Blocks and labels are path segments

A block's type and labels become dotted-key segments:

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
dotted-key representation. Both fail with `config.ErrBackendUnsafe`.

## Writing preserves the file

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

Setting or removing a block attribute or a top-level attribute is supported. Writing into a
sub-path of an object-valued attribute, or creating a new nested block from a dotted path, is
refused with `config.ErrInvalidTarget` rather than guessed. A `Set` can carry a string, bool,
integer, float, `[]string` or nil; any other value type fails with `config.ErrBackendUnsafe`.

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

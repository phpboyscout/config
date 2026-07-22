---
title: Read and write HCL
description: Read and write HCL configuration as a layer through the config-hcl sibling module.
tags: [how-to, formats, hcl]
---

# Read and write HCL

The core reads and writes only YAML. HCL comes from a sibling module,
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

## HCL as a configuration format — not Terraform

This module serves HCL the way Nomad, Consul, Vault, Packer and purpose-built HCL DSLs use it,
and **deliberately not Terraform**. A document that reaches outside itself for a value — a
`var.*` reference, `${local.x}` interpolation, or a function call — is **refused at load** with
`config.ErrBackendUnsafe` naming the construct:

```hcl
port = var.base_port + 1   # refused: no correct answer without a variable context
```

There is no correct answer without a variable context a configuration library does not have and
should not invent, and a value computed from a half-context is worse than an error because it
looks right. Point it at a `.tf` file and it says so, rather than silently dropping the
configuration it could not understand.

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

Two blocks sharing a type and every label collide (an error, not last-one-wins), and repeated
unlabelled blocks of one type are refused — they have no dotted-key representation.

## Writing preserves the file

Because every accepted document is expression-free, HCL is exactly as safe to edit as YAML.
Writing is HashiCorp's `hclwrite` — a targeted edit that preserves comments, block order and
formatting, changing only the value asked for. `Set("server.port", 9090)` on

```hcl
# public listener port — needs a firewall change
server {
  host = "localhost" # dev only
  port = 8080
}
```

changes `port` to `9090` and leaves every comment and the block untouched. Setting or removing a
block attribute or a top-level attribute is supported; writing into a sub-path of an
object-valued attribute, or creating a new nested block from a dotted path, is refused rather
than guessed.

## What it costs

This is the heaviest adapter: the config graph plus HashiCorp's HCL toolchain (`hashicorp/hcl/v2`,
`zclconf/go-cty`) and what those bring. An allowlist test in the module states the full set. No
filesystem library: you supply the `config.FS`.

## Related

- [Support a new file format](format-adapter.md) — how an adapter like this is built and tested
- [What survives a write](../explanation/write-fidelity.md) — the preservation contract HCL is
  held to

---
title: Read AWS SSM Parameter Store
description: Make AWS Systems Manager Parameter Store a config layer with config-aws-ssm — value types, why it is read-only, how SecureString values stay protected, and the polling cadence.
tags: [how-to, adapters, backends, aws, ssm]
---

# Read AWS SSM Parameter Store

Configuration in [AWS Systems Manager Parameter Store](https://docs.aws.amazon.com/systems-manager/latest/userguide/systems-manager-parameter-store.html)
becomes a config layer through the sibling module
[`config-aws-ssm`](https://gitlab.com/phpboyscout/go/config-aws-ssm), so a consumer who needs it
takes it — and the AWS SDK — and one who does not pays nothing.

```bash
go get gitlab.com/phpboyscout/go/config-aws-ssm
```

You build and configure the SSM client — region, credentials, endpoint all stay yours — and hand
it in. The adapter takes a **path prefix** that scopes and is stripped from the parameter names:

```go
import (
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"gitlab.com/phpboyscout/go/config"
	configawsssm "gitlab.com/phpboyscout/go/config-aws-ssm"
)

awsCfg, _ := awscfg.LoadDefaultConfig(ctx)
client := ssm.NewFromConfig(awsCfg)

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                        // YAML defaults
	config.WithBackend(configawsssm.FromClient(client, "/app/")),   // SSM outranks them
)
```

Parameter names are `/`-separated paths, so `/app/server/port` under prefix `/app/` reads as
`server.port` — taking part in precedence, per-key merge and provenance exactly as a file does.

```go
store.View().GetInt("server.port") // 8080, from SSM
```

## When you do *not* need this

If the values are static, or your platform already injects them as environment
variables, [`WithEnv`](load-and-merge.md) covers it with no SDK.

Reach for Parameter Store when values are managed centrally and change without a
deploy. It is read-only here, so keep a writable layer for anything your application
persists itself.

## Value types

- **`String`** — a scalar string; the View's typed accessors coerce it, and a JSON/YAML document
  value decodes into a subtree when you pass a codec: `configawsssm.FromClient(client, "/app/",
  configawsssm.WithValueCodec(configjson.Codec{}))`.
- **`StringList`** — read as a `[]string` leaf.
- **`SecureString`** — read **decrypted** (the adapter passes `WithDecryption`), and when a prefix
  contains one the whole layer reports [`Sensitive`](../explanation/dynamic-backends.md#sensitive-read-only-backends):
  the core then refuses to write any of its keys into a non-sensitive layer, so a decrypted secret
  cannot leak into a plain file.

## Read-only, and how writes behave

SSM's `PutParameter` overwrites unconditionally — there is no compare-and-swap to satisfy the
conflict trap the toolkit turns on, so `config-aws-ssm` is **read-only**. A write to a key it
defines routes to the writable layer beneath and is reported shadowed (or, for a `SecureString`
key, refused as a leak) — never silently applied to SSM. Write support is a tracked follow-on;
until then, provision parameters through your existing IaC.

## Watching

The adapter joins hot-reload by **polling** `GetParametersByPath` (`NativeWatch: false`) — SSM has
no change feed. The default interval is conservative because of SSM's API rate limits;
`WithPollInterval` overrides it.

## What it costs

| | |
|---|---|
| Modules added | **14** — 5 for the AWS SSM SDK, 9 for the `config` graph |

The config graph plus the AWS SDK for Go v2's SSM client (`service/ssm` and the SDK core — five
modules, asserted by an allowlist test). You build the client, so its config/credentials packages
are yours, not the adapter's. The testcontainers/LocalStack integration suite is test-only.

## Related

- [How dynamic backends work](../explanation/dynamic-backends.md) — the injected client, polling and
  the conflict spectrum this sits on
- [The adapter ecosystem](../explanation/adapters.md) — every adapter, with status and roadmap

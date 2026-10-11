---
title: Read AWS SSM Parameter Store
description: Make AWS Systems Manager Parameter Store a config layer with config-aws-ssm, covering value types, why it is read-only, how SecureString values stay protected, and the polling cadence.
tags: [how-to, adapters, backends, aws, ssm]
---

# Read AWS SSM Parameter Store

Configuration in [AWS Systems Manager Parameter Store](https://docs.aws.amazon.com/systems-manager/latest/userguide/systems-manager-parameter-store.html)
becomes a config layer through the sibling module
[`config-aws-ssm`](https://gitlab.com/phpboyscout/go/config-aws-ssm), so a consumer who needs it
takes it and the AWS SDK, and one who does not pays nothing.

```bash
go get gitlab.com/phpboyscout/go/config-aws-ssm
```

You build and configure the SSM client, so region, credentials and endpoint all stay yours, and
hand it in. The adapter takes a **path prefix** that scopes the read and is stripped from the
parameter names:

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
	config.WithFiles(fsys, "/etc/app.yaml"),                      // YAML defaults
	config.WithBackend(configawsssm.FromClient(client, "/app/")), // SSM outranks them
)
```

Parameter names are `/`-separated paths, so `/app/server/port` under prefix `/app/` reads as
`server.port`. It takes part in precedence, per-key merge and provenance exactly as a file does.

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

- **`String`** is a scalar string. The View's typed accessors coerce it, and a JSON or YAML document
  value decodes into a subtree when you pass a codec: `configawsssm.FromClient(client, "/app/",
  configawsssm.WithValueCodec(configjson.Codec{}))`.
- **`StringList`** is read as a `[]string` leaf.
- **`SecureString`** is read **decrypted** (the adapter asks for decryption). When the layer as last
  loaded contains one, the whole layer reports [`Sensitive`](../explanation/dynamic-backends.md#sensitive-read-only-backends),
  and the core then refuses to write any of its keys into a non-sensitive layer, so a decrypted
  secret cannot leak into a plain file. A codec is never applied to a `SecureString` or a
  `StringList`.

## Read-only, and how writes behave

SSM's `PutParameter` overwrites unconditionally. There is no compare-and-swap to satisfy the
conflict trap the toolkit turns on, so `config-aws-ssm` is **read-only**. A write to a key it
defines routes to the writable layer beneath and is reported shadowed (or, for a `SecureString`
key, refused as a leak). It is never silently applied to SSM. Write support is a tracked follow-on;
until then, provision parameters through your existing IaC.

## Watching

SSM has no change feed, so the adapter joins hot-reload by **polling** `GetParametersByPath`
(`NativeWatch: false`) and comparing each parameter's version with the one recorded at load. The
default interval is **60 seconds**, declared through `config.PollIntervalHinter`, because SSM's API
is rate limited. Pass the `WithPollInterval` option to the backend to change that default, or
`config.WithPollInterval` to `Store.Watch`, which wins.

## Getting a client

Building the client yourself is the default. Two further rungs exist, and the
second lives in a subpackage:

```go
// You resolved the config; the adapter builds the client.
b, err := configawsssm.FromConfig(cfg, "/app")

// Nothing at all. Note the separate import.
import ssmambient "gitlab.com/phpboyscout/go/config-aws-ssm/ambient"

b, err := ssmambient.Default(ctx, "/app")
```

Resolving the ambient AWS credential chain pulls in `sso`, `ssooidc`, `sts`, `imds` and the rest,
so it is kept out of the adapter's own graph. Import the subpackage and you pay for it; do not and
your footprint is exactly what it was. A test asserts precisely that.

There is no default region. AWS documents none, so a config without one returns `ErrNoRegion`
rather than a guess. Set `AWS_REGION`, or pass `ssmambient.WithRegion(...)` if yours comes from a
flag.

To share one resolved chain across several adapters, and across `go/signing` and `go/encryption`,
resolve it with [`go/awsclient`](https://awsclient.go.phpboyscout.uk) and use
`ssmambient.FromSource`.

None of the three rungs returns anything you need to close. The why behind the ladder is in
[who owns the connection](../explanation/connection-ownership.md).

## What it costs

| | |
|---|---|
| Adds | the AWS SSM SDK, on top of the `config` graph |

The config graph plus the AWS SDK for Go v2's SSM client (`service/ssm` and the SDK core,
asserted by an allowlist test). You build the client, so its config/credentials packages
are yours, not the adapter's. The testcontainers/LocalStack integration suite is test-only.

## Related

- [How dynamic backends work](../explanation/dynamic-backends.md): the injected client, polling and
  the conflict spectrum this sits on
- [The adapter ecosystem](../explanation/adapters.md): every adapter, with status and roadmap

---
title: Read AWS SSM Parameter Store
description: Make AWS Systems Manager Parameter Store a config layer with config-aws-ssm, covering value types, why it is read-only, how SecureString values stay protected, and the polling cadence.
tags: [how-to, adapters, backends, aws, ssm]
---

# Read AWS SSM Parameter Store

Configuration in [AWS Systems Manager Parameter Store](https://docs.aws.amazon.com/systems-manager/latest/userguide/systems-manager-parameter-store.html)
becomes a config layer through the sibling module
[`config-aws-ssm`](https://gitlab.com/phpboyscout/go/config-aws-ssm), so a consumer who needs it
takes it and the AWS SDK, and one who does not pays nothing. You build the SSM client and hand it in
with a **path prefix** that scopes the read and is stripped from the parameter names.

```bash
go get gitlab.com/phpboyscout/go/config-aws-ssm
```

```go
import (
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"gitlab.com/phpboyscout/go/config"
	configawsssm "gitlab.com/phpboyscout/go/config-aws-ssm"
)

awsCfg, err := awscfg.LoadDefaultConfig(ctx)
if err != nil {
	return err
}
client := ssm.NewFromConfig(awsCfg)

store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/app.yaml"),               // YAML defaults
	config.WithBackend(configawsssm.FromClient(client, "/app/")), // SSM outranks them
)
if err != nil {
	return err
}

port := store.View().GetInt("server.port") // 8080, from /app/server/port
```

It takes part in precedence, per-key merge and provenance exactly as a file does.

## Connect to Parameter Store

Building the client yourself is the default, so region, credentials and endpoint all stay yours.
Two further rungs exist, and the ambient one lives in a subpackage:

| Use | When |
|---|---|
| `FromClient(client, prefix)` | You built the `*ssm.Client`: any credential mechanism, a custom endpoint, or a client shared with other code. |
| `FromConfig(cfg, prefix)` | You resolved an `aws.Config` and want the adapter to build the client from it. |
| `ambient.Default(ctx, prefix)` | Nothing to configure: the ambient AWS chain. |

```go
// You resolved the config; the adapter builds the client.
b, err := configawsssm.FromConfig(cfg, "/app")

// Nothing at all. Note the separate import.
import ssmambient "gitlab.com/phpboyscout/go/config-aws-ssm/ambient"

b, err := ssmambient.Default(ctx, "/app")
```

None of the three rungs returns anything you need to close. The why behind the ladder is in
[who owns the connection](../explanation/connection-ownership.md).

Resolving the ambient AWS credential chain pulls in `sso`, `ssooidc`, `sts`, `imds` and the rest,
so it is kept out of the adapter's own graph. Import the subpackage and you pay for it; do not and
your footprint is exactly what it was. A test asserts precisely that.

To share one resolved chain across several adapters, and across `go/signing` and `go/encryption`,
resolve it with [`go/awsclient`](https://awsclient.go.phpboyscout.uk) and use
`ssmambient.FromSource`.

### Name a region

There is no default region. AWS documents none, so a config without one returns `ErrNoRegion`
rather than a guess. Set `AWS_REGION`, or, if yours comes from a flag, put it on the source:
`ssmambient.FromSource(ctx, awsclient.Ambient(ssmambient.WithRegion(region)), "/app")`.

## Map parameters to keys

Parameter names are `/`-separated paths, so `/app/server/port` under prefix `/app/` reads as
`server.port`. Each parameter type maps differently:

- **`String`** is a scalar string. The View's typed accessors coerce it, and a JSON or YAML document
  value decodes into a subtree when you pass a codec: `configawsssm.FromClient(client, "/app/",
  configawsssm.WithValueCodec(configjson.Codec{}))`.
- **`StringList`** is read as a `[]string` leaf.
- **`SecureString`** is read **decrypted** (the adapter asks for decryption). When the layer as last
  loaded contains one, the whole layer reports [`Sensitive`](../explanation/dynamic-backends.md#sensitive-read-only-backends),
  and the core then refuses to write any of its keys into a non-sensitive layer, so a decrypted
  secret cannot leak into a plain file. A codec is never applied to a `SecureString` or a
  `StringList`.

## Read-only, and where writes go

SSM's `PutParameter` overwrites unconditionally. There is no compare-and-swap to satisfy the
conflict trap the toolkit turns on, so `config-aws-ssm` is **read-only**. A write to a key it
defines routes to the writable layer beneath and is reported shadowed. It is never silently applied
to SSM. Write support is a tracked follow-on; until then, provision parameters through your
existing IaC.

When the layer holds a `SecureString`, that routed-beneath write is **refused** instead, because it
would put a decrypted secret into a plainer layer:

```go
_, err := store.Apply(ctx, config.Set("db.password", "rotated"))
// errors.Is(err, config.ErrSensitiveLeak): refused, and app.yaml is untouched
```

## Watch for changes

SSM has no change feed, so the adapter joins hot-reload by **polling** `GetParametersByPath`
(`NativeWatch: false`) and comparing each parameter's version with the one recorded at load. The
default interval is **60 seconds**, declared through `config.PollIntervalHinter`, because SSM's API
is rate limited. Pass the `WithPollInterval` option to the backend to change that default, or
`config.WithPollInterval` to `Store.Watch`, which wins:

```go
config.WithBackend(configawsssm.FromClient(client, "/app/",
	configawsssm.WithPollInterval(5*time.Minute)))
```

A failed poll is not treated as a change. The watch backs off for a second and retries, or for a
whole interval when SSM is throttling. See [hot-reload](hot-reload.md) for the store side.

## Test without AWS

The adapter reaches SSM through the one-method `configawsssm.ParamStore` interface, so a unit test
hands `New` a fake and needs no AWS:

```go
type fakeSSM []configawsssm.Param

func (f fakeSSM) GetByPath(context.Context, string, bool) ([]configawsssm.Param, error) {
	return f, nil
}

backend := configawsssm.New(fakeSSM{
	{Name: "/app/server/port", Type: "String", Value: "8080", Version: 1},
}, "/app/")
```

`Wrap(client)` is the real implementation, and what `FromClient` uses. The module's integration
suite, under `test/integration`, runs against [LocalStack](https://localstack.cloud/) under
testcontainers and is skipped unless `INT_TEST_INTEGRATION=1` is set; it needs a Docker daemon.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `config.ErrSensitiveLeak` | An `Apply` set a key this layer provides while it holds a `SecureString`, and the next writable layer is plainer. | Do not write SSM-sourced keys; change them in SSM. |
| `configawsssm.ErrNoRegion` | `FromConfig` got a config with no region. | Set one on the config. |
| `awsclient.ErrNoRegion` | The ambient rung found no region in the environment. | Set `AWS_REGION`, or use `FromSource` with `WithRegion`. |
| `fs.ErrNotExist` | Nothing exists under the prefix. | Check the prefix. The store tolerates it as an empty layer unless it is the first source and you set `RequireFirstSource`. |
| An AWS API error (wrapped, no sentinel) | Expired credentials, or a policy that lacks `ssm:GetParametersByPath` or the KMS grant for a `SecureString`. | Refresh the credentials, or widen the policy. |

## When you don't need this

If the values are static, or your platform already injects them as environment
variables, [`WithEnv`](load-and-merge.md) covers it with no SDK.

Reach for Parameter Store when values are managed centrally and change without a
deploy. It is read-only here, so keep a writable layer for anything your application
persists itself.

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

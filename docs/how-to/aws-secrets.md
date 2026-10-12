---
title: Read secrets from AWS Secrets Manager
description: Read AWS Secrets Manager as an ordinary config layer, with the leak guard that stops a secret being written to a plain file.
tags: [how-to, backends, aws, secrets]
---

# Read secrets from AWS Secrets Manager

[AWS Secrets Manager](https://aws.amazon.com/secrets-manager/) becomes an ordinary config layer
through [`config-aws-secrets`](https://gitlab.com/phpboyscout/go/config-aws-secrets), a sibling
module: a consumer who reads secrets from AWS takes it and its SDK, and one who does not pays
nothing. You build the AWS client and hand it in with a **prefix** that scopes the read and is
stripped from the names.

```bash
go get gitlab.com/phpboyscout/go/config-aws-secrets
```

```go
import (
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"gitlab.com/phpboyscout/go/config"
	configawssecrets "gitlab.com/phpboyscout/go/config-aws-secrets"
)

loaded, err := awscfg.LoadDefaultConfig(ctx)
if err != nil {
	return err
}
client := secretsmanager.NewFromConfig(loaded)

store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/app.yaml"),                  // YAML defaults
	config.WithBackend(configawssecrets.FromClient(client, "app/")), // secrets outrank them
)
if err != nil {
	return err
}

password := store.View().GetString("db.password") // from the secret app/db/password
```

A Secrets Manager layer takes part in precedence, per-key merge, provenance and hot-reload exactly
as a file does.

## Connect to Secrets Manager

Building the client yourself is the default. Two further rungs exist, each in both shapes (a prefix,
or one secret), and the ambient one lives in a subpackage:

| Use | When |
|---|---|
| `FromClient(client, prefix)` / `FromClientSecret(client, name, codec)` | You built the `*secretsmanager.Client`: any credential mechanism, a custom endpoint, or a client shared with other code. |
| `FromConfig(cfg, prefix)` / `FromConfigSecret(cfg, name, codec)` | You resolved an `aws.Config` and want the adapter to build the client from it. |
| `ambient.Default(ctx, prefix)` / `ambient.DefaultSecret(ctx, name, codec)` | Nothing to configure: the ambient AWS chain. |

```go
// You resolved the config; the adapter builds the client.
b, err := configawssecrets.FromConfig(cfg, "app/")
b, err := configawssecrets.FromConfigSecret(cfg, "app/config", codec)

// Nothing at all. Note the separate import.
import secretsambient "gitlab.com/phpboyscout/go/config-aws-secrets/ambient"

b, err := secretsambient.Default(ctx, "app/")
b, err := secretsambient.DefaultSecret(ctx, "app/config", codec)
```

Nothing here needs closing: the Secrets Manager client holds no connection of its own. Resolving the
ambient AWS credential chain pulls in `sso`, `sts`, `imds` and the rest, so it is kept out of the
adapter's own graph, with a test asserting the parent's footprint has not grown. The why behind the
ladder is in [who owns the connection](../explanation/connection-ownership.md).

### Credentials are yours

`FromClient` never resolves credentials, reads the environment, or picks a region or endpoint.
It uses the client you give it, which is what lets every AWS mechanism (instance roles, IRSA, SSO,
static keys, `AWS_PROFILE`) work without the adapter knowing any of them.

The IAM policy needs `secretsmanager:BatchGetSecretValue` and `secretsmanager:GetSecretValue` on
the secrets under your prefix, plus `kms:Decrypt` on their key if it is not the AWS-managed one.
Reading a [staging label](#read-a-staging-label) lists names first, so it also needs
`secretsmanager:ListSecrets`.

### Name a region

There is no default region. AWS documents none, so a config without one returns `ErrNoRegion`
rather than a guess. For the ambient rung, set `AWS_REGION`, or take the region from a flag with
`secretsambient.FromSource(ctx, awsclient.Ambient(secretsambient.WithRegion(region)), "app/")`.

## Map secrets to keys

Names are paths, so the prefix is stripped and what remains nests:

```
app/db/password  = "s3cr3t"           store.View().GetString("db.password")  // "s3cr3t"
app/cache/url    = "redis://…"   →    store.View().GetString("cache.url")    // "redis://…"
app/region       = "eu-west-2"        store.View().GetString("region")       // "eu-west-2"
```

### Prefix mode is the default here, unlike Vault

If you have read [the Vault how-to](vault.md), note the reversal. There a prefix walk is opt-in,
because Vault has no recursive list: a walk costs a request per directory plus one per secret, and
needs a `list` grant least-privilege policies often withhold.

Neither applies to Secrets Manager. `BatchGetSecretValue` accepts the same name filter
`ListSecrets` does and returns the **values** with it, so a whole prefix arrives in **one request**.
The natural model is also the cheap one, so it is the default. Same family, opposite defaults,
because the same abstraction costs different amounts in each system.

### One secret holding a whole document

The other idiomatic AWS shape is a single secret whose value is a JSON object. An RDS-managed
secret is exactly that. `FromClientSecret` reads one, decoding it through a codec:

```go
import configjson "gitlab.com/phpboyscout/go/config-json"

config.WithBackend(configawssecrets.FromClientSecret(client, "app/rds", configjson.Codec{}))
// {"username":"admin","port":5432}  becomes  GetString("username"), GetInt("port")
```

The codec is a **parameter here, not an option**, because a single secret's value is one opaque
string. Without something to decode it there is no tree to contribute, so requiring it at compile
time beats failing at startup. A value the codec cannot decode fails the load with
`config.ErrBackendParse`.

In prefix mode the codec stays optional, since the names already supply the structure. Add
`WithValueCodec` when secrets under the prefix hold JSON. A mixed prefix still works, because a
value the codec rejects stays a plain string rather than failing the load:

```go
config.WithBackend(configawssecrets.FromClient(client, "app/",
	configawssecrets.WithValueCodec(configjson.Codec{})))
```

### Binary secrets contribute no key

A certificate or keystore is skipped, and the rest of the prefix loads normally. That is not
ambiguity. It is data a `View` cannot serve, and one certificate in a shared prefix should not stop
an unrelated application starting.

### A partial read is refused

`BatchGetSecretValue` does not fail when it cannot read one of the secrets it matched. It returns
the successes and reports the rest separately, typically for a secret your policy excludes or whose
KMS key you cannot use. **The adapter refuses the load in that case**, naming what it could not
read:

```go
// errors.Is(err, configawssecrets.ErrPartialRead)
// configawssecrets: some secrets could not be read under "app/": app/locked (AccessDeniedException)
```

Serving the layer anyway would mean a configuration silently missing keys it should have, and here
a missing key is a password. An application that starts without one fails later, further from the
cause, or falls back to a default worse than not starting. This is the same
[refuse-ambiguity stance](../explanation/dynamic-backends.md#ambiguous-structure-is-refused-not-guessed)
the family takes elsewhere. If a secret genuinely is not for this application, scope the prefix so
it is not matched.

### Read a staging label

`AWSCURRENT` is what an application should read, and is the default. During a rotation you may want
the version before it:

```go
config.WithBackend(configawssecrets.FromClient(client, "app/",
	configawssecrets.WithVersionStage("AWSPREVIOUS")))
```

!!! warning "A staged read costs the bulk read"

    `BatchGetSecretValue` has **no staging parameter**; only the per-secret API does. So selecting
    any label other than the default turns a prefix read from **one request** into one listing plus
    **one request per secret**, and every watch poll pays it too. Worth knowing before you reach for
    it on a large prefix.

## Read-only, and where writes go

Every value here is a secret, so this backend declares itself `Sensitive`, and it is **read-only**
by policy. A write to a key it provides cannot land in Secrets Manager, so it would otherwise fall
through to the next writable layer, typically a plain YAML file. The core **refuses that write**:

```go
_, err := store.Apply(ctx, config.Set("db.password", "rotated"))
// errors.Is(err, config.ErrSensitiveLeak): refused, and app.yaml is untouched
```

See [sensitive read-only backends](../explanation/dynamic-backends.md#sensitive-read-only-backends)
for the reasoning. If a key needs to be writable, do not source it from Secrets Manager.

## Watch for changes

There is no change feed, so watching is **polling**, at **60 seconds** by default. That is slower than
the file watcher because these are billed API calls. The interval you pass to `Store.Watch` does
not apply to this layer; set this layer's own with `WithPollInterval`:

```go
config.WithBackend(configawssecrets.FromClient(client, "app/",
	configawssecrets.WithPollInterval(5*time.Minute)))
```

A poll compares every secret's `VersionId` with the ones recorded at load, so a rotation, an
out-of-band write, or a secret appearing or disappearing under the prefix all reach your observers.
At the default label that is one request; with `WithVersionStage` it is one per secret. A failed poll
is not treated as a change: the watch backs off for a second and tries again. See
[hot-reload](hot-reload.md) for the store side.

## Test without AWS

The adapter reaches the service through the three-method `configawssecrets.SecretsAPI` interface
(`Get`, `List`, and `Names` for the staged read), so a unit test hands `New` (or `NewSecret`) a fake
and needs no AWS:

```go
type fakeSecrets map[string]string

func (f fakeSecrets) Get(_ context.Context, name, _ string) (*configawssecrets.Secret, error) {
	v, ok := f[name]
	if !ok {
		return nil, nil // nil, nil is "absent"
	}
	return &configawssecrets.Secret{Name: name, Value: v, Version: "1"}, nil
}

func (f fakeSecrets) List(_ context.Context, prefix string) ([]configawssecrets.Secret, []configawssecrets.Failure, error) {
	var out []configawssecrets.Secret
	for name, v := range f {
		if strings.HasPrefix(name, prefix) {
			out = append(out, configawssecrets.Secret{Name: name, Value: v, Version: "1"})
		}
	}
	return out, nil, nil
}

func (fakeSecrets) Names(context.Context, string) ([]string, error) { return nil, nil }

backend := configawssecrets.New(fakeSecrets{"app/db/password": "s3cr3t"}, "app/")
```

`Wrap(client)` is the real implementation, and what `FromClient` uses. The module's integration
suite, under `test/integration`, runs against [LocalStack](https://localstack.cloud/) in a container
and is skipped unless `INT_TEST_INTEGRATION=1` is set; it needs a Docker daemon.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `configawssecrets.ErrPartialRead` | The bulk read matched secrets it could not return, typically through IAM or KMS. | Grant access, or scope the prefix so the secret is not matched. |
| `config.ErrBackendParse` | The codec could not decode a single secret's value. | Fix the secret's value, or pass the codec that matches it. |
| `config.ErrSensitiveLeak` | An `Apply` set a key this layer provides, and the next writable layer is plainer. | Do not write Secrets Manager keys; change them in AWS. |
| `configawssecrets.ErrNoRegion` | `FromConfig` or `FromConfigSecret` got a config with no region. | Set one on the config. |
| `awsclient.ErrNoRegion` | The ambient rung found no region in the environment. | Set `AWS_REGION`, or use `FromSource` with `WithRegion`. |
| `fs.ErrNotExist` | The prefix matched nothing, or the secret is absent. | Check the prefix or name. The store tolerates it as an empty layer unless it is the first source and you set `RequireFirstSource`. |
| An AWS API error (wrapped, no sentinel) | Expired credentials, or a policy that denies the call itself. | Refresh the credentials, or widen the policy. |

## When you don't need this

If the secret already arrives as an environment variable or a mounted file, which is
what most task and pod definitions do, `WithEnv` or `WithFiles` read it with no SDK.

Reach for Secrets Manager when your process should fetch for itself: rotation a running
process must pick up, or a per-caller audit trail.

## What it costs

| | |
|---|---|
| Adds | the AWS SDK, on top of the `config` graph |
| Requires | the `config` version named in this module's `go.mod`; `go get` brings it |
| Capability since | `config` **v0.7.0**, the release whose `backendconformance` requires a sensitive read-only backend to refuse the routed-beneath write |

The AWS SDK for Go v2 ships per service, so reading secrets does not drag in the rest of AWS.
`aws-sdk-go-v2/config` and `credentials` are how *you* build a client, so they are yours, not the
module's.

## Related

- [How dynamic backends work](../explanation/dynamic-backends.md): the mechanics every remote backend shares
- [Read secrets from Vault](vault.md): the other secrets manager, with the opposite default mode
- [Read AWS SSM Parameter Store](aws-ssm.md): its sibling for ordinary parameters
- [The adapter ecosystem](../explanation/adapters.md): every adapter, status and roadmap

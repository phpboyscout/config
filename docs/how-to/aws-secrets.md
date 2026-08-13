---
title: Read secrets from AWS Secrets Manager
description: Read AWS Secrets Manager as an ordinary config layer, with the leak guard that stops a secret being written to a plain file.
tags: [how-to, backends, aws, secrets]
---

# Read secrets from AWS Secrets Manager

The core reads and writes files. [AWS Secrets
Manager](https://aws.amazon.com/secrets-manager/) is a remote **secrets** backend, provided by a
sibling module, [`config-aws-secrets`](https://gitlab.com/phpboyscout/go/config-aws-secrets), so a
consumer who reads secrets from AWS takes it — and its SDK — and one who does not pays nothing.

```bash
go get gitlab.com/phpboyscout/go/config-aws-secrets
```

You build and configure the AWS client — that is where every credential, region and endpoint
decision lives — and hand it in with a **prefix** that scopes and is stripped from the names:

```go
import (
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"gitlab.com/phpboyscout/go/config"
	configawssecrets "gitlab.com/phpboyscout/go/config-aws-secrets"
)

loaded, _ := awscfg.LoadDefaultConfig(ctx)
client := secretsmanager.NewFromConfig(loaded)

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                          // YAML defaults
	config.WithBackend(configawssecrets.FromClient(client, "app/")),  // secrets outrank them
)
```

A Secrets Manager layer takes part in precedence, per-key merge, provenance and hot-reload exactly
as a file does. Names are paths, so the prefix is stripped and what remains nests:

```
app/db/password  = "s3cr3t"           store.View().GetString("db.password")  // "s3cr3t"
app/cache/url    = "redis://…"   →    store.View().GetString("cache.url")    // "redis://…"
app/region       = "eu-west-2"        store.View().GetString("region")       // "eu-west-2"
```

## When you do *not* need this

If the secret already arrives as an environment variable or a mounted file — which is
what most task and pod definitions do — `WithEnv` or `WithFiles` read it with no SDK.

Reach for Secrets Manager when your process should fetch for itself: rotation a running
process must pick up, or a per-caller audit trail.

## Prefix mode is the default here — unlike Vault

If you have read [the Vault how-to](vault.md), note the reversal. There a prefix walk is opt-in,
because Vault has no recursive list: a walk costs a request per directory plus one per secret, and
needs a `list` grant least-privilege policies often withhold.

Neither applies to Secrets Manager. `BatchGetSecretValue` accepts the same name filter
`ListSecrets` does and returns the **values** with it, so a whole prefix arrives in **one request**.
The natural model is also the cheap one, so it is the default. Same family, opposite defaults,
because the same abstraction costs different amounts in each system.

## One secret holding a whole document

The other idiomatic AWS shape is a single secret whose value is a JSON object — an RDS-managed
secret is exactly that. `FromClientSecret` reads one, decoding it through a codec:

```go
import configjson "gitlab.com/phpboyscout/go/config-json"

config.WithBackend(configawssecrets.FromClientSecret(client, "app/rds", configjson.Codec{}))
// {"username":"admin","port":5432}  →  GetString("username"), GetInt("port")
```

The codec is a **parameter here, not an option**, because a single secret's value is one opaque
string: without something to decode it there is no tree to contribute, so requiring it at compile
time is better than failing at startup.

In prefix mode the codec stays optional, since the names already supply the structure. Add
`WithValueCodec` when secrets under the prefix hold JSON, and a mixed prefix still works — a value
the codec rejects stays a plain string rather than failing the load:

```go
config.WithBackend(configawssecrets.FromClient(client, "app/",
	configawssecrets.WithValueCodec(configjson.Codec{})))
```

## Credentials are yours

The adapter **never** resolves credentials, reads the environment, or picks a region or endpoint.
It uses the client you give it, which is what lets every AWS mechanism — instance roles, IRSA, SSO,
static keys, `AWS_PROFILE` — work without the adapter knowing any of them.

The IAM policy needs `secretsmanager:BatchGetSecretValue` and `secretsmanager:GetSecretValue` on
the secrets under your prefix, plus `kms:Decrypt` on their key if it is not the AWS-managed one.

## A partial read is refused

`BatchGetSecretValue` does not fail when it cannot read one of the secrets it matched — it returns
the successes and reports the rest separately, typically for a secret your policy excludes or whose
KMS key you cannot use. **The adapter refuses the load in that case**, naming what it could not
read:

```go
// errors.Is(err, configawssecrets.ErrPartialRead)
// configawssecrets: some secrets could not be read under "app/": app/locked (AccessDeniedException)
```

Serving the layer anyway would mean a configuration silently missing keys it should have — and here
a missing key is a password. An application that starts without one fails later, further from the
cause, or falls back to a default worse than not starting. This is the same
[refuse-ambiguity stance](../explanation/dynamic-backends.md#ambiguous-structure-is-refused-not-guessed)
the family takes elsewhere.

If a secret genuinely is not for this application, scope the prefix so it is not matched.

**Binary secrets are different**: a certificate or keystore is skipped, contributing no key, and
the rest of the prefix loads normally. That is not ambiguity — it is data a `View` cannot serve —
and one certificate in a shared prefix should not stop an unrelated application starting.

## Reading a staging label

`AWSCURRENT` is what an application should read, and is the default. During a rotation you may want
the version before it:

```go
config.WithBackend(configawssecrets.FromClient(client, "app/",
	configawssecrets.WithVersionStage("AWSPREVIOUS")))
```

!!! warning "A staged read costs the bulk read"

    `BatchGetSecretValue` has **no staging parameter** — only the per-secret API does. So selecting
    any label other than the default turns a prefix read from **one request** into one listing plus
    **one request per secret**, and every watch poll pays it too. Worth knowing before you reach for
    it on a large prefix.

## Watching for change

No change feed, so watching is **polling**, at **60 seconds** by default — slower than the file
watcher because these are billed API calls:

```go
config.WithBackend(configawssecrets.FromClient(client, "app/",
	configawssecrets.WithPollInterval(5*time.Minute)))
```

A poll compares every secret's `VersionId`, so a rotation, an out-of-band write, or a secret
appearing or disappearing under the prefix all reach your observers. At the default label that is
one request; with `WithVersionStage` it is one per secret.

## Writing a secrets-provided key is refused

Every value here is a secret, so this backend declares itself `Sensitive`, and it is **read-only**.
Because the layer is read-only, a write to a key it provides cannot land in Secrets Manager — so it
would otherwise fall through to the next writable layer, typically a plain YAML file. The core
**refuses that write**:

```go
_, err := store.Apply(ctx, config.Set("db.password", "rotated"))
// errors.Is(err, config.ErrSensitiveLeak) — refused, and app.yaml is untouched
```

See [sensitive read-only backends](../explanation/dynamic-backends.md#sensitive-read-only-backends)
for the reasoning. If a key needs to be writable, do not source it from Secrets Manager.

## What it costs

| | |
|---|---|
| Modules added | **14** — 5 for the AWS SDK, 9 for the `config` graph |
| Requires | the `config` version named in this module's `go.mod` — `go get` brings it |
| Capability since | `config` **v0.7.0**, the release whose `backendconformance` requires a sensitive read-only backend to refuse the routed-beneath write |

Five modules for the SDK is the **leanest of any backend adapter in this toolkit**: the AWS SDK for
Go v2 ships per service, so reading secrets does not drag in the rest of AWS. Note what is absent —
`aws-sdk-go-v2/config` and `credentials` are how *you* build a client, so they are yours, not the
module's.

## Related

- [How dynamic backends work](../explanation/dynamic-backends.md) — the mechanics every remote backend shares
- [Read secrets from Vault](vault.md) — the other secrets manager, with the opposite default mode
- [Read AWS SSM Parameter Store](aws-ssm.md) — its sibling for ordinary parameters
- [The adapter ecosystem](../explanation/adapters.md) — every adapter, status and roadmap

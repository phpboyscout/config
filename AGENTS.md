# AGENTS.md

This file provides guidance to AI coding agents (Claude Code, agy, codex, etc.) when working with code in this repository.

Ways of working are deliberately not repeated here. They live in the phpboyscout
skills, and naming a skill tends to age better than restating it, since a
restatement goes stale while the skill does not.

## What this is

`gitlab.com/phpboyscout/go/config` is a module in the
[phpboyscout Go toolkit](https://go.phpboyscout.uk).

A `Store` is the single owner of configuration I/O. It reads every source,
merges them in a fixed precedence order, records which layer supplied each
value, and is the only thing that writes any of them back. The write path is
why the module exists: `Apply` routes a change at the layer that already owns
the key, so it lands where it will be read back, and the file returns with its
comments, key order and block style intact. Provenance (`View.Origin`,
`View.Shadowed`, `View.Explain`) answers "why is this value what it is", which a
merge-eager library cannot, because merging discards the answer before anyone
can ask for it.

### What it deliberately does not do

The boundary is the useful part, because most of what a reader expects to find
here lives in a sibling module instead.

- **It is not a format library.** The core reads and edits YAML. Every other
  format is a `Codec` handed to `NewCodecBackend`, shipped as its own module, so
  no consumer inherits a parser they do not use. (JSON is a partial exception,
  and a trap. See below.)
- **It never talks to a remote system.** No SDK, no HTTP client, no credential
  handling, no connection lifecycle. A remote source is a `Backend` an adapter
  supplies.
- **There is no provider registry and no plugin discovery.** A consumer builds
  the client and passes it in. `TestDependencyFootprint` in
  `depfootprint_test.go` enforces the result: an allowlist of every third-party
  module the *library* graph (`go list -deps .`, not `./...`) may contain.
  Adding an entry there is supposed to feel like a decision.
- **It does not express rich validation.** `StructSchema`'s whole vocabulary is
  a coarse type, `Required`, `Enum`, `Description`, `Default` and unknown-key
  detection. Anything beyond that is a JSON Schema document, which is
  `config-schema`'s job rather than a dialect grown here.
- **It does not inject defaults.** `FieldSchema.Default` feeds error messages
  and hints, nothing else.
- **It does not watch anything until asked.** `Store.Watch(ctx)` is an explicit
  call, and skipping it means configuration silently never reloads.

### The family, and where the line sits

**This is the core of the `config` family.** Twenty-six sibling modules carry
the `config-` prefix and every one of them requires this module, so a change to
a shape this module owns reaches all twenty-six. Twenty-five are adapters (a
`Codec` for a file format, or a `Backend` for a remote system) and
`config-schema` supplies a `Schema`.

What each side owns:

- A **codec** owns parsing and in-place editing of one format, and nothing else.
  Reading, fingerprinting for conflict detection, staging, atomic rename,
  rollback and watching are the core's and are shared, so an adapter cannot
  reimplement them wrongly. `conformance.Run` is what confirms, per adapter,
  that a real source in that format takes part in merge, precedence, provenance
  and conflict detection exactly as the built-in YAML one does.
- A **backend** owns its whole interaction with a remote system, which is why
  `backendconformance.Run` cannot construct one: the adapter supplies a factory
  plus a `Control` that stands in for another client of the same system.
- Both suites live in this repo, use the standard library `testing` package and
  nothing else (deliberately no testify), so an adapter that runs one takes on
  no assertion dependency it would otherwise avoid.

Two consequences worth holding on to:

- **Check the family before changing a shared interface.** The blast radius is
  the family, not this repo. Adding a case to either conformance suite is the
  same move: it changes what all twenty-five adapters must satisfy.
  `wide-refactor-expand-contract` covers sequencing a change that cannot land in
  one reviewable slice.
- **Release this module before its dependants, never after.** Upstream first
  means each dependant absorbs the bump into its already-open Release MR and
  tags once. Leaves first means all twenty-six tag twice, for nothing. The
  `release-train` skill covers the ordering.

## Where it has got to

Pre-1.0, on the v0.17.x line. Nothing is stable in the 1.0 sense, but the shape
has stopped moving: `Store`, `View`, `Plan`/`Apply`, `Watch`, the
`Backend`/`Codec`/`FS` seams and both conformance suites all came through the
family-wide connection-ladder work without a single constructor changing. The
whole family shipped as one train on 2026-08-17, and every adapter has now been
proven against its real service rather than against a fake.

Specs `0001` through `0012` are in this project's wiki, not in `docs/`, and an
adapter's spec lives in that adapter's own wiki numbered from `0001` there. So
"spec 0001" is ambiguous without naming the project. Only the umbrella specs,
the ones settling what a whole family of adapters shares, live here.

What is still moving:

- **The wiki `tracker` page is the live view** of the family. Read it first,
  then verify whatever you are about to act on, because it is a claim about when
  it was last written.
- **`config#6`**: `canary-full` has no `changes:` guard, so it runs all 19
  adapters on every default-branch pipeline. Measured at 27.6 minutes of compute
  for a release and 23.2 minutes for a documentation-only merge. Cost, not
  correctness.
- **`config#7`**: a comparative prose pass over `README.md` and `docs/`, which
  this file is not a substitute for.
- **`config#5`** is open, but its stated definition of done is superseded by
  config spec 0012 and the issue carries a comment saying so. Do not work it as
  written.
- **HOCON is parked**, not queued. It waits on a safe parser existing.

## The traps

Concrete things that have gone wrong here, or that look wrong and are not.

**A conflict fingerprint is captured at Load, never at Prepare.** This is the
single mistake both conformance suites exist to make unmissable, and
`backendconformance` says so in its package doc. A backend that fetches a fresh
version inside `Prepare` and compares `Verify` against *that* is comparing the
intruder's data with itself, so every stale write is accepted and conflict
detection silently never fires. The `conflict_detected` subtest catches it.

**Two YAML parsers, and the boundary between them must not be crossed.**
`YAMLCodec` reads the file twice on purpose. Values are decoded by
`go.yaml.in/yaml/v3`; document structure (comments, positions, and whether the
file can be edited safely at all) comes from `yamldoc`. The two disagree about
scalar types: `8080` decodes as `int` in one and `uint64` in the other, and
large integers survive one and are destroyed by the other. Values never come
from yamldoc, documents never come from the value parser. Both are on the
`depfootprint_test.go` allowlist for exactly this reason.

**The core already reads JSON, so do not send anyone to `config-json` for
that.** `docs/how-to/json.md` used to open with "the core reads and writes only
YAML"; YAML 1.2 is a superset of JSON, so the core's codec reads a JSON document
unaided and returns typed values. A core write to a `.json` file stays valid
JSON but comes back reflowed onto one line, and the core refuses JSON Lines
outright. Those two are `config-json`'s actual niche. `TestJSONDocClaims` in
`docsclaim_test.go` pins all three claims, and `TestDocsIndexWriteClaim` pins
the worked example on the docsite's front page, so a documentation edit that
contradicts the module fails a Go test.

**`Filtered` and `Constrained` do opposite things to a denied key, on purpose.**
Under `Filtered`, a visibility bound, a write to a denied key routes *past* that
backend and lands somewhere else. Under `Constrained`, a policy bound,
`Prepare` refuses it with `ErrForbiddenKey`. The asymmetry is deliberate and the
comment on `constrainedWritable.Prepare` says why: a forbidden credential that
quietly rerouted would be the same leak in a different file, with nothing said
about it.

**`goroutineID()` parses `runtime.Stack` output, and it is not a hack that
slipped past review.** Notification runs outside the Store lock (it has to, or
the Store deadlocks against anything an observer reads), so an unrelated
goroutine may legitimately write while observers execute. A flag or a mutex
cannot tell the two apart and would refuse the legitimate write. The idiomatic
alternative, threading a marked context through `Observable.Run`, is defeated
silently by an observer calling `context.Background()`, which turns a guaranteed
refusal into a cooperative one. Read the doc comment in `goroutine.go` before
touching it. It is what makes `ErrWriteFromObserver` a guarantee.

**A candidate built for validation must re-derive a key-aware backend.** The
environment backend resolves a variable name against the existing key space, so
a write that introduces a second key spelled the same way makes that variable
ambiguous. Carrying every non-written backend's layers over verbatim hid that
from the candidate, so the write validated and landed, and then the *next*
reload failed on a file the process had just written itself, leaving it on
last-known-good with no way back. The test that claimed to guard this used a
single file with no environment layer and could not have failed either way.

**The canary matrix is hand-maintained and is behind the family.**
`.gitlab-ci.yml` lists 19 adapters in `canary-full` and three in
`canary-subset`. Twenty-six siblings require this module. `config-aws-secrets`,
`config-azure-keyvault`, `config-etcd`, `config-filekv`, `config-gcp-secret`,
`config-keychain` and `config-schema` all pin a released `config` tag and appear
in neither matrix, so a core change that breaks one of them will not fail this
repo's pipeline. Adding a module to the family does not add it to the canary.

**A test here can pass while proving nothing, and it has, more than once.** Two
layers built by the same `fileSource` helper are indistinguishable to everything
downstream (`indexLayers` keys values by `Source`, so one silently overwrites
the other), which made a write-routing assertion incapable of failing. The
dependency guard passed when its scan produced no output at all, which is why
`sentinelPackage` now exists: a module known to be in the graph, whose absence
means the scan itself failed. `docs/development/testing.md` collects these under
"Failure modes to watch for". Read it before writing a test that guards
something subtle.

## The quality gate

`just ci` runs `tidy`, `test`, `test-race` and `lint`. `just` on its own is the
shorter `tidy lint test`. Run it before raising a merge request, so CI confirms
rather than discovers.

The Gherkin scenarios under `features/store/` run inside the ordinary `go test`
job, driven by `bdd_test.go`. `BDD_TAGS` selects a single feature or scenario.

## Which skills apply here

| When | Skill |
|---|---|
| Any new or changed public type, function or interface | `spec-driven-development` |
| Picking this work up, or putting it down | `programme-tracker` |
| Deciding whether something belongs in the core or in an adapter | `deep-modules` |
| Reaching for a third-party import | `use-the-go-toolkit` |
| Standing up a new `config-` sibling | `create-a-go-module` |
| Editing a `.feature` file or a step definition | `write-godog-scenarios`, `bdd-when-and-how` |
| Faking `time.Now`, the filesystem or a backend in a test | `race-safe-test-injection` |
| A watch or notification bug that will not reproduce on demand | `diagnose-with-a-red-loop` |
| Writing anything a reader can check against this code | `checkable-claims` |
| Editing `README.md` or anything under `docs/` | `diataxis-docs`, `write-like-a-person` |
| A change whose blast radius crosses the family | `wide-refactor-expand-contract` |
| A fix that belongs in an adapter rather than here | `raise-a-forge-issue` |
| Releasing this module and its dependants together | `release-train` |
| Before `glab mr create` on this repo | `verify-before-pr` |
| Writing a commit message or a merge request description | `conventional-commits`, `pre-1-0-release-safety` |
| Committing, branching, merging, or opening a merge request | `forge-publish-workflow` |

`env-gated-integration-tests` is absent by decision rather than by oversight.
The core has no test that needs a network, a credential or a real service, and
`bdd_test.go` records why its scenarios run in the ordinary test job: there is
no binary to build and nothing to stand up, so gating them would only delay the
feedback. That skill governs the adapters, not this repo.

> Skills are a Claude Code mechanism, shipped by the
> [phpboyscout marketplace](https://gitlab.com/phpboyscout/claude-code-plugins).
> An agent without them should treat a named skill as a topic to ask about
> rather than a file it can load.

## House rules

- Linear history. Rebase and fast-forward; never squash-merge from the UI.
- Conventional Commits, and the type decides whether a release is cut. Only
  `feat` and `fix` release. A change that repoints or removes a public interface
  is `feat`, not `refactor`, or it lands and never ships.
- No AI attribution in anything published, and never at-mention anyone.
- Never cut a release yourself. That is the maintainer's call, every time.

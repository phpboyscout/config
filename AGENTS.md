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
value, and is the only thing that writes any of them back. `Apply` routes a
change to the layer that already owns the key, so it lands where it will be read
back, and the file returns with its comments, key order and block style intact.

The boundary is the useful half. The core's only codec is YAML, though that is
not the same as reading only YAML: YAML 1.2 is a superset of JSON, so the core
reads a JSON document unaided and `config-json` exists for the two things the
core cannot do rather than for JSON support. Beyond parsing, the core never
talks to a remote system itself, has no provider registry, expresses only coarse
validation (rich schemas are `config-schema`'s job), injects no defaults, and
reloads nothing until `Store.Watch` is called.

**It is the core of the `config` family.** Twenty-six sibling modules carry the
`config-` prefix, and every one of them requires this module, so a change to a
shape this module owns reaches all twenty-six. A codec owns one format's parsing
and editing, a backend owns its whole interaction with one remote system, and
everything shared (fingerprinting, staging, atomic rename, rollback, watching)
stays the core's. `conformance.Run` and `backendconformance.Run` hold adapters
to that line.

Two consequences worth holding on to:

- **Check the family before changing a shared interface.** The blast radius is
  the family, not this repo. `wide-refactor-expand-contract` covers sequencing a
  change that cannot land in one reviewable slice.
- **Release this module before its dependants, never after.** Upstream first
  means each dependant absorbs the bump into its already-open Release MR and
  tags once. Leaves first means all twenty-six tag twice, for nothing. The
  `release-train` skill covers the ordering.

## Where it has got to

Pre-1.0, on the v0.17.x line. Nothing is stable in the 1.0 sense, but the shape
has stopped moving: `Store`, `View`, `Plan`/`Apply`, `Watch`, the
`Backend`/`Codec`/`FS` seams and both conformance suites came through the
family-wide connection-ladder work without a constructor changing. What still
moves is the family around those interfaces rather than the interfaces.

Specs live in this project's wiki rather than in `docs/`, and each adapter
numbers its own from `0001`, so "spec 0001" is ambiguous without a project name.
The wiki `tracker` page is the live view of the family, and is worth reading
first.

## The traps

**A conflict fingerprint is captured at Load, never at Prepare.** A backend that
fetches a fresh version inside `Prepare` and compares `Verify` against *that* is
comparing the intruder's data with itself, so every stale write is accepted and
conflict detection silently never fires. Both conformance suites exist to make
this one unmissable.

**Two YAML parsers, and the boundary between them must not be crossed.**
`YAMLCodec` reads each file twice on purpose. Values are decoded by
`go.yaml.in/yaml/v3`; document structure (comments, positions, and whether the
file can be edited safely at all) comes from `yamldoc`. The two disagree about
scalar types, so values never come from yamldoc and documents never come from
the value parser.

**`Filtered` and `Constrained` do opposite things to a denied key, on purpose.**
Under `Filtered`, a visibility bound, a write to a denied key routes *past* that
backend and lands somewhere else. Under `Constrained`, a policy bound, `Prepare`
refuses it with `ErrForbiddenKey`. The comment on
`constrainedWritable.Prepare` says why the asymmetry is deliberate.

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

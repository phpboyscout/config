---
title: Feature Specifications
description: How decisions about the config module get proposed, agreed, recorded and implemented.
tags: [development, specifications, workflow, planning]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Feature Specifications

**No implementation change without a spec it implements.** The spec is the authoritative
decision record; the code is downstream of it.

That rule earns its keep here more than in most projects. `config` is consumed by other
modules and by downstream tools, so a public API change is expensive to undo. And the
module's behaviour is deliberately counter-intuitive in several places — refusing writes
from inside observers, dropping superseded snapshots, handing back a usable store alongside
an error. Each of those is a decision someone made for a reason, and without the reason
written down the next person reads it as a bug and "fixes" it.

## Where specs live

```
docs/development/specs/YYYY-MM-DD-<slug>.md
```

The ISO date prefix orders them by creation; the slug says what they are. Examples:

- `2026-07-18-structure-preserving-config-writes.md`
- `2026-07-19-store-architecture.md`

## When a spec is required

The bar is **non-trivial or decision-bearing**. If a reasonable person would ask "why was
it done this way?", it needed a spec.

| Needs a spec | Does not |
|---|---|
| A new public type, function or interface | A typo or comment fix |
| A behaviour change a consumer could notice | A version-pin bump |
| A new backend, or a change to the Backend contract | An internal rename with no API effect |
| Anything touching write routing, notification ordering, or merge semantics | A test added for existing behaviour |
| Removing or renaming an exported name | Reformatting |

Forcing a spec onto a mechanical, reversible change is ceremony. Skipping one on a
decision-bearing change is how a module loses its own reasoning.

## Frontmatter

```yaml
---
title: <what the spec decides, as a phrase not a sentence>
date: YYYY-MM-DD
author: <your name>
status: draft
issue: phpboyscout/go/config#<n>      # optional
supersedes: <YYYY-MM-DD-slug>.md      # optional
---
```

## Status lifecycle

`status` is the single source of truth for where a decision stands.

| Status | Meaning |
|---|---|
| `draft` | Under discussion. **Do not implement against it yet.** |
| `approved` | Agreed. Safe to implement. |
| `implemented` | Shipped. Flip it when the change merges or tags. |
| `rejected` | Considered and decided against, or superseded by a later spec. |

**Keep rejected specs.** The value is the durable "we thought about X and chose not to,
for these reasons" record, so the question does not get re-litigated from scratch a year
later by someone who was not in the conversation. Add a `## Rejection rationale` section
explaining why, and leave the rest of the document intact.

## Structure

Adapt to the decision, but most specs want:

1. **Problem** — the current pain, specifically. Not "improve X" but what breaks and for
   whom.
2. **Decisions**, as a numbered list — **D1, D2, …** — so reviews, merge requests and later
   specs can cite "D7" precisely. This is the core of the document.
3. **Rejected alternatives** — what else was considered, and why it lost. Frequently the
   most valuable section six months later.
4. **Public API** — every exported name the change adds, removes or alters.
5. **Testing strategy** — how each decision will be verified. Evaluate godog fit for
   behaviour that spans components and time.
6. **Migration & compatibility** — what breaks, and what a consumer has to do about it.
7. **Open questions** — what is still live. Resolve these *with* the human before the
   status moves to `approved`.
8. **Implementation phases** — a prioritised breakdown, each independently verifiable.

### Numbering decisions

Number them and never renumber. A spec whose D7 means something different this month than
last is worse than no spec, because everything citing it is now silently wrong. If a
decision is withdrawn, mark it withdrawn in place and add a new one.

### Recording revisions

A spec amended after the fact — a decision that proved wrong in practice, or was
under-specified — gets a **dated revision note**, not a silent edit. The record should show
the change of mind.

The convention here is a numbered revision section, **R1, R2, …**, each stating what was
decided, what actually happened, and why the original was or was not right. Cross-link the
decision it revises, so a reader arriving at D8 sees that R10 amended it.

That cross-link is not decoration. In this module a decision was recorded, half implemented,
and the unimplemented half then documented as intended behaviour — because the person
writing the docs read the code rather than the spec. The link is what makes that visible.

## Working with an AI assistant

Specs here are usually drafted collaboratively with an AI assistant, which is good at
enumerating edge cases and bad at knowing which trade-off you want.

### Drafting

Give it the problem, the constraints, and the existing specs as format examples. Then:

- **Answer its open questions yourself.** If it picks the convenient option to keep moving,
  the decision has been made by nobody.
- **Challenge the edge cases** — concurrency, failure partway through, what a consumer sees
  when it goes wrong.
- **Make it name what it rejected.** A spec with no rejected alternatives has not thought
  about the problem.

### Implementing

Once a spec is `approved`, implementation is **test-first**, phase by phase:

1. Write the failing tests for the phase, derived from the spec's decisions and acceptance
   criteria.
2. Implement the minimum that passes them.
3. Refactor with the tests green.
4. `just ci` before moving to the next phase.

Writing the tests first is also how you find out whether the assistant understood the spec.
A misunderstanding shows up in an assertion, where it is cheap, rather than in the
implementation, where it is not.

!!! warning "Verify the work, do not accept it"
    The specific failure mode to watch for is **a test that passes for the wrong reason**.
    This module has had at least five: a green test upheld by an unrelated bug, a
    concurrency test whose harness was broken rather than the code, a fidelity test whose
    assertion encoded the same confusion as the implementation.

    A test that has never been watched to fail is not evidence of anything. See
    [Testing](../testing.md).

### Suggested drafting prompt

```markdown
Draft a feature specification for the `config` module.

## Problem

<What breaks today, for whom, and why the current design cannot accommodate it.>

## Constraints

- The Store is the sole owner of configuration I/O — nothing else reads, writes or watches
  a source. If this change needs a second owner, say so explicitly rather than working
  around it.
- The module is pre-1.0 but consumed by other phpboyscout modules; public API changes need
  a stated migration cost.
- Errors a caller might branch on are named sentinels matched with errors.Is.
- Backend capability is split across Backend / WritableBackend / WatchableBackend. Prefer a
  new narrow interface over widening an existing one.

## Context

<@-reference the relevant source files, the existing specs, and the guides for anything
the change touches.>

## Instructions

- Structure decisions as a numbered list (D1, D2, …) so they can be cited precisely.
- Include a "Rejected alternatives" section, with reasoning. Do not skip it.
- List every exported name added, removed or changed, and what it costs consumers.
- Describe how each decision will be tested, including what would falsely pass.
- Raise open questions rather than resolving them yourself — I will answer them.
- Set status to `draft`, and save to
  docs/development/specs/YYYY-MM-DD-<slug>.md
```

### Context worth attaching

| Always | Why |
|---|---|
| `docs/development/index.md` | conventions, structure, the one architectural rule |
| `docs/development/specs/` | existing specs, as format and depth examples |
| `go.mod` | module path and dependency versions |

| When relevant | |
|---|---|
| `store.go`, `snapshot.go`, `layer.go` | anything touching load, merge or precedence |
| `write.go`, `plan.go` | anything touching writing or routing |
| `notify.go`, `watch.go`, `settle.go` | anything touching observers or change detection |
| `backend.go` | anything touching the backend contract |
| `docs/explanation/` | the reasoning the change has to remain consistent with |

Err toward too much context rather than too little. An assistant can ignore a file it does
not need; it cannot infer one it was never given.

## Current specs

| Spec | Status |
|---|---|
| [config-aws-ssm adapter](2026-07-22-config-aws-ssm.md) | draft |
| [config-azure-appconfig adapter](2026-07-22-config-azure-appconfig.md) | draft |
| [config-gcp-parameter adapter](2026-07-22-config-gcp-parameter.md) | draft |
| [config-consul adapter](2026-07-22-config-consul.md) | implemented |
| [Dynamic backend adapters](2026-07-21-dynamic-backend-adapters.md) | approved |
| [Filesystem abstraction](2026-07-20-filesystem-abstraction.md) | approved |
| [Non-YAML format adapters](2026-07-20-non-yaml-format-adapters.md) | approved |
| [Store architecture](2026-07-19-store-architecture.md) | approved |
| [Structure-preserving config writes](2026-07-18-structure-preserving-config-writes.md) | superseded |

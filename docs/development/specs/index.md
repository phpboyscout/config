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

**Specs are wiki pages, not files in this repository.**

```
https://gitlab.com/phpboyscout/go/config/-/wikis/specs/<NNNN>-<slug>
```

A spec is a point-in-time decision record — written once, true of a moment, read later for
its conclusions. Twenty-six of them sitting in `docs/` buried the living documentation they
were beside, so they moved. Contributor guides, engineering standards and testing
conventions stay here, because those change when the code does.

- **[All `config` specs](https://gitlab.com/phpboyscout/go/config/-/wikis/specs/home)** — the
  core architecture and the three umbrella specs.
- **[Reports](https://gitlab.com/phpboyscout/go/config/-/wikis/reports/home)** — audits and
  reviews, which are the same kind of document.

### A spec belongs to the project it decides

The adapters are sibling modules, and so are their specs. An adapter's spec lives in **that
adapter's own wiki**, numbered from `0001` there — `config-vault` spec 0001, `config-consul`
spec 0001. Only the umbrella specs, the ones settling what a whole family of adapters
shares, live here.

That is why the numbers restart per project, and why "spec 0001" is ambiguous without
naming the project. Refer to one by project and name: *"config-vault 0001"*, *"the
dynamic-backend umbrella"*.

Eight adapters have no spec of their own — `config-afero` and the seven format adapters.
They were built under an umbrella, and each of their wikis says which one.

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
description: >-
  <A folded block scalar, wrapped at 120 columns, at most 600 characters. GitLab renders
  this at the top of the page, so a single 800-character line greets every reader as a
  wall of text. Past 600 characters you are writing an abstract, and an abstract belongs
  in the body where it can have structure.>
status: DRAFT
date: YYYY-MM-DD
authors: [Matt Cockayne]
tags: [specification, ...]
issue: phpboyscout/go/config#<n>      # optional
blocked-by: <the obstacle, named>     # optional
---

[[_TOC_]]
```

`[[_TOC_]]` generates a contents block from the headings. A spec past a couple of hundred
lines is unnavigable without one, and it costs a line.

## Status lifecycle

`status` is the single source of truth for where a decision stands.

| Status | Meaning |
|---|---|
| `DRAFT` | Under discussion. **Do not implement against it yet.** |
| `IN REVIEW` | Being reviewed. Still not safe to implement against. |
| `APPROVED` | Agreed. Safe to implement. |
| `IN PROGRESS` | Being built. |
| `IMPLEMENTED` | Shipped. Flip it when the change merges or tags. |
| `SUPERSEDED` | Replaced by a later spec, which it names. |
| `REJECTED` | Considered and decided against. |

### Blocked is not a status

A spec can be agreed and still unstartable — waiting on a release, a credential, a window.
Say so in `blocked-by`, naming the obstacle, and leave the status alone. Blocking is
orthogonal to status: a spec can be blocked while `DRAFT`, `APPROVED` or `IN PROGRESS`
alike, and a `HELD` status would force a false choice between "agreed" and "blocked" when
both are true. Clear the field when the obstacle clears — a stale `blocked-by` reads as
current and stops work that could have started.

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
   status moves to `APPROVED`.
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

## Writing for the wiki

A spec on the wiki is rendered by **GitLab Flavored Markdown**, not CommonMark. That brings
things worth using and one hazard worth knowing.

### Sigils autolink, and mostly not yet

On a wiki, `#123` becomes a link to issue 123, `!123` to a merge request, `~name` to a
label, `%1.2` to a milestone, `$123` to a snippet, and `@name` to a user.

`@name` is the one that does harm rather than merely being wrong: it resolves against every
user on the forge, so it links **and notifies, immediately, always**. Never write one.

Everything else resolves only when its target exists — which makes it a review hazard rather
than a rendering bug. A spec citing an upstream project's "issue #821", or its own
"Resolved #4", is not wrong today; it is wrong *later*, silently, when this project grows an
issue 821 or 4. **Backtick any sigil you did not mean as a reference** — `` `#821` `` stays
text permanently — or write the cross-project form that links to the thing you actually
meant:

```
phpboyscout/go/config!39        a merge request in another project
phpboyscout/go/config-consul#5  an issue in another project
```

Inline code and fenced blocks are never autolinked, so anything already in code is safe.

### Links out of a wiki page

A wiki page **cannot use a relative link into the repository** — those do not resolve. Link
to a repository file or a docs page by full URL, and to another wiki page by its slug:

```
[the umbrella](0005-dynamic-backend-adapters)                             a sibling spec
https://gitlab.com/phpboyscout/go/config-vault/-/wikis/specs/0001-...     another project's
https://config.go.phpboyscout.uk/how-to/vault/                            a docs page
```

### Worth using

**Mermaid**, in a fenced ` ```mermaid ` block, wherever the prose is describing a shape — an
order of calls, a state machine, a dependency graph. A diagram that lives *in* the spec stays
true to it; one in a separate image file drifts, and nobody notices because images do not
show up in a diff. `sequenceDiagram`, `stateDiagram-v2`, `flowchart` and `erDiagram` carry
most specs.

**Alerts** (`> [!important]`) for the one load-bearing fact a reader must not miss — sparingly,
because a spec where everything is important has nothing important in it. **`<details>`** to
keep long evidence available without it dominating the page.

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
- Set status to `DRAFT`, and save it as a wiki page at specs/<NNNN>-<slug>, taking the
  next free number in this project's wiki.
```

### Context worth attaching

| Always | Why |
|---|---|
| `docs/development/index.md` | conventions, structure, the one architectural rule |
| [the wiki specs](https://gitlab.com/phpboyscout/go/config/-/wikis/specs/home) | existing specs, as format and depth examples |
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

## The specs themselves

They are in the wiki, and the index there is generated from the specs rather than
maintained beside them:

- **[config specs](https://gitlab.com/phpboyscout/go/config/-/wikis/specs/home)** — the core
  architecture, and the umbrella specs governing each adapter family.
- **[config reports](https://gitlab.com/phpboyscout/go/config/-/wikis/reports/home)** — audits
  and reviews.

Each adapter's spec is in its own project's wiki; the list of all of them is on the
[config specs page](https://gitlab.com/phpboyscout/go/config/-/wikis/specs/home).

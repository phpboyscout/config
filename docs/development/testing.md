---
title: Testing
description: The testing philosophy for the config module, and the failure modes that have actually caught us out.
tags: [development, testing, quality]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Testing

Coverage sits above 90%, the race detector is part of CI, and behaviour that spans
components is covered by godog scenarios. None of that is the interesting part.

The interesting part is that this module has repeatedly had **tests that passed for the
wrong reason** — green because of an unrelated bug, or because the harness was broken
rather than the code. Most of this page is about recognising that.

## Running things

```bash
just              # tidy, lint, test — before every push
just ci           # the full local CI: tidy, test, race, lint
just test-race    # the race detector on its own
just coverage     # HTML coverage report
```

The race detector is not optional. Notification ordering, the settle window, the
`ObservedSection` binding and the watcher degradation path are all concurrent, and a race
in any of them is a real defect a user will eventually hit.

## Shape of a test

- **Parallel by default.** `t.Parallel()` unless there is a reason not to. There is no
  global state to fight over — that is deliberate.
- **In-memory filesystems.** `memFilesystem(t, files)` — afero's `MemMapFs` behind a
  `config.FS` adapter — for anything that would otherwise touch disk. afero is a *test*
  dependency now, deliberately: it is the best in-memory filesystem in Go and it stays out
  of what consumers inherit. Reserve real files for the handful of cases exercising a
  genuine OS watcher, and `config.Dir(t.TempDir())` where a real rooted directory is
  wanted.
- **Table-driven for variations**, individual functions for distinct guarantees. When a
  test asserts several independent promises, split it — a failure should name which promise
  broke, not just that something did.
- **Name the guarantee, not the mechanism.** `TestReadsNeverStraddleAReload` says what
  breaks if it fails; `TestView2` does not.

Test helpers live alongside the tests that use them; `storeOn`, `memFS` and
`memFilesystem` are the common ones. The afero adapter lives in `fs_afero_test.go`, which
also exports `WrapAfero` and `NewMemFS` for the external `config_test` package.

## Failure modes to watch for

Each of these is drawn from a real defect in this repository, not a hypothetical.

### A test passing because of another bug

A stale write fingerprint made every *second* write fail. That masked an unbounded observer
cascade, because the cascade could never get past its second iteration. Both were real
bugs, and each hid the other. Fixing the fingerprint made the cascade visible.

**What to do:** when a test passes, know *which* line makes it pass. If you cannot say,
break that line deliberately and check the test fails.

### A test that is vacuously true

`TestApply_ValidationAgreesWithTheReloadItCauses` used a single file and no environment
layer — so the two things it claimed to compare were trivially identical. It could not have
failed.

Another asserted a write created a missing file, but used a flat scalar, where the nesting
bug it was supposed to catch was impossible to reach.

**What to do:** ask what change to the production code would make this test fail. If the
answer is "none", it is not a test.

### A broken harness reading as a clean pass

Three times this has produced a confident, wrong conclusion:

- Measuring read coherence with `os.WriteFile`, which truncates before writing — so both
  libraries under test were being measured against a writer that exposed half-written
  files. The module appeared to tear 12,407 times. It does not. Writing atomically
  (temp file, then rename) gave 0.
- A negative control that injected an OpenTelemetry package to prove a dependency guard
  would catch it. It produced no output, which read as "not caught". It was a build failure
  from a bad escape in the harness; `grep` simply had nothing to match.
- A multi-file watch measurement that showed one notification and looked correct. Both
  writes had landed before the first reload ran. Varying the gap showed two.

**What to do:** when measuring anything involving concurrent file writes, make the writes
atomic first. When a check produces no output, confirm it *ran*. And watch every guard fail
at least once — **a guard nobody has watched fail is a guard nobody knows works.**

### Doc comment and test encoding the same confusion

A fidelity test asserted a quoted YAML value round-tripped in its source form, and the
doc comment above the function said the same thing. Both were wrong in the same way, so the
test confirmed the comment and neither confirmed the behaviour.

**What to do:** a test derived from the same misunderstanding as the code is not
independent evidence. Where a guarantee matters, assert the observable outcome a *user*
would see, not the intermediate representation.

### A test that forbids a correct outcome

`TestAddLayer_AFailingLayerWithdrawsOnlyItself` asserted that a concurrent good layer
always succeeds. It does not, and should not: both calls adopt a backend then reload every
backend, so if the bad layer is registered when the good one reloads, that reload fails —
correctly, because reloading is fail-closed. The test failed roughly once in several
hundred full-suite runs, for a correct reason.

**What to do:** under concurrency, assert the invariant rather than one interleaving's
outcome. Here the invariant is "a caller told its layer was added must find it
contributing" — true whichever order things happen in. Where practical, add a deterministic
sibling test that fails every time rather than occasionally.

## Testing the documentation

Documentation claims are tested, because they are the claims a user actually relies on.

| Test | Pins |
|---|---|
| `docsclaim_test.go` | the worked before/after example on the landing page |
| `coherence_test.go` | the measured read-coherence figure |
| `observer_contract_test.go` | all seven observer guarantees, one per test |
| `custombackend_test.go` | every snippet in the custom-backend guide, including the write path |
| `fidelity_test.go` | the comment- and structure-preservation contract |
| `depfootprint_test.go` | the dependency-footprint claim |

If you change behaviour one of these describes, the test failing is the reminder to update
the prose. That is the point.

A mechanical check worth re-running after editing guides — extract every Go block and
confirm it parses either as statements or as declarations. Blocks that parse *neither* way
mix top-level declarations with statements, and cannot be made valid wherever a reader puts
them. That check has caught four such blocks, one of them added minutes earlier in the same
editing pass.

## BDD scenarios

`features/` holds godog scenarios for behaviour that spans components and time — reload,
concurrency, watching, the typed-section contract. A scenario earns its place when the
behaviour is hard to see in a unit test: several components, an ordering, a failure that
has to propagate somewhere specific.

Steps live in `steps_*_test.go`, grouped by area. Keep step definitions thin — they should
drive the public API and assert, not contain logic of their own.

## Coverage

Above 90%, and it is a floor rather than a target. Coverage measures which lines ran, not
whether anything was verified, and every failure mode on this page occurred in code that
was covered.

The useful question is not "is this line covered" but "would a test fail if this line were
wrong". `just coverage` shows the first; only reading the assertions shows the second.

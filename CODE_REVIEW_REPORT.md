# Comprehensive Code Review Report

Date: 2026-07-19

Scope:

- `gitlab.com/phpboyscout/go/config` at `/home/matt/workspace/phpboyscout/go/config`
- `gitlab.com/phpboyscout/go/yamldoc` at `/home/matt/workspace/phpboyscout/go/yamldoc`
- Source, tests, README files, package godoc comments, and the documentation trees under `docs/` for both modules.

## Executive Summary

Both modules are in good shape. The design is coherent, the public contracts are unusually well documented, and the tests cover the hard parts: layered precedence, provenance, comment-preserving writes, watcher behavior, rollback, validation, and adversarial YAML editing.

The main review concerns are not basic correctness failures. They are edge cases around concurrency, exported mutable state, file metadata preservation, and small API/read-surface hazards. I did not find evidence of a broken core model, and the current automated checks pass.

## Verification

Commands run:

| Module | Command | Result |
|---|---:|---|
| `config` | `go test ./...` | Pass |
| `config` | `go test ./... -cover` | Pass, 90.3% statement coverage |
| `config` | `env GOCACHE=/tmp/config-go-cache go test -race ./...` | Pass |
| `config` | `env GOCACHE=/tmp/config-go-cache go vet ./...` | Pass |
| `config` | `env GOCACHE=/tmp/config-go-cache golangci-lint run ./...` | 0 issues, with sandbox cache warnings |
| `yamldoc` | `env GOCACHE=/tmp/yamldoc-go-cache go test ./... -cover` | Pass, 84.3% statement coverage |
| `yamldoc` | `env GOCACHE=/tmp/yamldoc-go-cache go test -race ./...` | Pass |
| `yamldoc` | `env GOCACHE=/tmp/yamldoc-go-cache go vet ./...` | Pass |
| `yamldoc` | `env GOCACHE=/tmp/yamldoc-go-cache golangci-lint run ./...` | 0 issues, with sandbox cache warnings |
| `yamldoc` | `env GOCACHE=/tmp/yamldoc-go-cache go test -run '^$' -fuzz FuzzParse -fuzztime 5s ./` | Pass |
| `yamldoc` | `env GOCACHE=/tmp/yamldoc-go-cache go test -run '^$' -fuzz FuzzEdit -fuzztime 5s ./` | Pass |

The first unspecialized `yamldoc` test run failed because the sandbox could not write to `/home/matt/.cache/go-build`. Rerunning with `GOCACHE=/tmp/yamldoc-go-cache` passed.

Documentation was reviewed by reading the README, package-level docs, how-to guides, explanation pages, migration/history pages, and approved specs for both projects. I did not run a full static-site build or external link checker in this pass.

## Findings

### High: Existing file permissions are not actually preserved by `config` writes

Location: `config` [write.go](write.go:16), [write.go](write.go:284), [write.go](write.go:318), [write.go](write.go:332)

The comment says newly created config files should be `0600`, while existing files keep their owner-chosen mode. The implementation stages every write with `afero.WriteFile(..., configFileMode)` and then renames the temp file over the target. On normal filesystems, the replacement file takes the temp file's mode, so an existing `0644`, `0640`, or custom-mode file becomes `0600`. Rollback also rewrites the original bytes with `configFileMode`.

Impact:

- Surprising metadata churn on every write.
- Potentially breaks group-readable config deployments.
- Contradicts the stated behavior in code comments.

Recommendation:

- In `Prepare`, capture the existing file mode when `existed == true`.
- Use that mode for the staged temp file when replacing an existing file.
- Preserve mode on rollback as well.
- Add tests covering an existing non-`0600` file, a newly created file, and rollback mode restoration.

### High: `AddLayer` mutates the backend list before the layer is validated

Location: `config` [store.go](store.go:237), [store.go](store.go:251), [store.go](store.go:256), [store.go](store.go:278), [store.go](store.go:294)

`AddLayer` appends the backend under one lock, releases the lock, then calls `reload` under a second lock. During that gap, another goroutine can see or load the unvalidated backend. If the added layer is invalid, another reload can report an error for a layer that has not really been accepted. Worse, concurrent `AddLayer` calls can interfere: one failed layer can restore an older backend slice and drop another layer added in the meantime.

Impact:

- `AddLayer` is not atomic even though the rest of `Store` is strongly serialized.
- Valid concurrent additions can be lost by a failed addition restoring a stale `previous` slice.
- Observers or reload-error callbacks can see transient states.

Recommendation:

- Make adoption and candidate validation one locked operation.
- Prefer a helper that builds a candidate backend slice locally, loads/validates from that candidate, and only assigns `s.backends` plus publishes `s.loaded/current` after success.
- Add race/concurrency tests for simultaneous valid and invalid `AddLayer` calls.

### Medium: `Plan` can mix routing metadata from one snapshot with values from another

Location: `config` [store.go](store.go:599)

`Plan` locks long enough to copy writable targets and source order, unlocks, then calls `s.Snapshot()` separately. A concurrent `Apply`, `Reload`, or successful `AddLayer` can publish a new snapshot between those two steps. The resulting plan can route a new snapshot using old source order, or old targets with new values.

Impact:

- The documented guarantee that `Plan` is exactly what `Apply` would execute can fail under concurrency.
- A dry run can report a target or shadowing chain that no longer matches the snapshot it is explaining.

Recommendation:

- Load `current := s.current.Load()` while holding `s.mu`, together with `writableTargets()` and `sourceOrder()`, then route that pinned snapshot after unlocking.
- Add a concurrency test that forces a reload/add-layer between metadata capture and snapshot load.

### Medium: `Sources` and `Watch` inspect `s.backends` without the store lock

Location: `config` [store.go](store.go:586), [store.go](store.go:989), [store.go](store.go:1026)

`Sources()` iterates `s.backends` without synchronization. `watchablePaths()` does the same. `AddLayer` mutates `s.backends` under `s.mu`, so these public paths can race with runtime layer changes.

Impact:

- Data race if `Sources()` or `Watch()` is called concurrently with `AddLayer`.
- `Watch()` can build its path set from a moving backend list.

Recommendation:

- Guard both methods with `s.mu`, copy the needed IDs/paths/filesystem while locked, then release.
- Add a race test for `Sources()` during repeated `AddLayer` calls.

### Medium: `Watch` assumes all file backends share one filesystem

Location: `config` [store.go](store.go:989), [store.go](store.go:1026)

`watchablePaths()` returns every file path but only the first filesystem it sees. This is fine for `WithFiles(fs, paths...)`, but `WithBackend(NewFileBackend(...))` and multiple `WithFiles` options can build a store over several filesystems. In that case the watcher stats or watches paths through the wrong filesystem.

Impact:

- Mixed-filesystem stores can silently miss changes or watch unrelated paths.
- This undercuts the "watching cannot be silently absent" design principle.

Recommendation:

- Either reject mixed file filesystems with `ErrWatchUnavailable`, or group paths by filesystem and start one watcher per filesystem.
- Add a test with two `afero.MemMapFs` instances or a mixed `BasePathFs`/memory setup.

### Medium: `yamldoc.File.Documents` and `File.Unsupported` expose mutable internal slices

Location: `yamldoc` [yamldoc.go](../yamldoc/yamldoc.go:93), [yamldoc.go](../yamldoc/yamldoc.go:103)

Both methods return the backing slices stored inside `File`. A caller cannot mutate unexported fields on `Document`, but it can replace, reorder, or nil entries in the returned `[]*Document`; it can also alter `Unsupported` entries in place. That makes later calls observe caller-created state.

Impact:

- A read-style API leaks mutable representation.
- Accidental caller changes can corrupt future document selection or unsupported reporting.

Recommendation:

- Return shallow copies from both methods:

  ```go
  return append([]*Document(nil), f.docs...)
  return append([]Unsupported(nil), f.unsupported...)
  ```

- Add tests showing mutation of the returned slices does not affect later calls.

### Medium: `config.Schema.Fields` exposes mutable validation internals

Location: `config` [schema.go](schema.go:80)

`Fields()` returns the schema's internal `map[string]FieldSchema`. A caller can mutate a schema after it has been passed to `WithSchema`, including while reload/write validation is running.

Impact:

- Validation rules can change behind the store's back.
- Concurrent mutation can race with validation.
- `SchemaOf` caches schemas, so mutation of a returned schema can affect later callers for the same type.

Recommendation:

- Return a defensive copy of the map.
- Consider deep-copying `Enum` slices in both `NewSchema` and `Fields()`.
- Add a test proving mutation of `Fields()` does not alter validation behavior.

### Medium: `yamldoc.Node.String` strips comments with a string search

Location: `yamldoc` [node.go](../yamldoc/node.go:212)

`Node.String()` renders the value and truncates at the first `" #"`. That removes inline comments for simple scalars, but it also truncates legitimate scalar content containing space-hash, especially quoted strings such as `"hello # world"`.

Impact:

- Read API can return a damaged representation for valid YAML values.
- This is especially surprising because `yamldoc` otherwise treats quoted `#` correctly in tests and unsupported scanning.

Recommendation:

- Avoid comment stripping by searching rendered text. Prefer rendering from node/token value where possible, or remove comments through AST comment fields before rendering a clone.
- Add tests for quoted and block scalar values containing `#`.

### Low: `yamldoc` unsupported-flow scanner does not handle escaped quotes

Location: `yamldoc` [unsupported.go](../yamldoc/unsupported.go:60), [unsupported.go](../yamldoc/unsupported.go:102), [unsupported.go](../yamldoc/unsupported.go:132)

The source scanner intentionally runs below the AST because the AST loses information needed to detect unsafe flow collections. That is reasonable, but its quote handling closes a quote on any matching quote byte. It does not account for backslash-escaped quotes in double-quoted scalars or doubled quotes in single-quoted scalars.

Impact:

- Possible false positives or false negatives around `#`, `{`, `}`, `[`, and `]` after escaped quote characters.

Recommendation:

- Extend the scanner's quote state machine for YAML string escaping rules.
- Add adversarial tests for escaped quotes containing `#` and flow delimiters.

### Low: Explicit write targets are not validated at planning time

Location: `config` [plan.go](plan.go:144), [write.go](write.go:225), [store.go](store.go:708)

An explicit `Change.Target` bypasses normal routing, which is documented. However, the plan can name a target that does not correspond to a writable source, a real backend, or a valid document. The error then appears later during `Apply`, sometimes as `ErrInternal`.

Impact:

- Dry runs can look actionable when the apply cannot work.
- Misconfigured explicit targets produce less helpful errors.

Recommendation:

- Validate explicit targets against `sourceOrder()`/`writableTargets()` during planning.
- Return `ErrInvalidTarget` for unknown source, non-writable source, or invalid document index.
- Add tests for stale, read-only, and out-of-range explicit targets.

### Low: `ValidateStruct` godoc still describes the old container API

Location: `config` [validate_generic.go](validate_generic.go:40)

The exported function signature is `ValidateStruct[T any](cfg *View, opts ...SchemaOption) error`, and the migration docs correctly say it takes the concrete view. The godoc above the function still says it "takes the Reader interface" and references `Props.Config` and `*Container`.

Impact:

- `pkg.go.dev` will publish stale migration-era guidance.
- Consumers may try to pass a `Reader` implementation and hit a compile error.

Recommendation:

- Rewrite the godoc around the current `*View` API.
- If accepting `Reader` is still desirable, change the signature deliberately and adapt validation to the interface.

### Low: Historical specs contain stale API examples that need stronger signposting

Location: `config` [docs/specs/2026-07-19-store-architecture.md](docs/specs/2026-07-19-store-architecture.md:392), [docs/specs/2026-07-19-store-architecture.md](docs/specs/2026-07-19-store-architecture.md:569), [docs/specs/2026-07-19-store-architecture.md](docs/specs/2026-07-19-store-architecture.md:798)

The current public guides are mostly aligned with the code, but the approved architecture spec intentionally preserves older decision text. Some sections still show `Containable`, `Container`, or "Viper resolves typed values" examples, with revision notes elsewhere explaining the newer state. That is valuable historically, but it is easy for a reader arriving from search to copy an obsolete API shape.

Impact:

- Specs double as documentation, so stale examples can leak into user understanding.
- The revision notes are accurate but not always close enough to the stale snippets they supersede.

Recommendation:

- Add explicit "historical / superseded API sketch" admonitions directly above stale code blocks.
- Prefer replacing examples with current `Store`, `View`, `Binder`, and `Observed` names while preserving the original decision text in prose.
- Add a lightweight docs grep check for removed public names in non-historical pages.

### Low: `yamldoc` founding spec API sketch no longer matches the concrete API

Location: `yamldoc` [docs/specs/2026-07-19-yamldoc.md](../yamldoc/docs/specs/2026-07-19-yamldoc.md:203), [docs/specs/2026-07-19-yamldoc.md](../yamldoc/docs/specs/2026-07-19-yamldoc.md:217), [docs/specs/2026-07-19-yamldoc.md](../yamldoc/docs/specs/2026-07-19-yamldoc.md:259)

The spec shows `type Node interface` with `Comments() Comments // head, line, foot`, while the implementation exposes a concrete `Node` struct and `Comments` has `Head` and `Line` only. The acceptance criteria also mention `key: []`, but the first-cut path model defers sequence indexing and removal.

Impact:

- The README/how-to pages are clearer than the spec, but the spec is linked prominently as the founding design.
- API consumers can be misled about foot-comment access and sequence editing support.

Recommendation:

- Add a revision note to the spec reconciling the sketch with the shipped API.
- Update the API block to show `type Node struct` behavior or label it explicitly as an early sketch.
- Narrow the empty-container criterion to mapping behavior until sequence paths exist.

### Low: Documentation should state mutability/ownership rules for returned metadata

Location: `config` [schema.go](schema.go:80), `yamldoc` [yamldoc.go](../yamldoc/yamldoc.go:93), [yamldoc.go](../yamldoc/yamldoc.go:103)

The code findings above recommend defensive copies for `Schema.Fields`, `File.Documents`, and `File.Unsupported`. Until those APIs are changed, the docs should state whether returned maps/slices are caller-owned or library-owned.

Impact:

- Callers do not know whether mutating returned collections is safe.
- The current behavior is surprising because most other read surfaces in `config` make defensive copies.

Recommendation:

- After fixing the APIs, document that returned maps/slices are copies.
- If not fixed immediately, document that callers must not mutate them.

## Documentation Review

### `config`

Strengths:

- The docs have a strong Diátaxis shape: tutorial, task guides, conceptual explanations, migration material, and specs each have distinct jobs.
- The README and `docs/index.md` communicate the core model quickly: single owner, layers, provenance, writes, typed sections, validation, and reload safety.
- The write, hot-reload, provenance, and store explanations are unusually explicit about failure modes and non-goals.
- Migration guidance is practical and names the real API changes.

Suggested improvements:

- Keep historical specs, but mark superseded snippets more aggressively so search results do not teach old `Containable`/Viper-era APIs.
- Align `ValidateStruct` godoc with the current signature.
- Add documentation for mixed-filesystem watch behavior once the implementation decision is made.
- Add documentation for file mode behavior after the implementation is corrected: new file mode, existing file mode preservation, and rollback behavior.
- Add a generated-doc or grep check that fails if removed public names appear outside `docs/specs/` and `docs/about/`.

### `yamldoc`

Strengths:

- The README clearly distinguishes editing from value decoding and states preservation limits without overpromising byte identity.
- The docs repeatedly reinforce the right boundary: no file I/O, no config semantics, no document precedence policy.
- The unsupported-constructs guide is valuable because it gives callers a clear policy hook instead of hiding substrate limitations.
- The comment-ownership and preservation explanations match the implementation's risk profile.

Suggested improvements:

- Reconcile the founding spec API sketch with the shipped concrete `Node` API.
- Add an explicit "no sequence path/index editing yet" note near any empty-sequence wording.
- Document whether `Documents()` and `Unsupported()` return copies.
- Add a short read-surface caveat for `Node.String()` once the quoted-hash behavior is fixed, or define it more narrowly as a source rendering helper rather than a semantic value accessor.

## Additional Hardening Suggestions

- Add file-mode tests for `config` writes and rollback. This is the highest-value missing test because it documents an explicit contract.
- Add a concurrency test suite around `AddLayer`, `Sources`, `Plan`, and `Watch` lifecycle interactions.
- Consider making `golangci-lint` cache location configurable in local scripts for restricted environments, e.g. `GOLANGCI_LINT_CACHE=/tmp/...`.
- Consider short fuzz smokes for `yamldoc` in CI if runtime budget allows. The existing fuzz targets produced high execution counts quickly.
- If cross-process durability matters later, revisit `filePending.Commit`: temp-and-rename gives atomic visibility, but there is no file or directory sync before returning.
- Add a docs check target that builds the site and optionally validates internal links.

## Positive Notes

- The `Store` model is coherent: snapshot immutability, single-owner writes, provenance, and watcher separation reinforce each other rather than competing.
- `config` tests are broad and targeted. The suite covers many failure modes that usually go untested in config libraries: rollback, shadowed writes, multi-document routing, observer re-entry, env ambiguity, and failed reload retention.
- `yamldoc` has the right test posture for its risk profile: corpus tests plus adversarial tests plus fuzz targets.
- Dependency footprint is disciplined. `yamldoc` has one production dependency, and `config` keeps Viper out while still using focused conversion/decoding libraries.

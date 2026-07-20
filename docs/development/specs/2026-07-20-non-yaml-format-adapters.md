---
title: Non-YAML format adapters — JSON, JSONL, TOML and beyond as sibling modules
date: 2026-07-20
author: matt.cockayne
status: draft
issue: phpboyscout/go/config#2
---

# Non-YAML format adapters

## Problem

The module reads and writes YAML and nothing else. The incumbent it replaces handles JSON,
TOML, HCL, INI, Java properties and dotenv, so "we only do YAML" is a real reason to choose
something else — not for the marquee features, but because a team with one `config.json`
cannot adopt this at all.

The obvious answer is to add decoders to the core. That is the wrong answer. Each format
brings a parser dependency, and the landing page now claims a smaller dependency graph than
the incumbent's — 26 non-stdlib packages against 36 — while doing more. Adding five parsers
to serve users who need one would forfeit that, and would make every consumer pay for
formats they will never open.

This spec establishes how other formats are supported **outside** the core, what the core
must expose for that to work, and how much of it can be shared rather than reimplemented
per format.

## What already works

Reading a new format needs **no core change at all**. This was verified before drafting, by
building a JSON/JSONL backend as a separate module against only the public API:

```go
config.NewStore(ctx,
    config.WithFiles(fsys, "/base.yaml"),                    // YAML, the default
    config.WithBackend(jsonfile.New(fsys, "/over.json")),    // outranks it
    config.WithBackend(jsonfile.NewLines(fsys, "/s.jsonl")),
)
```

Per-key merge across formats, precedence, shadowing and provenance all worked first time:

```
server.port = 9090 (from json:/over.json); also defined in /base.yaml
a           = 2    (from jsonl:/stream.jsonl#1); also defined in jsonl:/stream.jsonl
```

Two findings from that exercise, both load-bearing for this spec:

1. **JSONL maps onto the existing multi-document model exactly** — one line is one document
   is one layer, later lines winning. Nothing had to be invented.
2. **It found a bug.** `Source.String` rendered the `#N` document suffix only for
   `SourceFile`, so every JSONL layer printed identically and provenance could not say which
   line won. Fixed separately; noted here because it is the kind of defect only a second
   format surfaces, and there will be more.

## Decisions

### D1 — Format adapters are sibling modules; the core stays YAML-only

The core keeps exactly one built-in format. Every other format ships as its own module,
depended on only by consumers who use it.

YAML earns its place in the core because it is the default and because the
comment-preserving write path — the module's headline feature — is built on `yamldoc`.
Nothing else has that claim.

### D2 — The registration idiom is `config.WithBackend(...)`, not a per-format `StoreOption`

A sibling module **cannot** provide `WithJSONFile()`. `StoreOption` is `func(*Store)` and
`Store.backends` is unexported, so an external package can construct the function type but
cannot do anything useful inside it. Exporting a mutator to enable that would hand every
consumer a way to reach into a Store mid-construction, which is a much larger hole than the
ergonomic win.

So the idiom is:

```go
config.WithBackend(jsonfile.New(fsys, "/app.json"))
```

Marginally more verbose than `WithJSONFile(...)`, and better: it says *this is a backend* at
the call site, which is exactly what it is and what determines its precedence.

### D3 — The file-backed machinery is extracted and exported behind a codec seam

This is the substantive change, and the reason this spec is not simply "write some adapters".

Roughly 250 lines inside `fileBackend`/`filePending` have nothing to do with YAML: reading
the file and translating `fs.ErrNotExist`, fingerprinting content at load for conflict
detection, staging to a unique temp path, committing by atomic rename, preserving file mode,
resolving symlinks, rolling back, and watching. Every file-format adapter needs all of it.

Left unextracted, each adapter reimplements it and each gets it subtly wrong. The specific
trap is not hypothetical: **`Verify` must compare against the fingerprint taken at `Load`,
not at `Prepare`.** Compare against a prepare-time fingerprint and you compare the intruder's
data with itself, so conflict detection never fires and every happy-path test passes. The
author of the custom-backend guide made exactly this mistake while writing the guide that
warns about it.

The seam is a **codec**: the format-specific part is decoding bytes to values, and editing
bytes in place.

### D4 — `Codec` and `EditingCodec` split by capability, mirroring `Backend`/`WritableBackend`

```go
// Codec turns a source's bytes into layer values.
type Codec interface {
    // Decode returns one map per document in the source. A format with no
    // multi-document concept returns exactly one.
    Decode(path string, src []byte) ([]map[string]any, error)
}

// EditingCodec is a Codec that can also edit a document in place.
type EditingCodec interface {
    Codec

    // Check reports whether this content can be round-tripped safely. It is
    // called at load, so a source that cannot be edited is refused before the
    // user has made any edits to lose.
    Check(path string, src []byte) error

    // Apply edits the source, preserving whatever the format can preserve.
    Apply(path string, src []byte, edits []Edit) ([]byte, error)

    // Empty returns the content of a new, empty document, for creating a file
    // that does not exist yet.
    Empty() []byte
}
```

A read-only codec implements one method. A format that cannot be safely round-tripped simply
does not implement `EditingCodec`, and the resulting backend is not a write target — routing
skips it and a write lands in the next writable layer down, reported as shadowed rather than
failing.

This is the same reasoning that split `Backend` from `WritableBackend`: the type system
checks the capability once instead of every caller checking a flag, and a codec cannot claim
a capability it does not have.

### D5 — `NewCodecBackend` returns a concrete type matching the codec's capability

```go
func NewCodecBackend(filesystem afero.Fs, path string, codec Codec) Backend
```

It returns a backend implementing `WritableBackend` **only** when the codec implements
`EditingCodec`, by returning a different concrete type. It always implements
`WatchableBackend`, because a file on a filesystem is always watchable.

`Source.Writable` on the layers it produces is set to match, so routing and `Plan` agree with
the type system.

The alternative — one concrete type whose `Prepare` returns an error for read-only codecs —
is rejected: it advertises a capability at the type level and fails at call time, which is
precisely the failure mode the interface split exists to prevent.

### D6 — `fileBackend` becomes `NewCodecBackend` with a YAML codec

The existing YAML behaviour moves behind a `yamlCodec` implementing `EditingCodec`:
`Decode` is today's `decodeDocuments`, `Check` is `checkEditable`, `Apply` is `applyEdits`,
`Empty` is the seeding path.

`NewFileBackend` and `WithFiles` keep their signatures and behaviour exactly. This is a
refactor with no consumer-visible change, and the existing suite is the proof — including
`fidelity_test.go`, which asserts the comment-preservation contract end to end.

### D7 — Multi-document sources map onto `Source.Document`, for every format

One document is one layer, whatever "document" means for the format: a YAML document, a
JSONL line, a TOML file's top level (always one). Precedence within a source is document
order, later winning.

This is already how YAML behaves and it now has a second implementation to keep it honest.

### D8 — `Capabilities` stays declarative for now, and says so

`Capabilities` is currently consumed by nothing. This spec does not invent a consumer for it,
and the field documentation should be amended to say it is forward-declared rather than
leaving readers to assume enforcement that does not exist.

Two fields were examined and deliberately left alone:

- **`Sensitive`** — the leak it guards against (a secret written into a plain file) is
  already prevented *by construction*: writes are targeted edits, never a serialisation of
  the merged view, so no layer's values are ever materialised into another layer. Adding
  runtime enforcement would guard a path that does not exist. It becomes load-bearing the
  moment anything dumps or exports resolved configuration, and that is when to wire it up.
- **`PreservesComments`** — surfacing it in `Plan` output was considered and is close to
  vacuous for the formats in scope: JSON has no comments to lose. It becomes useful when a
  format that *has* comments gets a codec that cannot preserve them, which is a decision to
  take then, per format.

Recording both here so the next reader does not re-derive the analysis.

### D9 — One module per format

`gitlab.com/phpboyscout/go/config-json`, `config-toml`, and so on, rather than a single
`config-formats` module with subpackages.

A consumer needing JSON should not acquire a TOML parser in their module graph. Go's build
only links used packages, but the *module* requirement and its transitive `go.sum` entries
follow the whole module, which is exactly the footprint this design is trying to avoid.

It also matches the toolkit's existing shape: `yamldoc` is its own module for the same
reason.

### D10 — Write support is per-format and read-only is a first-class outcome

| Format | Read | Write | Notes |
|---|---|---|---|
| JSON | yes | yes | no comments to preserve; key order and indentation must survive |
| JSONL | yes | yes | one line per document; edits target a document index |
| TOML | yes | **deferred** | has comments; structure-preserving edit needs a `tomldoc` equivalent of `yamldoc` — a module-sized undertaking, not a codec |
| XML | yes | no | attribute-versus-element is ambiguous for a dotted-key model; read-only unless a concrete need appears |
| INI / properties / dotenv | yes | undecided | flat key spaces; needs a stated nesting rule before anything else |

Shipping TOML read-only first is deliberate. Reading it is most of the value, and a
half-working structure-preserving writer that mangles a user's file is worse than no writer
at all — the whole argument on the landing page.

## Rejected alternatives

**Add decoders to the core, switching on file extension.** The obvious approach, and what
the incumbent does. Rejected on dependency footprint: five parsers in the core, paid for by
every consumer, to serve the one format each of them actually uses. It also makes format
support a core release concern — a TOML parser bug would need a `config` release.

**One `config-formats` module with a package per format.** Simpler to publish and version.
Rejected because the module graph does not respect package boundaries: depending on it for
JSON pulls TOML's and XML's requirements into `go.sum`. See D9.

**Let each adapter own its file handling.** The smallest core change — export nothing new,
let adapters do what the proof-of-concept did. Rejected because the proof-of-concept was
read-only, where there is nothing hard to get wrong. The write path has one specific trap
that is invisible until someone races you, and every adapter would face it independently.
D3 exists so that mistake can be made once, in one place, with one regression test.

**A `Codec` that returns a parsed AST rather than editing bytes.** More elegant, and would
let the core implement `Apply` generically. Rejected because there is no common AST across
YAML, JSON and TOML that preserves each one's formatting concerns — the abstraction would
either lose what it exists to protect, or become a union of every format's quirks.

## Public API

Net new exported names in the core: **three**.

```go
type Codec interface{ ... }        // D4
type EditingCodec interface{ ... } // D4
func NewCodecBackend(filesystem afero.Fs, path string, codec Codec) Backend  // D5
```

Unchanged: `NewFileBackend`, `WithFiles`, `Backend`, `WritableBackend`, `WatchableBackend`,
`Pending`, `Edit`, `Layer`, `Source`, `Capabilities`, `NewWatcher`. Everything an adapter
needs is already exported — verified.

Nothing is removed. Nothing changes signature.

## Testing strategy

**The core refactor (D6) is verified by the existing suite.** If `fidelity_test.go`,
`write_test.go`, `rollback_test.go` and the godog scenarios all pass unchanged, the codec
seam did not change YAML behaviour. Any test that needs editing to accommodate the refactor
is a signal the refactor changed something it should not have.

**A conformance suite, exported for adapters to run against themselves.** The specific
value is that every adapter is then tested for the trap in D3 rather than trusted not to
hit it. Candidate assertions:

- a layer's values merge per-key with layers of other formats;
- absent source returns `fs.ErrNotExist` and is tolerated for an optional source;
- provenance names the source, and distinguishes documents where the format has them;
- **a change landing between `Load` and `Commit` is refused with `ErrConflict`** — the D3
  trap, asserted rather than hoped for;
- a rejected write leaves the source byte-identical;
- for an `EditingCodec`, a round-trip with no edits is a no-op.

**Per-format fidelity tests**, mirroring `fidelity_test.go`: what each format guarantees to
preserve, and — equally important — what it does not.

**A cross-format integration test in the core**, using a trivial in-repo codec (not a real
format) so the core's own suite covers the seam without acquiring a parser dependency.

## Migration & compatibility

No consumer change. `WithFiles` and `NewFileBackend` behave identically; YAML remains the
default and the only format the core knows.

Adapters are additive: a consumer adds a module and a `WithBackend` call.

The one thing to watch is that `Source.Kind` becomes genuinely open. Code switching on
`SourceKind` with a closed set of cases will silently mishandle new kinds — the bug already
fixed in `Source.String`, which is the canonical example. Any new switch on `Kind` needs a
default arm that degrades usefully.

## Open questions

1. **Module naming.** D9 proposes one module per format. `config-json` or `configjson`?
   Under `go/` alongside `config` and `yamldoc`, or somewhere else? The toolkit's existing
   convention should probably decide this rather than this spec.
2. **Which formats first.** D10 proposes JSON and JSONL for the first module. Is TOML read
   support wanted in the same phase, given it is likely the most-asked-for after JSON?
3. **Does `tomldoc` get built?** Structure-preserving TOML editing is a module-sized piece
   of work comparable to `yamldoc`. Worth committing to only if someone actually needs to
   *write* TOML; reading it is cheap.
4. **Should the conformance suite live in the core or its own module?** In the core it is
   available to every adapter without another dependency, but it drags `testing` into the
   main module's import graph. A `config-conformance` module keeps the core clean at the
   cost of another repository.
5. **INI/properties/dotenv nesting.** These are flat. `DATABASE_HOST` → `database.host`
   needs a stated rule, and the environment backend's key-resolution problem is a warning
   about how ambiguous that gets. Possibly out of scope entirely.

## Implementation phases

**Phase 1 — the codec seam, no behaviour change.** Extract `Codec`/`EditingCodec`, add
`NewCodecBackend`, reimplement `fileBackend` on top of it as a YAML codec. Done when the
existing suite passes unmodified.

**Phase 2 — the conformance suite.** Written against the YAML codec first, since its
behaviour is already known-good, then exported.

**Phase 3 — `config-json`.** JSON and JSONL, read and write. The first real consumer of the
seam, and the test of whether Phase 1 extracted the right thing.

**Phase 4 — `config-toml`, read-only.** Decode via an established parser; no `EditingCodec`.

**Phase 5 — revisit.** XML, INI, dotenv and TOML writing are decided on demand, informed by
what Phases 3 and 4 actually cost.

Phases 1 and 2 are the only ones touching this repository. Everything after is additive and
independently releasable, which is the point.

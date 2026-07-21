---
title: Non-YAML format adapters — advertised parity and beyond, as sibling modules
date: 2026-07-20
author: matt.cockayne
status: approved
approved: 2026-07-20
revised: 2026-07-21
issue: phpboyscout/go/config#2
---

# Non-YAML format adapters

## Revisions

### R1 — 2026-07-21: `NewCodecBackend` takes `config.FS`, not `afero.Fs`

**D5 and the Public API section give the constructor as
`NewCodecBackend(filesystem afero.Fs, path string, codec Codec)`. That signature was already
stale when this spec was approved, and building Phase 1 made it concrete.**

Phase 0 — the filesystem abstraction, specified separately and delivered with the core — replaced
`afero.Fs` throughout the public API with the module's own `config.FS` interface. The core no
longer references afero in library code at all (only in comments and its own test suite). Every
existing constructor already takes `config.FS`: `NewFileBackend(filesystem FS, path string)`,
`WithFiles(filesystem FS, ...)`. `NewCodecBackend` is the same seam and takes the same type.

The signature as built is:

```go
func NewCodecBackend(filesystem FS, path string, codec Codec) Backend
```

Nothing else in D5 changes: it still returns a concrete type implementing `WritableBackend`
exactly when the codec implements `EditingCodec`, and `WatchableBackend` always. This is a
correction to a type name the spec predates, not a change of design.

## Problem

The module reads and writes YAML and nothing else, so "we only do YAML" is a real reason to
choose something else — not for the marquee features, but because a team with one
`config.json` cannot adopt this at all.

**The bar is lower than it looks.** viper 1.21 advertises eleven extensions in
`SupportedExts` — json, toml, yaml, yml, properties, props, prop, hcl, tfvars, dotenv, env,
ini — and ships decoders for four families: JSON, TOML, YAML and dotenv. Measured, not
assumed:

```
json        ok  -> map[server:map[port:9090]]
toml        ok  -> map[server:map[port:9090]]
yaml        ok  -> map[server:map[port:9090]]
env         ok  -> map[server_port:9090]
hcl         ADVERTISED but FAILS: decoder not found for this format
ini         ADVERTISED but FAILS: decoder not found for this format
properties  ADVERTISED but FAILS: decoder not found for this format
tfvars      ADVERTISED but FAILS: decoder not found for this format
```

A `.hcl` file passes the extension check and then fails at parse time. That is worth
noticing for two reasons: the competitive bar for parity is **JSON, TOML and dotenv**, not
the advertised list; and a central list of supported formats is a thing that goes out of
sync with the decoders behind it. D1 and D9 make that failure mode structurally impossible
here — a format either has a module or it does not, and there is no list to drift.

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

> **Amended by [R1](#r1--2026-07-21-newcodecbackend-takes-configfs-not-aferofs): the parameter
> is `config.FS`, not `afero.Fs`.**

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

| Format | Module | Read | Write | Notes |
|---|---|---|---|---|
| YAML | core | yes | yes | the default; `yamldoc` gives comment-preserving edits |
| JSON | `config-json` | yes | yes | no comments; key order and indentation must survive |
| JSONL | `config-json` | yes | yes | one line per document; edits target a document index |
| TOML | `config-toml` | yes | fast follow | writer-shaped hole now, `tomldoc` next — see D16 |
| XML | `config-xml` | yes | no | attributes merge with elements; collisions refused — see D21 |
| INI | `config-ini` | yes | no | advertised by the incumbent, decoder absent |
| Java properties | `config-properties` | yes | no | advertised by the incumbent, decoder absent |
| dotenv | `config-dotenv` | yes | no | flat; the incumbent does decode this one |
| HCL / tfvars | `config-hcl` | declarative subset | fast follow | `hclwrite` makes writing cheap — see D11, D19 |
| HOCON | *deferred* | — | — | no usable parser exists; ours eventually — see D20 |

Read-only is the default position for anything that is not YAML or JSON. Reading is most of
the value, and a half-working structure-preserving writer that mangles a user's file is
worse than no writer at all — the whole argument on the landing page. Write support is added
per format when someone needs it and the format can support it honestly.

### D11 — Language formats are read, never written; substitution is resolved only when it is self-contained

HCL and HOCON are declarative and readable, and both get read support. Neither gets an
`EditingCodec`: routing skips them, and a write lands in the next writable layer down,
reported as shadowed rather than failing.

Writing is refused **for now on demand rather than difficulty**. The first draft justified it
by saying editing an expression-bearing document could silently change a value the user did
not touch. That reasoning does not survive contact with the rest of this decision: a document
containing expressions is already refused at load, so every document the adapter accepts is
expression-free and therefore exactly as safe to edit as YAML. The refusal that makes reading
honest also makes writing safe.

And writing is cheap — `hclwrite` does it already (D19). So the reason to ship read-only
first is that nothing has been shown to need it: the surveyed production codebase reads HCL
in three places and writes it in none. Write support is a fast follow under the promotion
pattern (D18) whenever a consumer appears, and the adapter is built with the writer-shaped
hole (D16) from the start.

**Reading needs a distinction that "declarative language" hides.** These two look alike and
are not:

```hocon
# HOCON — self-contained. The reference resolves inside this document.
base    { host = "localhost", port = 8080 }
derived { url = "http://"${base.host}":"${base.port} }
```

```hcl
# HCL — not self-contained. var.* is supplied from outside: variable files,
# -var flags, TF_VAR_ environment. None of which a config library has.
port = var.base_port + 1
```

**Self-contained substitution is resolved.** A HOCON reference to another path in the same
document has one correct answer, the HOCON specification defines how to compute it, and
resolving it produces exactly the value the author intended. Refusing it would reject a
large share of real HOCON files for no benefit.

**Externally-parameterised interpolation is refused**, at load, with `ErrBackendUnsafe`
naming the construct and the line. `var.base_port` has no correct answer without a variable
context this module does not have and should not invent, and a value computed from a
half-context is worse than an error because it looks right. The same treatment, and the same
reasoning, as a YAML flow collection that cannot be round-tripped safely: fail before anyone
relies on a wrong value.

Function calls (`file()`, `join()`) are refused on the same grounds — `file()` in particular
would read a file the Store does not know about, which is D12.

**Field evidence that the refusal is the right shape.** A production codebase reading HCL
this way already has the bug D11 prevents. It resolves attributes with an empty evaluation
context:

```go
val, diag := attr.Expr.Value(&hcl.EvalContext{})
```

That works for a string literal and fails for anything else. Its caller reads
`terraform.source` from a Terragrunt file, and a real file writing
`source = "git::...?ref=${local.ref}"` produces a diagnostic, which becomes a logged error
and an early return — the module is **silently dropped from the pipeline**. Nobody is told
the configuration was not understood; the work simply does not happen.

That is the same defect D11 refuses, at the same point, differing only in when it surfaces:
at use, quietly, per attribute, rather than at load, loudly, naming the construct. Refusing
at load is not a stricter version of that behaviour — it is the version that tells you.

The same codebase also draws exactly the line D11 draws. Where it genuinely needs the full
language — resolving a Terragrunt dependency graph — it does not use its HCL helper at all;
it calls Terragrunt's own parser, with a real evaluation context. Declarative extraction and
real evaluation are already separate tools there, independently.

**What provenance says for a resolved value.** `Origin` names the file and document, which
is what it can honestly say. It cannot point at "the expression on line 14", and a reader
tracing a substituted value follows it in the source. Noted because provenance is this
module's headline claim, and it is weaker here than for a plain data format — a real cost of
supporting these formats, not something to gloss.

### D12 — Includes are refused, because the Store owns file I/O

HOCON's `include "other.conf"` is a file read performed by the parser. That directly
contradicts the rule the whole architecture rests on: **the Store is the sole owner of
configuration I/O** (store-architecture D1).

A codec that follows an include reads a file the Store does not know about. That file is
then not watched, contributes no layer, appears in no provenance, and is invisible to
`Shadowed` — so a value would come from a file the module cannot name, which is the exact
failure this module exists to eliminate.

Includes are therefore refused at load, with an error that names the alternative: list the
files as separate layers, in the precedence order you want, and the merge you were asking
the include to perform happens where it can be explained.

```go
config.NewStore(ctx,
    config.WithBackend(hoconfile.New(fsys, "/base.conf")),
    config.WithBackend(hoconfile.New(fsys, "/override.conf")),
)
```

This is a better answer than the include, not merely an available one — each file keeps its
identity in provenance, and precedence is visible at the call site instead of buried in a
directive halfway down a file.

The same applies to any format with an include mechanism.

### D13 — The target is every format the incumbent advertises, plus more

The incumbent advertises eleven extensions and decodes four families. The target here is
**everything it advertises, working** — including HCL, tfvars, INI and Java properties, which
it names and cannot read — plus formats it never claimed, starting with HOCON, XML and JSONL.

Read-only counts as support. A format that can be read but not written is genuinely useful
and honestly described; a format that is advertised and fails at parse time is neither.

This is a positioning decision as much as a technical one. "Everything it claims, plus what
it doesn't" is a defensible sentence only if the advertised list actually works, which is why
the low-value formats (INI, properties) are in scope at all — each is a small parser and a
nesting rule, and leaving them out would leave the claim false.

### D14 — HOCON's environment-variable fallback is disabled

The HOCON specification says an unresolved substitution falls back to the system
environment. That must not happen here.

The module requires an environment prefix and treats it as a security control: without one,
any variable matching a configuration key could reconfigure the program, which on a shared
runner or multi-tenant host means an unrelated process's `LOG_LEVEL` reaches every tool.
A HOCON file containing `${HOME}` or `${LOG_LEVEL}` would walk straight through that control,
reading unprefixed environment through a side door the Store does not own.

So the codec resolves substitutions **within the document only**. An unresolved reference is
an error, not an environment lookup. A consumer who wants environment values adds
`config.WithEnv("PREFIX")` as an ordinary layer, where the prefix applies, provenance names
the variable, and precedence is visible at the call site.

This needs verifying against whichever Go HOCON implementation is chosen — if it performs
env fallback internally and cannot be configured not to, that disqualifies it.

### D15 — The core exports the key helpers a flat-format adapter needs

INI, Java properties and dotenv all produce flat keys that must become a nested tree, and
dotenv's names are ambiguous in exactly the way environment variables are. The core already
solves both, in `nest`, `mapKey` and `envName` — all unexported.

**`nest` is exported.** Every flat adapter needs it, and building the tree wrongly is the
documented trap in the custom-backend guide: `{"server.port": 9090}` produces a key with a
literal dot rather than a nested path. Layer keys are lower-cased at the boundary by
`normalised`, so case is not a coupling — shape is.

**The ambiguity resolver is exported for dotenv's benefit.** `DATABASE_HOST` could mean
`database.host` or `database_host`, and the existing strategy — resolve against the keys the
lower layers already define, fall back to underscore-as-separator, return `ErrAmbiguousEnvKey`
naming both candidates when two collide — is worth reusing rather than reimplementing. An
adapter that reimplements it and picks a candidate in map-iteration order behaves differently
between runs of the same program, which is the specific bug the current code exists to
prevent.

This is also the second real consumer of `Load`'s `below` argument, which until now only the
environment backend used. That the argument was there and sufficient is evidence the seam was
cut in the right place.

### D16 — `config-toml` ships read-only with a writer-shaped hole

TOML gets a structure-preserving writer via a `tomldoc` module, as a **fast follow** rather
than a prerequisite. `config-toml` ships read-only first so TOML reading is not blocked behind
a module-sized piece of work.

The evidence that `tomldoc` is genuinely necessary, rather than a preference: no existing Go
library round-trips TOML without destroying the file. `go-toml` v2 dropped editing entirely;
v1 has an editable `Tree`, and a load-set-write cycle produces this:

```toml
# before                          # after Set("server.port", 9090)
# Which port the listener binds.
# Needs a firewall change too.    [features]
[server]                            beta_ui = false
host = "localhost"  # dev only
port = 8080                       [server]
                                    host = "localhost"
# Feature flags.                    port = 9090
[features]
beta_ui = false
```

Every comment gone, sections alphabetised, indentation injected. That is the merged-view
write this module exists to prevent, arriving through a different door.

**What "writer-shaped hole" requires concretely:**

- The codec is a named exported type, not an anonymous value, so it can gain methods later
  without changing how it is constructed.
- Adding `Check`, `Apply` and `Empty` turns it into an `EditingCodec`, and `NewCodecBackend`
  (D5) then returns a writable backend automatically. No consumer call site changes.
- The adapter's constructor signature is fixed now and does not change when writing lands.
- The conformance suite asserts *read-only* behaviour for it today — routing skips it, writes
  land in the next writable layer and are reported as shadowed — so the transition is a
  visible test change rather than a silent one.

**The promotion itself follows D18**, which specifies the pattern generally rather than
treating this as a TOML-specific problem. In practice the exposure here is small: `tomldoc`
is intended as a quick follow, so few consumers will adopt `config-toml` while it is
read-only. The pattern is specified for the general case, not this one.

### D17 — The conformance suite lives in the core, using stdlib `testing` only

`gitlab.com/phpboyscout/go/config/conformance`, so every adapter can run it without taking
another dependency or tracking another module's version.

**It must not import testify.** The core has testify as a test dependency today, which costs
consumers nothing because nothing in the library graph reaches it. A `conformance` package
importing it would make it a link-time dependency for every adapter that runs the suite. The
suite therefore uses stdlib `testing` and plain comparisons.

`depfootprint_test.go` scopes to `go list -deps .` — the root package — so the core's
dependency claim stays honest and measures what a consumer of `config` actually inherits.

### D18 — Capability promotion is a supported lifecycle event, not a one-off concern

A backend shipping read-only and gaining write support later is expected to be the **normal
adoption path**, not a TOML-specific wrinkle. It lets a format be useful immediately while
the structure-preserving writer it deserves is built, and it will apply to every adapter
here and to backends other people write. It is therefore specified once, as a pattern.

**What promotion actually changes, measured rather than assumed.** With a read-only layer at
higher precedence than a writable one:

```
key the read-only layer DEFINES:  target=/base.yaml  effective=false  shadowedBy=[json:/over.json]
key nothing defines yet:          target=/base.yaml  effective=true   shadowedBy=[]
```

The two cases behave completely differently, and only one is a surprise.

**Writes to keys the layer already defines were never silent.** They routed to the layer
beneath and were reported with `Effective() == false` and the read-only layer named in
`ShadowedBy` — the module was already telling the consumer the write would not take effect.
Promotion turns a reported failure into a working write. That is an improvement, visible
before and after, and needs no ceremony.

**Writes to new keys are the real change.** Before promotion they land in the highest-precedence
*writable* layer; after, they land in the promoted one. Both work. The file differs. This is
the only genuinely silent part, and it is what the pattern exists to manage.

**Requirements on an adapter that intends to be promoted:**

1. **Constructor and codec type are stable from the first release.** Promotion adds methods
   to an existing exported codec type; it never changes how the backend is constructed. A
   consumer's call site is untouched.
2. **Ship read-only deliberately, and say so.** The package documentation states that write
   support is planned, so a consumer choosing the module knows which way it will move.
3. **The conformance suite asserts the current capability.** Read-only today means asserting
   that routing skips the layer; promotion flips that assertion. The transition is a visible
   test change in the adapter's own suite, not something noticed in production.
4. **Promotion is a minor version with a mandatory release note**, stating the routing
   consequence in the form above: writes to keys this layer defines now take effect; writes
   to new keys now land here rather than in the layer beneath.

A major version is not required. Promotion is additive at the type level, fixes a
reported-ineffective case, and the one behaviour that changes is stated. Reserving a major
bump for it would make the pattern expensive enough that adapters ship read-only and stay
that way.

**The tool a consumer uses to pin routing** is `Plan`, which already exists for exactly this:

```go
plan, err := store.Plan(config.Set("server.name", "prod"))
// plan.Targets() names the layers a write will touch — assert on it if it matters.
```

A consumer who cares where writes land asserts it, and the assertion fails loudly on
promotion rather than the behaviour changing quietly. This is worth documenting in
[Write a custom backend](../../how-to/custom-backend.md) as part of publishing a backend,
not only here.

### D19 — `hcldoc` is a thin wrapper over `hclwrite`, not a build like `tomldoc`

HCL is the opposite of TOML on cost. HashiCorp's `hclwrite` already performs
structure-preserving edits, and it does so to the same standard this module promises for
YAML. Verified:

```hcl
# Which port the public listener binds to.     # after SetAttributeValue("port", 9090):
# Changing this needs a firewall change too.   # ...identical, except
server {                                        server {
  host = "localhost"   # loopback only in dev     host = "localhost" # loopback only in dev
  port = 8080                                     port = 9090
}                                               }
```

Leading comments, the inline comment, block order and indentation all survive; only the
value asked for changed. Inline comment padding is normalised, exactly as the YAML path
does, and within the same stated contract.

So there is no `hcldoc` to build in the sense `tomldoc` must be built. What the adapter
needs is a codec that maps between HCL and layer values, and the writing is delegated.

**What the existing `als` package contributes.** It is a convenience wrapper over the same
libraries — `hclparse` for reading, `hclwrite` for editing — which is independently the same
dual-parse split this module uses for YAML. Its reusable part is the **path traversal**:
`traverseBlocks`, `getAttribute` and the `getNested*` family resolve a dotted path through
blocks, labels, objects, maps and list indices. That logic is the non-obvious work and is
worth porting.

Its writing is superseded by calling `hclwrite` directly, and two of its choices are worth
not repeating:

- `Update` matches on block *type* only, ignoring labels, so it would update every
  `resource` block at once. Its own documentation acknowledges this.
- `Get` splits a path across two arguments — block path and attribute path — and where the
  split falls is load-bearing and undocumented. One path is better.

**Cost implication.** HCL write support is cheaper than TOML's, not more expensive. That
inverts the assumption in the first draft of D11, which treated writing HCL as the hard part.
It is not; the hard part was always the semantics.

### D20 — HOCON is deferred until we have a parser of our own

No usable Go HOCON parser exists. `go-akka/configuration` has been unreleased since 2020.
`gurkankaymak/hocon` is maintained, and disqualified by D14: environment fallback is
hardcoded in its substitution resolver, with no option to turn it off.

```go
// gurkankaymak/hocon@v1.2.23 parser.go:227
} else if env, ok := os.LookupEnv(substitution.path); ok {
    return String(env), nil
```

Confirmed by probe — `${LEAKED_SECRET}` resolved from the process environment. That is the
prefix bypass D14 exists to prevent, and it is not configurable. The same probe found
`${base.host}":"${base.port}` producing `h""":"""1` instead of `h:1`, so correctness is a
second concern independent of the first.

Three alternatives were weighed and rejected. **Forking** means maintaining a parser for a
specification we do not control, indefinitely. **Refusing all substitutions** so the fallback
is unreachable would work, but loses the self-contained resolution D11 specifically allows,
and needs a lexer-aware pre-scan because `${x}` inside a quoted string is literal. **Using it
as-is** is not an option while it reads unprefixed environment.

So HOCON waits for a parser of our own. It is the one format here that is purely additive —
the incumbent never advertised it — so deferring costs nothing against D13, and it is
sequenced behind everything that does.

An upstream option to disable environment fallback would be a small and well-motivated
contribution, and would reopen this decision if it landed.

### D21 — XML merges attributes with child elements, and refuses what it cannot represent

XML was initially assessed as barely viable, on the grounds that its shape depends on its
data: `<host>a</host>` decodes to a scalar and two of them decode to a list, so a
configuration working with two entries would break with one. That is structurally true of
XML and cannot be fixed at the parser.

**It was also already solved, and the objection should have been checked before it was
raised.** Both read paths promote a scalar to a single-element slice:

```
host: a       GetStringSlice=[a]    Unmarshal into []string=[a]
host: [a, b]  GetStringSlice=[a b]  Unmarshal into []string=[a b]
```

A consumer declaring `Hosts []string` is correct for one element or many. The other
direction — two elements into a `string` field — fails loudly with a decode error rather
than silently choosing one. So the residual cost is a documented convention: **declare
repeatable elements as slices**, which is advice, not a blocker.

What remains genuinely needs deciding.

**Attributes and child elements share one namespace.** `<server port="8080"><host>local</host></server>`
gives `server.port` and `server.host`, and a path reads the same as it would in any other
format. Real configuration XML is attribute-heavy — log4j and .NET `appSettings` are almost
entirely attributes — so this is the common case rather than an edge one, and burdening it
with a sigil would make the format's ordinary paths the ugly ones.

A document where an attribute and a child element share a name is **refused at load** with
`ErrBackendUnsafe` naming both. This is the same treatment as an ambiguous environment
variable name: the module has two candidates, no basis to choose, and says so rather than
picking one. The alternative — letting elements win — silently discards the attribute, which
is the class of quiet wrong answer refused everywhere else here.

**An element with both attributes and text content is refused.** `<host type="dns">localhost</host>`
is simultaneously a scalar and a map, and a nested value tree cannot hold both at one key.
A magic `#text` key would handle it, at the cost of a path nobody would guess and that exists
in no other format. Refusing keeps the invariant that every path is either a value or a map,
and the construct is uncommon in real configuration — log4j, .NET `appSettings` and Maven
POMs all avoid it. The decision is reversible if evidence says otherwise.

XML stays **read-only**. No Go library round-trips it preserving formatting and comments, so
writing is in the same position as TOML but with materially less demand.

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

**Refusing HCL and HOCON outright as "not data formats".** The first draft of this spec came
close to it. Rejected because it conflates two things: HOCON's self-contained substitution
has one correct answer and is worth resolving, while HCL's `var.*` does not. Refusing both
would have discarded a large share of readable HOCON for a reason that only applies to HCL.
See D11.

**Full HCL evaluation with a supplied variable context.** Would make the adapter useful for
real Terraform configuration. Rejected for now on scope and honesty: it means implementing
(or vendoring) an evaluation context, keeping it consistent with whichever Terraform version
the user runs, and accepting that provenance cannot answer its central question for computed
values. If someone genuinely needs this, it is a different product — a Terraform
configuration reader — and should be specified as one rather than smuggled in as a codec.

**A `Codec` that returns a parsed AST rather than editing bytes.** More elegant, and would
let the core implement `Apply` generically. Rejected because there is no common AST across
YAML, JSON and TOML that preserves each one's formatting concerns — the abstraction would
either lose what it exists to protect, or become a union of every format's quirks.

## Public API

Net new exported names in the core:

```go
type Codec interface{ ... }        // D4
type EditingCodec interface{ ... } // D4
func NewCodecBackend(filesystem FS, path string, codec Codec) Backend  // D5, amended by R1

// D15 — what a flat-format adapter needs and would otherwise reimplement.
func NestKey(path string, value any) map[string]any
func ResolveFlatKey(name string, below []Layer) (string, error)
```

Plus one new package, `config/conformance` (D17), importing only stdlib `testing`.

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

1. ~~**Module naming.**~~ **Resolved 2026-07-20:** one module per format, named
   `config-<format>` — `config-json`, `config-xml`, `config-toml` — specifically to keep
   each consumer's dependency graph as lean as possible. See D9.
2. ~~**Which formats first.**~~ **Resolved 2026-07-20:** everything the incumbent
   advertises, plus HOCON, XML and JSONL. Read-only where that is the honest answer. See
   D13 and the phase list.
3. ~~**Does `tomldoc` get built?**~~ **Resolved 2026-07-20:** yes, committed, but phased —
   `config-toml` ships read-only with a writer-shaped hole and `tomldoc` follows. See D16.
4. ~~**Should the conformance suite live in the core or its own module?**~~
   **Resolved 2026-07-20:** in the core as `config/conformance`, stdlib `testing` only. See D17.
5. ~~**INI/properties/dotenv nesting.**~~ **Resolved 2026-07-20:** the rule differs per
   format and only dotenv is ambiguous. INI nests section plus key; properties are already
   dotted; dotenv reuses the environment backend's resolution, exported per D15.
6. ~~**How much real HCL survives D11?**~~ **Resolved 2026-07-20 by survey**, and the
   answer splits cleanly:

   - **Terraform and Terragrunt files largely do not survive it.** Real ones use
     `find_in_parent_folders()`, `${local.environment}`, `toset(var.…)` and `include`
     blocks. A declarative-subset reader gets little from them.
   - **Purpose-built HCL configuration survives it entirely.** A migration DSL in the
     surveyed codebase — labelled blocks, string and list literals, decoded with a *nil*
     evaluation context — passes without a single refusal, because it is variable-free by
     construction. Nomad, Consul, Vault and Packer configuration are the same shape.

   So `config-hcl` serves **HCL as a configuration format** and explicitly does not serve
   Terraform. That is worth stating in the module's own documentation rather than leaving a
   user to discover it: someone pointing it at a `.tf` file should get a clear refusal
   naming the construct, not a puzzling one.
7. ~~**HCL block labels.**~~ **Resolved 2026-07-20:** labels become path segments, so
   `migration "multi_state" "move_redis" { … }` is `migration.multi_state.move_redis`.
   Refusing labelled blocks was considered and rejected — both the surveyed migration DSL
   and Terraform itself use labels structurally, so refusing them would leave the adapter
   able to read almost nothing.

   Two blocks sharing a type and every label collide, and that is an error naming both
   rather than a silent last-one-wins. Repeated *unlabelled* blocks of the same type have no
   dotted-key representation at all and are refused for the same reason.
8. ~~**Should the flat formats share a module after all?**~~ **Resolved 2026-07-20:**
   separate modules, per D9. Their nesting rules genuinely differ, so they share less than
   the grouping suggested.
9. ~~**Which HOCON implementation.**~~ **Resolved 2026-07-20 by survey: none of them.**
   `go-akka/configuration` has not been released since 2020 and carries no tags.
   `gurkankaymak/hocon` is maintained but hardcodes environment fallback at `parser.go:227`
   with no option to disable it, which D14 disqualifies outright — a probe confirmed
   `${LEAKED_SECRET}` resolving straight out of the process environment. It also mangles
   string concatenation: `${base.host}":"${base.port}` yields `h""":"""1` rather than `h:1`.

   HOCON is therefore **deferred**, to be served eventually by a parser of our own. It is the
   one format in this plan that is purely additive — the incumbent never advertised it — so
   deferring costs nothing against the parity claim in D13.
10. ~~**How does `config-toml` gaining write support reach consumers?**~~
    **Resolved 2026-07-20:** as a documented minor version, per D18, which specifies
    capability promotion as a general pattern rather than a TOML-specific concern. Measuring
    it first showed the two cases behave differently and only one is silent.

## Implementation phases

**Phase 0 — the filesystem interface**, specified separately in
[the filesystem abstraction spec](2026-07-20-filesystem-abstraction.md) and delivered as part
of the **core** module rather than as a phase of this work. Every adapter takes a filesystem
in its constructor, so it has to be settled first; and `go-tool-base`, which depends wholly
on this module, migrates once instead of twice by taking it with the Store rewrite.

Everything below is a fast follow after the core module ships with YAML support.

**Phase 1 — the codec seam, no behaviour change.** Extract `Codec`/`EditingCodec`, add
`NewCodecBackend`, reimplement `fileBackend` on top of it as a YAML codec. Done when the
existing suite passes **unmodified** — a test needing adjustment means the refactor changed
something it should not have, and that is the gate rather than a green run.

**Phase 2 — the conformance suite.** Written against the YAML codec first, since its
behaviour is already known-good, then exported for adapters to run against themselves.

**Phase 3 — `config-json`.** JSON and JSONL, read and write. The first real consumer of the
seam, and the test of whether Phase 1 extracted the right thing. Write support here because
JSON is the most-asked-for format and has no comments to lose.

**Phase 4 — `config-toml`**, read-only, with the writer-shaped hole of D16: a named exported
codec type and a fixed constructor signature, so Phase 8 adds methods rather than restructuring.

**Phase 5 — the flat formats**: `config-dotenv`, `config-ini`, `config-properties`.
Read-only. Grouped because they share one problem — a flat key space needing a stated
nesting rule (OQ5) — and solving it once is most of the work. Sequenced here because they
are small, and because closing the advertised-parity gap (D13) is worth more than the
remaining formats individually.

**Phase 6 — `config-xml`**, read-only, per D21: attributes merged into the element
namespace, name collisions and mixed attribute-plus-text elements refused at load.

**Phase 7 — `config-hcl`**, read-only with a writer-shaped hole. Refuses external
parameterisation, function calls and includes at load (D11, D12). Ports the path-traversal
logic from the surveyed package (D19) and delegates any future writing to `hclwrite`. Its
documentation states plainly that it reads HCL-as-configuration and not Terraform.

Sequenced after the simpler adapters so the seam is settled before it meets a hard format,
but note the cost is now known to be lower than TOML's: there is no `hcldoc` to build.

**Phase 8 — `tomldoc` and TOML write support.** The committed fast follow: a
structure-preserving TOML editor, then `config-toml` gains an `EditingCodec` through the hole
left for it in Phase 4. Settle open question 10 before releasing it.

**Phase 9 — revisit.** A HOCON parser of our own, evaluated HCL, XML or flat-format writing,
and anything the earlier phases showed to be wrong.

Phases 1 and 2 are the only ones touching this repository. Everything after is additive and
independently releasable, which is the point.

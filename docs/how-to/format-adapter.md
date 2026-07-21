# Support a new file format

The core reads and writes YAML. Any other file format — JSON, TOML, INI, one of your own —
plugs in through a **codec**, shipped as its own module so no consumer inherits a parser they
do not use. You write the format-specific part; the file machinery — reading, conflict
detection, atomic writes, symlink handling, watching — is already written and shared.

This guide builds a minimal format adapter, reading first then writing, and proves it with the
conformance suite. Each stage stands on its own, so stop wherever your format stops: a
read-only codec is a first-class outcome, not a half-finished one.

!!! tip "The code here is compiled"
    The codec below is the trivial `key=value` format the conformance suite tests itself with,
    in
    [`conformance/conformance_test.go`](https://gitlab.com/phpboyscout/go/config/-/blob/main/conformance/conformance_test.go).
    It is deliberately not a real format — it is the smallest thing that exercises every part of
    the seam, so it is the clearest thing to learn from. The full `Codec` and `EditingCodec`
    definitions are on
    [pkg.go.dev](https://pkg.go.dev/gitlab.com/phpboyscout/go/config#Codec).

## Read: implement a `Codec`

Reading a format is one method — turn bytes into values:

```go
type Codec interface {
	Decode(path string, src []byte) ([]map[string]any, error)
}
```

`Decode` returns one map per document. A format with no multi-document concept returns exactly
one; an empty document is a `nil` entry rather than an omission, so the documents that follow it
keep their index in provenance. For the trivial line format that is one map of the `key=value`
pairs:

```go
type lineCodec struct{}

func (lineCodec) Decode(path string, src []byte) ([]map[string]any, error) {
	values := map[string]any{}

	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%w: %s: line %q has no '='", config.ErrBackendParse, path, line)
		}

		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}

	return []map[string]any{values}, nil
}
```

Decode values with the format's **own** parser — `encoding/json`, a TOML library, your own
scanner. That is the whole job of a read-only codec. A consumer wires it exactly like a file:

```go
config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                        // YAML, the default
	config.WithBackend(config.NewCodecBackend(fsys, "/app.conf", lineCodec{})),
)
```

The layer it produces takes part in precedence, merge, provenance and shadowing exactly as a
YAML one does. Because `lineCodec` cannot edit, the backend is **not** a write target: a write
to a key it defines lands in the writable layer beneath and is reported shadowed, never mangling
a file the codec cannot safely rewrite.

## Write: implement `EditingCodec` (optional)

To make the format writable, add three methods — the same capability split as
`Backend`/`WritableBackend`, one level down:

```go
type EditingCodec interface {
	Codec
	Check(path string, src []byte) error                     // refuse un-editable content at load
	Apply(path string, src []byte, edits []Edit) ([]byte, error) // edit the bytes in place
	Empty() []byte                                           // content of a new, empty document
}
```

`Apply` folds the edits into the source, preserving whatever the format can — key order,
comments, indentation. `Check` runs at **load**, so a source you cannot round-trip safely is
refused before the user has made edits to lose. For the trivial format there is nothing to
refuse and nothing to preserve beyond key order:

```go
type editableLineCodec struct{ lineCodec }

func (editableLineCodec) Check(string, []byte) error { return nil }
func (editableLineCodec) Empty() []byte              { return nil }

func (editableLineCodec) Apply(_ string, src []byte, edits []config.Edit) ([]byte, error) {
	// parse src into ordered key=value pairs, fold in the edits, re-emit.
	// (full body in conformance_test.go)
}
```

That is all. `NewCodecBackend` now returns a writable backend **automatically** — nothing about
how it is constructed changes — and the file machinery does the rest.

!!! warning "You do not touch the file machinery — and that is the point"
    Staging to a temp path, committing by atomic rename, preserving mode, resolving symlinks,
    and **detecting a change that landed between load and write** are the core's job, done once.
    That last one is the subtle trap the seam exists to remove: the conflict fingerprint must be
    taken at load, not at write, or conflict detection silently never fires. Because your codec
    never implements it, your codec cannot get it wrong.

    Keep the **documents-versus-values boundary**: `Decode` produces values; `Apply` edits
    bytes. If your format needs a structure-preserving editor (as YAML uses `yamldoc` and JSON
    uses key-order-aware editing), decode with one path and edit with the other, and never let
    the editor's decoded values reach the read side — the two disagree about scalar types.

## Prove it with the conformance suite

An adapter proves it behaves like a first-class backend by running the exported conformance
suite against its codec, in **one test**:

```go
import "gitlab.com/phpboyscout/go/config/conformance"

func TestConformance(t *testing.T) {
	conformance.Run(t, conformance.Suite{
		Codec:      editableLineCodec{},
		Sample:     []byte("level=info\nregion=eu\n"),
		Defines:    map[string]string{"level": "info", "region": "eu"},
		WriteKey:   "host", WriteValue: "localhost",
	})
}
```

You supply your codec, one valid sample source, and the keys that source exposes (`Defines`).
For an `EditingCodec` you also supply a key it can set (`WriteKey`/`WriteValue`). `Run` executes
one named subtest per contract, so a failure names exactly which one broke:

| Subtest | Checks |
|---|---|
| `capability_split` | the backend is a write target exactly when the codec can edit |
| `decodes_and_merges` | the codec's values merge per-key with a layer of another format |
| `absent_source_tolerated` | a missing source is tolerated for an optional layer |
| `provenance_names_source` | `Origin` names the file a value came from |
| `read_only_skipped_by_routing` | *(read-only codecs)* a write routes past it and is shadowed |
| `write_round_trips` | *(editing codecs)* a write is served and reaches disk |
| `no_edit_is_a_noop` | *(editing codecs)* `Apply` with no edits changes no value |
| `conflict_detected` | *(editing codecs)* a change between load and write is refused with `ErrConflict` |

The suite uses the standard library `testing` package and nothing else — no testify — so running
it adds no assertion-library dependency to your module. It exercises real file semantics through
`config.Dir(t.TempDir())`.

What it deliberately does **not** assert is format-specific: multi-document provenance (only
some formats have documents) and exact scalar types (`8080` as an int or a string). Those belong
in your adapter's own fidelity tests, alongside the conformance run — what your format guarantees
to preserve, and, equally important, what it does not.

## Ship it as its own module

One module per format — `config-json`, `config-toml`, `config-<yours>` — rather than a shared
one, so a consumer needing JSON does not pull a TOML parser into their `go.sum`:

```
module gitlab.com/you/config-<format>

require gitlab.com/phpboyscout/go/config v0.5.0
```

Export a constructor that hides the codec, so a consumer writes
`config.WithBackend(yourformat.New(fsys, path))`, and run the conformance suite in your module's
tests. If you ship read-only first and add writing later, that is a supported lifecycle — a
minor version with a release note, because the routing changes: see the
[format adapters spec](../development/specs/2026-07-20-non-yaml-format-adapters.md).

## Related

- [Backends & capabilities](../explanation/backends.md) — why a file's format is a codec, and
  how the type system decides what a backend can do
- [Write a custom backend](custom-backend.md) — for a source that is **not** a file: Consul, a
  secrets manager, an HTTP endpoint
- [What survives a write](../explanation/write-fidelity.md) — the preservation contract your
  `Apply` is held to

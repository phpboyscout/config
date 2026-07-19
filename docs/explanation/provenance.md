# Provenance

Every configuration debugging session opens with the same question: *why is this value
what it is?* Not "what is `db.host`" — the getter answers that — but "which of the six
things that could have set it actually did, and what else is fighting over it".

Most configuration libraries cannot answer that at all. They merge eagerly: files are
read, layered into one map, and the map is what survives. The moment the fold completes,
the information about who contributed what is gone, and no amount of inspection
afterwards can recover it. You are left with `grep` across your config files and a guess.

This module records provenance **during** the merge, because that is the only moment it
exists. `Origin`, `Shadowed` and `Explain` are how you ask for it back.

## Three questions, three methods

The three are not variations on one another. They answer genuinely different questions,
and reaching for the wrong one is how you end up with a misleading diagnostic.

**`Origin(path) (Source, bool)` — who won?**

```go
if src, ok := view.Origin("db.host"); ok {
    fmt.Println("db.host came from", src)
}
```

One answer, or none. Use it when you need the winning layer as data — to decide which
file to open in an editor, to warn that a value came from the environment rather than
from a reviewed file, to label a settings field as "set by `--host`".

Environment variables and flags are ordinary layers here, so `Origin` will happily
return `env:APP_DB_HOST`. That was a deliberate choice. An earlier design had the Store
answer "which file" and something above it add "…and it is shadowed by the environment",
which splits one question into two halves that a caller then has to reassemble. The
combined answer is strictly more information. A caller who genuinely wants file-only
provenance filters on the kind:

```go
for _, src := range view.Shadowed("db.host") {
    if src.Kind == config.SourceFile {
        // the highest-precedence file defining it is the last such entry
    }
}
```

A dedicated `FileOrigin` accessor was rejected precisely because its difference from
`Origin` is subtle, and subtle differences are how callers pick the wrong one.

**`Shadowed(path) []Source` — who else defines it?**

Every layer that defines the path, in precedence order, lowest first — so the last entry
is the one in effect. This is the list, not the winner.

"Which file do I edit?" and "why is my edit not taking effect?" are the same question
asked from two directions, and both need the whole chain. If you only know the winner
you cannot tell someone that their carefully edited `base.yaml` is being overridden three
layers up; if you only know the losers you cannot tell them what to do about it.

**`Explain(path) string` — say it in one line.**

```go
fmt.Println(view.Explain("db.host"))
// db.host = prod-db (from /etc/app/prod.yaml); also defined in /etc/app/base.yaml
```

`Explain` is for humans. It composes the value, the winning source and the rest of the
chain into a sentence you can print in a CLI, log on startup, or show in a tooltip. When
the path is not set at all it says so plainly rather than returning an empty line:

```
db.host is not set
```

The rule of thumb: `Origin` when you are going to branch on the answer, `Shadowed` when
you need the whole picture, `Explain` when a person is going to read it.

## Why provenance is leaf-only

`Origin("db.host")` answers. `Origin("db")` — where `db` holds several keys — reports
not-found, even though `db` plainly exists and `Get("db")` returns a map.

That is not an omission. A populated subtree is *assembled*: `db.host` might come from
`prod.yaml`, `db.port` from `base.yaml` and `db.password` from the environment. There is
no single source for `db`, so naming one would be a lie — and the most likely lie is the
last layer to touch the subtree, which is the one a user is least likely to want.

Rather than invent an answer, the merge deletes provenance for any path that turns out to
have entries beneath it, and `Origin` reports not-found. `Shadowed("db")` still works and
returns every layer contributing to that subtree, which is what the question was actually
reaching for. `Explain` follows the same rule and says so out loud:

```
db is a subtree assembled from /etc/app/base.yaml, /etc/app/prod.yaml
```

**An empty container is a leaf.** `key: {}` and `key: []` hold a value — just not any
entries — so they carry provenance like any scalar. This follows from the module's wider
position that emptiness is a value and never implies absence: an empty map is present for
`Has`, appears in `Keys()`, and survives a write untouched. Treating it as a leaf for
provenance keeps that consistent; treating it as a subtree would make provenance disagree
with the getters, which is exactly the class of inconsistency this design exists to
remove.

There is a related case worth knowing about. If a higher layer sets a **scalar** at a path
where a lower layer had a whole subtree, the subtree is no longer reachable, so the
provenance of everything beneath it is discarded along with it. Reporting that
`db.host` came from `base.yaml` when `db: disabled` has replaced the lot would send
somebody to edit a value nobody reads.

## Why it must be recorded at merge time

Merging is lossy, and irreversibly so.

Consider two files where the overlay sets `server.port: 9090` and the base sets both
`server.port: 8080` and `server.host: localhost`. After the fold you hold one map:
`{server: {host: localhost, port: 9090}}`. Nothing in that structure records that `port`
came from the overlay and `host` from the base. The overlay's `8080` is not merely
outranked — it is not present in the result at all.

You cannot reconstruct this afterwards. You could re-read every source and re-run the
merge, but then you are not inspecting the configuration you are actually using, you are
inspecting one you have rebuilt and hoped matches — and if a file changed in between, it
does not. You could keep the raw layers and search them on demand, which is closer, but it
duplicates the precedence rules in a second place where they can drift from the first.

So provenance is written as the fold happens. Each time a leaf value is placed into the
accumulator, the source that placed it is recorded against that path; each time a path
turns out to be a populated subtree, its entry is deleted. The provenance map and the
merged values are built by the same pass over the same layers, in the same order, and are
frozen into the same immutable snapshot. They cannot disagree, because there is no second
computation to disagree with.

This is also why a snapshot is the unit that carries provenance rather than the Store.
`Origin` on a snapshot is a map lookup — no I/O, no re-parsing, no locking — and a
snapshot taken an hour ago still answers honestly about the configuration it describes,
not about whatever the files say now.

## Provenance is what makes writes honest

Provenance is not only a diagnostic. It is the input to write routing.

When a change is routed, the Store walks the layers in reverse precedence order and picks
the first **writable** layer that already defines the path — which is a provenance
question. It then asks which layers still outrank that target and also define the path,
and reports them on the resulting operation as `ShadowedBy`. That is how you get told:

> written to `/etc/app/prod.yaml`, but `env:APP_DB_HOST` still wins

A library without provenance can write your value to a file and report success, while the
effective configuration does not move an inch. It has no way to know, and therefore no way
to tell you. See [Write configuration](../how-to/write-config.md) for how to use that
report.

## What provenance does not tell you

Worth stating plainly, so you do not go looking for it:

- **No line numbers.** A `Source` identifies a layer — a file path, a document index
  within that file, an environment variable name, a flag name — not a position within it.
- **A document is a layer.** A file holding several YAML documents contributes one source
  per document, rendered as `config.yaml#1` for the second. Later documents override
  earlier ones. That is *this module's* interpretation, chosen for consistency with how it
  already treats files; the YAML specification describes a multi-document stream as a
  sequence of independent documents and says nothing about overlay.
- **No history.** Provenance describes one snapshot. It does not record what a value used
  to be or which write changed it.
- **Values are not provenance.** `Shadowed` names the layers that define a path; it does
  not tell you what each of them says. If you need the losing values, you have the layer
  identities and can read the files.

## Related

- [The Store](the-store.md) — why one component owns config I/O, and what a snapshot is
- [Write configuration](../how-to/write-config.md) — routing, shadowed writes, conflicts
- [Precedence & merge model](precedence-and-merge.md) — how layers fold together

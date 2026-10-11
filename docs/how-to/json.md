---
title: Read and write JSON
description: Read and write JSON and JSON Lines configuration, structure-preserving, through config-json.
tags: [how-to, formats, json]
---

# Read and write JSON

[JSON Lines](https://jsonlines.org) and structure-preserving JSON writes come from a sibling
module, [`config-json`](https://gitlab.com/phpboyscout/go/config-json), so a consumer who needs
them takes it and one who does not pays nothing.

**Plain JSON reading is already in the core**, and the next section says when that is enough.

```bash
go get gitlab.com/phpboyscout/go/config-json
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	configjson "gitlab.com/phpboyscout/go/config-json"
)

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "/etc/app.yaml"),                    // YAML, the default
	config.WithBackend(configjson.New(fsys, "/etc/app.json")),  // outranks it
	config.WithBackend(configjson.NewLines(fsys, "/var/lib/app/state.jsonl")),
)
```

A JSON layer takes part in precedence, per-key merge, provenance and write routing exactly as a
YAML file does. `New` reads a single JSON document. `NewLines` reads JSON Lines, one object per
line.

## When you do *not* need this

YAML 1.2 is a superset of JSON, so the core's YAML codec **reads a JSON document unaided**.
`WithFiles(fsys, "app.json")` works with no module at all, and the values come back typed as you
would expect.

The core also writes one back, and for the common edit it keeps the layout. A change to a value
that already exists is made in place, so a pretty-printed file stays pretty-printed and a
config file in review does not turn into a one-line diff. `Set("server.port", 9090)` on:

```json
{
  "server": {
    "host": "h",
    "port": 8080
  }
}
```

leaves the file as valid JSON with only the number changed. Removing a key works the same way.

What the core cannot do is add a key to a JSON file and keep it JSON. A new key is written in
YAML's flow style, so `Set("server.host", "x")` on `{"server":{"port":8080}}` produces

```
{"server":{"port":8080, host: x}}
```

which the core can read back and a JSON parser cannot. The core also refuses **JSON Lines**: a
multi-document stream is not a YAML document, and it fails at load with `config.ErrBackendParse`
rather than half-reading it.

So reach for `config-json` when you write new keys into JSON files, or when you read JSON Lines.
Everything else (precedence, merge, provenance, hot reload) is identical either way, because
both are ordinary layers.

## Reading

Reading decodes with the standard library, so it adds no dependency at all. A JSON document's
top level must be an **object**. An array or a scalar cannot carry configuration keys, and is
refused at load with `config.ErrBackendUnsafe` naming the file rather than failing obscurely
later.

```go
port := store.View().GetInt("server.port")
```

## Writing preserves the file

The reason this is not just `json.Marshal` is the reason the whole toolkit exists.
Re-serialising a decoded document **alphabetises the keys and discards the formatting**, which
is the merged-view write that [structure-preserving writes](../explanation/write-fidelity.md)
exist to avoid. So a write edits the document in place:

=== "Before"

    ```json
    {
      "server": {
        "host": "localhost",
        "port": 8080
      }
    }
    ```

=== "After `Set("server.port", 9090)`"

    ```json
    {
      "server": {
        "host": "localhost",
        "port": 9090
      }
    }
    ```

One value changed. An edit to a key that **already exists** leaves every other byte exactly as
it was: key order, indentation, the untouched `host`, the trailing newline.

Adding a **new** key is the one case that touches formatting. The key is inserted and the
document re-indented to its own detected style (two-space, four-space, tabs), because the
alternative is a key jammed in without matching its neighbours. A single-line document stays
single-line.

JSON has no comments to lose, so that is the whole of the fidelity contract: **key order always
survives, and indentation is preserved on an edit and matched on an insertion.** The backend
reports `PreservesComments` as false for that reason. There is nothing to preserve.

A key such as `a.b` is addressed as nested `a` then `b`, as in every other format. Characters
that carry meaning in the underlying path syntax (`*`, `?`, `#`, `@`, `|`) and purely numeric
keys are escaped, so each segment addresses a literal object key and never a wildcard or an
array index.

## JSON Lines is several layers

One line is one document, the same model a multi-document YAML file uses. The line index is the
document index, provenance distinguishes them (`state.jsonl#1`), and a later line outranks an
earlier one for the same key. Each line's top level must be an object. A blank line contributes
no document but still spends its index, so the lines after it keep the identity provenance
reports. An edit targets a specific line and leaves the others byte-identical.

```
shared = from-second (from /var/lib/app/state.jsonl#1); also defined in /var/lib/app/state.jsonl
```

## What it costs

| | |
|---|---|
| Adds | tidwall sjson, gjson, match and pretty, on top of the `config` graph |

Reading is standard-library only. Writing adds `tidwall/sjson` (edit in place) and
`tidwall/pretty` (re-indent an inserted key). These are small packages, asserted by an
allowlist test in the module so an unforeseen transitive addition fails its build. There is no
filesystem library, because you supply the `config.FS`.

## Related

- [Support a new file format](format-adapter.md): write an adapter for a format not yet
  covered, with `config-json` as the worked example
- [What survives a write](../explanation/write-fidelity.md): the preservation contract JSON is
  held to

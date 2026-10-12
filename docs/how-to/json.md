---
title: Read and write JSON
description: Read and write JSON and JSON Lines configuration, structure-preserving, through config-json.
tags: [how-to, formats, json]
---

# Read and write JSON

The core already reads JSON: YAML 1.2 is a superset of JSON, so `WithFiles(fsys, "app.json")`
works with no module at all. [`config-json`](https://gitlab.com/phpboyscout/go/config-json), a
sibling module, exists for the two things the core cannot do: add a key to a JSON file and keep it
JSON, and read [JSON Lines](https://jsonlines.org). A consumer who needs them takes it, and one
who does not pays nothing. [When you don't need this](#when-you-dont-need-this) says where the
line falls.

```bash
go get gitlab.com/phpboyscout/go/config-json
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	configjson "gitlab.com/phpboyscout/go/config-json"
)

store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/app.yaml"),               // real files outrank them
	config.WithBackend(configjson.New(config.OS(), "/etc/app.json")), // outranks it
	config.WithBackend(configjson.NewLines(config.OS(), "/var/lib/app/state.jsonl")),
)
if err != nil {
	return err
}

port := store.View().GetInt("server.port") // 8080
```

with `/etc/app.json` containing:

```json
{
  "server": {
    "host": "localhost",
    "port": 8080
  }
}
```

A JSON layer takes part in precedence, per-key merge, provenance and write routing exactly as a
YAML file does. `New` reads a single JSON document. `NewLines` reads JSON Lines, one object per
line.

## Map JSON to keys

Objects nest, so `server` and `port` give `server.port`. Reading decodes with the standard
library, so it adds no dependency at all; numbers decode as `float64`, and the read path casts
them. A JSON document's top level must be an **object**. An array or a scalar cannot carry
configuration keys, and is refused at load with `config.ErrBackendUnsafe` naming the file rather
than failing obscurely later.

A key such as `a.b` is addressed as nested `a` then `b`, as in every other format. Characters
that carry meaning in the underlying path syntax (`*`, `?`, `#`, `@`, `|`) and purely numeric
keys are escaped, so each segment addresses a literal object key and never a wildcard or an
array index.

### JSON Lines is several layers

One line is one document, the same model a multi-document YAML file uses. The line index is the
document index, provenance distinguishes them (`state.jsonl#1`), and a later line outranks an
earlier one for the same key. Each line's top level must be an object. A blank line contributes
no document but still spends its index, so the lines after it keep the identity provenance
reports. An edit targets a specific line and leaves the others byte-identical.

```
shared = from-second (from /var/lib/app/state.jsonl#1); also defined in /var/lib/app/state.jsonl
```

## Write back to the file

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

Which layer a change lands in is the core's decision, not this adapter's. See
[where a change lands](write-config.md#where-a-change-lands).

## Watch for changes

A JSON or JSON Lines file is watched like any file-backed source: native filesystem notification
where it works, polling where it does not, at the interval you pass to `Store.Watch` with
`config.WithPollInterval` (two seconds by default). See [hot-reload](hot-reload.md).

## Test with a string

`Codec` is exported, so decoding or editing a string needs no store and no filesystem:

```go
docs, err := configjson.Codec{}.Decode("app.json", []byte(`{"server":{"port":8080}}`))
// docs[0]["server"].(map[string]any)["port"] == float64(8080)
```

To test through a store, point `config.Dir(t.TempDir())` at a file you write first.
[Support a new file format](format-adapter.md) covers the conformance suite the adapter itself runs.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `config.ErrBackendParse` | The file, or one line of a JSON Lines file, is not valid JSON. It names the file. | Fix the file. |
| `config.ErrBackendUnsafe` | The top level, or a JSON Lines line, is an array or a scalar rather than an object. | Wrap the values in an object. |

## When you don't need this

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

which the core can read back and a JSON parser cannot. That is tracked as
[yamldoc#25](https://gitlab.com/phpboyscout/go/yamldoc/-/work_items/25). The core also refuses
**JSON Lines**: a multi-document stream is not a YAML document, and it fails at load with
`config.ErrBackendParse` rather than half-reading it.

So reach for `config-json` when you write new keys into JSON files, or when you read JSON Lines.
Everything else (precedence, merge, provenance, hot reload) is identical either way, because
both are ordinary layers.

## What it costs

| | |
|---|---|
| Adds | tidwall sjson, gjson, match and pretty, on top of the `config` graph |

Reading is standard-library only. Writing adds `tidwall/sjson` (edit in place) and
`tidwall/pretty` (re-indent an inserted key). These are small packages, asserted by an
allowlist test in the module so an unforeseen transitive addition fails its build. There is no
filesystem library, because you supply the `config.FS`.

## Related

- [Support a new file format](format-adapter.md): how an adapter like this is built and tested
- [What survives a write](../explanation/write-fidelity.md): the preservation contract JSON is
  held to

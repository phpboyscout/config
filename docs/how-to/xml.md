---
title: Read XML
description: Read XML configuration as a layer through the config-xml sibling module.
tags: [how-to, formats, xml]
---

# Read XML

XML files become a config layer through [`config-xml`](https://gitlab.com/phpboyscout/go/config-xml),
a sibling module: the core's own codec is YAML, and this one adds **nothing** to your dependency
graph, because it reads XML with the standard library. The layer is read-only.

```bash
go get gitlab.com/phpboyscout/go/config-xml
```

```go
import (
	"gitlab.com/phpboyscout/go/config"
	configxml "gitlab.com/phpboyscout/go/config-xml"
)

store, err := config.NewStore(ctx,
	config.WithFiles(config.OS(), "/etc/app.yaml"),               // real files outrank them
	config.WithBackend(configxml.New(config.OS(), "/etc/app.xml")), // outranks it
)
if err != nil {
	return err
}

port := store.View().GetInt("server.port")    // 8080  (an attribute)
host := store.View().GetString("server.host") // "localhost"  (a child element)
```

with `/etc/app.xml` containing:

```xml
<config>
  <server port="8080"><host>localhost</host></server>
</config>
```

## Map XML to keys

Real configuration XML is attribute-heavy (log4j and .NET `appSettings` are almost entirely
attributes), so an attribute reads by the same dotted path a child element would. That is why
`port` and `host` above are both under `server`.

The root element is the container, so it does not appear in the path. Namespace declarations
(`xmlns`, `xmlns:*`) are dropped as XML machinery rather than configuration. Values are strings,
and the read path casts them.

A repeated element becomes a slice, and a single one a scalar that the read path promotes to a
one-element slice when asked. Declare a repeatable element as a slice and it reads correctly
whether the file has one entry or many:

```go
aliases := store.View().GetStringSlice("alias") // one <alias> or several, both decode
```

### What it refuses, at load

Two shapes cannot be a value tree, and are refused with `config.ErrBackendUnsafe` naming the
element rather than silently dropping half of it:

- an **attribute and a child element that share a name**, which are two candidates for one key
  with no basis to choose between them;
- an element with **both text content and attributes or children**, which is at once a scalar
  and a map, and a key cannot be both.

A root element with neither attributes nor children is refused the same way, because it carries
no configuration. This is the same treatment an ambiguous environment variable name gets: two
candidates, no basis to choose, so it says so rather than picking one.

## Read-only, and where writes go

No Go library round-trips XML preserving formatting and comments, so `config-xml` reads and does
not write. A write to a key an XML layer defines is routed past the read-only layer and reported
shadowed rather than failing. With no writable layer at all, the write fails with
`config.ErrNoWritableLayer`.

## Watch for changes

An XML file is watched like any file-backed source: native filesystem notification where it works,
polling where it does not, at the interval you pass to `Store.Watch` with
`config.WithPollInterval` (two seconds by default). See [hot-reload](hot-reload.md).

## Test with a string

`Codec` is exported, so decoding a string needs no store and no filesystem:

```go
docs, err := configxml.Codec{}.Decode("app.xml", []byte(`<config><server port="8080"/></config>`))
// docs[0]["server"].(map[string]any)["port"] == "8080"
```

To test through a store, point `config.Dir(t.TempDir())` at a file you write first.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `config.ErrBackendParse` | The document is not well-formed XML. | Fix the document. |
| `config.ErrBackendUnsafe` | An attribute and a child element share a name, an element mixes text with attributes or children, or the root carries nothing. The error names the element. | Rename one of the pair, or move the text into its own child element. |
| `config.ErrNoWritableLayer` | An `Apply` set a key and no layer in the store can be written. | Add a writable layer, such as a YAML file. |

## When you don't need this

XML here is **read-only**, so it cannot be the layer your application writes back to. If you
own the format, YAML or JSON will serve you better in every respect.

Reach for XML when the file is not yours: an existing enterprise or legacy document you have to
consume where it is, rather than convert and keep in step.

## What it costs

| | |
|---|---|
| Adds | **nothing** beyond the `config` graph, and an allowlist test in the module fails if that changes |

`encoding/xml` is standard library, so there is no parser module, and no filesystem library
either because you supply the `config.FS`.

## Related

- [Support a new file format](format-adapter.md): how an adapter like this is built and tested
- [Backends & capabilities](../explanation/backends.md): why a read-only layer is skipped by
  write routing

---
title: What survives a write
description: What survives a write — comments, key order, quoting and anchors — and why the file stays yours.
tags: [explanation, writes, fidelity]
---

# What survives a write

A configuration file is a document a person wrote, not a serialisation of a data
structure. It carries comments explaining why a value is what it is, an order that groups
related settings, anchors that avoid repeating a block, and formatting choices someone
made on purpose. A write that discards all of that has technically persisted the change
and practically vandalised the file.

So writing does not serialise the merged view over the top of your file. Each target
document is **edited in place**: parsed into a structure that retains everything the
parser saw, modified at the node addressed, and written back out.

## The contract

**Guaranteed.** The data structure itself, and comments staying attached to the keys they
describe. Key order, quoting style, block scalars, anchors, aliases and merge keys are
preserved. Repeated writes converge rather than drifting further from the original each
time.

**Not guaranteed.** Blank lines, indentation, comment alignment, the `---` marker on a
single-document file, or byte-for-byte identity. Comment *style* is yours to choose;
comment *retention* is what is promised. Normalising style on write — including flow
style to block style — is within the contract.

The line between those two lists is drawn at meaning. Anything that changes what the
document says, or what a reader understands it to say, is preserved. Anything that is
purely how it is laid out may be normalised, because guaranteeing byte-level identity
would mean never being able to fix anything about the layout.

## Two consequences

### Some documents are refused at load

A multi-line flow collection with interior comments cannot be round-tripped safely — the
closing delimiter is swallowed into the comment, producing YAML that no parser will
accept.

Rather than let you discover that at commit time, after you have made your edits and have
nowhere to put them, `NewStore` refuses such a source up front with `ErrBackendUnsafe`,
naming the file and the offending construct. Reformat the collection onto one line, or
move the comments out of it.

Failing at load is the kinder failure. The alternative is a program that starts fine,
runs fine, and destroys a file the first time a user changes a setting.

### Invisible characters are escaped on write

Every character a reader can see survives verbatim — emoji, CJK, accented Latin, Greek,
Cyrillic. Bidirectional controls and the invisible-space family (zero-width space, word
joiner, soft hyphen and the rest) are written as escapes instead.

This is a security property rather than a formatting choice. Those characters make a
document render one way and parse another: text that reads as one thing to the person
reviewing it and means another to the machine. That is the Trojan Source construct
([CVE-2021-42574](https://nvd.nist.gov/vuln/detail/CVE-2021-42574)), and a configuration
file — reviewed by eye, trusted by default, and often the thing that decides where
traffic goes — is exactly where it matters.

The escaping is lossless: decoding returns the original string byte for byte, so a value
containing a zero-width joiner still reads back containing one. The file will simply look
different from what you fed it, which is the point — an invisible character becomes
visible.

## Why replacing a map is different

Setting a map-valued key replaces the node it addresses, and comments, anchors and block
styles *within* that subtree may not survive. That is not a gap in the contract above; it
is what you asked for.

By supplying a whole map you assert ownership of that subtree. The alternative —
deep-merging it — would make "this subtree is now exactly this" inexpressible, which is
precisely what a consumer replacing a catalogue needs to say.

The cost is real. A `themes:` subtree carrying two anchors, eight aliases, eighteen
comment lines and nine block scalars becomes, after a whole-subtree replace, six literal
expanded copies of what was a deliberately DRY structure. Nothing was lost in the sense
of information, and everything was lost in the sense of authorship.

If you know what changed, say so precisely: diff before against after and issue targeted
`Set` and `Remove` calls. See
[setting a map replaces the whole subtree](../how-to/write-config.md#setting-a-map-replaces-the-whole-subtree).

## Related

- [Write configuration](../how-to/write-config.md) — the recipes
- [Backends & capabilities](backends.md) — what a backend must support to be written to
- [The Store](the-store.md) — why one component owns every write

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
document is **edited in place** by yamldoc: the bytes of the edit's footprint are
replaced, and every other byte is left exactly as it was.

## The contract

**Guaranteed.** Every byte you did not change: comments and the column they were aligned
to, blank lines, indentation, key order, quoting style, block scalars, anchors, aliases
and merge keys, the `---` marker, line endings, a byte order mark. A comment keeps the
key it belongs to: one directly above a key or on its line goes with that key when the
key is removed; one separated from the key by a blank line, or after the last key of a
block, stays. Repeated writes cannot drift, because an unedited byte is never rewritten.

**Changed by a write, and only by a write.** The value you set, spelt so that it reads
back as the type you gave: a string stays a string however it looks, a float stays a
float. A new key, appended after the last existing key in the block style and indentation
of the file around it. The keys a map-valued `Set` did not mention, which are removed.

The line between those two lists is drawn at the edit. yamldoc re-parses every write and
checks it against an independent statement of what the edit meant, comment ownership and
untouched bytes included, before anything reaches the file; a write that would change
more than it was asked to is refused, and the file is left as it was.

## Two consequences

### Some documents are refused at load

A file that is not YAML fails to parse, with `ErrBackendParse`. A file that is YAML but
does not mean anything under its schema, such as one whose alias names no anchor or whose
mapping has two keys that are the same value spelt differently, cannot be edited safely:
every write that depended on the broken part would be refused.

Rather than let you discover that at commit time, after you have made your edits and have
nowhere to put them, `NewStore` refuses such a source up front with `ErrBackendUnsafe`,
naming the file and the problem. Fix the alias or the duplicate key.

Failing at load is the kinder failure. The alternative is a program that starts fine,
runs fine, and refuses a file the first time a user changes a setting.

### Invisible characters are escaped on write

Every character a reader can see survives verbatim — emoji, CJK, accented Latin, Greek,
Cyrillic. Bidirectional controls and the invisible-space family (zero-width space, word
joiner, soft hyphen and the rest) in a value this module *writes* are escaped instead. A
value already in the file is the author's and stays as it was; every untouched byte does.

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
- [Limitations](../reference/limitations.md#writes) — what a write does not promise

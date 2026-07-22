# History

This module did not start as a configuration system. It started as a thin
wrapper over [Viper](https://github.com/spf13/viper), written many years ago to
do one thing Viper left to the caller: load several configuration files and
merge them automatically, in a defined order, without every project
reimplementing the loop.

Viper was the obvious foundation, and it was the right one. It did everything
that was wanted and a great deal more besides. The wrapper existed only to close
the small distance between what Viper offered and what one project needed.

## Open sourced, then carried

The package was published at
[github.com/phpboyscout/config](https://github.com/phpboyscout/config), where it
stayed stable for a long time — small tweaks, the occasional fix, and a series
of private forks as it travelled into different companies alongside its author.
That is an unglamorous kind of longevity, and a telling one: code that gets
carried from job to job is code that keeps earning its place.

Through all of it the shape stayed the same. Viper did the work; the wrapper
added multi-file loading and merging, and later a small interface so consuming
code depended on a contract rather than on a library.

## `go-tool-base`, and the gaps

When `go-tool-base` began in March 2026, the container came with it and got the
attention a shared foundation deserves. This is where the wrapper stopped being
only about loading files and started closing gaps in earnest: typed sections
that stayed current across a reload, struct-tag validation, hot-reload that
failed closed rather than swapping in a broken configuration, a filesystem
abstraction so any of it could be tested.

Each of those was a gap being worked around. Individually each workaround was
reasonable. Collectively they were beginning to describe a different component
than the one being wrapped.

## The straw

In July 2026 the container was extracted into this module, and an issue was
raised against it: writes should preserve comments, key order and formatting,
and should change only the file that actually owns the key.

That request was not, on its own, exotic. What made it decisive is that it could
not be satisfied by wrapping. Viper writes by re-encoding its resolved view of
the world, which means a saved file loses every comment its author wrote and
gains every value that came from somewhere else — including secrets that arrived
through the environment. No amount of wrapping changes that, because the
information needed to do better is discarded during the merge, long before a
write is contemplated.

The issue was the straw rather than the cause. The workarounds had been
accumulating for years.

## What replaced it

Investigating the request properly meant asking what the module actually needed
rather than what could be layered on top, and the answer turned out to be a
different architecture: one component that owns config I/O end to end, so that
provenance can be recorded while merging rather than reconstructed afterwards,
and so a write can be aimed at the layer that owns the key.

That is the [Store](../explanation/the-store.md). Along the way the document
layer became [`yamldoc`](https://yamldoc.go.phpboyscout.uk), a separate module
for editing YAML without destroying what a person wrote in it, and Viper — by
then reduced to resolving typed values from data handed to it — turned out to
have nothing left to do that was not already being done directly by the two
libraries it wraps for the purpose.

So it was removed. Not as a verdict on Viper, but because the module had
gradually stopped being a wrapper.

## On Viper

Viper is an excellent library and remains one. It does upwards of ninety-five
per cent of what most projects will ever need from configuration, and it makes
that ninety-five per cent simple and efficient to reach. Any project that fits
inside it should use it and get on with something more interesting.

This module exists because a particular set of requirements — comment-preserving
layer-correct writes, per-key provenance, multi-document files as first-class
layers — sits outside what Viper sets out to do. That is a difference in scope,
not in quality.

None of this would exist without it. A decade of Viper being dependable is what
made it reasonable to build on for so long, and what made it possible to
eventually see clearly what a replacement would have to do. That is a good
pedigree to inherit.

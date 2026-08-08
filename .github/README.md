# config

**Layered configuration for Go that can tell you where a value came from — and
write one back without wrecking the file.** Files, environment, flags and remote
backends merged into one store, with provenance for every value and
structure-preserving writes.

> **This is a read-only mirror. The canonical repository is on GitLab:**
> **https://gitlab.com/phpboyscout/go/config**
>
> Issues and merge requests are handled there.

## Installing

```
go get gitlab.com/phpboyscout/go/config
```

The module path is the GitLab one. `go get github.com/phpboyscout/config` will
not work: that path was an older, separate module, and this repository no longer
declares it.

File formats, filesystems and dynamic backends are separate modules, so you only
compile the ones you use. They are listed at **https://go.phpboyscout.uk**.

## Documentation

Full documentation: **https://config.go.phpboyscout.uk**

---
title: Read & write config over SFTP
description: Point config at a configuration file on a remote host over SSH, through an sftp.Client you own.
tags: [how-to, filesystem, sftp, ssh, remote]
---

# Read & write config over SFTP

When a tool's configuration lives on a **remote host reached over SSH**, the
[`config-sftp`](https://gitlab.com/phpboyscout/go/config-sftp) sibling module wraps an
[`*sftp.Client`](https://pkg.go.dev/github.com/pkg/sftp) as a `config.FS`.

```bash
go get gitlab.com/phpboyscout/go/config-sftp
```

You build and configure the SSH connection and the SFTP client — every host, key, known-hosts and
timeout decision stays yours — and hand the client in:

```go
import (
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"gitlab.com/phpboyscout/go/config"
	configsftp "gitlab.com/phpboyscout/go/config-sftp"
)

conn, _ := ssh.Dial("tcp", "host:22", sshConfig)   // your SSH config, your host-key policy
client, _ := sftp.NewClient(conn)
defer client.Close()

store, err := config.NewStore(ctx,
	config.WithFiles(configsftp.Wrap(client), "/etc/app/config.yaml"),
	config.WithEnv("APP"),
)
```

`Wrap` is the whole API, and the layer reads **and writes** — a `store.Apply` stages beside the
target and renames over it, so a remote reader never sees a half-written file.

## Atomicity depends on the server

The commit prefers the `posix-rename@openssh.com` extension for an **atomic overwrite** where the
server advertises it (OpenSSH does), and falls back to a plain rename otherwise, with a narrow
non-atomic window documented in the module. The atomicity is ultimately the remote server's
implementation of `rename(2)` over its own storage — the adapter prefers the strongest primitive
available but cannot enforce what the far side does.

## Hot-reload is polled, at a sensible cadence

An SFTP host has no real local path, so it is watched by **polling** rather than `fsnotify`. Because
each poll is a network round-trip, `config-sftp` declares a **15-second** default cadence through
`config.PollIntervalHinter` — far calmer than the 2-second local default, and overridable:

```go
store, _ := config.NewStore(ctx,
	config.WithFiles(configsftp.Wrap(client), "/etc/app/config.yaml"),
	config.WithPollInterval(30*time.Second),   // your call wins over the 15s hint
)

stop, _ := store.Watch(ctx)   // re-reads over SFTP on the chosen cadence
defer stop()
```

## What it costs

`github.com/pkg/sftp` and `golang.org/x/crypto` (for the SSH transport), plus `config` and what it
already brings — asserted by an allowlist test in the module. The tests run against an in-process
SFTP server, so they need no external host and no Docker.

## Related

- [How filesystem adapters work](../explanation/filesystem-adapters.md) — real-path vs polled
  reload, and the poll-interval hint
- [Hot-reload safely](../explanation/hot-reload-safety.md) — how the Store coalesces and stays quiet
  when nothing changed
- [Backends & capabilities](../explanation/backends.md) — the `config.FS` interface

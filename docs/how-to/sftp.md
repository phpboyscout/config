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

You build and configure the SSH connection and the SFTP client, so every host, key, known-hosts
and timeout decision stays yours, and hand the client in:

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

`Wrap` takes the client and returns a `config.FS`. The layer reads **and writes**: a
`store.Apply` stages beside the target and renames over it, so a remote reader never sees a
half-written file.

You opened the client, so you close it. `Wrap` returns a plain `config.FS` with no `Close`.

## When you do *not* need this

If the file can be fetched at deploy time, or the remote path is already mounted locally, the
core reads it with no adapter and no SSH dependency at runtime.

Reach for SFTP when the file must be read **where it lives**: because it changes independently
of your deploys, or because your process is the thing that writes it back.

## Atomicity depends on the server

The commit prefers the `posix-rename@openssh.com` extension for an **atomic overwrite**, and
`Wrap` checks once, from the live connection, whether the server advertises it. OpenSSH does.
Without the extension the adapter tries a plain rename. If that fails because the target
exists, it removes the target and renames again, which leaves a narrow non-atomic window.

The adapter prefers the strongest primitive available, but it cannot enforce what the far side
does with `rename(2)` over its own storage.

New files are created at the server's umask and then narrowed to the requested mode with a
`Chmod` **before** any content is written, so a staged file holding credentials is never
briefly readable at the umask default.

## Hot-reload is polled, at a sensible cadence

An SFTP host has no real local path, so the wrapper does not satisfy `config.RealPather` and
the Store watches it by **polling** rather than `fsnotify`. Each poll is a network round-trip,
so `config-sftp` declares a **15-second** default cadence through `config.PollIntervalHinter`.
That is far calmer than the 2-second local default.

`WithPollInterval` is a **watch option**, passed to `Watch` and not to `NewStore`. Setting it
explicitly overrides the hint:

```go
store, _ := config.NewStore(ctx,
	config.WithFiles(configsftp.Wrap(client), "/etc/app/config.yaml"),
)

stop, _ := store.Watch(ctx, config.WithPollInterval(30*time.Second))   // your call wins over the 15s hint
defer stop()
```

When a poll sees a change, the Store waits for a settle window (250ms by default, bounded at
four windows) so a burst of writes becomes one reload, then compares values and notifies
observers only if the configuration actually changed. [React to changes with
hot-reload](hot-reload.md) covers the options.

A write through a remote symlink lands on the link's target rather than replacing the link,
because the wrapper satisfies `config.LinkReader`.

## Getting a client

Building the `*sftp.Client` yourself is the default. One further rung exists, for when you hold
an `*ssh.Client` and want the adapter to open the SFTP subsystem:

```go
conn, err := ssh.Dial("tcp", host, sshCfg)   // yours; you close it
defer conn.Close()

fsys, err := configsftp.FromSSH(conn)

store, err := config.NewStore(ctx,
	config.WithFiles(fsys, "config.yaml"),
	config.WithCloser(fsys))                  // closing the store closes the SFTP subsystem only
defer store.Close()
```

`FromSSH` returns a `*configsftp.OwnedFS`, because it opened the subsystem and so owes it a
`Close`. Handing it to `config.WithCloser` makes the Store close it. `Close` is safe to call
more than once. A nil connection returns `configsftp.ErrNoConn`, and
`configsftp.WithClientOptions(...)` passes `sftp.ClientOption` values such as `MaxPacket`
through to `sftp.NewClient`.

`FromSSH` **does I/O**, unlike the equivalent rung in the other adapters. An SFTP client is a
subsystem channel plus a version handshake, so obtaining one is a round trip by definition. The
connection is already established, so it is one exchange rather than a dial, and a failure
names the subsystem rather than surfacing later as a confusing read error.

Closing it releases the subsystem and **never your SSH connection**. That transport is yours
and may carry other things. Close the store before the connection, as the `defer` order above
does.

### There is deliberately no zero-conf rung

SFTP *has* an ambient convention (an agent, `~/.ssh/config`, `known_hosts`) and this adapter
deliberately does not take it, because using it would mean choosing a **host-key verification
policy** on your behalf. Trust-on-first-use silently accepts a man in the middle. Requiring
`known_hosts` fails exactly the unattended machines most likely to want zero configuration.
Skipping verification is not something a configuration library should ship.

Build the `ssh.ClientConfig` yourself, decide your own `HostKeyCallback`, and hand over the
connection.

## What it costs

| | |
|---|---|
| Adds | pkg/sftp and its filesystem walker, on top of the `config` graph |

That is `github.com/pkg/sftp` and `golang.org/x/crypto` (for the SSH transport), plus `config`
and what it already brings. An allowlist test in the module asserts it. The tests run against
an in-process SFTP server, so they need no external host and no Docker.

## Related

- [How filesystem adapters work](../explanation/filesystem-adapters.md): real-path against
  polled reload, and the poll-interval hint
- [Hot-reload safely](../explanation/hot-reload-safety.md): how the Store coalesces changes
  and stays quiet when nothing changed
- [Backends & capabilities](../explanation/backends.md): the `config.FS` interface

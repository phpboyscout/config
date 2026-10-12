---
title: Read and write config over SFTP
description: Point config at a configuration file on a remote host over SSH, through an sftp.Client you own.
tags: [how-to, filesystem, sftp, ssh, remote]
---

# Read and write config over SFTP

When a tool's configuration lives on a **remote host reached over SSH**, the
[`config-sftp`](https://gitlab.com/phpboyscout/go/config-sftp) sibling module wraps an
[`*sftp.Client`](https://pkg.go.dev/github.com/pkg/sftp) as a `config.FS`. You build the SSH
connection and the SFTP client, so every host, key, known-hosts and timeout decision stays yours,
and hand the client in.

```bash
go get gitlab.com/phpboyscout/go/config-sftp
```

```go
import (
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"gitlab.com/phpboyscout/go/config"
	configsftp "gitlab.com/phpboyscout/go/config-sftp"
)

conn, err := ssh.Dial("tcp", "host:22", sshConfig) // your SSH config, your host-key policy
if err != nil {
	return err
}
defer conn.Close()

client, err := sftp.NewClient(conn)
if err != nil {
	return err
}
defer client.Close()

store, err := config.NewStore(ctx,
	config.WithFiles(configsftp.Wrap(client), "/etc/app/config.yaml"),
	config.WithEnv("APP"),
)
if err != nil {
	return err
}

host := store.View().GetString("db.host") // from host:/etc/app/config.yaml
```

The remote file is read, merged, written and watched exactly as a local file would be.

## Connect over SSH

Building the `*sftp.Client` yourself is the default. One further rung exists, for when you hold
an `*ssh.Client` and want the adapter to open the SFTP subsystem:

| Use | When |
|---|---|
| `Wrap(client)` | You built the `*sftp.Client`. You opened it, so you close it; `Wrap` returns a plain `config.FS` with no `Close`. |
| `FromSSH(conn)` | You hold an `*ssh.Client` and want the adapter to open the SFTP subsystem. Close what it returns. |

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

## Map remote files to keys

The `config.FS` name is the remote path, and the config keys come from the file's contents, parsed
by the same codec a local file would use. A write through a remote symlink lands on the link's
target rather than replacing the link, because the wrapper satisfies `config.LinkReader`.

## Write back to the remote file

The layer reads **and writes**: a `store.Apply` stages beside the target and renames over it, so a
remote reader never sees a half-written file.

### Atomicity depends on the server

The commit prefers the `posix-rename@openssh.com` extension for an **atomic overwrite**, and
`Wrap` checks once, from the live connection, whether the server advertises it. OpenSSH does.
Without the extension the adapter tries a plain rename. If that fails because the target
exists, it removes the target and renames again, which leaves a narrow non-atomic window.

The adapter prefers the strongest primitive available, but it cannot enforce what the far side
does with `rename(2)` over its own storage.

New files are created at the server's umask and then narrowed to the requested mode with a
`Chmod` **before** any content is written, so a staged file holding credentials is never
briefly readable at the umask default.

## Watch for changes

An SFTP host has no real local path, so the wrapper does not satisfy `config.RealPather` and
the Store watches it by **polling** rather than `fsnotify`. Each poll is a network round-trip,
so `config-sftp` declares a **15-second** default cadence through `config.PollIntervalHinter`.
That is far calmer than the 2-second local default.

The adapter has no interval option of its own. `config.WithPollInterval` is a **watch option**,
passed to `Watch` and not to `NewStore`, and setting it explicitly overrides the hint:

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

## Test without a remote host

`pkg/sftp` ships its own server, so a test can speak the real SFTP wire protocol in one process,
with no network and no `sshd`. Join `sftp.NewServer` to `sftp.NewClientPipe` over two `io.Pipe`s and
wrap the client. The server serves the local filesystem, so use paths under a `t.TempDir()`:

```go
clientRead, serverWrite := io.Pipe()
serverRead, clientWrite := io.Pipe()

server, err := sftp.NewServer(struct {
	io.Reader
	io.WriteCloser
}{serverRead, serverWrite})
if err != nil {
	t.Fatal(err)
}
go func() { _ = server.Serve() }()

client, err := sftp.NewClientPipe(clientRead, clientWrite)
if err != nil {
	t.Fatal(err)
}
t.Cleanup(func() {
	_ = server.Close() // first: closing the client first blocks forever
	_ = client.Close()
})

fsys := configsftp.Wrap(client)
```

Close the server before the client. The server's `Close` hands the client's receive loop the EOF it
waits on, and `Serve` returning does not.

The module's own suite runs this way, so it needs no external host and no Docker. A
higher-fidelity suite that dials a real SSH server on a loopback socket runs in the same `go test`
when `SFTP_TEST_LOOPBACK=1` is set.

## Errors you'll see

| Error | Cause | Fix |
|---|---|---|
| `config.ErrConflict` | The remote file changed between load and write. | Reload and apply the change again. |
| `configsftp.ErrNoConn` | `FromSSH` got a nil `*ssh.Client`. | Pass the established connection. |
| `fs.ErrNotExist` | The remote file is absent. | Check the path. The store tolerates it as an empty layer unless it is the first source and you set `RequireFirstSource`. |
| A permission error from the server | The SSH user cannot read the file, or write beside it to stage. | Grant the user access to the file and its directory. |

## When you don't need this

If the file can be fetched at deploy time, or the remote path is already mounted locally, the
core reads it with no adapter and no SSH dependency at runtime.

Reach for SFTP when the file must be read **where it lives**: because it changes independently
of your deploys, or because your process is the thing that writes it back.

## What it costs

| | |
|---|---|
| Adds | pkg/sftp and its filesystem walker, on top of the `config` graph |

That is `github.com/pkg/sftp` and `golang.org/x/crypto` (for the SSH transport), plus `config`
and what it already brings. An allowlist test in the module asserts it.

## Related

- [How filesystem adapters work](../explanation/filesystem-adapters.md): real-path against
  polled reload, and the poll-interval hint
- [Hot-reload safely](../explanation/hot-reload-safety.md): how the Store coalesces changes
  and stays quiet when nothing changed
- [Backends & capabilities](../explanation/backends.md): the `config.FS` interface

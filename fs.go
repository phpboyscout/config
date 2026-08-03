package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gitlab.com/phpboyscout/go/errors"
)

// ErrReadOnlyFS is returned by the write methods of a read-only [FS] —
// WriteFile, Rename, Remove and MkdirAll. A filesystem adapter over an
// inherently read-only source (an io/fs.FS, an HTTP URL) returns it so a caller
// can errors.Is it, rather than a bare fs.ErrPermission that a genuine
// permission failure is indistinguishable from. See the filesystem adapters
// spec, D4.
var ErrReadOnlyFS = errors.NewSentinel("config.read_only_f_s", "config: filesystem is read-only")

// FS is the filesystem surface this module needs, and the whole of it.
//
// It is deliberately small. Six operations is what loading, watching and
// writing configuration actually requires, and stating them exactly is what
// lets a caller supply the operating system, a rooted directory, an in-memory
// filesystem or their own implementation without inheriting a dependency
// chosen on their behalf.
//
// The standard library has no equivalent. [io/fs.FS] is read-only by design —
// no write, no rename, no remove — so it cannot express a module whose central
// feature is writing configuration back. [os.Root] has every operation needed
// and is hardened against escaping its root, but it is a concrete type with no
// in-memory implementation, so nothing can substitute for it in a test.
//
// Adding a method here is a breaking change for every implementation, so
// capability beyond this minimum is expressed as an optional interface —
// [RealPather], [LinkReader] and [DirLister] — that an implementation may simply
// not satisfy. See D1 of the filesystem abstraction spec.
type FS interface {
	// ReadFile returns a source's contents, or an error satisfying
	// errors.Is(err, fs.ErrNotExist) when it is absent. That distinction is
	// load-bearing: a missing overlay is normal and a missing base file is a
	// broken installation, and only the Store can decide which.
	ReadFile(name string) ([]byte, error)

	// WriteFile replaces a file's contents.
	WriteFile(name string, data []byte, perm fs.FileMode) error

	// Stat reports a file's metadata. Used to preserve permissions across a
	// write and to confirm a path is the file the watcher thinks it is.
	Stat(name string) (fs.FileInfo, error)

	// Rename moves a file, and is how a write is committed: content is staged
	// beside the target and renamed over it, so a reader never observes a
	// half-written file.
	Rename(oldpath, newpath string) error

	// Remove deletes a file, used to discard staged content that was never
	// committed.
	Remove(name string) error

	// MkdirAll creates a directory and any missing parents, for writing a
	// configuration file into a directory that does not exist yet.
	MkdirAll(path string, perm fs.FileMode) error
}

// RealPather optionally reports the operating-system path backing a name.
//
// Native filesystem notification works on real paths, so a filesystem that has
// one can be watched by fsnotify, and one that does not — an in-memory
// filesystem, a network abstraction — is polled instead.
//
// This replaced a switch on concrete afero types, which could not see through a
// wrapper it did not recognise: a consumer decorating the real filesystem with
// their own type silently lost native notification and got polling with no
// diagnosis. Asking the filesystem about itself has no such blind spot.
//
// A real path is a claim, not proof. The watcher still confirms that both sides
// see the same file before pointing fsnotify at it, because a base-path wrapper
// over an in-memory filesystem produces real-looking paths for files that exist
// only in memory.
//
// Implement this only when the filesystem genuinely has operating-system paths.
// Satisfying it unconditionally — returning false from the method instead of not
// having the method — makes every filesystem look watchable, so native
// notification is selected and the absence of anything to watch is discovered
// one path at a time. An implementation must not satisfy an optional interface
// it cannot honour.
type RealPather interface {
	RealPath(name string) (realPath string, ok bool)
}

// DirLister optionally enumerates a directory.
//
// [FS] deliberately has no listing method: every source it was built for is
// named, so a backend asks for the file it was configured with rather than
// discovering what is there. A backend whose whole input is a *directory* — a
// mounted ConfigMap, a secrets directory, one file per key — needs the other
// shape, and this is it.
//
// Optional rather than part of FS, for the reason [RealPather] is: adding a
// method to FS breaks every filesystem adapter in the family at once, and most
// of them exist to serve named files. A filesystem that cannot enumerate — or
// one for which enumeration would be unbounded, such as an object store with no
// delimiter — simply does not have the method.
//
// The same rule applies as to the other optional interfaces: an implementation
// must not satisfy one it cannot honour. Returning an error from ReadDir rather
// than omitting the method makes every filesystem look listable, so a consumer
// selects it and discovers the absence one directory at a time.
//
// ReadDir reports the directory's entries without following into them. An
// absent directory returns an error satisfying errors.Is(err, fs.ErrNotExist),
// so a caller can tell "nothing configured here yet" from a real failure.
type DirLister interface {
	ReadDir(name string) ([]fs.DirEntry, error)
}

// LinkReader optionally resolves a symbolic link.
//
// A configuration file reached through a symlink should be written through it —
// editing the link itself would replace it with a regular file and detach every
// other path that pointed at the target. A filesystem with no notion of links
// does not implement this, and paths are used as given.
type LinkReader interface {
	Readlink(name string) (string, error)
}

// PollIntervalHinter optionally reports how often a filesystem that must be
// polled should be checked for changes.
//
// A filesystem with no real path is watched by polling (see [RealPather]), and
// the default cadence suits a local one. But for a remote filesystem — an object
// store, a network share — each poll is a round-trip, and often a billed API
// call, so the 2-second default is far too eager. Such a filesystem implements
// this to declare a sensible default (minutes, not seconds), which the watcher
// adopts unless the consumer sets one explicitly with [WithPollInterval] — an
// explicit interval always wins.
//
// Implement this only when the filesystem genuinely wants a non-default cadence;
// a local or in-memory filesystem should not, so it keeps the responsive
// default.
//
// A [WatchableBackend] may implement this too, and [Store.Watch] consults it the
// same way: a backend polling a remote store (a parameter store, a KV service)
// declares a slower cadence than the local-file default, and an explicit
// [WithPollInterval] still overrides it.
type PollIntervalHinter interface {
	PollInterval() time.Duration
}

// OS returns an FS backed by the operating system.
func OS() FS { return osFS{} }

// osFS is the operating system, unrooted.
type osFS struct{}

func (osFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

func (osFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(name, data, perm)
}

func (osFS) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }

func (osFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

func (osFS) Remove(name string) error { return os.Remove(name) }

func (osFS) MkdirAll(path string, perm fs.FileMode) error { return os.MkdirAll(path, perm) }

// RealPath is the identity: an operating-system path is already one.
func (osFS) RealPath(name string) (string, bool) { return name, true }

func (osFS) Readlink(name string) (string, error) { return os.Readlink(name) }

func (osFS) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }

// Dir returns an FS rooted at a directory.
//
// Every operation is confined to the directory: a path resolving outside it —
// through "..", an absolute path, or a symlink pointing away — is refused by
// the operating system rather than by a check this module performs. A tool
// reading configuration from a directory the user named gets that containment
// without asking for it.
//
// The root is opened per operation rather than held open. An earlier version
// kept the handle for the FS's lifetime, which read as a feature — the root
// survived being renamed underneath it — and was a file-descriptor leak: FS has
// no Close, nothing in the module closes a filesystem, and a caller that builds
// one per unit of work accumulates descriptors until the process ends. Measured
// at a default 1024-descriptor limit it failed after 1021 calls, and at the 256
// typical of macOS after 253 — which the documented testing pattern of
// config.Dir(t.TempDir()) per test would reach in a large suite.
//
// The cost is one extra openat per operation. Configuration is read at startup,
// on reload and on write, so that is not a hot path; a leak that surfaces only
// at scale is the worse trade.
//
// **Paths are relative to the root.** Pass "config.yaml", not
// "/etc/app/config.yaml" — an absolute path is treated as an attempt to escape
// and fails with "path escapes from parent" rather than fs.ErrNotExist. That
// distinction matters to the Store, which treats a missing optional source as
// normal and anything else as fatal, so an absolute path turns a file that is
// merely absent into a hard failure.
func Dir(path string) (FS, error) {
	// Opened and closed immediately, so a path that is not a usable directory
	// fails here rather than at the first read.
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}

	if err := root.Close(); err != nil {
		return nil, err
	}

	return rootFS{path: path}, nil
}

// rootFS confines every operation to one directory tree.
//
// It holds the path rather than an open handle; see [Dir] for why.
type rootFS struct{ path string }

// with opens the root, runs fn against it, and closes it again.
//
// Every method goes through here, so there is exactly one place a descriptor is
// acquired and exactly one place it is released.
func (r rootFS) with(fn func(*os.Root) error) error {
	root, err := os.OpenRoot(r.path)
	if err != nil {
		return err
	}

	defer func() { _ = root.Close() }()

	return fn(root)
}

func (r rootFS) ReadFile(name string) ([]byte, error) {
	var out []byte

	err := r.with(func(root *os.Root) error {
		var readErr error

		out, readErr = root.ReadFile(name)

		return readErr
	})

	return out, err
}

func (r rootFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return r.with(func(root *os.Root) error { return root.WriteFile(name, data, perm) })
}

func (r rootFS) Stat(name string) (fs.FileInfo, error) {
	var out fs.FileInfo

	err := r.with(func(root *os.Root) error {
		var statErr error

		out, statErr = root.Stat(name)

		return statErr
	})

	return out, err
}

func (r rootFS) Rename(oldpath, newpath string) error {
	return r.with(func(root *os.Root) error { return root.Rename(oldpath, newpath) })
}

func (r rootFS) Remove(name string) error {
	return r.with(func(root *os.Root) error { return root.Remove(name) })
}

func (r rootFS) MkdirAll(path string, perm fs.FileMode) error {
	return r.with(func(root *os.Root) error { return root.MkdirAll(path, perm) })
}

// RealPath joins the name onto the root's own path. Safe to do without
// re-checking containment, because every operation that uses the result has
// already been refused by os.Root if it escaped.
func (r rootFS) RealPath(name string) (string, bool) {
	return filepath.Join(r.path, name), true
}

// ReadDir lists a directory through the root, so listing is confined exactly as
// every other operation is.
//
// os.Root has no ReadDir of its own, so the directory is opened through the root
// — which is where the containment is enforced — and read from the handle. A
// traversal that escaped would leak the names of files the caller was never
// given access to: a smaller breach than reading them, and still a breach.
func (r rootFS) ReadDir(name string) ([]fs.DirEntry, error) {
	var out []fs.DirEntry

	err := r.with(func(root *os.Root) error {
		dir, openErr := root.Open(name)
		if openErr != nil {
			return openErr
		}

		defer func() { _ = dir.Close() }()

		var readErr error

		out, readErr = dir.ReadDir(-1)

		return readErr
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

func (r rootFS) Readlink(name string) (string, error) {
	var out string

	err := r.with(func(root *os.Root) error {
		var linkErr error

		out, linkErr = root.Readlink(name)

		return linkErr
	})

	return out, err
}

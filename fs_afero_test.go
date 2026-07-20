package config

import (
	"io/fs"

	"github.com/spf13/afero"
)

// aferoFS adapts an afero filesystem to config.FS.
//
// It lives in test scope deliberately. afero is the best in-memory filesystem
// in Go and remains what the testing guide recommends, but a consumer of this
// module should not inherit it — so the library depends on the interface and
// the suite depends on afero, which keeps it out of `go list -deps .` and out
// of everyone else's graph. See D4 of the filesystem abstraction spec.
type aferoFS struct{ fs afero.Fs }

// wrapAfero adapts an afero filesystem, implementing RealPather only when the
// underlying filesystem genuinely has operating-system paths.
//
// The conditional matters. RealPather is a claim that native notification can
// work at all, and satisfying it unconditionally made every in-memory
// filesystem look watchable — the watcher selected fsnotify and only discovered
// per path that there was nothing to watch. An implementation must not satisfy
// an optional interface it cannot honour.
func wrapAfero(filesystem afero.Fs) FS {
	base := aferoFS{fs: filesystem}

	switch filesystem.(type) {
	case *afero.OsFs, *afero.BasePathFs:
		return aferoRealFS{aferoFS: base}
	default:
		return base
	}
}

// aferoRealFS is an afero filesystem the operating system can see.
type aferoRealFS struct{ aferoFS }

func (a aferoRealFS) RealPath(name string) (string, bool) {
	if base, ok := a.fs.(*afero.BasePathFs); ok {
		if real, err := base.RealPath(name); err == nil {
			return real, true
		}
	}

	return name, true
}

func (a aferoFS) ReadFile(name string) ([]byte, error) { return afero.ReadFile(a.fs, name) }

func (a aferoFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return afero.WriteFile(a.fs, name, data, perm)
}

func (a aferoFS) Stat(name string) (fs.FileInfo, error) { return a.fs.Stat(name) }

func (a aferoFS) Rename(oldpath, newpath string) error { return a.fs.Rename(oldpath, newpath) }

func (a aferoFS) Remove(name string) error { return a.fs.Remove(name) }

func (a aferoFS) MkdirAll(path string, perm fs.FileMode) error {
	return a.fs.MkdirAll(path, perm)
}

func (a aferoFS) Readlink(name string) (string, error) {
	reader, ok := a.fs.(afero.LinkReader)
	if !ok {
		return "", fs.ErrInvalid
	}

	return reader.ReadlinkIfPossible(name)
}

var (
	_ FS         = aferoFS{}
	_ LinkReader = aferoFS{}
	_ FS         = aferoRealFS{}
	_ RealPather = aferoRealFS{}
	_ FS         = osFS{}
	_ RealPather = osFS{}
	_ LinkReader = osFS{}
	_ FS         = rootFS{}
	_ RealPather = rootFS{}
	_ LinkReader = rootFS{}
)

// NewMemFS returns an empty in-memory FS.
//
// Exported because package config_test cannot reach the unexported adapter, and
// test files in package config can export identifiers it consumes. That is the
// whole of the sharing — earlier versions also exported WrapAfero and a seeded
// MemFilesystem, neither of which anything called.
func NewMemFS() FS { return wrapAfero(afero.NewMemMapFs()) }

var (
	_ FS         = aferoFS{}
	_ LinkReader = aferoFS{}
	_ FS         = aferoRealFS{}
	_ RealPather = aferoRealFS{}
	_ FS         = osFS{}
	_ RealPather = osFS{}
	_ LinkReader = osFS{}
	_ FS         = rootFS{}
	_ RealPather = rootFS{}
	_ LinkReader = rootFS{}
)

package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"gitlab.com/phpboyscout/go/errors"
)

// TestDir_ConfinesEveryOperation covers the containment Dir exists to provide.
//
// The guarantee is os.Root's rather than this module's, which is exactly why it
// is asserted here: a change in the Go version that weakened it would otherwise
// pass silently, and consumers pointing this at a directory a user named are
// relying on it.
func TestDir_ConfinesEveryOperation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()

	if err := os.WriteFile(filepath.Join(root, "inside.yaml"), []byte("a: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(outside, "secret.yaml"), []byte("s: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	filesystem, err := Dir(root)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}

	// What is inside is readable.
	if _, err := filesystem.ReadFile("inside.yaml"); err != nil {
		t.Errorf("reading a file inside the root failed: %v", err)
	}

	escapes := map[string]string{
		"parent traversal": filepath.Join("..", filepath.Base(outside), "secret.yaml"),
		"absolute path":    filepath.Join(outside, "secret.yaml"),
	}

	for name, path := range escapes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := filesystem.ReadFile(path); err == nil {
				t.Errorf("reading %s escaped the root", path)
			}

			if err := filesystem.WriteFile(path, []byte("x: 1\n"), 0o600); err == nil {
				t.Errorf("writing %s escaped the root", path)
			}
		})
	}

	// A symlink pointing out of the root must not be a way through it either.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(filepath.Join(outside, "secret.yaml"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := filesystem.ReadFile("escape"); err == nil {
		t.Error("a symlink out of the root was followed")
	}
}

// TestDir_ReportsRealPaths keeps native notification working for a rooted
// filesystem: it has operating-system paths, so it must say so.
func TestDir_ReportsRealPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	filesystem, err := Dir(root)
	if err != nil {
		t.Fatal(err)
	}

	pather, ok := filesystem.(RealPather)
	if !ok {
		t.Fatal("a rooted real filesystem must implement RealPather")
	}

	got, ok := pather.RealPath("app.yaml")
	if !ok {
		t.Fatal("RealPath reported no path for a rooted real filesystem")
	}

	if want := filepath.Join(root, "app.yaml"); got != want {
		t.Errorf("RealPath = %q, want %q", got, want)
	}
}

// TestRealPather_AbsenceSelectsPolling is the negative case.
//
// A filesystem with no operating-system paths must not implement RealPather,
// and the watcher must fall back to polling rather than pointing fsnotify at
// something it cannot see.
func TestRealPather_AbsenceSelectsPolling(t *testing.T) {
	t.Parallel()

	memory := memFS(t, map[string]string{"/app.yaml": "a: 1\n"})

	if _, ok := memory.(RealPather); ok {
		t.Error("an in-memory filesystem must not claim operating-system paths")
	}

	if _, ok := NewWatcher(memory, time.Second).(*pollWatcher); !ok {
		t.Error("a filesystem without real paths must be polled")
	}

	// And the real filesystem still selects native notification.
	if _, ok := NewWatcher(OS(), time.Second).(*fsnotifyWatcher); !ok {
		t.Error("the operating system must use native notification")
	}
}

// TestFS_AbsentFileIsDistinguishable is the one error the Store branches on:
// a missing overlay is normal, a missing base file is a broken installation,
// and only fs.ErrNotExist lets it tell them apart.
func TestFS_AbsentFileIsDistinguishable(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	rooted, err := Dir(root)
	if err != nil {
		t.Fatal(err)
	}

	// A rooted filesystem takes paths relative to its root; the others take
	// them as given.
	cases := map[string]struct {
		filesystem FS
		path       string
	}{
		"os":     {OS(), filepath.Join(root, "nothing-here.yaml")},
		"rooted": {rooted, "nothing-here.yaml"},
		"memory": {memFS(t, nil), "/nothing-here.yaml"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.filesystem.ReadFile(tc.path)
			if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("reading an absent file gave %v, want fs.ErrNotExist", err)
			}
		})
	}
}

// TestDir_AbsolutePathIsNotAMissingFile pins the trap the doc comment warns
// about: an absolute path handed to a rooted filesystem reads as an escape
// attempt, not as an absent file, so the Store treats it as fatal rather than
// as a missing optional source.
func TestDir_AbsolutePathIsNotAMissingFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	filesystem, err := Dir(root)
	if err != nil {
		t.Fatal(err)
	}

	_, err = filesystem.ReadFile(filepath.Join(root, "absent.yaml"))
	if err == nil {
		t.Fatal("an absolute path was accepted by a rooted filesystem")
	}

	if errors.Is(err, fs.ErrNotExist) {
		t.Error("an absolute path reported as merely absent — the Store would " +
			"skip it as an optional source rather than reporting the mistake")
	}
}

// TestDir_DoesNotAccumulateDescriptors pins the fix for a leak that only
// surfaces at scale.
//
// An earlier Dir held its *os.Root open for the FS's lifetime, which read as a
// feature — the root survived a rename — and leaked one descriptor per call,
// since FS has no Close and nothing in the module closes a filesystem. At a
// default 1024-descriptor limit it failed after 1021 calls; at the 256 typical
// of macOS, after 253. The documented testing pattern is config.Dir(t.TempDir())
// per test, so a large suite would have reached it.
//
// Linux-only: it reads /proc to count descriptors, and there is no portable
// equivalent worth the complexity.
func TestDir_DoesNotAccumulateDescriptors(t *testing.T) {
	t.Parallel()

	count := func() int {
		entries, err := os.ReadDir("/proc/" + strconv.Itoa(os.Getpid()) + "/fd")
		if err != nil {
			return -1
		}

		return len(entries)
	}

	if count() < 0 {
		t.Skip("/proc unavailable; descriptor counting is Linux-only")
	}

	dir := t.TempDir()
	before := count()

	for range 200 {
		filesystem, err := Dir(dir)
		if err != nil {
			t.Fatal(err)
		}

		if err := filesystem.WriteFile("a.yaml", []byte("a: 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := filesystem.ReadFile("a.yaml"); err != nil {
			t.Fatal(err)
		}
	}

	// A small margin for descriptors the runtime opens for its own reasons.
	if leaked := count() - before; leaked > 5 {
		t.Errorf("200 Dir calls leaked %d descriptors; the root must not be held open", leaked)
	}
}

// DirLister is what an adapter over a directory of files needs, and nothing in
// FS provides it: the interface has ReadFile, WriteFile, Stat, Rename, Remove
// and MkdirAll, none of which enumerates.
//
// It is optional rather than part of FS because adding a method would break
// every filesystem adapter in the family — afero, billy, iofs, sftp and the
// three object stores — to serve one consumer.

func TestOS_ListsADirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{"log.level", "database.host"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	lister, ok := OS().(DirLister)
	if !ok {
		t.Fatal("the operating-system filesystem must implement DirLister")
	}

	entries, err := lister.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	if got := namesOf(entries); len(got) != 2 {
		t.Errorf("ReadDir returned %v, want both files", got)
	}
}

func TestDir_ListsWithinTheRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "conf"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "conf", "log.level"), []byte("debug"), 0o600); err != nil {
		t.Fatal(err)
	}

	filesystem, err := Dir(root)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}

	lister, ok := filesystem.(DirLister)
	if !ok {
		t.Fatal("a rooted filesystem must implement DirLister")
	}

	entries, err := lister.ReadDir("conf")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	if got := namesOf(entries); len(got) != 1 || got[0] != "log.level" {
		t.Errorf("ReadDir = %v, want [log.level]", got)
	}
}

// Listing must be confined exactly as every other rooted operation is. A
// traversal that reads a directory outside the root would leak the names of
// files the caller was never given access to, which is a smaller breach than
// reading them and still a breach.
func TestDir_ListingCannotEscapeTheRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()

	if err := os.WriteFile(filepath.Join(outside, "secret.yaml"), []byte("s: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	filesystem, err := Dir(root)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}

	lister := filesystem.(DirLister)

	if _, err := lister.ReadDir(filepath.Join("..", filepath.Base(outside))); err == nil {
		t.Error("ReadDir escaped the root via .., leaking the names of files outside it")
	}
}

// An absent directory reports fs.ErrNotExist, so a consumer can tell "nothing
// configured here yet" from "this filesystem cannot list".
func TestDirLister_AbsentDirectoryIsNotExist(t *testing.T) {
	t.Parallel()

	lister := OS().(DirLister)

	if _, err := lister.ReadDir(filepath.Join(t.TempDir(), "nope")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadDir of an absent directory = %v, want fs.ErrNotExist", err)
	}
}

func namesOf(entries []fs.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}

	return out
}

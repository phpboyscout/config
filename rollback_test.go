package config

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/afero"
)

// failingRenameFs fails the rename of a nominated path, which is how a commit
// is made to fail partway through a multi-target set. Everything else behaves
// normally, so prepare and verify succeed and the failure lands where a real
// one would: at the point of making staged content visible.
type failingRenameFs struct {
	FS

	mu       sync.Mutex
	failFor  string
	attempts int
}

var errRenameRefused = errors.New("rename refused by the test filesystem")

func (f *failingRenameFs) Rename(oldname, newname string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.attempts++

	if strings.Contains(newname, f.failFor) {
		return errRenameRefused
	}

	return f.FS.Rename(oldname, newname)
}

// AC13 — a commit that fails partway restores the targets already committed.
//
// The restore has to return the Store to its previous state too, not only the
// file. Commit advances the backend's conflict fingerprint, and the Store never
// re-reads after committing, so a rollback that leaves the fingerprint pointing
// at content no longer on disk leaves that backend permanently unusable: every
// later write to it fails as a conflict with a change nobody made.
func TestApply_RollbackRestoresTheStoreNotOnlyTheFile(t *testing.T) {
	t.Parallel()

	base := wrapAfero(afero.NewMemMapFs())
	if err := base.WriteFile("/base.yaml", []byte("onlybase: 1\n"), 0o644); err != nil {
		t.Fatalf("seed base: %v", err)
	}

	if err := base.WriteFile("/over.yaml", []byte("onlyover: 1\n"), 0o644); err != nil {
		t.Fatalf("seed over: %v", err)
	}

	filesystem := &failingRenameFs{FS: base, failFor: "over.yaml"}

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/base.yaml", "/over.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Targets both files. One commit succeeds, the other is refused.
	_, err = s.Apply(context.Background(),
		Set("onlybase", 2),
		Set("onlyover", 2))
	if err == nil {
		t.Fatal("expected the apply to fail when a commit was refused")
	}

	// The committed target was restored on disk.
	if got := readFile(t, filesystem, "/base.yaml"); !strings.Contains(got, "onlybase: 1") {
		t.Errorf("the committed target was not restored:\n%s", got)
	}

	// And the Store can still write to it. This is the half that was missing:
	// the file was restored while the backend's fingerprint still described the
	// rolled-back content.
	filesystem.mu.Lock()
	filesystem.failFor = "nothing-matches-this"
	filesystem.mu.Unlock()

	if _, err := s.Apply(context.Background(), Set("onlybase", 3)); err != nil {
		t.Fatalf("a later write to the restored file failed: %v", err)
	}

	if got := s.View().GetInt("onlybase"); got != 3 {
		t.Errorf("onlybase = %d, want 3", got)
	}
}

// A rollback that fails must say precisely what is in which state. A caller
// told only "the commit failed" cannot know whether to retry, restore by hand,
// or leave well alone.
func TestApply_UnrecoverablePartialCommitNamesWhatIsWhere(t *testing.T) {
	t.Parallel()

	base := wrapAfero(afero.NewMemMapFs())
	if err := base.WriteFile("/base.yaml", []byte("onlybase: 1\n"), 0o644); err != nil {
		t.Fatalf("seed base: %v", err)
	}

	if err := base.WriteFile("/over.yaml", []byte("onlyover: 1\n"), 0o644); err != nil {
		t.Fatalf("seed over: %v", err)
	}

	// Commits base, refuses over, then refuses the write that would restore
	// base — so the set is left genuinely half-applied.
	filesystem := &hostileFs{FS: base}

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/base.yaml", "/over.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	filesystem.arm()

	_, err = s.Apply(context.Background(),
		Set("onlybase", 2),
		Set("onlyover", 2))
	if err == nil {
		t.Fatal("expected the apply to fail")
	}

	if !errors.Is(err, ErrPartialCommit) {
		t.Fatalf("err = %v, want ErrPartialCommit — the set was left half applied", err)
	}

	// Both targets have to be identifiable from the message, or the caller
	// cannot tell which one needs attention.
	for _, want := range []string{"base.yaml", "over.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %s: %v", want, err)
		}
	}
}

// hostileFs commits the first target, refuses the second, then refuses to
// restore the first — the sequence that produces an unrecoverable partial
// commit. It stays inert until armed so that loading and staging succeed.
type hostileFs struct {
	FS

	mu      sync.Mutex
	armed   bool
	renames int
}

func (f *hostileFs) arm() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.armed = true
}

func (f *hostileFs) Rename(oldname, newname string) error {
	f.mu.Lock()

	if !f.armed {
		f.mu.Unlock()

		return f.FS.Rename(oldname, newname)
	}

	f.renames++
	first := f.renames == 1
	f.mu.Unlock()

	if first {
		return f.FS.Rename(oldname, newname)
	}

	return errRenameRefused
}

func (f *hostileFs) WriteFile(name string, data []byte, perm fs.FileMode) error {
	f.mu.Lock()
	armed, renames := f.armed, f.renames
	f.mu.Unlock()

	// Once a commit has landed, refuse the restoring write to that same file.
	if armed && renames > 0 && strings.HasSuffix(name, "base.yaml") {
		return errRenameRefused
	}

	return f.FS.WriteFile(name, data, perm)
}

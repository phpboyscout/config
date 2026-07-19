package config

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// Commenting a configuration file out entirely is an ordinary thing to do, and
// a scaffolded file often ships as nothing but a header. Such a file parses
// fine and contributes no values, so it loads — but it has no mapping for an
// edit to land in, and a write to it used to fail permanently with an error
// naming the YAML library rather than anything the user could act on.
func TestApply_WritesToAFileWithNoMappingRoot(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"comment only":        "# my config\n",
		"blank lines":         "\n\n",
		"bare document start": "---\n",
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			filesystem := memFS(t, map[string]string{"/app.yaml": content})

			s, err := NewStore(context.Background(), WithFiles(filesystem, "/app.yaml"))
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}

			if _, err := s.Apply(context.Background(), Set("server.port", 8080)); err != nil {
				t.Fatalf("Apply: %v", err)
			}

			if got := s.View().GetInt("server.port"); got != 8080 {
				t.Errorf("server.port = %d, want 8080", got)
			}

			if err := s.Reload(context.Background()); err != nil {
				t.Fatalf("the written file does not reload: %v", err)
			}
		})
	}
}

// Whatever was already in the file is kept. A header explaining what the file
// is for is the most likely thing to be there, and losing it to the first write
// would be the fidelity failure this module exists to prevent.
func TestApply_SeedingKeepsWhatWasAlreadyInTheFile(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{
		"/app.yaml": "# Server settings live here.\n# Uncomment to override.\n",
	})

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/app.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := s.Apply(context.Background(), Set("server.port", 8080)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := readFile(t, filesystem, "/app.yaml")
	for _, want := range []string{"Server settings live here", "Uncomment to override", "port: 8080"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q after seeding:\n%s", want, got)
		}
	}
}

// A source deleted while the process runs is a legitimate state for an optional
// file. Leaving the backend's record of it in place made every later write fail
// as a conflict with a change nobody made — including the write that would have
// recreated the file.
func TestApply_AFileDeletedAtRuntimeCanBeRecreated(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "port: 1\n"})

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/app.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if err := filesystem.Remove("/app.yaml"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if err := s.Reload(context.Background()); err != nil {
		t.Fatalf("Reload after deletion: %v", err)
	}

	if _, err := s.Apply(context.Background(), Set("port", 2)); err != nil {
		t.Fatalf("could not write to a file that was deleted at runtime: %v", err)
	}

	if got := s.View().GetInt("port"); got != 2 {
		t.Errorf("port = %d, want 2", got)
	}
}

// A nil Binder is a supported input — ObserveSection guards for it twice — so
// it must return an empty section rather than dereferencing a typed-nil.
func TestObserveSection_NilBinderReturnsAnEmptySection(t *testing.T) {
	t.Parallel()

	type settings struct {
		Host string `mapstructure:"host"`
	}

	section, err := ObserveSection[settings](nil, "server")
	if err != nil {
		t.Fatalf("ObserveSection: %v", err)
	}

	if section == nil {
		t.Fatal("no section returned")
	}

	if section.Exists() {
		t.Error("a section bound to nothing reports that it exists")
	}
}

// Tag translation has to reach a struct wherever it sits. Stopping at the first
// non-struct meant an element inside a list decoded as its zero value —
// silently, with no error, which is the exact failure translateKeys exists to
// remove.
func TestUnmarshal_TranslatesTagsInsideCollections(t *testing.T) {
	t.Parallel()

	type item struct {
		Name string `yaml:"itemName"`
	}

	type cfg struct {
		Items  []item          `mapstructure:"items"`
		ByName map[string]item `mapstructure:"byname"`
		Nested [][]item        `mapstructure:"nested"`
	}

	filesystem := memFS(t, map[string]string{
		"/app.yaml": "items:\n  - itemName: alpha\nbyname:\n  first:\n    itemName: beta\n" +
			"nested:\n  - - itemName: gamma\n",
	})

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/app.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	var got cfg
	if err := s.View().Unmarshal(&got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if len(got.Items) != 1 || got.Items[0].Name != "alpha" {
		t.Errorf("slice element = %+v, want Name alpha", got.Items)
	}

	if got.ByName["first"].Name != "beta" {
		t.Errorf("map element = %+v, want Name beta", got.ByName)
	}

	if len(got.Nested) != 1 || len(got.Nested[0]) != 1 || got.Nested[0][0].Name != "gamma" {
		t.Errorf("nested slice element = %+v, want Name gamma", got.Nested)
	}
}

// The file mode comment promises that a file which already exists keeps the
// mode its owner chose. Staging through a temporary file was quietly tightening
// it to owner-only on every write.
func TestApply_AnExistingFileKeepsItsMode(t *testing.T) {
	t.Parallel()

	filesystem := afero.NewMemMapFs()
	if err := afero.WriteFile(filesystem, "/app.yaml", []byte("port: 1\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/app.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := s.Apply(context.Background(), Set("port", 2)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	info, err := filesystem.Stat("/app.yaml")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("mode = %v, want the owner's 0644 to survive the write", got)
	}
}

// A file this module creates holds credentials as often as not, so it must not
// inherit a permissive default.
func TestApply_ACreatedFileIsOwnerOnly(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/base.yaml": "a: 1\n"})

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/base.yaml", "/new.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := s.Apply(context.Background(), Set("secret", "value")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	info, err := filesystem.Stat("/new.yaml")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if got := info.Mode().Perm(); got != configFileMode {
		t.Errorf("mode = %v, want %v for a file this module created", got, configFileMode)
	}
}

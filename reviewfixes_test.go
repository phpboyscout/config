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

// A layer keeps the spelling its source used, while every lookup normalises.
// Routing therefore declared a mixed-case key brand new and wrote it to a
// different file, leaving the file that owns it holding a stale value — the
// layer-correctness this module exists for, defeated by a spelling.
func TestApply_RoutesAMixedCaseKeyToTheFileThatOwnsIt(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{
		"/base.yaml": "logLevel: debug\n",
		"/over.yaml": "other: 1\n",
	})

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/base.yaml", "/over.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	plan, err := s.Plan(Set("logLevel", "info"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if got := plan.Operations[0].Target.Name; got != "/base.yaml" {
		t.Errorf("routed to %s, want the file that defines the key", got)
	}

	if plan.Operations[0].Creates {
		t.Error("an existing key was reported as newly created")
	}

	if _, err := s.Apply(context.Background(), Set("logLevel", "info")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := readFile(t, filesystem, "/base.yaml"); !strings.Contains(got, "logLevel: info") {
		t.Errorf("the owning file was not updated:\n%s", got)
	}

	if got := readFile(t, filesystem, "/over.yaml"); strings.Contains(got, "logLevel") {
		t.Errorf("the key was duplicated into another file:\n%s", got)
	}

	// And the layer still reports as defining it, which is what Shadowed needs.
	if defs := s.View().Shadowed("logLevel"); len(defs) != 1 {
		t.Errorf("Shadowed = %v, want the one file defining it", defs)
	}
}

// The document layer matches literally, so a caller addressing server.port as
// Server.Port used to get a second, differently cased block written beside the
// real one while the original value stayed put.
func TestApply_AMixedCasePathEditsTheExistingKey(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "server:\n  port: 8080\n"})

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/app.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := s.Apply(context.Background(), Set("Server.Port", 9090)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := readFile(t, filesystem, "/app.yaml")

	if strings.Contains(got, "Server:") || strings.Contains(got, "Port:") {
		t.Errorf("a differently cased duplicate was written:\n%s", got)
	}

	if !strings.Contains(got, "port: 9090") {
		t.Errorf("the existing key was not updated:\n%s", got)
	}

	if v := s.View().GetInt("server.port"); v != 9090 {
		t.Errorf("server.port = %d, want 9090", v)
	}
}

// Required means configured, and false is a configuration. Judging it by
// zero-ness told an operator who had deliberately turned a feature off that
// they had not set it, and refused to start.
func TestValidate_RequiredAcceptsADeliberateZeroValue(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Telemetry struct {
			Enabled bool `config:"telemetry.enabled" validate:"required"`
			Retries int  `config:"telemetry.retries" validate:"required"`
		}
	}

	schema, err := NewSchema(WithStructSchema(cfg{}))
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}

	if _, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{
			"/app.yaml": "telemetry:\n  enabled: false\n  retries: 0\n",
		}), "/app.yaml"),
		WithSchema(schema)); err != nil {
		t.Fatalf("a deliberately disabled feature was rejected as unset: %v", err)
	}

	// Genuinely absent is still an error.
	if _, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{"/app.yaml": "other: 1\n"}), "/app.yaml"),
		WithSchema(schema)); err == nil {
		t.Error("an absent required field was accepted")
	}
}

// A schema written for a section is meant to be applied to that section.
// Validate read the whole snapshot regardless of the view's scope, so a scoped
// validation reported every field of the section as missing.
func TestValidate_AScopedViewValidatesItsOwnSubtree(t *testing.T) {
	t.Parallel()

	type section struct {
		Host string `config:"host" validate:"required"`
	}

	schema, err := NewSchema(WithStructSchema(section{}))
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}

	s, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{"/app.yaml": "server:\n  host: h\n"}), "/app.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if result := s.View().Sub("server").Validate(schema); !result.Valid() {
		t.Errorf("a scoped view failed validation of its own subtree: %v", result.Errors)
	}

	// The unscoped view genuinely does not have a top-level host.
	if result := s.View().Validate(schema); result.Valid() {
		t.Error("the unscoped view passed a schema describing a nested section")
	}
}

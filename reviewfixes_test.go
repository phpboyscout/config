package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/spf13/pflag"
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

// A View over nothing must read as empty configuration. Every Snapshot method
// already guards a nil receiver; reaching the field did not, so a derived view
// of a missing key — the documented way to ask for an optional section —
// crashed instead of reading as absent.
func TestView_IsNilTolerant(t *testing.T) {
	t.Parallel()

	empty := NewView(nil)

	if empty.SectionExists("") || empty.Has("a") || empty.Get("a") != nil {
		t.Error("a view over no snapshot did not read as empty")
	}

	if got := empty.Keys(); len(got) != 0 {
		t.Errorf("Keys() = %v, want empty", got)
	}

	s := storeOn(t, memFS(t, map[string]string{"/app.yaml": "server:\n  host: h\n"}), "/app.yaml")

	type opts struct {
		A string `mapstructure:"a"`
	}

	// Sub of a missing key returns nil, which must still be usable.
	section, err := UnmarshalSection[opts](s.View().Sub("missing"), "opts")
	if err != nil {
		t.Fatalf("UnmarshalSection over a missing section: %v", err)
	}

	if section.Exists {
		t.Error("a section under a missing prefix reports that it exists")
	}
}

// An embedded struct's fields belong to its parent. yaml and json both spell
// that ",inline"; only mapstructure calls it squash. A struct shared with
// either — the whole reason those tags are honoured — left every inherited
// field at its zero value.
func TestUnmarshal_EmbeddedStructsAreInlined(t *testing.T) {
	t.Parallel()

	type common struct {
		Level string `yaml:"level"`
	}

	type section struct {
		common `yaml:",inline"`
		Name   string `yaml:"name"`
	}

	s := storeOn(t, memFS(t, map[string]string{
		"/app.yaml": "sect:\n  level: debug\n  name: n\n",
	}), "/app.yaml")

	got, err := UnmarshalSection[section](s.View(), "sect")
	if err != nil {
		t.Fatalf("UnmarshalSection: %v", err)
	}

	if got.Value.Level != "debug" {
		t.Errorf("embedded field = %q, want debug", got.Value.Level)
	}

	if got.Value.Name != "n" {
		t.Errorf("Name = %q, want n", got.Value.Name)
	}
}

// A repeatable flag is a list. String() renders one for display as "[a,b]", so
// storing that gave a single garbage element instead of the values passed.
func TestFlags_ARepeatableFlagStaysAList(t *testing.T) {
	t.Parallel()

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.StringSlice("tag", nil, "tags")

	if err := flags.Parse([]string{"--tag", "a", "--tag", "b"}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	s, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{"/app.yaml": "x: 1\n"}), "/app.yaml"),
		WithFlags(flags))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	got := s.View().GetStringSlice("tag")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("tag = %#v, want [a b]", got)
	}
}

// Strict mode polices what someone wrote into a configuration source. An
// orchestrator exporting an unrelated prefixed variable would otherwise stop
// the application starting.
func TestValidate_StrictModeIgnoresAmbientEnvironmentVariables(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Log struct {
			Level string `config:"log.level"`
		}
	}

	schema, err := NewSchema(WithStructSchema(cfg{}), WithStrictMode())
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}

	if _, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{"/app.yaml": "log:\n  level: info\n"}), "/app.yaml"),
		WithEnv("APP", envOf("APP_VERSION=1.2.3", "APP_HOME=/srv")),
		WithSchema(schema)); err != nil {
		t.Fatalf("an unrelated environment variable stopped the store: %v", err)
	}

	// A typo in the file itself is still caught.
	if _, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{"/app.yaml": "log:\n  levle: info\n"}), "/app.yaml"),
		WithSchema(schema)); err == nil {
		t.Error("strict mode accepted a misspelled key in a configuration file")
	}
}

// A configuration file managed by a dotfile tool is routinely a symlink into a
// tracked repository. Committing is a rename over the target path, and rename
// replaces the link rather than writing through it — so the link was destroyed
// and the file the user actually keeps their config in never changed.
func TestApply_WritesThroughASymlink(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	real := filepath.Join(dir, "real.yaml")
	link := filepath.Join(dir, "link.yaml")

	if err := os.WriteFile(real, []byte("a: 1\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	s, err := NewStore(context.Background(), WithFiles(afero.NewOsFs(), link))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := s.Apply(context.Background(), Set("a", 2)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}

	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file")
	}

	body, err := os.ReadFile(real)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if !strings.Contains(string(body), "a: 2") {
		t.Errorf("the file the link points at was not updated:\n%s", body)
	}
}

// A pinned target naming no writable source is the caller's mistake, and it
// used to plan a plausible dry run and then fail at apply with an internal
// invariant error — the module blaming itself for a typo, and the preview
// disagreeing with the write.
func TestPlan_RejectsATargetThatNamesNothing(t *testing.T) {
	t.Parallel()

	s := storeOn(t, memFS(t, map[string]string{"/app.yaml": "a: 1\n"}), "/app.yaml")

	bogus := Source{Kind: SourceFile, Name: "/typo.yaml", Writable: true}
	change := Set("a", 2)
	change.Target = &bogus

	_, err := s.Plan(change)
	if !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("err = %v, want ErrInvalidTarget", err)
	}

	if !strings.Contains(err.Error(), "/typo.yaml") {
		t.Errorf("the error does not name the target: %v", err)
	}
}

// A pinned target built by hand will not reproduce every field of the Source
// the Store holds, so matching has to be by identity rather than whole-struct
// equality.
func TestPlan_AcceptsATargetBuiltByHand(t *testing.T) {
	t.Parallel()

	s := storeOn(t, memFS(t, map[string]string{
		"/base.yaml": "a: 1\n",
		"/over.yaml": "b: 1\n",
	}), "/base.yaml", "/over.yaml")

	// Deliberately missing Writable, which a caller would not think to set.
	pinned := Source{Kind: SourceFile, Name: "/base.yaml"}
	change := Set("a", 2)
	change.Target = &pinned

	plan, err := s.Plan(change)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if got := plan.Operations[0].Target.Name; got != "/base.yaml" {
		t.Errorf("target = %s, want /base.yaml", got)
	}

	if plan.Operations[0].Creates {
		t.Error("an existing key was reported as newly created")
	}
}

// A removal never creates anything. Reporting otherwise made a dry run describe
// the opposite of what it would do.
func TestPlan_RemovingAnAbsentKeyDoesNotReportCreation(t *testing.T) {
	t.Parallel()

	s := storeOn(t, memFS(t, map[string]string{"/app.yaml": "a: 1\n"}), "/app.yaml")

	plan, err := s.Plan(Remove("nothere"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if plan.Operations[0].Creates {
		t.Error("removing an absent key was reported as creating it")
	}

	if strings.Contains(plan.String(), "new key") {
		t.Errorf("the dry run describes a removal as a creation: %q", plan.String())
	}
}

// Cancelling the context is the natural way to stop watching, and it is why the
// event loop watches ctx.Done at all — but only the returned stop function used
// to close the watcher, leaking the handle and the goroutine feeding it.
func TestWatch_CancellingTheContextReleasesTheWatcher(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")

	if err := os.WriteFile(path, []byte("a: 1\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	w := NewWatcher(afero.NewOsFs(), time.Hour)

	fired := make(chan struct{}, 4)

	stop, err := w.Watch(ctx, []string{path}, func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	defer stop()

	cancel()
	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(path, []byte("a: 2\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-fired:
		t.Error("a cancelled watch is still delivering events")
	case <-time.After(300 * time.Millisecond):
	}
}

// A layer that fails to load must withdraw itself and nothing else. Reinstating
// the backend list captured before it was adopted discarded any layer another
// goroutine added in between — and that caller had been told its AddLayer
// succeeded, so its configuration silently read back as absent.
func TestAddLayer_AFailingLayerWithdrawsOnlyItself(t *testing.T) {
	t.Parallel()

	s := storeOn(t, memFS(t, map[string]string{"/app.yaml": "a: 1\n"}), "/app.yaml")

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		goodErr  error
		badErred bool
	)

	wg.Add(2)

	go func() {
		defer wg.Done()

		err := s.AddLayer(context.Background(), "bad", strings.NewReader("a:\n  - x\n :\n"))

		mu.Lock()
		badErred = err != nil
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()

		err := s.AddLayer(context.Background(), "good", strings.NewReader("b: from-good\n"))

		mu.Lock()
		goodErr = err
		mu.Unlock()
	}()

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	if !badErred {
		t.Error("an unparseable layer was accepted")
	}

	if goodErr != nil {
		t.Fatalf("the good layer was refused: %v", goodErr)
	}

	// The good layer was reported as accepted, so it must actually be there.
	if got := s.View().GetString("b"); got != "from-good" {
		t.Errorf("b = %q — a layer reported as added is not contributing", got)
	}
}

// An in-memory source is not a file, and saying it is invites the reader to
// open something that does not exist. A compiled-in default and a layer added
// at runtime are also different things, and provenance should say which.
func TestProvenance_InMemorySourcesReportWhatTheyAre(t *testing.T) {
	t.Parallel()

	s, err := NewStore(context.Background(),
		WithReaders(NamedSource{Name: "defaults.yaml", Content: []byte("log:\n  level: info\n")}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	src, ok := s.View().Origin("log.level")
	if !ok {
		t.Fatal("no provenance for a compiled-in default")
	}

	if src.Kind != SourceDefault {
		t.Errorf("kind = %q, want %q", src.Kind, SourceDefault)
	}

	if got := src.String(); got != "default:defaults.yaml" {
		t.Errorf("rendered as %q, want it to name the source", got)
	}

	// A runtime layer is an override, not a default and not a file.
	s2 := storeOn(t, memFS(t, map[string]string{"/app.yaml": "a: 1\n"}), "/app.yaml")

	if err := s2.AddLayer(context.Background(), "computed", strings.NewReader("z: 1\n")); err != nil {
		t.Fatalf("AddLayer: %v", err)
	}

	src2, _ := s2.View().Origin("z")
	if src2.Kind != SourceOverride {
		t.Errorf("kind = %q, want %q", src2.Kind, SourceOverride)
	}

	if got := src2.String(); got != "override:computed" {
		t.Errorf("rendered as %q, want it to name the layer", got)
	}
}

// A path means nothing without the filesystem it is relative to. Taking the
// first backend's filesystem and using it for every path meant a store mixing a
// real file with an in-memory one statted half its sources against the wrong
// filesystem, found them permanently absent, and reported watching as
// established while never noticing a change.
func TestWatch_GroupsPathsByFilesystem(t *testing.T) {
	t.Parallel()

	inMemory := afero.NewMemMapFs()
	if err := afero.WriteFile(inMemory, "/mem.yaml", []byte("m: 1\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	dir := t.TempDir()
	onDisk := filepath.Join(dir, "real.yaml")

	if err := os.WriteFile(onDisk, []byte("r: 1\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s, err := NewStore(context.Background(),
		WithBackend(NewFileBackend(inMemory, "/mem.yaml")),
		WithBackend(NewFileBackend(afero.NewOsFs(), onDisk)))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	notified := make(chan struct{}, 4)
	s.AddObserverFunc(func(Observed) error {
		select {
		case notified <- struct{}{}:
		default:
		}

		return nil
	})

	stop, err := s.Watch(context.Background(), WithPollInterval(20*time.Millisecond))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	defer stop()

	if err := os.WriteFile(onDisk, []byte("r: 2\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-notified:
	case <-time.After(5 * time.Second):
		t.Fatal("a change to a source on a second filesystem was never reported")
	}
}

// An edit that preserves a file's length within one timestamp tick is invisible
// to a stat-based comparison, and timestamp granularity is coarse on several
// filesystems. The write path already hashes content for exactly this reason.
func TestPollWatcher_DetectsASameLengthEdit(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "level: info\n"})
	w := &pollWatcher{fs: filesystem, interval: 5 * time.Millisecond}

	changed := make(chan struct{}, 1)

	stop, err := w.Watch(context.Background(), []string{"/app.yaml"}, func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	defer stop()

	// Same byte length, different content.
	if err := afero.WriteFile(filesystem, "/app.yaml", []byte("level: warn\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("a same-length edit was never detected")
	}
}

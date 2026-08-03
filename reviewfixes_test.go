package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/spf13/pflag"
	"gitlab.com/phpboyscout/go/errors"
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

	filesystem := wrapAfero(afero.NewMemMapFs())
	if err := filesystem.WriteFile("/app.yaml", []byte("port: 1\n"), 0o644); err != nil {
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

	s, err := NewStore(context.Background(), WithFiles(OS(), link))
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

	w := NewWatcher(OS(), time.Hour)

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

	// The good layer is allowed to fail here, and this test used to forbid it.
	//
	// Both calls adopt their backend and then reload every backend, so if the
	// bad one is registered when the good one reloads, the good one's reload
	// legitimately fails: reloading is fail-closed, and a configuration
	// containing an unparseable layer must be refused whoever asked for it.
	// Requiring success made the test fail rarely and for a correct reason.
	//
	// What must hold either way is the invariant the original defect broke: a
	// caller told its layer was added must find that layer contributing. The
	// bug was that a concurrent add could discard a layer whose caller had
	// already been told it succeeded.
	if goodErr == nil {
		if got := s.View().GetString("b"); got != "from-good" {
			t.Errorf("b = %q — a layer reported as added is not contributing", got)
		}
	}

	// And the failed layer must not be left behind, whatever the interleaving:
	// a backend that cannot load would break every later reload.
	if got := s.View().Shadowed("a"); len(got) != 1 {
		t.Errorf("layers defining a = %v, want only the file — the bad layer was not withdrawn", got)
	}
}

// TestAddLayer_AFailedLayerIsWithdrawnDeterministically covers the same
// guarantee without concurrency, so a regression fails every time rather than
// occasionally.
func TestAddLayer_AFailedLayerIsWithdrawnDeterministically(t *testing.T) {
	t.Parallel()

	s := storeOn(t, memFS(t, map[string]string{"/app.yaml": "a: 1\n"}), "/app.yaml")

	if err := s.AddLayer(context.Background(), "bad",
		strings.NewReader("a:\n  - x\n :\n")); err == nil {
		t.Fatal("an unparseable layer was accepted")
	}

	// The bad backend must be gone, so a later add still works.
	if err := s.AddLayer(context.Background(), "good",
		strings.NewReader("b: from-good\n")); err != nil {
		t.Fatalf("a later layer was refused, so the failed one was left registered: %v", err)
	}

	if got := s.View().GetString("b"); got != "from-good" {
		t.Errorf("b = %q, want \"from-good\"", got)
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

	inMemory := wrapAfero(afero.NewMemMapFs())
	if err := inMemory.WriteFile("/mem.yaml", []byte("m: 1\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	dir := t.TempDir()
	onDisk := filepath.Join(dir, "real.yaml")

	if err := os.WriteFile(onDisk, []byte("r: 1\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s, err := NewStore(context.Background(),
		WithBackend(NewFileBackend(inMemory, "/mem.yaml")),
		WithBackend(NewFileBackend(OS(), onDisk)))
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
	if err := filesystem.WriteFile("/app.yaml", []byte("level: warn\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("a same-length edit was never detected")
	}
}

// pinnedBinder hands out a fixed view and captures the observer, so a test can
// deliver notifications in a chosen order rather than racing for one.
type pinnedBinder struct {
	view      *View
	observers []func(Observed) error
}

func (b *pinnedBinder) View() *View { return b.view }

func (b *pinnedBinder) AddObserverFunc(f func(Observed) error) {
	b.observers = append(b.observers, f)
}

func sectionSnapshot(version uint64, host string) *Snapshot {
	return newSnapshot(version, []Layer{{
		Source: Source{Kind: SourceFile, Name: "/app.yaml"},
		Values: map[string]any{"server": map[string]any{"host": host}},
	}})
}

// A Store publishes under its lock but notifies outside it, so nothing orders
// two notifications against each other. An observed section is the one thing
// that caches what it is told, so a delivery arriving out of order left it
// holding older values under a higher version — permanently stale, while
// reporting itself current to anything watching Version.
func TestObserveSection_IgnoresAnOutOfOrderDelivery(t *testing.T) {
	t.Parallel()

	type settings struct {
		Host string `mapstructure:"host"`
	}

	binder := &pinnedBinder{view: NewView(sectionSnapshot(1, "first"))}

	section, err := ObserveSection[settings](binder, "server")
	if err != nil {
		t.Fatalf("ObserveSection: %v", err)
	}

	if got := section.Value().Host; got != "first" {
		t.Fatalf("initial host = %q, want first", got)
	}

	// Deliver the newer snapshot, then an older one.
	for _, observe := range binder.observers {
		if err := observe(NewView(sectionSnapshot(3, "newest"))); err != nil {
			t.Fatalf("observer: %v", err)
		}

		if err := observe(NewView(sectionSnapshot(2, "older"))); err != nil {
			t.Fatalf("observer: %v", err)
		}
	}

	if got := section.Value().Host; got != "newest" {
		t.Errorf("host = %q — a stale delivery overwrote a newer one", got)
	}
}

// The equality check and the store have to happen together. Reading the
// previous section, deciding, and then storing was a read-modify-write that two
// concurrent notifications could interleave, leaving the later writer — which
// may carry the older configuration — as the one that wins.
func TestObserveSection_ConcurrentDeliveriesSettleOnTheNewest(t *testing.T) {
	t.Parallel()

	type settings struct {
		Host string `mapstructure:"host"`
	}

	binder := &pinnedBinder{view: NewView(sectionSnapshot(1, "first"))}

	section, err := ObserveSection[settings](binder, "server")
	if err != nil {
		t.Fatalf("ObserveSection: %v", err)
	}

	observe := binder.observers[0]

	var wg sync.WaitGroup

	for i := range uint64(32) {
		wg.Add(1)

		go func(n uint64) {
			defer wg.Done()

			_ = observe(NewView(sectionSnapshot(n+2, fmt.Sprintf("host-%02d", n))))
		}(i)
	}

	wg.Wait()

	// Whatever order they arrived in, the newest source must be the one held.
	if got := section.Value().Host; got != "host-31" {
		t.Errorf("host = %q, want host-31 — the newest delivery did not win", got)
	}
}

// Two backends answering to the same ID is a routing hazard: backendByID
// returns the first match, so a write routed at the second silently lands on the
// first. adoptBackend rejects it at runtime; NewStore used to append unchecked,
// so the same collision was accepted when the sources were declared up front.
func TestNewStore_RejectsDuplicateBackendIDs(t *testing.T) {
	t.Parallel()

	_, err := NewStore(context.Background(),
		WithReaders(NamedSource{Name: "dup", Content: []byte("a: 1\n")}),
		WithReaders(NamedSource{Name: "dup", Content: []byte("b: 2\n")}),
	)
	if !errors.Is(err, ErrInvalidTarget) {
		t.Errorf("err = %v, want ErrInvalidTarget for two sources sharing an ID", err)
	}

	// Distinct IDs still build.
	if _, err := NewStore(context.Background(),
		WithReaders(NamedSource{Name: "one", Content: []byte("a: 1\n")}),
		WithReaders(NamedSource{Name: "two", Content: []byte("b: 2\n")}),
	); err != nil {
		t.Errorf("two distinctly named sources were refused: %v", err)
	}
}

// externalBackend stands in for a backend defined outside this package, which
// is the case the seam exists for and the one the concrete type assertion made
// impossible.
type externalBackend struct {
	mu       sync.Mutex
	onChange func()
	stopped  bool
}

func (e *externalBackend) ID() string { return "external" }

func (e *externalBackend) Capabilities() Capabilities { return Capabilities{} }

func (e *externalBackend) Load(context.Context, []Layer) ([]Layer, error) {
	return []Layer{{
		Source: Source{Kind: SourceDefault, Name: "external"},
		Values: map[string]any{"external": "value"},
	}}, nil
}

func (e *externalBackend) Watch(
	_ context.Context,
	_ time.Duration,
	onChange func(),
) (func(), error) {
	e.mu.Lock()
	e.onChange = onChange
	e.mu.Unlock()

	return func() {
		e.mu.Lock()
		e.stopped = true
		e.mu.Unlock()
	}, nil
}

func (e *externalBackend) fire() {
	e.mu.Lock()
	fn := e.onChange
	e.mu.Unlock()

	if fn != nil {
		fn()
	}
}

// Watching asked for a concrete unexported type, so a backend supplied through
// WithBackend — the one thing that option exists for — could never be watched,
// and in a mixed set was silently omitted while Watch reported success.
func TestWatch_UsesTheBackendSeamNotAConcreteType(t *testing.T) {
	t.Parallel()

	backend := &externalBackend{}

	s, err := NewStore(context.Background(), WithBackend(backend))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	stop, err := s.Watch(context.Background())
	if err != nil {
		t.Fatalf("a watchable backend was refused: %v", err)
	}

	// It is genuinely wired up, not merely accepted.
	backend.fire()

	stop()

	backend.mu.Lock()
	defer backend.mu.Unlock()

	if !backend.stopped {
		t.Error("stopping the Store did not stop the backend's watch")
	}
}

// A backend that cannot watch is not watchable, and saying so is the point.
func TestWatch_RefusesWhenNothingCanWatch(t *testing.T) {
	t.Parallel()

	s, err := NewStore(context.Background(), WithReaders(NamedSource{
		Name: "embedded", Content: []byte("a: 1\n"),
	}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := s.Watch(context.Background()); !errors.Is(err, ErrWatchUnavailable) {
		t.Errorf("err = %v, want ErrWatchUnavailable", err)
	}
}

// countingBackend records what it was handed, so a test can assert that a
// backend genuinely receives the layers beneath it rather than being told
// separately by a call someone can forget to make.
type countingBackend struct {
	mu    sync.Mutex
	below [][]Layer
}

func (c *countingBackend) ID() string { return "counting" }

func (c *countingBackend) Capabilities() Capabilities { return Capabilities{} }

func (c *countingBackend) Load(_ context.Context, below []Layer) ([]Layer, error) {
	c.mu.Lock()
	c.below = append(c.below, below)
	c.mu.Unlock()

	return nil, nil
}

// A backend whose reading of its own input depends on what is already defined
// used to be told separately, before Load, by whichever Store method remembered
// to. That contract was invisible in the interface and was forgotten once.
func TestBackend_ReceivesTheLayersBeneathIt(t *testing.T) {
	t.Parallel()

	counter := &countingBackend{}

	s, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{"/app.yaml": "server:\n  port: 1\n"}), "/app.yaml"),
		WithBackend(counter))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	counter.mu.Lock()
	first := counter.below
	counter.mu.Unlock()

	if len(first) == 0 {
		t.Fatal("the backend was never loaded")
	}

	if len(first[0]) == 0 {
		t.Fatal("the backend was not given the layers beneath it")
	}

	// And it is given them again after a write, because the write may have
	// changed the key space it resolves against.
	if _, err := s.Apply(context.Background(), Set("server.host", "h")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	counter.mu.Lock()
	defer counter.mu.Unlock()

	if len(counter.below) < 2 {
		t.Fatal("the backend was not re-read after a write")
	}

	latest := counter.below[len(counter.below)-1]

	var sawHost bool

	for _, l := range latest {
		if _, ok := lookup(l.Values, []string{"server", "host"}); ok {
			sawHost = true
		}
	}

	if !sawHost {
		t.Error("the re-read did not include the key the write created")
	}
}

type applySettings struct {
	Host string `mapstructure:"host"`
}

// applyRecorder binds a section and records every delivery.
func applyRecorder(t *testing.T, host string) (*ObservedSection[applySettings], *pinnedBinder, *[]SectionChange[applySettings]) {
	t.Helper()

	binder := &pinnedBinder{view: NewView(sectionSnapshot(1, host))}
	got := &[]SectionChange[applySettings]{}

	section, err := ObserveSection[applySettings](binder, "server",
		WithSectionApply(func(c SectionChange[applySettings]) error {
			*got = append(*got, c)

			return nil
		}))
	if err != nil {
		t.Fatalf("ObserveSection: %v", err)
	}

	return section, binder, got
}

// The apply callback is where a consumer reacts to configuration, and the
// natural wish is to use it for the initial setup too rather than duplicating
// that logic at startup. It cannot fire during binding, because the callback
// usually closes over something built afterwards — so the caller is given the
// trigger and says when its dependencies exist.
func TestObserveSection_WithoutApplyInitialDeliveryIsChangeOnly(t *testing.T) {
	t.Parallel()

	_, binder, got := applyRecorder(t, "first")

	if len(*got) != 0 {
		t.Fatalf("binding delivered %d change(s) before the caller was ready", len(*got))
	}

	if err := binder.observers[0](NewView(sectionSnapshot(2, "second"))); err != nil {
		t.Fatalf("observer: %v", err)
	}

	if len(*got) != 1 {
		t.Fatalf("got %d deliveries, want one", len(*got))
	}

	if (*got)[0].Initial || !(*got)[0].Changed {
		t.Errorf("delivery = %+v, want a non-initial change", (*got)[0])
	}
}

func TestObserveSection_ApplyInitialDeliversThenChanges(t *testing.T) {
	t.Parallel()

	section, binder, got := applyRecorder(t, "first")

	if err := section.ApplyInitial(); err != nil {
		t.Fatalf("ApplyInitial: %v", err)
	}

	// Safe in a startup path that may run twice.
	if err := section.ApplyInitial(); err != nil {
		t.Fatalf("second ApplyInitial: %v", err)
	}

	if err := binder.observers[0](NewView(sectionSnapshot(2, "second"))); err != nil {
		t.Fatalf("observer: %v", err)
	}

	if len(*got) != 2 {
		t.Fatalf("got %d deliveries, want an initial and one change", len(*got))
	}

	assertDelivery(t, "initial", (*got)[0], true, "first")
	assertDelivery(t, "change", (*got)[1], false, "second")
}

// assertDelivery checks one delivery's flags and payload. Initial and Changed
// are complements, so asserting one settles both.
func assertDelivery(t *testing.T, label string, got SectionChange[applySettings], initial bool, host string) {
	t.Helper()

	if got.Initial != initial {
		t.Errorf("%s delivery: Initial = %v, want %v", label, got.Initial, initial)
	}

	if got.Changed == initial {
		t.Errorf("%s delivery: Changed = %v, want %v", label, got.Changed, !initial)
	}

	if got.Current.Value.Host != host {
		t.Errorf("%s delivery: host = %q, want %q", label, got.Current.Value.Host, host)
	}
}

// Delivering the section the binding started with, after a later one has been
// applied, would hand the caller stale configuration and call it initial.
func TestObserveSection_ApplyInitialIsANoOpAfterAChange(t *testing.T) {
	t.Parallel()

	section, binder, got := applyRecorder(t, "first")

	if err := binder.observers[0](NewView(sectionSnapshot(2, "second"))); err != nil {
		t.Fatalf("observer: %v", err)
	}

	if err := section.ApplyInitial(); err != nil {
		t.Fatalf("ApplyInitial: %v", err)
	}

	if len(*got) != 1 {
		t.Errorf("got %d deliveries, want the change alone", len(*got))
	}
}

// "Is this section configured" and "which key do I read it from" have to be the
// same question. The existence probe was inherited from the pre-rewrite
// container and took the first non-empty tag; the decoder, written for the
// rewrite, treats mapstructure as canonical with yaml and json as aliases. A
// field written under an alias therefore reported the section absent, and the
// caller received a zero value the decoder would have filled in.
func TestUnmarshalSection_ProbeAndDecodeAgreeOnTagPrecedence(t *testing.T) {
	t.Parallel()

	type tagged struct {
		Addr string `json:"addr" yaml:"address"`
	}

	// The section key is absent, so the probe alone decides existence.
	s := storeOn(t, memFS(t, map[string]string{"/app.yaml": "address: 1.2.3.4\n"}), "/app.yaml")

	got, err := UnmarshalSection[tagged](s.View(), "")
	if err != nil {
		t.Fatalf("UnmarshalSection: %v", err)
	}

	if !got.Exists {
		t.Fatal("a configured section was reported absent")
	}

	if got.Value.Addr != "1.2.3.4" {
		t.Errorf("Addr = %q — the probe and the decoder disagree about the tag", got.Value.Addr)
	}

	// A direct decode of the same view must agree.
	var direct tagged
	if err := s.View().Unmarshal(&direct); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if direct.Addr != got.Value.Addr {
		t.Errorf("section decode %q, direct decode %q — the two entry points disagree",
			got.Value.Addr, direct.Addr)
	}
}

// An empty key is the scope the view already describes, which is what a
// prefix-less binding means. Resolving it as a path found nothing and returned
// without decoding, so the caller got an untouched target.
func TestUnmarshalKey_AnEmptyKeyMeansTheWholeScope(t *testing.T) {
	t.Parallel()

	type settings struct {
		Host string `mapstructure:"host"`
		Port int    `mapstructure:"port"`
	}

	s := storeOn(t, memFS(t, map[string]string{
		"/app.yaml": "server:\n  host: h\n  port: 8080\n",
	}), "/app.yaml")

	var whole settings
	if err := s.View().UnmarshalKey("", &whole); err != nil {
		t.Fatalf("UnmarshalKey: %v", err)
	}

	// The root has no host, so this decodes nothing — but it must behave as
	// Unmarshal does rather than silently skipping.
	var viaUnmarshal settings
	if err := s.View().Unmarshal(&viaUnmarshal); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if whole != viaUnmarshal {
		t.Errorf("UnmarshalKey(\"\") = %+v, Unmarshal = %+v — they describe the same view", whole, viaUnmarshal)
	}

	// Scoped to the section, an empty key decodes that section.
	var scoped settings
	if err := s.View().Sub("server").UnmarshalKey("", &scoped); err != nil {
		t.Fatalf("scoped UnmarshalKey: %v", err)
	}

	if scoped.Host != "h" || scoped.Port != 8080 {
		t.Errorf("scoped decode = %+v, want the server section", scoped)
	}
}

// os.Environ returns the process environment block in whatever order it holds,
// so when two variables map onto overlapping key paths an unsorted walk lets
// the winner change between runs of the same program with the same environment.
func TestEnv_OrderingIsDeterministic(t *testing.T) {
	t.Parallel()

	// Two variables that both resolve into the log subtree.
	vars := []string{"APP_LOG=fromlog", "APP_LOG_LEVEL=fromlevel"}

	first := ""

	for range 20 {
		s, err := NewStore(context.Background(),
			WithFiles(memFS(t, map[string]string{"/app.yaml": "other: 1\n"}), "/app.yaml"),
			WithEnv("APP", envOf(vars...)))
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}

		got := fmt.Sprint(s.View().Get("log"))
		if first == "" {
			first = got

			continue
		}

		if got != first {
			t.Fatalf("resolution changed between runs: %q then %q", first, got)
		}
	}
}

// Creating a file produces one document, so addressing a later one is a request
// that cannot be met. It used to surface as an internal invariant violation,
// which blames the module for the caller's mistake.
func TestApply_CreatingAFileCannotAddressALaterDocument(t *testing.T) {
	t.Parallel()

	s := storeOn(t, memFS(t, map[string]string{"/base.yaml": "a: 1\n"}), "/base.yaml", "/new.yaml")

	target := Source{Kind: SourceFile, Name: "/new.yaml", Writable: true, Document: 1}
	change := Set("a", 2)
	change.Target = &target

	_, err := s.Plan(change)
	if err == nil {
		t.Fatal("a target naming a document of a file that does not exist was accepted")
	}

	if !errors.Is(err, ErrInvalidTarget) {
		t.Errorf("err = %v, want ErrInvalidTarget", err)
	}
}

// Strict mode asks whether a key was authored, and Origin names only the layer
// that won — so a typo genuinely written into a config file stopped being
// reported the moment anyone exported a variable that happened to override it.
func TestValidate_StrictModeCatchesATypoEvenWhenShadowed(t *testing.T) {
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

	// The file has a typo, and an environment variable overrides that same key.
	if _, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{"/app.yaml": "log:\n  levle: info\n"}), "/app.yaml"),
		WithEnv("APP", envOf("APP_LOG_LEVLE=debug")),
		WithSchema(schema)); err == nil {
		t.Error("a misspelled key in a file was ignored because an env var shadowed it")
	}

	// An ambient variable alone is still not the application's business.
	if _, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{"/app.yaml": "log:\n  level: info\n"}), "/app.yaml"),
		WithEnv("APP", envOf("APP_VERSION=1.2.3")),
		WithSchema(schema)); err != nil {
		t.Errorf("an unrelated environment variable tripped strict mode: %v", err)
	}
}

// Validation and the accessors must agree: a value GetInt or GetDuration reads
// happily is that type as far as this module is concerned. Hand-rolled tables
// had drifted from the accessors in three places.
func TestValidate_AgreesWithTheAccessors(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Timeout time.Duration `config:"timeout"`
		Count   int           `config:"count"`
	}

	schema, err := NewSchema(WithStructSchema(cfg{}))
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}

	cases := map[string]string{
		"unitless duration from a file": "timeout: 500\ncount: 1\n",
		"duration with a unit":          "timeout: 5s\ncount: 1\n",
		"large positive integer":        "timeout: 1s\ncount: 9223372036854775807\n",
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := NewStore(context.Background(),
				WithFiles(memFS(t, map[string]string{"/app.yaml": body}), "/app.yaml"),
				WithSchema(schema))
			if err != nil {
				t.Fatalf("the schema rejected a value the accessors read: %v", err)
			}

			// And the accessor genuinely reads it, which is the agreement.
			if s.View().GetDuration("timeout") == 0 {
				t.Error("GetDuration read nothing from a value the schema accepted")
			}
		})
	}

	// Something that is not a duration at all is still refused.
	if _, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{"/app.yaml": "timeout: half past two\ncount: 1\n"}), "/app.yaml"),
		WithSchema(schema)); err == nil {
		t.Error("a value that is not a duration was accepted")
	}
}

// A path polled because it could not be watched natively must not be polled a
// second time when the rest of the set degrades, or every change to it would be
// reported twice.
func TestCoverage_DoesNotPollTheSamePathTwice(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "a: 1\n"})

	var fired atomic.Int64

	cover := &coverage{
		poll:     &pollWatcher{fs: filesystem, interval: 5 * time.Millisecond},
		ctx:      context.Background(),
		onChange: func() { fired.Add(1) },
	}

	if err := cover.ensurePolled([]string{"/app.yaml"}); err != nil {
		t.Fatalf("ensurePolled: %v", err)
	}

	// The same path again, as a runtime degrade would ask for.
	if err := cover.ensurePolled([]string{"/app.yaml", "/other.yaml"}); err != nil {
		t.Fatalf("second ensurePolled: %v", err)
	}

	defer cover.stop()

	if err := filesystem.WriteFile("/app.yaml", []byte("a: 2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	time.Sleep(80 * time.Millisecond)

	if got := fired.Load(); got > 1 {
		t.Errorf("one change reported %d times — the path is polled more than once", got)
	}
}

// A Store publishes under its lock and notifies outside it, so without ordering
// nothing stops two concurrent writes delivering their snapshots in either
// order. An observer told about version 3 and then version 2 is holding older
// configuration than it was already given, and every observer that caches
// anything derived from what it is told has that exposure — not just the typed
// sections that happened to defend themselves.
func TestNotify_NeverDeliversAnOlderSnapshotAfterANewer(t *testing.T) {
	t.Parallel()

	s := storeOn(t, memFS(t, map[string]string{
		"/app.yaml": "a: 0\nb: 0\nc: 0\nd: 0\ne: 0\nf: 0\ng: 0\nh: 0\n",
	}), "/app.yaml")

	var (
		mu   sync.Mutex
		seen []uint64
	)

	s.AddObserverFunc(func(cfg Observed) error {
		mu.Lock()
		seen = append(seen, cfg.Snapshot().Version())
		mu.Unlock()

		return nil
	})

	var wg sync.WaitGroup

	for _, key := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		wg.Add(1)

		go func(k string) {
			defer wg.Done()

			_, _ = s.Apply(context.Background(), Set(k, 1))
		}(key)
	}

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Fatalf("observer was told version %d after version %d: %v",
				seen[i], seen[i-1], seen)
		}
	}

	// And the last thing it was told is the configuration that is actually live.
	if len(seen) > 0 && seen[len(seen)-1] != s.Snapshot().Version() {
		t.Errorf("last delivery was version %d, live is %d",
			seen[len(seen)-1], s.Snapshot().Version())
	}
}

// The race above is real but very hard to provoke — a competing write has to
// complete a whole file write inside the window — so the guarantee is asserted
// directly rather than waited for. Delivering out of order is what an unordered
// notifier does; the notifier must not pass it on.
func TestNotify_SupersededDeliveryIsNotPassedOn(t *testing.T) {
	t.Parallel()

	var n notifier

	var (
		mu   sync.Mutex
		seen []uint64
	)

	n.addObserver(ObserverFunc(func(cfg Observed) error {
		mu.Lock()
		seen = append(seen, cfg.Snapshot().Version())
		mu.Unlock()

		return nil
	}))

	layers := []Layer{{
		Source: Source{Kind: SourceFile, Name: "/app.yaml"},
		Values: map[string]any{"a": 1},
	}}

	n.notify(newSnapshot(3, layers))
	n.notify(newSnapshot(2, layers))
	n.notify(newSnapshot(4, layers))

	mu.Lock()
	defer mu.Unlock()

	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Fatalf("observer was told version %d after version %d: %v",
				seen[i], seen[i-1], seen)
		}
	}

	if len(seen) == 0 || seen[len(seen)-1] != 4 {
		t.Errorf("deliveries = %v, want the newest last", seen)
	}
}

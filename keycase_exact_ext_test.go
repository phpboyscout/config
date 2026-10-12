package config_test

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"gitlab.com/phpboyscout/go/errors"

	"gitlab.com/phpboyscout/go/config"
)

// A Store built with WithCaseSensitiveKeys keeps every key as its source spells
// it. Each test runs the same sources through both modes, so the difference is
// stated directly rather than inferred. Spec 0015 D2 to D6 and D12.

var exactMode = []config.StoreOption{config.WithCaseSensitiveKeys()}

// filesStore builds a Store over YAML files written in order, lowest precedence
// first, returning the directory so a test can read back what a write did.
func filesStore(t *testing.T, opts []config.StoreOption, files ...string) (config.FS, *config.Store) {
	t.Helper()

	fsys, err := config.Dir(t.TempDir())
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}

	all := append([]config.StoreOption(nil), opts...)

	for i, content := range files {
		name := "layer" + string(rune('a'+i)) + ".yaml"

		if err := fsys.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}

		all = append(all, config.WithBackend(config.NewFileBackend(fsys, name)))
	}

	store, err := config.NewStore(context.Background(), all...)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	return fsys, store
}

func readFile(t *testing.T, fsys config.FS, name string) string {
	t.Helper()

	data, err := fsys.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}

	return string(data)
}

func TestExactKeys_LookupsCompareExactly(t *testing.T) {
	t.Parallel()

	const doc = "Server:\n  Port: 9090\n"

	_, folded := filesStore(t, nil, doc)
	_, exact := filesStore(t, exactMode, doc)

	for _, path := range []string{"Server.Port", "server.port", "SERVER.PORT"} {
		if !folded.View().Has(path) {
			t.Errorf("folded: Has(%q) = false, want every spelling to resolve", path)
		}
	}

	view := exact.View()

	if !view.Has("Server.Port") || view.GetInt("Server.Port") != 9090 {
		t.Errorf("exact: Server.Port = %v, want 9090", view.Get("Server.Port"))
	}

	for _, path := range []string{"server.port", "SERVER.PORT", "server.Port"} {
		if view.Has(path) || view.IsSet(path) || len(view.DefinedIn(path)) > 0 {
			t.Errorf("exact: %q resolves, want only Server.Port to", path)
		}

		if _, ok := view.Origin(path); ok {
			t.Errorf("exact: Origin(%q) found a source", path)
		}
	}
}

func TestExactKeys_KeysAndSubKeepTheSpelling(t *testing.T) {
	t.Parallel()

	_, folded := filesStore(t, nil, "Server:\n  Port: 9090\n")
	_, exact := filesStore(t, exactMode, "Server:\n  Port: 9090\n")

	view := exact.View()

	if got := view.Keys(); len(got) != 1 || got[0] != "Server.Port" {
		t.Errorf("exact: Keys() = %v, want [Server.Port]", got)
	}

	if view.Sub("server") != nil {
		t.Error("exact: Sub(\"server\") is non-nil, want nil for a key spelled Server")
	}

	if sub := view.Sub("Server"); sub == nil || sub.GetInt("Port") != 9090 {
		t.Errorf("exact: Sub(\"Server\").Port = %v, want 9090", sub)
	}

	if !view.CaseSensitiveKeys() || folded.View().CaseSensitiveKeys() {
		t.Error("CaseSensitiveKeys does not report the mode the Store was built in")
	}
}

func TestExactKeys_PrecedenceIsPerExactKey(t *testing.T) {
	t.Parallel()

	_, folded := filesStore(t, nil, "port: 1\n", "Port: 2\n")
	_, exact := filesStore(t, exactMode, "port: 1\n", "Port: 2\n")

	if got := folded.View().GetInt("port"); got != 2 {
		t.Errorf("folded: port = %d, want 2 from the later layer", got)
	}

	if got, other := exact.View().GetInt("port"), exact.View().GetInt("Port"); got != 1 || other != 2 {
		t.Errorf("exact: port, Port = %d, %d, want 1, 2 as two keys", got, other)
	}
}

func TestExactKeys_CaseOnlyVariantsInOneLayerAreDistinctKeys(t *testing.T) {
	t.Parallel()

	_, store := filesStore(t, exactMode, "port: 1\nPort: 2\nPORT: 3\n")

	view := store.View()
	if view.GetInt("port") != 1 || view.GetInt("Port") != 2 || view.GetInt("PORT") != 3 {
		t.Errorf("port, Port, PORT = %v, %v, %v, want 1, 2, 3", view.Get("port"), view.Get("Port"), view.Get("PORT"))
	}
}

func TestExactKeys_NonStringKeysThatStringifyAlikeStillCollide(t *testing.T) {
	t.Parallel()

	_, err := config.NewStore(context.Background(), config.WithCaseSensitiveKeys(),
		config.WithBackend(staticBackend{name: "mem", values: map[string]any{
			"ports": map[any]any{1: "a", 1.0: "b"},
		}}))
	if !errors.Is(err, config.ErrKeyCollision) {
		t.Errorf("err = %v, want ErrKeyCollision", err)
	}
}

func TestExactKeys_AWriteUsesTheCallersSpelling(t *testing.T) {
	t.Parallel()

	fsys, store := filesStore(t, exactMode, "Server:\n  Port: 9090\n")

	if _, err := store.Apply(context.Background(), config.Set("Server.Port", 8080)); err != nil {
		t.Fatalf("Apply(Server.Port): %v", err)
	}

	if _, err := store.Apply(context.Background(), config.Set("server.port", 7070)); err != nil {
		t.Fatalf("Apply(server.port): %v", err)
	}

	want := "Server:\n  Port: 8080\nserver:\n  port: 7070\n"
	if got := readFile(t, fsys, "layera.yaml"); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}

	if got := store.View().GetInt("Server.Port"); got != 8080 {
		t.Errorf("Server.Port = %d, want 8080", got)
	}
}

func TestExactKeys_ARemoveTakesOnlyTheExactKey(t *testing.T) {
	t.Parallel()

	fsys, store := filesStore(t, exactMode, "port: 1\nPort: 2\n")

	if _, err := store.Apply(context.Background(), config.Remove("Port")); err != nil {
		t.Fatalf("Apply(Remove(Port)): %v", err)
	}

	if got := readFile(t, fsys, "layera.yaml"); got != "port: 1\n" {
		t.Errorf("file = %q, want only port left", got)
	}
}

func TestExactKeys_FiltersMatchExactly(t *testing.T) {
	t.Parallel()

	values := map[string]any{"DB": map[string]any{"Password": "s3cret", "User": "app"}}

	for _, tc := range []struct {
		pattern string
		hidden  bool
	}{
		{"DB.Password", true},
		{"DB.*", true},
		{"db.password", false},
		{"DB.PASSWORD", false},
	} {
		store := filteredExact(t, values, config.Deny(tc.pattern))

		if hidden := !store.View().Has("DB.Password"); hidden != tc.hidden {
			t.Errorf("Deny(%q): DB.Password hidden = %v, want %v", tc.pattern, hidden, tc.hidden)
		}
	}

	allowed := filteredExact(t, values, config.Allow("DB.User")).View()
	if allowed.GetString("DB.User") != "app" || allowed.Has("DB.Password") {
		t.Errorf("Allow(DB.User): keys = %v, want only DB.User", allowed.Keys())
	}
}

func filteredExact(t *testing.T, values map[string]any, opts ...config.FilterOption) *config.Store {
	t.Helper()

	store, err := config.NewStore(context.Background(), config.WithCaseSensitiveKeys(),
		config.WithBackend(config.Filtered(sensitiveBackend{name: "vault", values: values}, opts...)))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	return store
}

func TestExactKeys_ForbidMatchesExactly(t *testing.T) {
	t.Parallel()

	const doc = "Credentials:\n  Token: literal-secret\n"

	for _, tc := range []struct {
		pattern string
		refused bool
	}{
		{"Credentials.*", true},
		{"credentials.*", false},
	} {
		_, err := config.NewStore(context.Background(), config.WithCaseSensitiveKeys(),
			config.WithBackend(config.Constrained(config.NewReaderBackend("suspect", []byte(doc)),
				config.Forbid(tc.pattern))))

		if refused := errors.Is(err, config.ErrInvalidConfig); refused != tc.refused {
			t.Errorf("Forbid(%q): refused = %v (err %v), want %v", tc.pattern, refused, err, tc.refused)
		}
	}
}

// The leak guard compares exactly too: in a case-sensitive Store db.password
// beside a sensitive DB.Password is a different key, and writing it leaks
// nothing that source holds.
func TestExactKeys_TheSensitiveLeakGuardComparesExactly(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		filter  []config.FilterOption
		write   string
		refused bool
	}{
		{"defined, same spelling", nil, "DB.Password", true},
		{"defined, other spelling", nil, "db.password", false},
		{"withheld, same spelling", []config.FilterOption{config.Deny("DB.Password")}, "DB.Password", true},
		{"withheld, other spelling", []config.FilterOption{config.Deny("DB.Password")}, "db.password", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fsys, err := config.Dir(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}

			if err := fsys.WriteFile("app.yaml", []byte("app: demo\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			secrets := config.Filtered(sensitiveBackend{name: "vault", values: map[string]any{
				"DB": map[string]any{"Password": "s3cret"},
			}}, tc.filter...)

			store, err := config.NewStore(context.Background(), config.WithCaseSensitiveKeys(),
				config.WithBackend(config.NewFileBackend(fsys, "app.yaml")),
				config.WithBackend(secrets))
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}

			_, err = store.Apply(context.Background(), config.Set(tc.write, "new"))
			if refused := errors.Is(err, config.ErrSensitiveLeak); refused != tc.refused {
				t.Errorf("Apply(Set(%q)): err = %v, want refused = %v", tc.write, err, tc.refused)
			}
		})
	}
}

func TestExactKeys_ASpellingOnlyChangeIsAChange(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		opts    []config.StoreOption
		notifed bool
	}{
		{nil, false},
		{exactMode, true},
	} {
		fsys, store := filesStore(t, tc.opts, "port: 1\n")

		notified := false

		store.AddObserverFunc(func(config.Observed) error {
			notified = true

			return nil
		})

		if err := fsys.WriteFile("layera.yaml", []byte("Port: 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := store.Reload(context.Background()); err != nil {
			t.Fatalf("Reload: %v", err)
		}

		if notified != tc.notifed {
			t.Errorf("exact = %v: observers notified = %v, want %v", len(tc.opts) > 0, notified, tc.notifed)
		}
	}
}

// --- D3: how the mode reaches each site ----------------------------------

func TestExactKeys_AMountRegisteredBeforeTheOptionMatchesExactly(t *testing.T) {
	t.Parallel()

	schema, err := config.NewSchema(config.WithStructSchema(struct {
		TTL int `config:"ttl" validate:"required"`
	}{}))
	if err != nil {
		t.Fatal(err)
	}

	// The option comes after the mount, so the mount's prefix must not have
	// been folded when WithSchemaAt ran.
	opts := []config.StoreOption{config.WithSchemaAt("Plugins.Cache", schema, config.Required), config.WithCaseSensitiveKeys()}

	if _, err := config.NewStore(context.Background(),
		append(opts, config.WithReaders(config.NamedSource{Name: "a", Content: []byte("Plugins:\n  Cache:\n    ttl: 5\n")}))...,
	); err != nil {
		t.Errorf("NewStore over Plugins.Cache.ttl: %v, want it valid", err)
	}

	_, err = config.NewStore(context.Background(),
		append(opts, config.WithReaders(config.NamedSource{Name: "a", Content: []byte("plugins:\n  cache:\n    ttl: 5\n")}))...)
	if !errors.Is(err, config.ErrInvalidConfig) {
		t.Errorf("NewStore over plugins.cache.ttl: err = %v, want the Plugins.Cache mount missing", err)
	}
}

func TestExactKeys_ValidateSelectsMountsExactly(t *testing.T) {
	t.Parallel()

	schema, err := config.NewSchema(config.WithStructSchema(struct {
		TTL int `config:"ttl"`
	}{}))
	if err != nil {
		t.Fatal(err)
	}

	store, err := config.NewStore(context.Background(), config.WithCaseSensitiveKeys(),
		config.WithSchemaAt("Plugins", schema),
		config.WithReaders(config.NamedSource{Name: "a", Content: []byte("Plugins:\n  ttl: soon\n")}))
	if !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("NewStore: err = %v, want the bad ttl refused", err)
	}

	if got := store.Validate("Plugins"); got.Valid() {
		t.Error("Validate(\"Plugins\") ran no mount, want the Plugins mount")
	}

	if got := store.Validate("plugins"); !got.Valid() {
		t.Errorf("Validate(\"plugins\") = %v, want no mount selected in a case-sensitive Store", got.Errors)
	}
}

func TestFoldedKeys_AMixedCaseMountIsFolded(t *testing.T) {
	t.Parallel()

	schema, err := config.NewSchema(config.WithStructSchema(struct {
		TTL int `config:"ttl"`
	}{}))
	if err != nil {
		t.Fatal(err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithSchemaAt("Plugins", schema),
		config.WithReaders(config.NamedSource{Name: "a", Content: []byte("plugins:\n  ttl: soon\n")}))
	if !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("NewStore: err = %v, want the bad ttl refused", err)
	}

	got := store.Validate("plugins")
	if len(got.Errors) != 1 || got.Errors[0].Key != "plugins.ttl" {
		t.Errorf("Validate(\"plugins\") = %v, want one failure at plugins.ttl", got.Errors)
	}
}

// Each core decorator forwards the mode to what it wraps, so a constraint
// beneath a filter, or a filter beneath a constraint, matches exactly too.
func TestExactKeys_DecoratorsForwardTheMode(t *testing.T) {
	t.Parallel()

	const doc = "Credentials:\n  Token: literal-secret\n"

	for name, wrap := range map[string]func() config.Backend{
		"constraint beneath a filter": func() config.Backend {
			return config.Filtered(config.Constrained(config.NewReaderBackend("src", []byte(doc)),
				config.Forbid("credentials.*")), config.Deny("other"))
		},
		"filter beneath a constraint": func() config.Backend {
			return config.Constrained(config.Filtered(config.NewReaderBackend("src", []byte(doc)),
				config.Deny("credentials.token")), config.Forbid("other"))
		},
	} {
		store, err := config.NewStore(context.Background(), config.WithCaseSensitiveKeys(), config.WithBackend(wrap()))
		if err != nil {
			t.Errorf("%s: %v, want the lower-case pattern not to match Credentials.Token", name, err)

			continue
		}

		if !store.View().Has("Credentials.Token") {
			t.Errorf("%s: Credentials.Token hidden, want the lower-case pattern not to match it", name)
		}
	}
}

type cachedSchemaShape struct {
	Server struct {
		Port int `config:"port" validate:"required"`
	}
}

// SchemaOf caches per type, so one schema is built once and has to serve a
// Store of either mode in the same process.
func TestExactKeys_OneCachedSchemaServesBothModes(t *testing.T) {
	t.Parallel()

	_, folded := filesStore(t, nil, "server:\n  port: 1\n")
	_, exact := filesStore(t, exactMode, "Server:\n  port: 1\n")
	_, exactMisspelt := filesStore(t, exactMode, "server:\n  port: 1\n")

	if err := config.ValidateStruct[cachedSchemaShape](folded.View(), config.WithStrictMode()); err != nil {
		t.Errorf("folded: %v", err)
	}

	if err := config.ValidateStruct[cachedSchemaShape](exact.View()); err != nil {
		t.Errorf("exact, Server.port: %v", err)
	}

	if err := config.ValidateStruct[cachedSchemaShape](exactMisspelt.View()); !errors.Is(err, config.ErrInvalidConfig) {
		t.Errorf("exact, server.port: err = %v, want Server.port required", err)
	}

	if err := config.ValidateStruct[cachedSchemaShape](folded.View()); err != nil {
		t.Errorf("folded, after an exact validation: %v", err)
	}
}

// A camelCase config tag used to be reported unknown in a folded Store, because
// the keys were folded and the tag was not.
func TestExactKeys_ACamelCaseTagIsKnownInAFoldedStore(t *testing.T) {
	t.Parallel()

	type shape struct {
		LogLevel string `config:"logLevel"`
	}

	_, store := filesStore(t, nil, "logLevel: debug\n")

	if err := config.ValidateStruct[shape](store.View(), config.WithStrictMode()); err != nil {
		t.Errorf("ValidateStruct: %v, want logLevel known", err)
	}
}

// readerOnly hides every optional method a View has, as a consumer's own
// wrapper or a mock would.
type readerOnly struct{ config.Reader }

// probedReader reports no section, as a mock may, so UnmarshalSection decides
// by probing for the target's fields.
type probedReader struct{ readerOnly }

func (probedReader) SectionExists(string) bool { return false }

type forwardingReader struct{ probedReader }

func (r forwardingReader) CaseSensitiveKeys() bool {
	return r.Reader.(config.KeyCaseReporter).CaseSensitiveKeys()
}

type portSection struct{ Port int }

func TestExactKeys_UnmarshalSectionProbesInTheReadersMode(t *testing.T) {
	t.Parallel()

	_, store := filesStore(t, exactMode, "app:\n  port: 1\n")
	probed := probedReader{readerOnly{store.View()}}

	// Forwarded, the probe is exact, and field Port does not match key port.
	got, err := config.UnmarshalSection[portSection](forwardingReader{probed}, "app")
	if err != nil {
		t.Fatal(err)
	}

	if got.Exists {
		t.Errorf("forwarded: section = %+v, want absent", got)
	}

	// Not forwarded, the probe reads folded (spec 0015 OQ6), so the section is
	// found; the decode is the View's own, so the value stays unmatched.
	got, err = config.UnmarshalSection[portSection](probed, "app")
	if err != nil {
		t.Fatal(err)
	}

	if !got.Exists || got.Value.Port != 0 {
		t.Errorf("not forwarded: section = %+v, want present and undecoded", got)
	}
}

func TestExactKeys_ValidateStructReadsAWrapperAsFolded(t *testing.T) {
	t.Parallel()

	type shape struct {
		Server struct {
			Port int `config:"port" validate:"required"`
		}
	}

	_, store := filesStore(t, exactMode, "server:\n  port: 1\n")

	if err := config.ValidateStruct[shape](store.View()); !errors.Is(err, config.ErrInvalidConfig) {
		t.Errorf("View: err = %v, want Server.port missing", err)
	}

	if err := config.ValidateStruct[shape](readerOnly{store.View()}); err != nil {
		t.Errorf("wrapper: %v, want the folded field key server.port to resolve", err)
	}
}

// outsideDecorator stands for a decorator written outside this module. Only
// one that forwards BindKeyCase leaves the backend it wraps in the Store's mode.
type outsideDecorator struct {
	config.Backend

	forward bool
}

func (d outsideDecorator) BindKeyCase(caseSensitive bool) error {
	if b, ok := d.Backend.(config.KeyCaseBinder); ok && d.forward {
		return b.BindKeyCase(caseSensitive)
	}

	return nil
}

func TestExactKeys_AnOutsideDecoratorMustForwardTheMode(t *testing.T) {
	t.Parallel()

	for _, forward := range []bool{true, false} {
		filter := config.Filtered(sensitiveBackend{name: "vault", values: map[string]any{
			"DB": map[string]any{"Password": "s3cret"},
		}}, config.Deny("db.password"))

		store, err := config.NewStore(context.Background(), config.WithCaseSensitiveKeys(),
			config.WithBackend(outsideDecorator{Backend: filter, forward: forward}))
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}

		// Forwarded, the filter matches exactly and db.password does not name
		// DB.Password. Not forwarded, it still folds and hides it.
		if visible := store.View().Has("DB.Password"); visible != forward {
			t.Errorf("forward = %v: DB.Password visible = %v", forward, visible)
		}
	}
}

// --- D4 and D5: environment variables and flags --------------------------

func envStore(t *testing.T, opts []config.StoreOption, doc string, env ...string) (*config.Store, error) {
	t.Helper()

	return config.NewStore(context.Background(), append(append([]config.StoreOption(nil), opts...),
		config.WithReaders(config.NamedSource{Name: "file", Content: []byte(doc)}),
		config.WithEnv("APP", config.WithEnviron(func() []string { return env })))...)
}

func TestExactKeys_AnEnvVariableReachesAnExistingMixedCaseKey(t *testing.T) {
	t.Parallel()

	for _, variable := range []string{"APP_Log_Level=debug", "APP_LOG_LEVEL=debug"} {
		store, err := envStore(t, exactMode, "Log:\n  Level: info\n", variable)
		if err != nil {
			t.Fatalf("%s: %v", variable, err)
		}

		if got := store.View().GetString("Log.Level"); got != "debug" {
			t.Errorf("%s: Log.Level = %q, want debug", variable, got)
		}
	}
}

func TestExactKeys_AnEnvVariableMatchingTwoSpellingsIsRefused(t *testing.T) {
	t.Parallel()

	_, err := envStore(t, exactMode, "Log:\n  Level: info\nlog:\n  level: warn\n", "APP_LOG_LEVEL=debug")
	if !errors.Is(err, config.ErrAmbiguousEnvKey) {
		t.Errorf("err = %v, want ErrAmbiguousEnvKey", err)
	}
}

func TestExactKeys_AnEnvVariableWithNoMatchFallsBackToLowerCase(t *testing.T) {
	t.Parallel()

	store, err := envStore(t, exactMode, "app: demo\n", "APP_Log_Level=debug")
	if err != nil {
		t.Fatal(err)
	}

	if got := store.View().GetString("log.level"); got != "debug" {
		t.Errorf("log.level = %q, want debug", got)
	}
}

// A folded Store compares the variable as given. Upper-casing it there would
// send APP_Log_Level to the flat log_level instead of creating log.level.
func TestFoldedKeys_AnEnvVariableKeepsTodaysRule(t *testing.T) {
	t.Parallel()

	store, err := envStore(t, nil, "log_level: info\n", "APP_Log_Level=debug")
	if err != nil {
		t.Fatal(err)
	}

	view := store.View()
	if view.GetString("log_level") != "info" || view.GetString("log.level") != "debug" {
		t.Errorf("log_level, log.level = %q, %q, want info, debug", view.GetString("log_level"), view.GetString("log.level"))
	}
}

func TestKeys_FlagsKeepTheirSpelling(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		opts  []config.StoreOption
		flag  string
		bind  []config.FlagOption
		read  string
		other string
	}{
		{"folded, dashed name", nil, "log-level", nil, "log.level", "LOG.LEVEL"},
		{"folded, mixed-case name", nil, "Log-Level", nil, "log.level", "Log.Level"},
		{"exact, mixed-case name", exactMode, "Log-Level", nil, "Log.Level", ""},
		{"exact, bound to a mixed-case key", exactMode, "level", []config.FlagOption{config.BindFlag("level", "Log.Level")}, "Log.Level", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
			flags.String(tc.flag, "", "")

			if err := flags.Parse([]string{"--" + tc.flag + "=debug"}); err != nil {
				t.Fatal(err)
			}

			store, err := config.NewStore(context.Background(), append(append([]config.StoreOption(nil), tc.opts...),
				config.WithReaders(config.NamedSource{Name: "file", Content: []byte("app: demo\n")}),
				config.WithFlags(flags, tc.bind...))...)
			if err != nil {
				t.Fatal(err)
			}

			if got := store.View().GetString(tc.read); got != "debug" {
				t.Errorf("%s = %q, want debug; keys %v", tc.read, got, store.View().Keys())
			}

			if tc.other != "" && store.View().GetString(tc.other) != "debug" {
				t.Errorf("%s does not resolve in a folded Store", tc.other)
			}
		})
	}
}

// --- D6: struct decoding -------------------------------------------------

func TestExactKeys_DecodingMatchesFieldsExactly(t *testing.T) {
	t.Parallel()

	type untagged struct{ AccessKeyID string }

	type tagged struct {
		Region string `mapstructure:"region"`
	}

	for _, tc := range []struct {
		doc        string
		key, other string
	}{
		{"AccessKeyID: a\nregion: r\n", "a", "r"},
		{"accesskeyid: a\nRegion: r\n", "", ""},
		{"AccessKeyID: a\nACCESSKEYID: b\naccesskeyid: c\nregion: r\nREGION: s\n", "a", "r"},
	} {
		_, store := filesStore(t, exactMode, tc.doc)

		var u untagged
		if err := store.View().Unmarshal(&u); err != nil {
			t.Fatal(err)
		}

		var g tagged
		if err := store.View().Unmarshal(&g); err != nil {
			t.Fatal(err)
		}

		if u.AccessKeyID != tc.key || g.Region != tc.other {
			t.Errorf("%q: AccessKeyID, Region = %q, %q, want %q, %q", tc.doc, u.AccessKeyID, g.Region, tc.key, tc.other)
		}
	}
}

func TestFoldedKeys_DecodingStillMatchesAnyCase(t *testing.T) {
	t.Parallel()

	_, store := filesStore(t, nil, "AccessKeyID: a\n")

	var got struct{ AccessKeyID string }
	if err := store.View().Unmarshal(&got); err != nil {
		t.Fatal(err)
	}

	if got.AccessKeyID != "a" {
		t.Errorf("AccessKeyID = %q, want a", got.AccessKeyID)
	}
}

// --- D12 and binding: one mode per backend -------------------------------

func TestExactKeys_ANestedStoreOfTheOtherModeIsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		wrap func(*config.Store) config.Backend
	}{
		{"plain", func(s *config.Store) config.Backend { return config.Nested(s, "team") }},
		{"promotable", func(s *config.Store) config.Backend {
			return config.Nested(s, "team", config.NestedPromotable)
		}},
		{"filtered", func(s *config.Store) config.Backend {
			return config.Filtered(config.Nested(s, "team"), config.Deny("secret"))
		}},
		{"constrained", func(s *config.Store) config.Backend {
			return config.Constrained(config.Nested(s, "team"), config.Forbid("secret"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			for _, inner := range [][]config.StoreOption{nil, exactMode} {
				_, innerStore := filesStore(t, inner, "team: a\n")

				outer := exactMode
				if len(inner) > 0 {
					outer = nil
				}

				_, err := config.NewStore(context.Background(), append(append([]config.StoreOption(nil), outer...),
					config.WithBackend(tc.wrap(innerStore)))...)
				if !errors.Is(err, config.ErrKeyCaseMismatch) {
					t.Errorf("inner exact = %v: err = %v, want ErrKeyCaseMismatch", len(inner) > 0, err)
				}

				_, err = config.NewStore(context.Background(), append(append([]config.StoreOption(nil), inner...),
					config.WithBackend(tc.wrap(innerStore)))...)
				if err != nil {
					t.Errorf("inner exact = %v, same mode: %v", len(inner) > 0, err)
				}
			}
		})
	}
}

func TestExactKeys_ABackendBoundToOneModeRefusesTheOther(t *testing.T) {
	t.Parallel()

	shared := config.Filtered(staticBackend{name: "mem", values: map[string]any{"a": 1}}, config.Deny("b"))

	if _, err := config.NewStore(context.Background(), config.WithBackend(shared)); err != nil {
		t.Fatalf("first Store: %v", err)
	}

	_, err := config.NewStore(context.Background(), config.WithCaseSensitiveKeys(), config.WithBackend(shared))
	if !errors.Is(err, config.ErrKeyCaseMismatch) {
		t.Errorf("err = %v, want ErrKeyCaseMismatch", err)
	}

	if !strings.Contains(err.Error(), "mem") {
		t.Errorf("err = %v, want it to name the backend", err)
	}
}

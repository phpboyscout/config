package config_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"gitlab.com/phpboyscout/go/errors"

	"gitlab.com/phpboyscout/go/config"
)

// An empty key cannot be addressed by a dotted path, and the module's own walks
// disagreed about where it sat, so a layer holding one is refused (go/config#32).

func TestEmptyKey_ALayerHoldingOneIsRefused(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ doc, at string }{
		"top level":           {"\"\": 1\nport: 2\n", "at the top level"},
		"under a parent":      {"c:\n  \"\": 3\n", `under "c"`},
		"beside a sibling":    {"a:\n  \"\": 1\n  b: 2\n", `under "a"`},
		"deep":                {"a:\n  b:\n    \"\": x\n", `under "a.b"`},
		"inside a list":       {"items:\n  - \"\": a\n", `under "items[0]"`},
		"as an empty mapping": {"c:\n  \"\": {}\n", `under "c"`},
		"whitespace only":     {"\" \": 1\n", `whitespace-only key " " at the top level`},
		"tab, nested":         {"c:\n  \"\\t\": 1\n", `whitespace-only key "\t" under "c"`},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, err := config.NewStore(context.Background(),
				config.WithReaders(config.NamedSource{Name: "app.yaml", Content: []byte(tc.doc)}))
			if !errors.Is(err, config.ErrEmptyKey) {
				t.Fatalf("NewStore err = %v, want ErrEmptyKey", err)
			}

			if store != nil {
				t.Error("NewStore returned a Store alongside an empty key; it must return none")
			}

			for _, want := range []string{"app.yaml", tc.at} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %s", err, want)
				}
			}
		})
	}
}

func TestEmptyKey_IsRefusedUnderExactKeysToo(t *testing.T) {
	t.Parallel()

	_, err := config.NewStore(context.Background(), config.WithCaseSensitiveKeys(),
		config.WithReaders(config.NamedSource{Name: "app.yaml", Content: []byte("c:\n  \"\": 3\n")}))
	if !errors.Is(err, config.ErrEmptyKey) {
		t.Fatalf("NewStore err = %v, want ErrEmptyKey", err)
	}
}

func TestEmptyKey_IsNotACollision(t *testing.T) {
	t.Parallel()

	_, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "app.yaml", Content: []byte("c:\n  \"\": 3\n")}))
	if errors.Is(err, config.ErrKeyCollision) {
		t.Errorf("err = %v; an empty key is its own refusal, not ErrKeyCollision", err)
	}
}

func TestEmptyKey_AReloadKeepsTheLastGoodSnapshot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	writeFile(t, path, "port: 1\n")

	fsys, err := config.Dir(dir)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}

	store, err := config.NewStore(context.Background(), config.WithBackend(config.NewFileBackend(fsys, "app.yaml")))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	notified := 0

	store.AddObserverFunc(func(config.Observed) error {
		notified++

		return nil
	})

	writeFile(t, path, "port: 1\nserver:\n  \"\": 2\n")

	if err := store.Reload(context.Background()); !errors.Is(err, config.ErrEmptyKey) {
		t.Fatalf("Reload err = %v, want ErrEmptyKey", err)
	}

	if got := store.View().GetInt("port"); got != 1 {
		t.Errorf("port = %d after a refused reload, want the last good 1", got)
	}

	if notified != 0 {
		t.Errorf("observers notified %d times by a refused reload, want 0", notified)
	}
}

func TestEmptyKey_AddLayerWithdrawsOnlyItself(t *testing.T) {
	t.Parallel()

	store, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "base.yaml", Content: []byte("port: 1\n")}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	err = store.AddLayer(context.Background(), "extra.yaml", strings.NewReader("host: a\n\"\": b\n"))
	if !errors.Is(err, config.ErrEmptyKey) {
		t.Fatalf("AddLayer err = %v, want ErrEmptyKey", err)
	}

	if store.View().IsSet("host") {
		t.Error("host is set; the refused layer was not withdrawn")
	}

	if err := store.AddLayer(context.Background(), "extra.yaml", strings.NewReader("host: a\n")); err != nil {
		t.Errorf("re-adding the corrected layer: %v", err)
	}
}

// The environment and the command line are ambient: a stray variable must not
// stop the tool starting. A name yielding an empty segment was never readable,
// so it is skipped rather than refused.
func TestEmptyKey_AnEnvNameWithAnEmptySegmentIsSkipped(t *testing.T) {
	t.Parallel()

	env := config.WithEnviron(func() []string {
		return []string{"APP_SERVER__PORT=1", "APP_FOO_=2", "APP__X=3", "APP_SERVER_HOST=h"}
	})

	store, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "app.yaml", Content: []byte("server:\n  port: 8080\n")}),
		config.WithEnv("APP", env))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	view := store.View()
	if got := view.GetInt("server.port"); got != 8080 {
		t.Errorf("server.port = %d, want the file's 8080", got)
	}

	if got := view.GetString("server.host"); got != "h" {
		t.Errorf("server.host = %q, want the well-formed variable still applied", got)
	}
}

func TestEmptyKey_AFlagNameWithAnEmptySegmentIsSkipped(t *testing.T) {
	t.Parallel()

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("server--port", "", "")
	flags.String("server-host", "", "")

	if err := flags.Parse([]string{"--server--port=1", "--server-host=h"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	store, err := config.NewStore(context.Background(), config.WithBackend(config.NewFlagBackend(flags)))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if got := store.View().GetString("server.host"); got != "h" {
		t.Errorf("server.host = %q, want h", got)
	}
}

func TestEmptyKey_APrefixOnlyEnvNameIsSkipped(t *testing.T) {
	t.Parallel()

	env := config.WithEnviron(func() []string { return []string{"APP_=1", "APP_PORT=2"} })

	store, err := config.NewStore(context.Background(), config.WithEnv("APP", env))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if got := store.View().GetInt("port"); got != 2 {
		t.Errorf("port = %d, want 2", got)
	}
}

// A leading dash needs no case: pflag refuses "---port" before the backend sees it.
func TestEmptyKey_AFlagWithATrailingDashIsSkipped(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"port-", "server-port-"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
			flags.String(name, "", "")

			if err := flags.Parse([]string{"--" + name + "=1"}); err != nil {
				t.Fatalf("Parse: %v", err)
			}

			if _, err := config.NewStore(context.Background(), config.WithBackend(config.NewFlagBackend(flags))); err != nil {
				t.Errorf("NewStore: %v; a flag yielding an empty segment must be skipped", err)
			}
		})
	}
}

func TestEmptyKey_AKeyWithTextAroundSpacesStillLoads(t *testing.T) {
	t.Parallel()

	_, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "app.yaml", Content: []byte("\"a b\": 1\n")}))
	if err != nil {
		t.Fatalf("NewStore: %v; only a key with no text at all is refused", err)
	}
}

func TestEmptyKey_AWhitespaceFlagSegmentIsSkipped(t *testing.T) {
	t.Parallel()

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("server- ", "", "")

	if err := flags.Parse([]string{"--server- =1"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if _, err := config.NewStore(context.Background(), config.WithBackend(config.NewFlagBackend(flags))); err != nil {
		t.Errorf("NewStore: %v; a flag yielding a whitespace-only key must be skipped", err)
	}
}

func TestEmptyKey_AWhitespaceEnvSegmentIsSkipped(t *testing.T) {
	t.Parallel()

	env := config.WithEnviron(func() []string { return []string{"APP_SERVER_ =1", "APP_SERVER_PORT=2"} })

	store, err := config.NewStore(context.Background(), config.WithEnv("APP", env))
	if err != nil {
		t.Fatalf("NewStore: %v; a variable yielding a whitespace-only key must be skipped", err)
	}

	if got := store.View().GetInt("server.port"); got != 2 {
		t.Errorf("server.port = %d, want 2", got)
	}
}

func TestEmptyKey_ANestedWhitespaceKeyIsRefusedThoughAPathReachedIt(t *testing.T) {
	t.Parallel()

	_, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "app.yaml", Content: []byte("c:\n  \" \": 1\n  \"  \": 2\n")}))
	if !errors.Is(err, config.ErrEmptyKey) {
		t.Fatalf("NewStore err = %v, want ErrEmptyKey", err)
	}
}

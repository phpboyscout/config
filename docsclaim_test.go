package config_test

import (
	"context"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/go/config"
)

// TestDocsIndexWriteClaim pins the worked example on the documentation's front
// page. That page contrasts a merged-view write against this module's, and the
// contrast is the argument for the whole design — so it is asserted here rather
// than trusted to stay true.
//
// If this fails, docs/index.md is now claiming something the module does not do.
func TestDocsIndexWriteClaim(t *testing.T) {
	t.Parallel()

	const original = `# Which port the public listener binds to.
# Changing this needs a firewall change too — talk to platform first.
server:
  host: localhost   # loopback only in dev
  port: 8080

# Feature flags. Keep alphabetical.
features:
  beta_ui: false
`

	// Exactly what the page shows: an embedded default the file never mentions,
	// and a secret arriving from the environment.
	const want = `# Which port the public listener binds to.
# Changing this needs a firewall change too — talk to platform first.
server:
  host: localhost # loopback only in dev
  port: 9090

# Feature flags. Keep alphabetical.
features:
  beta_ui: false
`

	fsys := config.NewMemFS()
	if err := fsys.WriteFile("/config.yaml", []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{
			Name: "embedded:defaults.yaml", Content: []byte("log:\n  level: info\n"),
		}),
		config.WithFiles(fsys, "/config.yaml"),
		config.WithEnv("APP", config.WithEnviron(func() []string {
			return []string{"APP_DB_PASSWORD=hunter2-prod-secret"}
		})),
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.Apply(context.Background(), config.Set("server.port", 9090)); err != nil {
		t.Fatal(err)
	}

	got, err := fsys.ReadFile("/config.yaml")
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != want {
		t.Errorf("the front page's worked example is no longer true.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// The two failures the page calls out by name, asserted individually so a
	// regression says which promise broke.
	if strings.Contains(string(got), "hunter2-prod-secret") {
		t.Error("a value from the environment was written into the file")
	}

	if strings.Contains(string(got), "log:") {
		t.Error("an embedded default was materialised into the user's file")
	}
}

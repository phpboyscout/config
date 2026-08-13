package config_test

import (
	"context"
	"errors"
	"sort"
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

// TestComposeSchemasDocClaims pins the output docs/how-to/compose-schemas.md
// quotes verbatim. The page's argument is that an aggregate report names who
// objected, so the strings it shows a reader are part of the contract.
//
// If this fails, that page is now quoting messages the module does not produce.
func TestComposeSchemasDocClaims(t *testing.T) {
	t.Parallel()

	server, err := config.NewSchema(config.WithStructSchema(struct {
		Host string `config:"host" validate:"required"`
	}{}))
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}

	fsys := config.NewMemFS()
	if err := fsys.WriteFile("/app.yaml",
		[]byte("other: x\ncredentials:\n  token: literal-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithBackend(config.Constrained(
			config.NewFileBackend(fsys, "/app.yaml"),
			config.Forbid("credentials.*"),
			config.ConstraintName("project-config-file"),
		)),
		config.WithSchemaAt("server", server, config.Required),
	)
	if err != nil && !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("NewStore: %v", err)
	}

	// What the page prints, as "key: message [contributor]".
	got := map[string]bool{}
	for _, e := range store.Validate().Errors {
		got[e.Key+": "+e.Message+" ["+e.Contributor+"]"] = true
	}

	for _, want := range []string{
		"server: required configuration section is missing [server]",
		"credentials.token: supplied by a source that is forbidden to carry it [project-config-file]",
	} {
		if !got[want] {
			t.Errorf("the page quotes %q, which the module no longer produces.\ngot: %v", want, keysOf(got))
		}
	}

}

// TestComposeSchemasDocWriteRefusal pins the refusal message the same page
// quotes, and the sentinel it tells the reader to match.
func TestComposeSchemasDocWriteRefusal(t *testing.T) {
	t.Parallel()

	fsys := config.NewMemFS()
	if err := fsys.WriteFile("/app.yaml", []byte("safe: yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithBackend(config.Constrained(
			config.NewFileBackend(fsys, "/app.yaml"), config.Forbid("credentials.*"))),
	)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	_, err = store.Apply(context.Background(), config.Set("credentials.token", "sneaky"))

	const wantMsg = `config: forbidden key: "credentials.token" may not be written to source "/app.yaml"`
	if err == nil || err.Error() != wantMsg {
		t.Errorf("the page quotes the refusal as %q, got %v", wantMsg, err)
	}

	if !errors.Is(err, config.ErrForbiddenKey) {
		t.Error("the page tells the reader to match ErrForbiddenKey")
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}

// TestJSONDocClaims pins what docs/how-to/json.md now tells a reader they do NOT
// need config-json for. The page previously said the core "reads and writes only
// YAML", which sent people to a module for something already in the box.
func TestJSONDocClaims(t *testing.T) {
	t.Parallel()

	// Claim 1: the core reads a JSON document unaided, YAML 1.2 being a superset.
	store, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "app.json", Content: []byte(
			`{"server": {"host": "h", "port": 8080}}`)}),
	)
	if err != nil {
		t.Fatalf("the page says a JSON document loads with no module: %v", err)
	}

	if got := store.View().GetInt("server.port"); got != 8080 {
		t.Errorf("server.port = %d, want 8080 — values must come back typed", got)
	}

	// Claim 2: it refuses JSON Lines, which is half of why config-json exists.
	_, err = config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "s.jsonl", Content: []byte("{\"a\":1}\n{\"b\":2}\n")}),
	)
	if err == nil {
		t.Error("the page says the core refuses JSON Lines; it accepted them")
	}
}

func TestJSONDocClaims_CoreWriteReflowsButStaysValid(t *testing.T) {
	t.Parallel()

	// Claim 3, and the reason to reach for config-json: a core write to a .json
	// file produces valid JSON but loses the layout it had.
	fsys := config.NewMemFS()
	if err := fsys.WriteFile("/app.json",
		[]byte("{\n  \"server\": {\n    \"host\": \"h\",\n    \"port\": 8080\n  }\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := config.NewStore(context.Background(), config.WithFiles(fsys, "/app.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := store.Apply(context.Background(), config.Set("server.port", 9090)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got, err := fsys.ReadFile("/app.json")
	if err != nil {
		t.Fatal(err)
	}

	const want = `{"server": {"host": "h", "port": 9090}}`
	if strings.TrimSpace(string(got)) != want {
		t.Errorf("the page quotes the reflowed result as\n  %s\ngot\n  %s", want, got)
	}

	if strings.Count(strings.TrimSpace(string(got)), "\n") != 0 {
		t.Error("the page's point is that the layout collapses to one line")
	}
}

package config

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// serverConfig is the smallest shape that can express each of the failure
// modes D15 distinguishes: a required key, a typed key and a restricted key.
type serverConfig struct {
	Server struct {
		Host string `config:"server.host" validate:"required"`
		Port int    `config:"server.port"`
	}
	Log struct {
		Level string `config:"log.level" enum:"debug,info,warn"`
	}
}

func portSchema(t *testing.T) *Schema {
	t.Helper()

	schema, err := NewSchema(WithStructSchema(serverConfig{}))
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}

	return schema
}

// schemaStore builds a Store that may legitimately start invalid — several of
// these tests depend on exactly that — so it tolerates ErrInvalidConfig while
// still failing on anything that genuinely leaves no configuration behind.
func schemaStore(t *testing.T, files map[string]string, paths ...string) (*Store, FS) {
	t.Helper()

	filesystem := memFS(t, files)

	s, err := NewStore(context.Background(),
		WithFiles(filesystem, paths...),
		WithSchema(portSchema(t)))
	if err != nil && !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewStore: %v", err)
	}

	if s == nil {
		t.Fatal("NewStore returned no Store, leaving an invalid config unrepairable")
	}

	return s, filesystem
}

// A write that would produce an invalid configuration must be refused before
// anything reaches the file. Validating after the fact would leave the file
// changed and the process running on last-known-good — the worst of both.
func TestApply_RefusesAWriteThatWouldInvalidateTheConfig(t *testing.T) {
	t.Parallel()

	s, filesystem := schemaStore(t, map[string]string{
		"/app.yaml": "server:\n  host: h\n  port: 8080\nlog:\n  level: info\n",
	}, "/app.yaml")

	_, err := s.Apply(context.Background(), Set("log.level", "verbose"))
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}

	// Nothing reached the file.
	content, readErr := filesystem.ReadFile("/app.yaml")
	if readErr != nil {
		t.Fatalf("read: %v", readErr)
	}

	if strings.Contains(string(content), "verbose") {
		t.Errorf("the refused value was written anyway:\n%s", content)
	}

	// And the published configuration is untouched.
	if got := s.View().GetString("log.level"); got != "info" {
		t.Errorf("log.level = %q, want info", got)
	}
}

// A file may be invalid on its own and valid once merged — a base omitting a
// key its overlay supplies. Validating the layer in isolation would reject a
// perfectly correct write.
func TestApply_ValidatesTheMergedResultNotTheLayerAlone(t *testing.T) {
	t.Parallel()

	// base.yaml has no server.host at all; over.yaml supplies it.
	s, _ := schemaStore(t, map[string]string{
		"/base.yaml": "server:\n  port: 8080\n",
		"/over.yaml": "server:\n  host: h\n",
	}, "/base.yaml", "/over.yaml")

	// The write lands in over.yaml, which alone lacks the required port type
	// context. Merged, everything is present and correct.
	if _, err := s.Apply(context.Background(), Set("server.port", 9090)); err != nil {
		t.Fatalf("Apply rejected a write that is valid once merged: %v", err)
	}

	if got := s.View().GetInt("server.port"); got != 9090 {
		t.Errorf("server.port = %d, want 9090", got)
	}
}

// A configuration that is already invalid must stay editable. Otherwise one
// missing key makes the config unfixable through the very surface designed to
// fix it.
func TestApply_AnAlreadyInvalidConfigRemainsEditable(t *testing.T) {
	t.Parallel()

	// server.host is required and absent, so this config is invalid from the
	// start.
	s, _ := schemaStore(t, map[string]string{
		"/app.yaml": "server:\n  port: 8080\n",
	}, "/app.yaml")

	// An unrelated edit must go through despite the pre-existing violation.
	if _, err := s.Apply(context.Background(), Set("server.port", 9090)); err != nil {
		t.Fatalf("Apply refused an edit to an already-invalid config: %v", err)
	}

	// And the edit that repairs it must go through too.
	if _, err := s.Apply(context.Background(), Set("server.host", "h")); err != nil {
		t.Fatalf("Apply refused the edit that fixes the config: %v", err)
	}

	if got := s.View().GetString("server.host"); got != "h" {
		t.Errorf("server.host = %q, want h", got)
	}
}

// Adding a new violation to a config that already had one must still fail, and
// the error has to separate the two — a caller told only "config is invalid"
// cannot tell whether they broke it.
func TestApply_DistinguishesAnIntroducedViolationFromAPreExistingOne(t *testing.T) {
	t.Parallel()

	// server.host is required and missing: pre-existing.
	s, _ := schemaStore(t, map[string]string{
		"/app.yaml": "server:\n  port: 8080\nlog:\n  level: info\n",
	}, "/app.yaml")

	_, err := s.Apply(context.Background(), Set("log.level", "verbose"))
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}

	msg := err.Error()

	if !strings.Contains(msg, "log.level") {
		t.Errorf("error does not name the key the change broke:\n%s", msg)
	}

	if !strings.Contains(msg, "already") || !strings.Contains(msg, "server.host") {
		t.Errorf("error does not report the pre-existing violation as pre-existing:\n%s", msg)
	}
}

// Validation is opt-in. A Store with no schema must write without complaint —
// many containers legitimately carry none.
func TestApply_NoSchemaMeansNoValidation(t *testing.T) {
	t.Parallel()

	s := newTestStore(t, map[string]string{"/app.yaml": "log:\n  level: info\n"}, "/app.yaml")

	if _, err := s.Apply(context.Background(), Set("log.level", "anything at all")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

// The environment and flags are part of the resolved configuration, so a write
// has to be validated against them too. Validating files alone would accept a
// write that the next reload rejects.
func TestApply_ValidatesAgainstEnvironmentAndFlagLayers(t *testing.T) {
	t.Parallel()

	// The file's log.level is fine; the environment overrides it with a value
	// the schema forbids. The config is therefore already invalid, and an
	// unrelated write must still succeed.
	s, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{
			"/app.yaml": "server:\n  host: h\n  port: 8080\nlog:\n  level: info\n",
		}), "/app.yaml"),
		WithEnv("APP", envOf("APP_LOG_LEVEL=verbose")),
		WithSchema(portSchema(t)))
	if err != nil && !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := s.Apply(context.Background(), Set("server.port", 9090)); err != nil {
		t.Fatalf("Apply refused an unrelated write over a pre-existing env violation: %v", err)
	}

	// But a write that introduces a *new* violation is still caught, even
	// though the resolved value it produces is masked by the environment.
	_, err = s.Apply(context.Background(), Set("server.port", "not-a-number"))
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}
}

// Pre-write validation and reload validation must agree by construction. If
// they can disagree, a write validates, lands, and is then rejected by the
// reload it triggers — leaving the file changed and the process on
// last-known-good.
func TestApply_ValidationAgreesWithTheReloadItCauses(t *testing.T) {
	t.Parallel()

	s, _ := schemaStore(t, map[string]string{
		"/app.yaml": "server:\n  host: h\n  port: 8080\nlog:\n  level: info\n",
	}, "/app.yaml")

	if _, err := s.Apply(context.Background(), Set("log.level", "warn")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The same content, read back from scratch, must validate too.
	if err := s.Reload(context.Background()); err != nil {
		t.Fatalf("the reload caused by an accepted write was rejected: %v", err)
	}

	if got := s.View().GetString("log.level"); got != "warn" {
		t.Errorf("log.level = %q, want warn", got)
	}
}

package config

import (
	"context"
	"testing"

	"github.com/spf13/pflag"
)

// A schema describes what a value must *mean*, not how the layer that supplied
// it happened to encode it. Environment variables and command-line flags are
// strings by nature — the operating system offers nothing else — so a schema
// declaring server.port an int must accept "9090" from the environment exactly
// as it accepts 9090 from a file.
//
// The rule this enforces: validation and the accessors must agree. If GetInt
// returns the value happily, validation calling it the wrong type is validation
// being wrong.
type portOnly struct {
	Server struct {
		Port    int     `config:"server.port"`
		Timeout string  `config:"server.timeout"`
		Debug   bool    `config:"server.debug"`
		Ratio   float64 `config:"server.ratio"`
	}
}

func portOnlySchema(t *testing.T) *Schema {
	t.Helper()

	schema, err := NewSchema(WithStructSchema(portOnly{}))
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}

	return schema
}

func TestValidate_AcceptsTypedValuesSuppliedAsStrings(t *testing.T) {
	t.Parallel()

	s, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{
			"/app.yaml": "server:\n  port: 8080\n  debug: false\n  ratio: 0.5\n",
		}), "/app.yaml"),
		WithEnv("APP", envOf(
			"APP_SERVER_PORT=9090",
			"APP_SERVER_DEBUG=true",
			"APP_SERVER_RATIO=0.75",
		)),
		WithSchema(portOnlySchema(t)))
	if err != nil {
		t.Fatalf("a schema rejected values supplied by the environment: %v", err)
	}

	v := s.View()

	if got := v.GetInt("server.port"); got != 9090 {
		t.Errorf("server.port = %d, want 9090", got)
	}

	if !v.GetBool("server.debug") {
		t.Error("server.debug did not survive as true")
	}

	if got := v.GetFloat("server.ratio"); got != 0.75 {
		t.Errorf("server.ratio = %v, want 0.75", got)
	}
}

func TestValidate_AcceptsTypedValuesSuppliedByFlags(t *testing.T) {
	t.Parallel()

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.Int("server-port", 0, "port")

	if err := flags.Parse([]string{"--server-port", "7070"}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	s, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{"/app.yaml": "server:\n  port: 8080\n"}), "/app.yaml"),
		WithFlags(flags),
		WithSchema(portOnlySchema(t)))
	if err != nil {
		t.Fatalf("a schema rejected a value supplied by a flag: %v", err)
	}

	if got := s.View().GetInt("server.port"); got != 7070 {
		t.Errorf("server.port = %d, want 7070", got)
	}
}

// The leniency is for values that genuinely denote the declared type. A string
// that is not a number is still not a number, and saying so is the whole point
// of declaring the field an int.
func TestValidate_StillRejectsAStringThatIsNotTheDeclaredType(t *testing.T) {
	t.Parallel()

	_, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{"/app.yaml": "server:\n  port: 8080\n"}), "/app.yaml"),
		WithEnv("APP", envOf("APP_SERVER_PORT=not-a-number")),
		WithSchema(portOnlySchema(t)))

	if err == nil {
		t.Fatal("a value that is not an int was accepted for an int field")
	}
}

// A field declared a string keeps meaning a string: a numeric-looking value is
// legitimate for it, and coercion must not run the other way.
func TestValidate_AStringFieldAcceptsNumericLookingText(t *testing.T) {
	t.Parallel()

	s, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{"/app.yaml": "server:\n  timeout: 30s\n"}), "/app.yaml"),
		WithEnv("APP", envOf("APP_SERVER_TIMEOUT=60")),
		WithSchema(portOnlySchema(t)))
	if err != nil {
		t.Fatalf("a string field rejected numeric-looking text: %v", err)
	}

	if got := s.View().GetString("server.timeout"); got != "60" {
		t.Errorf("server.timeout = %q, want 60", got)
	}
}

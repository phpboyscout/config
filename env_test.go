package config

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func envOf(vars ...string) EnvOption {
	return WithEnviron(func() []string { return vars })
}

func TestEnv_OverridesFilesAndReportsProvenance(t *testing.T) {
	t.Parallel()

	s, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{
			"/app.yaml": "server:\n  host: file-host\n  port: 8080\n",
		}), "/app.yaml"),
		WithEnv("APP", envOf("APP_SERVER_HOST=env-host", "UNRELATED=ignored")))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	v := s.View()

	if got := v.GetString("server.host"); got != "env-host" {
		t.Errorf("server.host = %q, want the environment to win", got)
	}

	if got := v.GetInt("server.port"); got != 8080 {
		t.Errorf("server.port = %d, want the file value to survive", got)
	}

	src, ok := v.Origin("server.host")
	if !ok || src.Kind != SourceEnv {
		t.Fatalf("Origin(server.host) = %v/%v, want an environment source", src, ok)
	}

	if src.String() != "env:APP_SERVER_HOST" {
		t.Errorf("provenance = %q, want env:APP_SERVER_HOST", src.String())
	}
}

// The prefix is a security control: on a shared host, an unrelated process
// setting a generic variable must not be able to reconfigure this one.
func TestEnv_UnprefixedVariablesCannotReachConfiguration(t *testing.T) {
	t.Parallel()

	s, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{"/app.yaml": "log:\n  level: info\n"}), "/app.yaml"),
		WithEnv("APP", envOf("LOG_LEVEL=debug", "OTHER_LOG_LEVEL=trace")))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if got := s.View().GetString("log.level"); got != "info" {
		t.Errorf("log.level = %q, want the unprefixed variables to be ignored", got)
	}
}

// Reverse-mapping a variable name is ambiguous — APP_SERVER_PORT could be
// server.port or server_port — so it is resolved against the keys the layers
// beneath already define.
func TestEnv_DisambiguatesAgainstKnownKeys(t *testing.T) {
	t.Parallel()

	s, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{
			"/app.yaml": "server:\n  port: 2\n  host: h\n",
		}), "/app.yaml"),
		WithEnv("APP", envOf("APP_SERVER_PORT=9999")))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if got := s.View().GetInt("server.port"); got != 9999 {
		t.Errorf("server.port = %d, want the variable matched to the existing nested key", got)
	}
}

// When two existing keys are spelled the same way as a variable, the variable
// cannot express which is meant. Choosing would be non-deterministic — the
// candidates arrive in map iteration order — so it is reported instead.
func TestEnv_AmbiguousVariableIsRefused(t *testing.T) {
	t.Parallel()

	_, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{
			"/app.yaml": "server_port: 1\nserver:\n  port: 2\n",
		}), "/app.yaml"),
		WithEnv("APP", envOf("APP_SERVER_PORT=9999")))

	if !errors.Is(err, ErrAmbiguousEnvKey) {
		t.Fatalf("err = %v, want ErrAmbiguousEnvKey", err)
	}

	// The error has to name both candidates, or it is not actionable.
	for _, want := range []string{"APP_SERVER_PORT", "server.port", "server_port"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

func TestEnv_NestedKeysAndEnvOnlyValues(t *testing.T) {
	t.Parallel()

	s, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{
			"/app.yaml": "server:\n  tls:\n    enabled: false\n",
		}), "/app.yaml"),
		WithEnv("APP", envOf(
			"APP_SERVER_TLS_ENABLED=true",
			"APP_FEATURE_BETA=on", // defined nowhere else
		)))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	v := s.View()

	if !v.GetBool("server.tls.enabled") {
		t.Error("deeply nested key not overridden by the environment")
	}

	// A key that exists only in the environment is still visible, rather than
	// resolvable but absent from enumeration.
	if got := v.GetString("feature.beta"); got != "on" {
		t.Errorf("feature.beta = %q, want the env-only value", got)
	}

	found := false

	for _, k := range v.Keys() {
		if k == "feature.beta" {
			found = true
		}
	}

	if !found {
		t.Errorf("env-only key missing from Keys(): %v", v.Keys())
	}
}

// The environment cannot be written to, so routing must never choose it.
func TestEnv_IsNeverAWriteTarget(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "host: file-host\n"})

	s, err := NewStore(context.Background(),
		WithFiles(filesystem, "/app.yaml"),
		WithEnv("APP", envOf("APP_HOST=env-host")))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	plan, err := s.Plan(Set("host", "new-host"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	op := plan.Operations[0]
	if op.Target.Kind == SourceEnv {
		t.Fatal("routed a write at the environment")
	}

	// And the caller is told the write will not be visible.
	if op.Effective() {
		t.Error("write reported as effective while the environment still wins")
	}
}

// Binding a flag that the user did not set means its default silently masks
// configuration — the file says one thing, nothing was passed, and the flag
// default wins.
func TestFlags_OnlyChangedFlagsContribute(t *testing.T) {
	t.Parallel()

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.Int("port", 8080, "port")
	flags.String("host", "default-host", "host")

	if err := flags.Parse([]string{"--port", "7070"}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	s, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{
			"/app.yaml": "port: 9090\nhost: file-host\n",
		}), "/app.yaml"),
		WithFlags(flags))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	v := s.View()

	if got := v.GetInt("port"); got != 7070 {
		t.Errorf("port = %d, want the explicitly passed flag to win", got)
	}

	// host was never passed, so its default must not mask the file.
	if got := v.GetString("host"); got != "file-host" {
		t.Errorf("host = %q, want the file value — an unset flag must not clobber it", got)
	}

	src, _ := v.Origin("port")
	if src.String() != "flag:--port" {
		t.Errorf("provenance = %q, want flag:--port", src.String())
	}
}

func TestFlags_NameMapping(t *testing.T) {
	t.Parallel()

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("log-level", "info", "level")
	flags.String("listen", "", "address")

	if err := flags.Parse([]string{"--log-level", "debug", "--listen", ":9000"}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	s, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{"/app.yaml": "a: 1\n"}), "/app.yaml"),
		WithFlags(flags, BindFlag("listen", "server.address")))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	v := s.View()

	// A dashed flag name becomes a dotted key by default.
	if got := v.GetString("log.level"); got != "debug" {
		t.Errorf("log.level = %q, want debug", got)
	}

	// An explicit binding wins over the default mapping.
	if got := v.GetString("server.address"); got != ":9000" {
		t.Errorf("server.address = %q, want :9000", got)
	}
}

// Precedence runs file, then environment, then flags — least deliberate to
// most.
func TestPrecedence_FileThenEnvThenFlag(t *testing.T) {
	t.Parallel()

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("host", "", "host")

	if err := flags.Parse([]string{"--host", "flag-host"}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	s, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{
			"/app.yaml": "host: file-host\nfromfile: yes\nenvbeatsfile: file\n",
		}), "/app.yaml"),
		WithEnv("APP", envOf("APP_ENVBEATSFILE=env", "APP_HOST=env-host")),
		WithFlags(flags))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	v := s.View()

	cases := []struct {
		path, want, why string
	}{
		{"host", "flag-host", "a flag is the most deliberate input"},
		{"envbeatsfile", "env", "the environment outranks the file"},
		{"fromfile", "yes", "the file is used when nothing overrides it"},
	}

	for _, c := range cases {
		if got := v.GetString(c.path); got != c.want {
			t.Errorf("%s = %q, want %q — %s", c.path, got, c.want, c.why)
		}
	}

	explained := v.Explain("host")
	if !strings.Contains(explained, "flag:--host") || !strings.Contains(explained, "also defined in") {
		t.Errorf("Explain does not describe the full chain:\n%s", explained)
	}
}

func TestEnv_NoPrefixContributesNothing(t *testing.T) {
	t.Parallel()

	s, err := NewStore(context.Background(),
		WithFiles(memFS(t, map[string]string{"/app.yaml": "a: 1\n"}), "/app.yaml"),
		WithEnv("", envOf("A=2", "APP_A=3")))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if got := s.View().GetInt("a"); got != 1 {
		t.Errorf("a = %d, want the file value — an unprefixed env layer must contribute nothing", got)
	}
}

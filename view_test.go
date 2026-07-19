package config

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
)

func viewOn(t *testing.T, src string) *View {
	t.Helper()

	s := storeOn(t, memFS(t, map[string]string{"/app.yaml": src}), "/app.yaml")

	return s.View()
}

func TestView_TypedAccessors(t *testing.T) {
	t.Parallel()

	v := viewOn(t, `str: hello
num: 8080
flt: 1.5
flag: true
dur: 30s
list:
  - a
  - b
numstr: "9090"
`)

	cases := []struct {
		name string
		got  any
		want any
	}{
		{"string", v.GetString("str"), "hello"},
		{"int", v.GetInt("num"), 8080},
		{"float", v.GetFloat("flt"), 1.5},
		{"bool", v.GetBool("flag"), true},
		{"duration", v.GetDuration("dur"), 30 * time.Second},
		{"int from string", v.GetInt("numstr"), 9090},
		{"string from int", v.GetString("num"), "8080"},
		{"missing string", v.GetString("nope"), ""},
		{"missing int", v.GetInt("nope"), 0},
	}

	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %v (%T), want %v", c.name, c.got, c.got, c.want)
		}
	}

	if got := v.GetStringSlice("list"); len(got) != 2 || got[0] != "a" {
		t.Errorf("GetStringSlice = %v, want [a b]", got)
	}
}

// A scoped view must stay live against the snapshot rather than holding a
// detached copy — the trap that made the incumbent's Sub serve stale values.
func TestView_SubIsScopedNotDetached(t *testing.T) {
	t.Parallel()

	v := viewOn(t, "server:\n  host: localhost\n  tls:\n    enabled: true\n    port: 8443\n")

	server := v.Sub("server")
	if server == nil {
		t.Fatal("Sub(server) returned nil")
	}

	if got := server.GetString("host"); got != "localhost" {
		t.Errorf("scoped GetString(host) = %q, want localhost", got)
	}

	// Nesting accumulates.
	tls := server.Sub("tls")
	if tls == nil {
		t.Fatal("Sub(tls) returned nil")
	}

	if got := tls.GetInt("port"); got != 8443 {
		t.Errorf("nested GetInt(port) = %d, want 8443", got)
	}

	// The scoped view resolves through the full path, so provenance still
	// works and reports the real key.
	if _, ok := tls.Origin("port"); !ok {
		t.Error("Origin through a scoped view failed")
	}

	// Absent keys give nil, so `if sub != nil` guards behave.
	if v.Sub("nope") != nil {
		t.Error("Sub of a missing key should be nil")
	}
}

func TestView_Keys_ScopedToPrefix(t *testing.T) {
	t.Parallel()

	v := viewOn(t, "server:\n  host: h\n  port: 1\nother: 2\n")

	server := v.Sub("server")

	keys := server.Keys()
	if len(keys) != 2 {
		t.Fatalf("scoped Keys() = %v, want two", keys)
	}

	for _, k := range keys {
		if strings.Contains(k, "server.") {
			t.Errorf("scoped key %q still carries the prefix", k)
		}
	}
}

func TestView_Unmarshal(t *testing.T) {
	t.Parallel()

	v := viewOn(t, `server:
  host: localhost
  port: 8080
  timeout: 30s
  tags: a,b
`)

	type serverSettings struct {
		Host    string        `mapstructure:"host"`
		Port    int           `mapstructure:"port"`
		Timeout time.Duration `mapstructure:"timeout"`
		Tags    []string      `mapstructure:"tags"`
	}

	var got serverSettings
	if err := v.UnmarshalKey("server", &got); err != nil {
		t.Fatalf("UnmarshalKey: %v", err)
	}

	if got.Host != "localhost" || got.Port != 8080 {
		t.Errorf("decoded = %+v", got)
	}

	if got.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s (string durations must decode)", got.Timeout)
	}

	if len(got.Tags) != 2 {
		t.Errorf("Tags = %v, want two entries from a comma-separated string", got.Tags)
	}

	// Decoding through a scoped view addresses the subtree relative to it.
	var scoped serverSettings
	if err := v.Sub("server").Unmarshal(&scoped); err != nil {
		t.Fatalf("scoped Unmarshal: %v", err)
	}

	if scoped.Port != 8080 {
		t.Errorf("scoped decode = %+v", scoped)
	}
}

// A decode is one operation against one snapshot, so the struct it produces
// cannot mix values from either side of a reload.
func TestView_UnmarshalIsInternallyConsistent(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "server:\n  host: old\n  port: 1\n"})

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/app.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	v := s.View()

	// A reload lands after the view was taken.
	if err := afero.WriteFile(filesystem, "/app.yaml", []byte("server:\n  host: new\n  port: 2\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	if err := s.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	type settings struct {
		Host string `mapstructure:"host"`
		Port int    `mapstructure:"port"`
	}

	var got settings
	if err := v.UnmarshalKey("server", &got); err != nil {
		t.Fatalf("UnmarshalKey: %v", err)
	}

	if got.Host != "old" || got.Port != 1 {
		t.Errorf("decoded = %+v, want both fields from the snapshot the view was taken from", got)
	}
}

// A sequence of reads inside With cannot straddle a reload.
func TestStore_With_PinsASnapshot(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{"/app.yaml": "host: old\nport: 1\n"})

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/app.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	var host string

	var port int

	err = s.With(func(v *View) error {
		host = v.GetString("host")

		// A reload lands mid-block.
		if err := afero.WriteFile(filesystem, "/app.yaml", []byte("host: new\nport: 2\n"), 0o644); err != nil {
			return err
		}

		if err := s.Reload(context.Background()); err != nil {
			return err
		}

		port = v.GetInt("port")

		return nil
	})
	if err != nil {
		t.Fatalf("With: %v", err)
	}

	if host != "old" || port != 1 {
		t.Errorf("host=%q port=%d — reads straddled a reload; want both from one snapshot", host, port)
	}

	// Outside the block, the new snapshot is visible.
	if got := s.View().GetString("host"); got != "new" {
		t.Errorf("after With, host = %q, want new", got)
	}
}

func TestView_HasAndSectionExists(t *testing.T) {
	t.Parallel()

	v := viewOn(t, "scalar: 1\nsection:\n  a: 1\nemptymap: {}\n")

	cases := []struct {
		path    string
		has     bool
		section bool
	}{
		{"scalar", true, false},
		{"section", true, true},
		{"emptymap", true, true},
		{"missing", false, false},
	}

	for _, c := range cases {
		if got := v.Has(c.path); got != c.has {
			t.Errorf("Has(%q) = %v, want %v", c.path, got, c.has)
		}

		if got := v.IsSet(c.path); got != c.has {
			t.Errorf("IsSet(%q) = %v, want %v", c.path, got, c.has)
		}

		if got := v.SectionExists(c.path); got != c.section {
			t.Errorf("SectionExists(%q) = %v, want %v", c.path, got, c.section)
		}
	}
}

// The question every configuration debugging session starts with.
func TestView_Explain(t *testing.T) {
	t.Parallel()

	filesystem := memFS(t, map[string]string{
		"/base.yaml": "host: base-host\nonly: here\n",
		"/over.yaml": "host: over-host\n",
	})

	s, err := NewStore(context.Background(),
		WithFiles(filesystem, "/base.yaml", "/over.yaml"),
		WithBackend(staticBackend{
			id: "env",
			layers: []Layer{{
				Source: Source{Kind: SourceEnv, Name: "APP_HOST"},
				Values: map[string]any{"host": "env-host"},
			}},
		}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	v := s.View()

	explained := v.Explain("host")
	for _, want := range []string{"env-host", "env:APP_HOST", "also defined in", "/base.yaml", "/over.yaml"} {
		if !strings.Contains(explained, want) {
			t.Errorf("Explain(host) missing %q:\n%s", want, explained)
		}
	}

	if got := v.Explain("only"); !strings.Contains(got, "/base.yaml") || strings.Contains(got, "also defined") {
		t.Errorf("Explain(only) = %q, want a single source and no shadowing", got)
	}

	if got := v.Explain("missing"); !strings.Contains(got, "not set") {
		t.Errorf("Explain(missing) = %q", got)
	}
}

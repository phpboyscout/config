package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"testing"
	"time"
)

// severity is a consumer's own type: an enum expressed the idiomatic Go way,
// which this module has never heard of and must still be able to read.
type severity int

const (
	severityUnset severity = iota
	severityDebug
	severityInfo
)

func (s *severity) UnmarshalText(text []byte) error {
	switch string(text) {
	case "debug":
		*s = severityDebug
	case "info":
		*s = severityInfo
	default:
		return errUnknownSeverity
	}

	return nil
}

var errUnknownSeverity = errorString("unknown severity")

type errorString string

func (e errorString) Error() string { return string(e) }

func typedStore(t *testing.T) *Store {
	t.Helper()

	return storeOn(t, memFS(t, map[string]string{
		"/app.yaml": `count: 42
ratio: 1.5
enabled: true
timeout: 5s
size: 10mb
bare: 2048
ports:
  - 8080
  - 8081
labels:
  env: prod
  tier: web
routes:
  api:
    - /v1
    - /v2
bind: 10.0.0.1
prefix: 10.0.0.0/8
endpoint: https://example.com/api
zone: Europe/London
level: debug
`,
	}), "/app.yaml")
}

// readsAs asserts that Value decodes a path into T and matches want, rendered
// through its own String method where it has one.
func readsAs[T any](t *testing.T, cfg Reader, path string, want string) {
	t.Helper()

	got, err := Value[T](cfg, path)
	if err != nil {
		t.Errorf("%s: %v", path, err)

		return
	}

	if rendered := fmt.Sprint(got); rendered != want {
		t.Errorf("%s = %s, want %s", path, rendered, want)
	}
}

// Value reads any type, which is what keeps the interface from needing a method
// per type — including types this module has never heard of.
func TestValue_ReadsAnyType(t *testing.T) {
	t.Parallel()

	cfg := typedStore(t).View()

	readsAs[int64](t, cfg, "count", "42")
	readsAs[[]int](t, cfg, "ports", "[8080 8081]")
	readsAs[map[string]string](t, cfg, "labels", "map[env:prod tier:web]")

	// Types the decoder was taught, none of which has an accessor.
	readsAs[netip.Addr](t, cfg, "bind", "10.0.0.1")
	readsAs[netip.Prefix](t, cfg, "prefix", "10.0.0.0/8")
	readsAs[*url.URL](t, cfg, "endpoint", "https://example.com/api")
	readsAs[*time.Location](t, cfg, "zone", "Europe/London")

	// And a type belonging to the consumer, not to this module.
	if got, err := Value[severity](cfg, "level"); err != nil || got != severityDebug {
		t.Errorf("severity = %v, %v", got, err)
	}

	// An absent key is the zero value and no error, as the accessors are.
	if got, err := Value[int](cfg, "nothing.here"); err != nil || got != 0 {
		t.Errorf("absent = %v, %v", got, err)
	}
}

func TestView_TypedAccessors_Widths(t *testing.T) {
	t.Parallel()

	cfg := typedStore(t).View()

	if got := cfg.GetInt64("count"); got != 42 {
		t.Errorf("GetInt64 = %d", got)
	}

	if got := cfg.GetInt32("count"); got != 42 {
		t.Errorf("GetInt32 = %d", got)
	}

	if got := cfg.GetUint("count"); got != 42 {
		t.Errorf("GetUint = %d", got)
	}

	if got := cfg.GetUint64("count"); got != 42 {
		t.Errorf("GetUint64 = %d", got)
	}

	// GetFloat64 is the incumbent's name for the same reading, so porting code
	// does not have to be edited to say the same thing differently.
	if cfg.GetFloat64("ratio") != cfg.GetFloat("ratio") {
		t.Error("GetFloat64 and GetFloat disagree")
	}
}

func TestView_TypedAccessors_Collections(t *testing.T) {
	t.Parallel()

	cfg := typedStore(t).View()

	if got := cfg.GetIntSlice("ports"); len(got) != 2 || got[1] != 8081 {
		t.Errorf("GetIntSlice = %v", got)
	}

	if got := cfg.GetStringMap("labels"); got["tier"] != "web" {
		t.Errorf("GetStringMap = %v", got)
	}

	if got := cfg.GetStringMapString("labels"); got["env"] != "prod" {
		t.Errorf("GetStringMapString = %v", got)
	}

	if got := cfg.GetStringMapStringSlice("routes"); len(got["api"]) != 2 {
		t.Errorf("GetStringMapStringSlice = %v", got)
	}
}

func TestView_GetSizeInBytes(t *testing.T) {
	t.Parallel()

	cfg := typedStore(t).View()

	if got := cfg.GetSizeInBytes("size"); got != 10*1024*1024 {
		t.Errorf("10mb = %d, want %d", got, 10*1024*1024)
	}

	// A bare number is a count of bytes.
	if got := cfg.GetSizeInBytes("bare"); got != 2048 {
		t.Errorf("bare = %d, want 2048", got)
	}

	// Something that is not a size reads as zero, which is what the other
	// accessors do rather than panicking.
	if got := cfg.GetSizeInBytes("enabled"); got != 0 {
		t.Errorf("non-size = %d, want 0", got)
	}
}

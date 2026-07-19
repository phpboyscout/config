package config_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/phpboyscout/go/config"
)

type typedProviderConfig struct {
	Key     string        `mapstructure:"key"`
	Env     string        `mapstructure:"env"`
	Enabled bool          `mapstructure:"enabled"`
	Timeout time.Duration `mapstructure:"timeout"`
}

type typedFullConfig struct {
	OpenAI typedProviderConfig `mapstructure:"openai"`
}

type complexSectionConfig struct {
	APIKey   string               `json:"api_key"`
	YAMLName string               `yaml:"yaml_name"`
	Count    uint                 `mapstructure:"count"`
	Ratio    float64              `mapstructure:"ratio"`
	Skipped  string               `mapstructure:"-"`
	Inline   complexInlineConfig  `mapstructure:",squash"`
	Pointer  *complexPointerValue `mapstructure:"pointer"`
}

type complexInlineConfig struct {
	InlineName string `mapstructure:"inline_name"`
}

// viewFrom builds a view over in-memory YAML, which is how compiled-in
// defaults and test fixtures reach configuration.
func viewFrom(t *testing.T, yaml string) *config.View {
	t.Helper()

	return storeFrom(t, yaml).View()
}

// viewWithEnv builds a view whose configuration includes an environment
// layer. The environment is a source like any other here, so a test that
// depends on it configures it rather than mutating process state — which is
// global, and would make parallel tests interfere.
func viewWithEnv(t *testing.T, yaml string, vars ...string) *config.View {
	t.Helper()

	s, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "test.yaml", Content: []byte(yaml)}),
		config.WithEnv("GTB", config.WithEnviron(func() []string { return vars })))
	require.NoError(t, err)

	return s.View()
}

// storeFrom builds a Store over in-memory YAML. Observation belongs to the
// Store rather than to a view, because a view is one snapshot and observing is
// about the transition between them.
func storeFrom(t *testing.T, yaml string) *config.Store {
	t.Helper()

	s, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "test.yaml", Content: []byte(yaml)}))
	require.NoError(t, err)

	return s
}

type complexPointerValue struct {
	Value string `mapstructure:"value"`
}

func TestContainer_Unmarshal(t *testing.T) {
	t.Parallel()

	c := viewFrom(t, `
openai:
  key: file-key
  enabled: true
`)

	var out typedFullConfig
	require.NoError(t, c.Unmarshal(&out))

	assert.Equal(t, "file-key", out.OpenAI.Key)
	assert.True(t, out.OpenAI.Enabled)
}

func TestContainer_UnmarshalKey(t *testing.T) {
	t.Parallel()

	c := viewFrom(t, `
openai:
  key: file-key
  env: OPENAI_API_KEY
  enabled: true
  timeout: 5s
`)

	var out typedProviderConfig
	require.NoError(t, c.UnmarshalKey("openai", &out))

	assert.Equal(t, "file-key", out.Key)
	assert.Equal(t, "OPENAI_API_KEY", out.Env)
	assert.True(t, out.Enabled)
	assert.Equal(t, 5*time.Second, out.Timeout)
}

func TestContainer_UnmarshalKey_RejectsInvalidTargets(t *testing.T) {
	t.Parallel()

	c := viewFrom(t, `
openai:
  key: file-key
`)

	require.ErrorContains(t, c.UnmarshalKey("openai", nil), "nil target")

	var out *typedProviderConfig
	require.ErrorContains(t, c.UnmarshalKey("openai", out), "result must be addressable")
}

func TestContainer_UnmarshalKey_PreservesEnvBinding(t *testing.T) {
	t.Parallel()

	c := viewWithEnv(t,
		"openai:\n  key: file-key\n  enabled: false\n",
		"GTB_OPENAI_KEY=env-key")

	var out typedProviderConfig
	require.NoError(t, c.UnmarshalKey("openai", &out))

	assert.Equal(t, "env-key", out.Key)
	assert.False(t, out.Enabled)
}

func TestContainer_UnmarshalKey_SubContainerPreservesEnvBinding(t *testing.T) {
	t.Parallel()

	c := viewWithEnv(t,
		"providers:\n  openai:\n    key: file-key\n",
		"GTB_PROVIDERS_OPENAI_KEY=nested-env-key")

	providers := c.Sub("providers")
	require.NotNil(t, providers)

	var out typedProviderConfig
	require.NoError(t, providers.UnmarshalKey("openai", &out))

	assert.Equal(t, "nested-env-key", out.Key)
}

func TestContainer_UnmarshalKey_OverlaysResolvedComplexFields(t *testing.T) {
	t.Parallel()

	c := viewFrom(t, `
section:
  api_key: file-api-key
  yaml_name: file-yaml-name
  count: 7
  ratio: 1.5
  skipped: should-not-map
  inline_name: inline-value
  pointer:
    value: pointer-value
`)

	var out complexSectionConfig
	require.NoError(t, c.UnmarshalKey("section", &out))

	assert.Equal(t, "file-api-key", out.APIKey)
	assert.Equal(t, "file-yaml-name", out.YAMLName)
	assert.Equal(t, uint(7), out.Count)
	assert.InDelta(t, 1.5, out.Ratio, 0.001)
	assert.Empty(t, out.Skipped)
	assert.Equal(t, "inline-value", out.Inline.InlineName)
	require.NotNil(t, out.Pointer)
	assert.Equal(t, "pointer-value", out.Pointer.Value)
}

func TestContainer_SectionExists(t *testing.T) {
	t.Parallel()

	c := viewFrom(t, `
openai:
  key: file-key
`)

	assert.True(t, c.SectionExists(""))
	assert.True(t, c.SectionExists("openai"))
	assert.False(t, c.SectionExists("missing"))
}

func TestUnmarshalSection(t *testing.T) {
	t.Parallel()

	c := viewFrom(t, `
openai:
  key: file-key
`)

	section, err := config.UnmarshalSection[typedProviderConfig](c, "openai")
	require.NoError(t, err)

	assert.True(t, section.Exists)
	assert.Equal(t, "file-key", section.Value.Key)

	missing, err := config.UnmarshalSection[typedProviderConfig](c, "anthropic")
	require.NoError(t, err)

	assert.False(t, missing.Exists)
	assert.Zero(t, missing.Value)
}

func TestUnmarshalSection_NilConfig(t *testing.T) {
	t.Parallel()

	section, err := config.UnmarshalSection[typedProviderConfig](nil, "openai")
	require.NoError(t, err)

	assert.False(t, section.Exists)
	assert.Zero(t, section.Value)
}

// A section that exists only in the environment is still a section. The
// incumbent could resolve such a value but not see it, so a typed section
// bound to it decoded as absent.
func TestUnmarshalSection_EnvOnlyNestedValueExists(t *testing.T) {
	t.Parallel()

	s, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "test.yaml", Content: []byte("other: value\n")}),
		config.WithEnv("GTB", config.WithEnviron(func() []string {
			return []string{"GTB_OPENAI_KEY=env-only-key"}
		})))
	require.NoError(t, err)

	c := s.View()

	section, err := config.UnmarshalSection[typedProviderConfig](c, "openai")
	require.NoError(t, err)

	assert.True(t, section.Exists)
	assert.Equal(t, "env-only-key", section.Value.Key)
}

func TestMustUnmarshalSection(t *testing.T) {
	t.Parallel()

	c := viewFrom(t, `
openai:
  key: file-key
`)

	section := config.MustUnmarshalSection[typedProviderConfig](c, "openai")

	assert.True(t, section.Exists)
	assert.Equal(t, "file-key", section.Value.Key)
}

func TestMustUnmarshalSection_PanicsOnDecodeError(t *testing.T) {
	t.Parallel()

	c := viewFrom(t, `
openai:
  timeout: definitely-not-a-duration
`)

	require.Panics(t, func() {
		_ = config.MustUnmarshalSection[typedProviderConfig](c, "openai")
	})
}

func TestObserveSection_InitialUnmarshalAndRegistersObserver(t *testing.T) {
	t.Parallel()

	c := storeFrom(t, `
openai:
  key: file-key
`)

	defaults := typedProviderConfig{Timeout: 5 * time.Second}
	binding, err := config.ObserveSection[typedProviderConfig](
		c,
		"openai",
		config.WithSectionDefaults(defaults, mergeTypedProviderConfig),
	)
	require.NoError(t, err)

	assert.True(t, binding.Exists())
	assert.Equal(t, "file-key", binding.Value().Key)
	assert.Equal(t, 5*time.Second, binding.Value().Timeout)
	require.NotNil(t, binding.Current())
	assert.Equal(t, binding.Value(), *binding.Current())
	assert.Equal(t, uint64(1), binding.Version())
	assert.Len(t, c.Observers(), 1)
}

func TestObservedSection_ZeroValue(t *testing.T) {
	t.Parallel()

	var binding config.ObservedSection[typedProviderConfig]

	assert.False(t, binding.Exists())
	assert.Zero(t, binding.Value())
	assert.Nil(t, binding.Current())
	assert.Equal(t, uint64(0), binding.Version())
}

func TestObserveSection_DefaultsRequireMergeForExistingSection(t *testing.T) {
	t.Parallel()

	c := storeFrom(t, `
openai:
  key: file-key
`)

	_, err := config.ObserveSection[typedProviderConfig](
		c,
		"openai",
		config.WithSectionDefaults(typedProviderConfig{Timeout: time.Second}, nil),
	)

	require.ErrorContains(t, err, "section defaults require a merge function")
}

func TestObserveSection_DynamicDefaultsRehydrateOnReload(t *testing.T) {
	t.Parallel()

	c, src := mutableStoreFrom(t, `
defaults:
  timeout: 5s
openai:
  key: file-key
`)

	binding, err := config.ObserveSection[typedProviderConfig](
		c,
		"openai",
		config.WithSectionDefaultFunc(func(cfg config.Observed) typedProviderConfig {
			return typedProviderConfig{Timeout: cfg.GetDuration("defaults.timeout")}
		}, mergeTypedProviderConfig),
	)
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, binding.Value().Timeout)

	src.set("defaults:\n  timeout: 9s\nopenai:\n  key: file-key\n")
	require.NoError(t, c.Reload(context.Background()))

	assert.Equal(t, 9*time.Second, binding.Value().Timeout)
	assert.Equal(t, uint64(2), binding.Version())
}

func TestObserveSection_RehydratesOnObserver(t *testing.T) {
	t.Parallel()

	c, src := mutableStoreFrom(t, `
openai:
  key: initial-key
`)

	applied := make([]config.SectionChange[typedProviderConfig], 0, 1)
	binding, err := config.ObserveSection[typedProviderConfig](
		c,
		"openai",
		config.WithSectionApply(func(change config.SectionChange[typedProviderConfig]) error {
			applied = append(applied, change)

			return nil
		}),
	)
	require.NoError(t, err)

	initial := binding.Current()
	require.NotNil(t, initial)

	src.set("openai:\n  key: reload-key\n")
	require.NoError(t, c.Reload(context.Background()))

	assert.Equal(t, "reload-key", binding.Value().Key)
	assert.NotSame(t, initial, binding.Current())
	assert.Equal(t, uint64(2), binding.Version())
	require.Len(t, applied, 1)
	assert.True(t, applied[0].Changed)
	assert.False(t, applied[0].Initial)
	assert.Equal(t, uint64(2), applied[0].Version)
	assert.Equal(t, "initial-key", applied[0].Previous.Value.Key)
	assert.True(t, applied[0].Current.Exists)
	assert.Equal(t, "reload-key", applied[0].Current.Value.Key)
}

func TestObserveSection_UnchangedReloadDoesNotApply(t *testing.T) {
	t.Parallel()

	c, src := mutableStoreFrom(t, `
openai:
  key: initial-key
`)

	applyCalls := 0
	binding, err := config.ObserveSection[typedProviderConfig](
		c,
		"openai",
		config.WithSectionApply(func(config.SectionChange[typedProviderConfig]) error {
			applyCalls++

			return nil
		}),
	)
	require.NoError(t, err)

	initial := binding.Current()
	require.NotNil(t, initial)

	src.set("openai:\n  key: initial-key\nunrelated:\n  value: changed\n")
	require.NoError(t, c.Reload(context.Background()))

	assert.Equal(t, "initial-key", binding.Value().Key)
	assert.Same(t, initial, binding.Current())
	assert.Equal(t, uint64(1), binding.Version())
	assert.Zero(t, applyCalls)
}

func TestObserveSection_CustomEqualityControlsChangeDetection(t *testing.T) {
	t.Parallel()

	c, src := mutableStoreFrom(t, `
openai:
  key: initial-key
  env: INITIAL_ENV
`)

	applyCalls := 0
	binding, err := config.ObserveSection[typedProviderConfig](
		c,
		"openai",
		config.WithSectionEqual(func(previous, current config.Section[typedProviderConfig]) bool {
			return previous.Exists == current.Exists && previous.Value.Key == current.Value.Key
		}),
		config.WithSectionApply(func(config.SectionChange[typedProviderConfig]) error {
			applyCalls++

			return nil
		}),
	)
	require.NoError(t, err)

	src.set("openai:\n  key: initial-key\n  env: ROTATED_ENV_NAME\n")
	require.NoError(t, c.Reload(context.Background()))

	assert.Equal(t, "INITIAL_ENV", binding.Value().Env)
	assert.Equal(t, uint64(1), binding.Version())
	assert.Zero(t, applyCalls)

	src.set("openai:\n  key: rotated-key\n  env: ROTATED_ENV_NAME\n")
	require.NoError(t, c.Reload(context.Background()))

	assert.Equal(t, "rotated-key", binding.Value().Key)
	assert.Equal(t, "ROTATED_ENV_NAME", binding.Value().Env)
	assert.Equal(t, uint64(2), binding.Version())
	assert.Equal(t, 1, applyCalls)
}

func TestObserveSection_InvalidReloadPreservesPriorSnapshot(t *testing.T) {
	t.Parallel()

	c, src := mutableStoreFrom(t, `
openai:
  key: initial-key
`)

	applyCalls := 0
	binding, err := config.ObserveSection[typedProviderConfig](
		c,
		"openai",
		config.WithSectionValidator(func(next typedProviderConfig) error {
			if next.Key == "invalid-key" {
				return assert.AnError
			}

			return nil
		}),
		config.WithSectionApply(func(config.SectionChange[typedProviderConfig]) error {
			applyCalls++

			return nil
		}),
	)
	require.NoError(t, err)

	previous := binding.Current()
	require.NotNil(t, previous)

	var observerErr error

	c.OnObserverError(func(e error) { observerErr = e })

	src.set("openai:\n  key: invalid-key\n")

	// The reload itself succeeds — the configuration did change. What must not
	// happen is the section adopting a value its validator rejected.
	require.NoError(t, c.Reload(context.Background()))
	require.Error(t, observerErr, "a failing observer must be reported, not swallowed")

	assert.Equal(t, "initial-key", binding.Value().Key)
	assert.Same(t, previous, binding.Current())
	assert.Equal(t, uint64(1), binding.Version())
	assert.Zero(t, applyCalls)
}

func TestObserveSection_DecodeErrorPreservesPriorSnapshot(t *testing.T) {
	t.Parallel()

	c, src := mutableStoreFrom(t, `
openai:
  timeout: 5s
`)

	applyCalls := 0
	binding, err := config.ObserveSection[typedProviderConfig](
		c,
		"openai",
		config.WithSectionApply(func(config.SectionChange[typedProviderConfig]) error {
			applyCalls++

			return nil
		}),
	)
	require.NoError(t, err)

	previous := binding.Current()
	require.NotNil(t, previous)

	var observerErr error

	c.OnObserverError(func(e error) { observerErr = e })

	src.set("openai:\n  key: file-key\n  timeout: not-a-duration\n")

	require.NoError(t, c.Reload(context.Background()))
	require.Error(t, observerErr, "a decode failure must be reported, not swallowed")

	assert.Equal(t, 5*time.Second, binding.Value().Timeout)
	assert.Same(t, previous, binding.Current())
	assert.Equal(t, uint64(1), binding.Version())
	assert.Zero(t, applyCalls)
}

// mutableSource is an in-memory backend whose content the test can change,
// so a reload can be exercised for real rather than simulated by calling
// observers directly. Simulating it would prove the observers run, not that a
// reload runs them.
type mutableSource struct {
	mu      sync.Mutex
	content []byte
}

func (m *mutableSource) ID() string { return "test.yaml" }

func (m *mutableSource) Capabilities() config.Capabilities { return config.Capabilities{} }

func (m *mutableSource) set(yaml string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.content = []byte(yaml)
}

func (m *mutableSource) Load(ctx context.Context) ([]config.Layer, error) {
	m.mu.Lock()
	content := m.content
	m.mu.Unlock()

	return config.NewReaderBackend("test.yaml", content).Load(ctx)
}

// mutableStoreFrom builds a Store over content the test can change.
func mutableStoreFrom(t *testing.T, yaml string) (*config.Store, *mutableSource) {
	t.Helper()

	src := &mutableSource{content: []byte(yaml)}

	s, err := config.NewStore(context.Background(), config.WithBackend(src))
	require.NoError(t, err)

	return s, src
}

func mergeTypedProviderConfig(defaults, overlay typedProviderConfig) typedProviderConfig {
	if overlay.Key != "" {
		defaults.Key = overlay.Key
	}
	if overlay.Env != "" {
		defaults.Env = overlay.Env
	}
	if overlay.Timeout != 0 {
		defaults.Timeout = overlay.Timeout
	}

	defaults.Enabled = overlay.Enabled

	return defaults
}

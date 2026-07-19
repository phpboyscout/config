package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

// ErrAmbiguousEnvKey is returned when a set environment variable could
// designate more than one configuration key, so honouring it would mean
// guessing which the user meant.
var ErrAmbiguousEnvKey = errors.New("config: environment variable is ambiguous")

// envBackend contributes configuration from environment variables.
type envBackend struct {
	prefix string
	// environ is the source of variables, injectable so tests need not mutate
	// process state and race each other.
	environ func() []string
	// known is the key space of the layers below, used to disambiguate variable
	// names. See mapKey.
	known []string
}

// EnvOption configures the environment backend.
type EnvOption func(*envBackend)

// WithEnviron overrides where environment variables are read from. Tests use
// it to avoid mutating process state, which is global and makes parallel tests
// interfere with each other.
func WithEnviron(fn func() []string) EnvOption {
	return func(b *envBackend) { b.environ = fn }
}

// NewEnvBackend returns a backend contributing environment variables that
// carry the given prefix.
//
// The prefix is required, and is a security control rather than tidiness.
// Without one, any environment variable matching a configuration key can
// silently change behaviour — on a shared CI runner, a container host or a
// multi-tenant box, an unrelated process setting LOG_LEVEL or AI_PROVIDER
// would reconfigure every tool running there. With a prefix, unprefixed
// variables cannot reach configuration at all.
//
// Pass the prefix without a trailing underscore.
func NewEnvBackend(prefix string, opts ...EnvOption) Backend {
	b := &envBackend{
		prefix:  strings.TrimSuffix(strings.ToUpper(prefix), "_"),
		environ: os.Environ,
	}

	for _, opt := range opts {
		opt(b)
	}

	return b
}

func (b *envBackend) ID() string { return "env:" + b.prefix }

// observeKnownKeys receives the key space of the layers beneath, which is what
// makes the reverse mapping from a variable name unambiguous.
func (b *envBackend) observeKnownKeys(keys []string) {
	b.known = keys
}

// Capabilities reports the environment as readable but never writable.
// Persisting to it would not survive the process, so routing must skip it.
func (b *envBackend) Capabilities() Capabilities {
	return Capabilities{Writable: false, NativeWatch: false}
}

func (b *envBackend) Load(ctx context.Context) ([]Layer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if b.prefix == "" {
		// Without a prefix there is nothing safe to scan for, so the layer is
		// empty rather than swallowing the whole environment.
		return nil, nil
	}

	prefix := b.prefix + "_"

	var layers []Layer

	for _, entry := range b.environ() {
		name, value, found := strings.Cut(entry, "=")
		if !found || !strings.HasPrefix(name, prefix) {
			continue
		}

		key, err := mapKey(strings.TrimPrefix(name, prefix), b.known)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", err, name)
		}

		if key == "" {
			continue
		}

		layers = append(layers, Layer{
			Source: Source{Kind: SourceEnv, Name: name, Writable: false},
			Values: nest(key, value),
		})
	}

	return layers, nil
}

// mapKey converts the variable part of an environment name into a dotted key.
//
// The conversion is genuinely ambiguous: APP_SERVER_PORT could mean
// server.port or server_port, and nothing in the name says which. Resolving it
// against the keys the lower layers already define removes the guesswork where
// it matters, because a variable is nearly always overriding something that
// already exists. A key defined nowhere else falls back to treating every
// underscore as a separator.
//
// When two existing keys would both be spelled the same way, the variable
// cannot express which is meant, and this reports that rather than choosing.
// Choosing would be worse than it sounds: the candidates arrive in map
// iteration order, so the winner would vary between runs of the same program.
func mapKey(suffix string, known []string) (string, error) {
	if suffix == "" {
		return "", nil
	}

	var matches []string

	for _, candidate := range known {
		if envName(candidate) == suffix {
			matches = append(matches, candidate)
		}
	}

	switch len(matches) {
	case 0:
		return strings.ReplaceAll(strings.ToLower(suffix), "_", "."), nil
	case 1:
		return matches[0], nil
	default:
		slices.Sort(matches)

		return "", fmt.Errorf("%w: it could mean %s", ErrAmbiguousEnvKey, strings.Join(matches, " or "))
	}
}

// envName renders a dotted key as the environment variable name that would
// supply it, without the prefix.
func envName(key string) string {
	return strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

// nest expands a dotted key into the nested map a layer is built from.
func nest(key string, value any) map[string]any {
	segs := strings.Split(key, ".")

	current := value

	for i := len(segs) - 1; i > 0; i-- {
		current = map[string]any{segs[i]: current}
	}

	return map[string]any{segs[0]: current}
}

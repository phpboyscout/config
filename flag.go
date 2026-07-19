package config

import (
	"context"
	"strings"

	"github.com/spf13/pflag"
)

// flagBackend contributes configuration from command-line flags.
type flagBackend struct {
	flags *pflag.FlagSet
	// names maps flag names to configuration keys. A flag not listed here uses
	// its own name with dashes converted to dots.
	names map[string]string
}

// FlagOption configures the flag backend.
type FlagOption func(*flagBackend)

// BindFlag maps a flag name to a configuration key, for the cases where the
// two differ.
func BindFlag(flagName, key string) FlagOption {
	return func(b *flagBackend) {
		if b.names == nil {
			b.names = map[string]string{}
		}

		b.names[flagName] = key
	}
}

// NewFlagBackend returns a backend contributing flags the user actually
// changed.
//
// Only changed flags contribute, and that is the whole point. Binding a flag
// regardless of whether it was set means its default silently masks
// configuration: the file says port 9090, nobody passed --port, and the
// effective value is the flag default of 8080. The user then has no way to
// discover why their configuration is being ignored.
func NewFlagBackend(flags *pflag.FlagSet, opts ...FlagOption) Backend {
	b := &flagBackend{flags: flags}

	for _, opt := range opts {
		opt(b)
	}

	return b
}

func (b *flagBackend) ID() string { return "flags" }

// Capabilities reports flags as readable but never writable: a flag lives for
// one invocation, so persisting to it is meaningless.
func (b *flagBackend) Capabilities() Capabilities {
	return Capabilities{}
}

func (b *flagBackend) Load(ctx context.Context, _ []Layer) ([]Layer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if b.flags == nil {
		return nil, nil
	}

	var layers []Layer

	b.flags.Visit(func(f *pflag.Flag) {
		// Visit only walks flags the user actually set, which is exactly the
		// filter this layer needs.
		key := b.keyFor(f.Name)
		if key == "" {
			return
		}

		layers = append(layers, Layer{
			Source: Source{Kind: SourceFlag, Name: "--" + f.Name, Writable: false},
			Values: nest(key, flagValue(f)),
		})
	})

	return layers, nil
}

func (b *flagBackend) keyFor(name string) string {
	if mapped, ok := b.names[name]; ok {
		return mapped
	}

	return strings.ReplaceAll(name, "-", ".")
}

// flagValue extracts a flag's value, keeping a repeatable flag a list.
//
// String() renders a slice flag as "[a,b]" for display, and storing that gives
// a single garbage element rather than the two values the user passed. pflag
// exposes the elements through SliceValue, so ask for them.
func flagValue(f *pflag.Flag) any {
	if sv, ok := f.Value.(pflag.SliceValue); ok {
		items := sv.GetSlice()

		out := make([]any, len(items))
		for i, item := range items {
			out[i] = item
		}

		return out
	}

	return f.Value.String()
}

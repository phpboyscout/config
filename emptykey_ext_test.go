package config_test

import (
	"context"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/go/errors"

	"gitlab.com/phpboyscout/go/config"
)

// An empty key cannot be addressed by a dotted path, and the module's own walks
// disagreed about where it sat, so a layer holding one is refused (go/config#32).

func TestEmptyKey_ALayerHoldingOneIsRefused(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ doc, at string }{
		"top level":           {"\"\": 1\nport: 2\n", "at the top level"},
		"under a parent":      {"c:\n  \"\": 3\n", `under "c"`},
		"beside a sibling":    {"a:\n  \"\": 1\n  b: 2\n", `under "a"`},
		"deep":                {"a:\n  b:\n    \"\": x\n", `under "a.b"`},
		"inside a list":       {"items:\n  - \"\": a\n", `under "items[0]"`},
		"as an empty mapping": {"c:\n  \"\": {}\n", `under "c"`},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, err := config.NewStore(context.Background(),
				config.WithReaders(config.NamedSource{Name: "app.yaml", Content: []byte(tc.doc)}))
			if !errors.Is(err, config.ErrEmptyKey) {
				t.Fatalf("NewStore err = %v, want ErrEmptyKey", err)
			}

			if store != nil {
				t.Error("NewStore returned a Store alongside an empty key; it must return none")
			}

			for _, want := range []string{"app.yaml", tc.at} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %s", err, want)
				}
			}
		})
	}
}

func TestEmptyKey_IsRefusedUnderExactKeysToo(t *testing.T) {
	t.Parallel()

	_, err := config.NewStore(context.Background(), config.WithCaseSensitiveKeys(),
		config.WithReaders(config.NamedSource{Name: "app.yaml", Content: []byte("c:\n  \"\": 3\n")}))
	if !errors.Is(err, config.ErrEmptyKey) {
		t.Fatalf("NewStore err = %v, want ErrEmptyKey", err)
	}
}

func TestEmptyKey_IsNotACollision(t *testing.T) {
	t.Parallel()

	_, err := config.NewStore(context.Background(),
		config.WithReaders(config.NamedSource{Name: "app.yaml", Content: []byte("c:\n  \"\": 3\n")}))
	if errors.Is(err, config.ErrKeyCollision) {
		t.Errorf("err = %v; an empty key is its own refusal, not ErrKeyCollision", err)
	}
}

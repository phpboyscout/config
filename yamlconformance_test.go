package config_test

import (
	"testing"

	yaml "go.yaml.in/yaml/v3"

	"gitlab.com/phpboyscout/go/config"
	"gitlab.com/phpboyscout/go/config/backendconformance"
)

// The core YAML codec is run through the same backendconformance suite the
// remote adapters use, so the module exercises its own file backend against the
// shared contract — in particular the renderable-scalar / hostile-key case, whose
// escaping guarantee is a property of the YAML emitter (yaml_codec.go). A real
// on-disk directory is used rather than an in-memory filesystem so the watch
// subtest goes through fsnotify and settles in microseconds rather than waiting
// on the poll cadence.

// fileControl stands in for another client of the same file: it edits the file
// out of band (for the conflict and watch assertions) and re-opens a backend over
// it (to prove a committed write reached disk).
type fileControl struct {
	fs   config.FS
	name string
}

func (c *fileControl) Mutate(t *testing.T) {
	t.Helper()

	// A change to a value visible in the merged config, so a reload is not
	// coalesced away as a no-op, and a different content hash, so the conflict
	// check fires.
	const changed = "level: externally-changed\nserver:\n  port: 9090\n"
	if err := c.fs.WriteFile(c.name, []byte(changed), 0o600); err != nil {
		t.Fatalf("mutating %s: %v", c.name, err)
	}
}

func (c *fileControl) Reopen(*testing.T) config.Backend {
	return config.NewFileBackend(c.fs, c.name)
}

func writeYAMLSeed(t *testing.T, fsys config.FS, name string, seed map[string]any) {
	t.Helper()

	data, err := yaml.Marshal(seed)
	if err != nil {
		t.Fatalf("marshalling seed: %v", err)
	}

	if err := fsys.WriteFile(name, data, 0o600); err != nil {
		t.Fatalf("writing seed %s: %v", name, err)
	}
}

func TestBackendConformance_YAMLFileBackend(t *testing.T) {
	t.Parallel()

	backendconformance.Run(t, backendconformance.Suite{
		NewBackend: func(t *testing.T, seed map[string]any) (config.Backend, backendconformance.Control) {
			fsys, err := config.Dir(t.TempDir())
			if err != nil {
				t.Fatalf("config.Dir: %v", err)
			}

			const name = "app.yaml"

			// A nil seed means an absent source: leave the file uncreated so Load
			// reports fs.ErrNotExist.
			if seed != nil {
				writeYAMLSeed(t, fsys, name, seed)
			}

			return config.NewFileBackend(fsys, name), &fileControl{fs: fsys, name: name}
		},
		Seed:     conformanceSeed(),
		Defines:  conformanceDefines(),
		WriteKey: "level", WriteValue: "debug",
	})
}

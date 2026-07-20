package config_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestDependencyFootprint is this module's statement of "framework-free".
//
// The comment this replaces described go/config as "the Viper wrapper", and
// exempted the Viper stack from the guard on the grounds that it was
// intrinsic. That stopped being true when the Store replaced the container:
// viper is gone, and what remains — afero, pflag, cast, fsnotify, mapstructure
// — is depended on directly and deliberately rather than inherited.
//
// What is forbidden is the rest: go-tool-base itself, Cobra, the TUI stack,
// OpenTelemetry, and cloud SDKs. A configuration module that drags a CLI
// framework or a cloud SDK into every consumer's graph has stopped being a
// library. testify and godog are test-only and are likewise not listed.
//
// For the record, and checked when this was written: the library graph is 26
// non-stdlib packages across 10 modules, against viper 1.21.0's 36 across 13.
// The numbers are not asserted here — they move with upstream and a test that
// fails when someone else adds a package is noise — but they are the reason
// this guard is worth keeping.
func TestDependencyFootprint(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", "./...").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}

	forbidden := []string{
		"github.com/spf13/viper",
		"gitlab.com/phpboyscout/go-tool-base",
		"github.com/spf13/cobra",
		"github.com/charmbracelet",
		"charm.land",
		"go.opentelemetry.io",
		"github.com/aws/aws-sdk-go",
		"cloud.google.com/go",
		"github.com/Azure/azure-sdk",
	}

	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		for _, bad := range forbidden {
			if strings.HasPrefix(dep, bad) {
				t.Errorf("forbidden dependency in graph: %s (matched %q)", dep, bad)
			}
		}
	}
}

package config_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestDependencyFootprint is this module's statement of "framework-free — with one
// deliberate exception". Every OTHER module in the phpboyscout Go toolkit forbids the
// Viper stack in its dependency graph; go/config is the module that exception was
// written for. It *is* the Viper wrapper, so its guard makes NO assertion about
// viper/afero/pflag/cast/fsnotify/mapstructure/gotenv — those are intrinsic. What it
// does forbid is the rest: go-tool-base itself, Cobra, the TUI stack, OpenTelemetry,
// and cloud SDKs. testify/objx (pulled by the published configmock package) are
// permitted test-assertion dependencies and are likewise not listed.
func TestDependencyFootprint(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", "./...").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}

	forbidden := []string{
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

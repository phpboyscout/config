package config_test

import (
	"os/exec"
	"strings"
	"testing"
)

// allowedModules is every third-party module this library may pull into a
// consumer's dependency graph.
//
// An allowlist rather than a denylist, which is the whole point. The list this
// replaced named Cobra, the TUI stack, OpenTelemetry and the cloud SDKs — all
// things nobody would plausibly add to a configuration library, and all
// obvious in a go.mod diff if they did. It could not catch the case that
// actually matters: a direct dependency bumping its own requirements and
// dragging something heavy in transitively, which changes nothing in this
// module's go.mod but a version number.
//
// Adding an entry here should feel like a decision, because it is one. This
// module's pitch includes having a smaller graph than what it replaced, and a
// claim like that decays silently unless something checks it.
// sentinelPackage is a dependency that must always be present — cast backs the typed accessors, so it is always present.
// Its absence from the scan means the scan itself failed.
const sentinelPackage = "github.com/spf13/cast"

// selfModule is this module, which `go list -deps .` naturally includes. It is
// not a dependency and is skipped rather than allowlisted, so the list above
// stays a list of things consumers inherit from elsewhere.
const selfModule = "gitlab.com/phpboyscout/go/config"

var allowedModules = []string{
	// The estate's errors package, which replaced the standard
	// library's here so that hints, stacks and structured attributes
	// survive. It has NO dependencies of its own, so this adds one
	// module to the graph and nothing beneath it.
	"gitlab.com/phpboyscout/go/errors",

	// Command-line flags, for the flag backend.
	"github.com/spf13/pflag",
	// Type coercion, for the typed accessors.
	"github.com/spf13/cast",
	// Struct decoding, for sections and Unmarshal.
	"github.com/go-viper/mapstructure",
	// Native filesystem notification, with a polling fallback.
	"github.com/fsnotify/fsnotify",
	// Comment-preserving YAML editing, and the parser beneath it.
	"gitlab.com/phpboyscout/go/yamldoc",
	"github.com/goccy/go-yaml",
	// YAML values. Deliberately separate from the document parser above: the
	// two disagree about scalar types, and the boundary must not be crossed.
	"go.yaml.in/yaml",
	// Pulled by the above, not by this module directly.
	"golang.org/x/sys",
}

// TestDependencyFootprint is this module's statement of "framework-free".
//
// It inspects the *library* graph — `go list -deps .`, not `./...` — because
// that is what a consumer inherits. The previous version checked the test
// graph too and so policed testify, go-spew, difflib and objx, none of which
// reach anyone importing this package. Test dependencies are the suite's
// business; this test is about what the module costs to depend on.
//
// Historical note worth keeping: the comment here once described go/config as
// "the Viper wrapper" and exempted the Viper stack as intrinsic. That stopped
// being true when the Store replaced the container, and the comment outlived
// the fact by several commits — which is the argument for asserting a property
// rather than describing one.
func TestDependencyFootprint(t *testing.T) {
	t.Parallel()

	// The library graph only. Test-only dependencies never reach a consumer.
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}

	// Prove the scan actually ran. An empty or malformed result yields one empty
	// string, every check below is skipped, and the test passes green — which is
	// exactly how a broken harness has read as a clean pass here before.
	seen := false

	for pkg := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(pkg, sentinelPackage) {
			seen = true
		}

		if pkg == selfModule || strings.HasPrefix(pkg, selfModule+"/") {
			continue
		}

		if !thirdParty(pkg) || permitted(pkg) {
			continue
		}

		t.Errorf("undeclared dependency in the library graph: %s\n"+
			"If this is deliberate, add its module to allowedModules and say why. "+
			"If it arrived transitively from a version bump, that is exactly what "+
			"this test is for.", pkg)
	}

	if !seen {
		t.Fatalf("the dependency scan produced no recognisable output: %q is always "+
			"in this module's graph, so its absence means `go list` failed rather "+
			"than that the graph is clean", sentinelPackage)
	}
}

// thirdParty reports whether an import path is outside the standard library.
//
// The standard library has no dot in its first path segment; everything else
// is fetched from somewhere.
func thirdParty(pkg string) bool {
	first, _, _ := strings.Cut(pkg, "/")

	return strings.Contains(first, ".")
}

// permitted reports whether a package belongs to an allowed module.
//
// Prefix matching, so a module's subpackages are covered by one entry, and
// major-version suffixes do not each need listing.
func permitted(pkg string) bool {
	for _, module := range allowedModules {
		if pkg == module || strings.HasPrefix(pkg, module+"/") {
			return true
		}
	}

	return false
}

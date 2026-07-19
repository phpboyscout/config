package config_test

import (
	"os"
	"testing"

	"github.com/cucumber/godog"
)

// TestFeatures runs the Gherkin scenarios under features/.
//
// The scenarios live in the external test package and drive the module through
// its public API only. That is deliberate: a scenario that reaches into
// unexported state stops describing behaviour a consumer can rely on, which is
// the only thing a scenario is for.
//
// Unlike the sibling modules, these run as part of the ordinary test job rather
// than behind an end-to-end gate. There is no binary to build and no external
// service to stand up — the behaviour under test spans components but stays in
// process, so gating it would only delay the feedback.
func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: initializeScenario,
		Options: &godog.Options{
			Format:   formatFor(t),
			Paths:    []string{"features"},
			Tags:     tagExpression(),
			Strict:   true, // an undefined or pending step fails the run
			TestingT: t,
		},
	}

	if suite.Run() != 0 {
		t.Fatal("feature tests failed")
	}
}

// initializeScenario registers every step definition. Each area contributes its
// own initialiser, mirroring the features/<area>/ layout.
func initializeScenario(ctx *godog.ScenarioContext) {
	initStoreSteps(ctx)
}

// formatFor keeps local runs readable and CI output parseable.
func formatFor(t *testing.T) string {
	t.Helper()

	if os.Getenv("CI") != "" {
		return "junit"
	}

	return "pretty"
}

// tagExpression allows a single feature or scenario to be run in isolation,
// following the sibling modules' convention.
func tagExpression() string {
	return os.Getenv("BDD_TAGS")
}

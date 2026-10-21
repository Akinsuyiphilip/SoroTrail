package scripts_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The release pipeline is only exercised by a tag push, so a regression in
// these workflow files is invisible until a release is already public. The
// tests below read the workflow YAML the way GitHub does and pin the
// properties that make the snapshot check worth having: it validates the
// configuration, it builds every target, it runs the artefact it produced, it
// publishes nothing, and it is pinned to a goreleaser release. Each assertion
// is written so that removing the guarantee — deleting a step, unpinning the
// version, letting `check` drift after the build — fails here instead of
// failing on a tag.
const (
	ciWorkflowPath      = ".github/workflows/ci.yml"
	releaseWorkflowPath = ".github/workflows/release.yml"

	// goreleaserJobName is the job this issue adds.
	goreleaserJobName = "goreleaser"

	// goreleaserAction is the action that installs goreleaser in both
	// workflows, so the two pins are comparable.
	goreleaserAction = "goreleaser/goreleaser-action@"

	// exactVersion matches a pin that names one release, as opposed to a
	// moving ref like `latest`, `v2`, or `v2.18`. A moving ref is the thing
	// that turns an unrelated pull request red the day upstream publishes.
	exactVersion = `^v?\d+\.\d+\.\d+$`
)

type workflowFile struct {
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	Steps []workflowStep `yaml:"steps"`
}

type workflowStep struct {
	Name string         `yaml:"name"`
	Uses string         `yaml:"uses"`
	Run  string         `yaml:"run"`
	If   string         `yaml:"if"`
	With map[string]any `yaml:"with"`
	Env  map[string]any `yaml:"env"`
}

// loadWorkflow parses one workflow relative to the repository root, found by
// walking up to go.mod so the test does not hard-code how deep scripts/ sits.
func loadWorkflow(t *testing.T, rel string) workflowFile {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "no go.mod found above %s", dir)
		dir = parent
	}

	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	require.NoError(t, err, "%s must be readable; these workflows are the release path", rel)

	var workflow workflowFile
	require.NoError(t, yaml.Unmarshal(data, &workflow), "parsing %s", rel)
	require.NotEmpty(t, workflow.Jobs, "%s declares no jobs", rel)
	return workflow
}

// withString renders one `with:` value as the workflow author wrote it. Values
// arrive as whatever YAML inferred — a bool for install-only, an int for
// fetch-depth — and every assertion here cares about the text, not the type.
func (s workflowStep) withString(key string) (string, bool) {
	value, ok := s.With[key]
	if !ok {
		return "", false
	}
	return fmt.Sprint(value), true
}

// goreleaserPins returns the `version:` of every goreleaser-action step,
// keyed by the workflow it lives in.
func goreleaserPins(t *testing.T, workflows map[string]workflowFile) map[string]string {
	t.Helper()

	pins := map[string]string{}
	pattern := regexp.MustCompile(exactVersion)
	for name, workflow := range workflows {
		for _, job := range workflow.Jobs {
			for _, step := range job.Steps {
				if !strings.HasPrefix(step.Uses, goreleaserAction) {
					continue
				}
				version, ok := step.withString("version")
				require.True(t, ok,
					"%s: the goreleaser action has no version, so it resolves to whatever upstream defaults to", name)
				require.Regexp(t, pattern, version,
					"%s: goreleaser is pinned to %q; pin an exact release", name, version)
				pins[name] = version
			}
		}
	}
	require.Len(t, pins, len(workflows),
		"every release-related workflow must install goreleaser through the official action, got %v", pins)
	return pins
}

// TestGoreleaserIsPinnedAcrossTheWorkflows covers the two places the version is
// written. They are separate files because one publishes and one only
// validates, so nothing but this test stops them drifting — and a drift means
// the validated version is not the version that ships.
func TestGoreleaserIsPinnedAcrossTheWorkflows(t *testing.T) {
	t.Parallel()

	pins := goreleaserPins(t, map[string]workflowFile{
		ciWorkflowPath:      loadWorkflow(t, ciWorkflowPath),
		releaseWorkflowPath: loadWorkflow(t, releaseWorkflowPath),
	})

	assert.Equal(t, pins[releaseWorkflowPath], pins[ciWorkflowPath],
		"CI validates a different goreleaser release than the one that publishes: %v", pins)
}

// TestCIGoreleaserJob_ValidatesThenBuilds pins the shape of the job itself:
// the configuration check runs first because it is nearly free and catches most
// configuration errors on its own, and the snapshot release after it is what
// proves every target in the builds matrix actually compiles.
func TestCIGoreleaserJob_ValidatesThenBuilds(t *testing.T) {
	t.Parallel()

	job, ok := loadWorkflow(t, ciWorkflowPath).Jobs[goreleaserJobName]
	require.True(t, ok, "%s must define a %q job", ciWorkflowPath, goreleaserJobName)

	checkAt, snapshotAt := -1, -1
	for i, step := range job.Steps {
		switch {
		case strings.Contains(step.Run, "goreleaser check"):
			checkAt = i
		case strings.Contains(step.Run, "goreleaser release") && strings.Contains(step.Run, "--snapshot"):
			snapshotAt = i
			assert.Contains(t, step.Run, "--clean",
				"the snapshot must build from a clean dist/, or a stale artefact can satisfy the checks below")
			assert.NotContains(t, step.Run, "release --clean",
				"a bare `release` would publish on a tag; this job must only ever snapshot")
		}
	}

	require.NotEqual(t, -1, checkAt, "the job must run `goreleaser check`")
	require.NotEqual(t, -1, snapshotAt, "the job must run `goreleaser release --snapshot`")
	assert.Less(t, checkAt, snapshotAt,
		"`goreleaser check` must run before the snapshot build, so a configuration error is reported without compiling six targets")
}

// TestCIGoreleaserJob_PublishesNothing is the fork-safety and blast-radius
// guard: the job runs on pull requests from forks, which get a read-only token,
// so it must never reach for a secret or hand goreleaser a token that would let
// it publish. A snapshot needs neither.
func TestCIGoreleaserJob_PublishesNothing(t *testing.T) {
	t.Parallel()

	job, ok := loadWorkflow(t, ciWorkflowPath).Jobs[goreleaserJobName]
	require.True(t, ok, "%s must define a %q job", ciWorkflowPath, goreleaserJobName)

	for _, step := range job.Steps {
		rendered := strings.Join([]string{step.Name, step.Run, fmt.Sprint(step.Env), fmt.Sprint(step.With)}, "\n")
		assert.NotContains(t, rendered, "secrets.",
			"step %q must not read a secret: a fork pull request runs with a read-only token", step.Name)
		assert.NotContains(t, rendered, "GITHUB_TOKEN",
			"step %q must not hand goreleaser a token; a snapshot publishes nothing", step.Name)
	}
	assert.NotContains(t, job.Steps[0].Uses+job.Steps[0].Name, "publish",
		"the job publishes nothing")
}

// TestCIGoreleaserJob_RunsTheArtefact is the ldflags guard. The link flags in
// .goreleaser.yaml name buildinfo variables by full import path, and the linker
// ignores a target that does not resolve — the binary builds and reports
// "unknown" forever. Only executing the artefact catches that, so the job has
// to contain a step that does, and it has to fail on the placeholder rather
// than print it.
func TestCIGoreleaserJob_RunsTheArtefact(t *testing.T) {
	t.Parallel()

	job, ok := loadWorkflow(t, ciWorkflowPath).Jobs[goreleaserJobName]
	require.True(t, ok, "%s must define a %q job", ciWorkflowPath, goreleaserJobName)

	var runner workflowStep
	found := false
	for _, step := range job.Steps {
		// The step that invokes a binary built by the snapshot, rather than
		// goreleaser itself.
		if strings.Contains(step.Run, "dist/") && strings.Contains(step.Run, "version") {
			runner, found = step, true
			break
		}
	}
	require.True(t, found,
		"the job must run a binary the snapshot built; otherwise an ldflag that never reaches the artefact is invisible")

	assert.Contains(t, runner.Run, "unknown",
		"the step must name the placeholder it rejects, or an unstamped binary passes")
	for _, field := range []string{"version", "commit", "build-date"} {
		assert.Contains(t, runner.Run, field,
			"the step must assert the stamped %q field", field)
	}
	assert.Contains(t, runner.Run, "exit 1",
		"the step must fail the job when the build info is missing")
}

// TestCIGoreleaserJob_IsScopedButAlwaysReports covers the compromise that keeps
// a six-target build off unrelated pull requests without breaking the status
// check. The job always runs and always reports, and only its goreleaser steps
// are conditional — a step that gates the job at workflow or job level would
// leave a required check waiting for a status that never arrives, which is the
// failure the aggregate `test` job above exists to avoid.
func TestCIGoreleaserJob_IsScopedButAlwaysReports(t *testing.T) {
	t.Parallel()

	job, ok := loadWorkflow(t, ciWorkflowPath).Jobs[goreleaserJobName]
	require.True(t, ok, "%s must define a %q job", ciWorkflowPath, goreleaserJobName)

	scopeAt := -1
	for i, step := range job.Steps {
		if strings.Contains(step.Run, "check_release_scope.sh") {
			scopeAt = i
			break
		}
	}
	require.NotEqual(t, -1, scopeAt,
		"the job must ask scripts/check_release_scope.sh whether the diff can affect the release build")

	for _, step := range job.Steps[:scopeAt] {
		assert.Empty(t, step.If,
			"step %q runs before the scope is known, so it cannot be conditional", step.Name)
	}

	for _, step := range job.Steps[scopeAt+1:] {
		// Checkout and setup-go have to run unconditionally for the job to
		// report at all; everything that consumes the release pipeline is
		// gated on the decision.
		if strings.HasPrefix(step.Uses, "actions/checkout@") || strings.HasPrefix(step.Uses, "actions/setup-go@") {
			continue
		}
		assert.Equal(t, "steps.scope.outputs.run == 'true'", step.If,
			"step %q must be gated on the scope decision", step.Name)
	}

	// The generated changelog walks commit history, so a shallow checkout
	// silently yields an empty section rather than a failure.
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			depth, ok := step.withString("fetch-depth")
			assert.True(t, ok, "checkout must set fetch-depth; goreleaser reads commit history")
			assert.Equal(t, "0", depth, "checkout must fetch full history for the changelog")
		}
	}
}

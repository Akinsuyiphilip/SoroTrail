package scripts_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scopeResult captures one gate run: its exit code, stdout, and the `run=`
// value the script wrote to $GITHUB_OUTPUT.
type scopeResult struct {
	code   int
	output string
	runOut string
}

// runScope feeds a list of repository-relative paths to
// scripts/check_release_scope.sh, the way the CI step feeds it the output of
// `git diff --name-only`. GITHUB_OUTPUT is pointed at a file so the assertions
// read the same output the workflow does, not the human-readable line.
func runScope(t *testing.T, files []string, args ...string) scopeResult {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)

	githubOutput := filepath.Join(t.TempDir(), "github_output")
	require.NoError(t, os.WriteFile(githubOutput, nil, 0o644))

	cmd := exec.Command("bash", append([]string{filepath.Join(wd, "check_release_scope.sh")}, args...)...)
	cmd.Stdin = strings.NewReader(strings.Join(files, "\n"))
	cmd.Env = append(envWithout(os.Environ(), "GITHUB_OUTPUT"), "GITHUB_OUTPUT="+githubOutput)
	out, err := cmd.CombinedOutput()

	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !assert.ErrorAs(t, err, &exitErr, "running the scope gate: %v", err) {
			return scopeResult{}
		}
		code = exitErr.ExitCode()
	}

	runOut, err := os.ReadFile(githubOutput)
	require.NoError(t, err)
	return scopeResult{code: code, output: string(out), runOut: string(runOut)}
}

func requireScopeBash(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("the gate is a bash script; CI runs it on Linux")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
}

// TestCheckReleaseScope_DecidesPerDiff is the core contract: a diff that can
// change what the release pipeline produces runs the job, and one that cannot
// skips it. Each case names the path that must drive the decision, so a
// pattern that is added, dropped or loosened shows up as a failing row rather
// than as a job that quietly stopped running on releases.
func TestCheckReleaseScope_DecidesPerDiff(t *testing.T) {
	requireScopeBash(t)

	tests := []struct {
		name  string
		files []string
		want  string // "true" or "false"
	}{
		{
			name:  "the goreleaser config itself",
			files: []string{".goreleaser.yaml"},
			want:  "true",
		},
		{
			name:  "the module",
			files: []string{"go.mod"},
			want:  "true",
		},
		{
			name:  "the module checksums",
			files: []string{"go.sum"},
			want:  "true",
		},
		{
			name:  "the Makefile that stamps the same build info",
			files: []string{"Makefile"},
			want:  "true",
		},
		{
			name:  "the release Dockerfile",
			files: []string{"Dockerfile"},
			want:  "true",
		},
		{
			name:  "a templated Dockerfile",
			files: []string{"Dockerfile.sorotrail"},
			want:  "true",
		},
		{
			name:  "the buildinfo package the ldflags name",
			files: []string{"internal/buildinfo/buildinfo.go"},
			want:  "true",
		},
		{
			name:  "the CI workflow that runs the job",
			files: []string{".github/workflows/ci.yml"},
			want:  "true",
		},
		{
			name:  "the release workflow",
			files: []string{".github/workflows/release.yml"},
			want:  "true",
		},
		{
			name:  "the scope gate itself",
			files: []string{"scripts/check_release_scope.sh"},
			want:  "true",
		},
		{
			name:  "an affecting file behind an unrelated one",
			files: []string{"README.md", "docs/triage.md", "go.mod", "README.md"},
			want:  "true",
		},
		{
			name:  "an empty diff is validated rather than skipped",
			files: nil,
			want:  "true",
		},
		{
			name:  "documentation only",
			files: []string{"README.md", "CHANGELOG.md", "docs/triage.md"},
			want:  "false",
		},
		{
			name:  "application source only",
			files: []string{"internal/api/handler.go", "internal/store/sqlite.go", "cmd/sorotrail/main.go"},
			want:  "false",
		},
		{
			name:  "the generated client only",
			files: []string{"pkg/client/client.gen.go", "api/openapi.yaml"},
			want:  "false",
		},
		{
			// A near miss must not match: a glob that loses its anchor would
			// classify any Dockerfile-ish or buildinfo-ish path as in scope
			// and quietly run a six-target build, or worse.
			name:  "paths that merely resemble the accepted ones",
			files: []string{"DockerfileX", "docs/Dockerfile.md", "internal/buildinfo_extra/x.go", "go.mod.bak", "mygoto/sum"},
			want:  "false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runScope(t, tt.files)

			assert.Equal(t, 0, got.code, "the gate decides, it does not fail: %s", got.output)
			assert.Equal(t, "run="+tt.want+"\n", got.runOut)

			decision := "[SKIP]"
			if tt.want == "true" {
				decision = "[RUN]"
			}
			assert.Contains(t, got.output, decision)
		})
	}
}

// TestCheckReleaseScope_ReportsWhy covers the human half of the decision: a
// skipped job has to say which diff it looked at, or a release-config change
// that was wrongly skipped leaves no trace to debug.
func TestCheckReleaseScope_ReportsWhy(t *testing.T) {
	requireScopeBash(t)

	t.Run("names the file that forced the run", func(t *testing.T) {
		got := runScope(t, []string{"README.md", "internal/buildinfo/buildinfo.go"})
		assert.Contains(t, got.output, "internal/buildinfo/buildinfo.go",
			"the message must name the changed path that put the diff in scope")
	})

	t.Run("says the diff was out of scope", func(t *testing.T) {
		got := runScope(t, []string{"README.md"})
		assert.Contains(t, got.output, "cannot differ")
	})
}

// TestCheckReleaseScope_ForceAll covers the caller-cannot-scope path: the
// workflow uses it on pushes to main and when it fails to compute a diff.
// Running the build when in doubt is the safe direction, so this must never
// report SKIP.
func TestCheckReleaseScope_ForceAll(t *testing.T) {
	requireScopeBash(t)

	t.Run("with no input at all", func(t *testing.T) {
		got := runScope(t, nil, "--all")
		assert.Equal(t, 0, got.code)
		assert.Equal(t, "run=true\n", got.runOut)
	})

	t.Run("even when the diff would be out of scope", func(t *testing.T) {
		got := runScope(t, []string{"README.md"}, "--all")
		assert.Equal(t, 0, got.code)
		assert.Equal(t, "run=true\n", got.runOut, "--all must override the file list, not be reconciled with it")
	})
}

// TestCheckReleaseScope_EchoesWithoutGithubOutput keeps the gate usable by
// hand — the workflow reads $GITHUB_OUTPUT, but a contributor running the
// command from the docstring needs the same answer on stdout.
func TestCheckReleaseScope_EchoesWithoutGithubOutput(t *testing.T) {
	requireScopeBash(t)

	wd, err := os.Getwd()
	require.NoError(t, err)

	var buf bytes.Buffer
	cmd := exec.Command("bash", filepath.Join(wd, "check_release_scope.sh"))
	cmd.Stdin = strings.NewReader("go.mod\n")
	cmd.Stdout = &buf
	// Scrub GITHUB_OUTPUT rather than assuming the test environment lacks it.
	cmd.Env = append(envWithout(os.Environ(), "GITHUB_OUTPUT"), "GITHUB_OUTPUT=")
	require.NoError(t, cmd.Run())

	assert.Contains(t, buf.String(), "run=true")
}

// TestCheckReleaseScope_RejectsUnknownFlags guards the interface the workflow
// calls: a typo in the workflow (say --everything) must fail loudly instead of
// falling through to reading an empty stdin and reporting a decision.
func TestCheckReleaseScope_RejectsUnknownFlags(t *testing.T) {
	requireScopeBash(t)

	got := runScope(t, nil, "--everything")
	assert.NotEqual(t, 0, got.code, "an unknown flag must not be silently ignored")
	assert.Empty(t, got.runOut, "no decision may be published for a rejected invocation")
}

// envWithout returns env with every entry naming one of keys removed.
func envWithout(env []string, keys ...string) []string {
	kept := make([]string, 0, len(env))
	for _, entry := range env {
		drop := false
		for _, key := range keys {
			if strings.HasPrefix(entry, key+"=") {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, entry)
		}
	}
	return kept
}

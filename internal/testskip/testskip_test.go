package testskip

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTool_Decide pins the three outcomes a dependency can have. The
// distinction that matters is between "missing, so skip" and "missing, so
// fail": the first keeps a contributor without Helm working, the second is
// what lets a CI job assert that the tests really ran.
func TestTool_Decide(t *testing.T) {
	t.Run("a present program continues", func(t *testing.T) {
		tool := Tool{Name: "sh", Binary: "sh"}
		assert.True(t, tool.Present())
		assert.Equal(t, ActionContinue, tool.Decide())
	})

	t.Run("a missing program skips", func(t *testing.T) {
		tool := Tool{Name: "definitely-not-installed", Binary: "definitely-not-installed"}
		assert.False(t, tool.Present())
		assert.Equal(t, ActionSkip, tool.Decide())
	})

	t.Run("an unset environment variable means the dependency is missing", func(t *testing.T) {
		const env = "SOROTRAIL_TEST_PREREQS_UNSET"
		require.NoError(t, os.Unsetenv(env))
		tool := Tool{Name: "Postgres", EnvVar: env}
		assert.False(t, tool.Present())
		assert.Equal(t, ActionSkip, tool.Decide())
	})

	t.Run("a set environment variable means the dependency is there", func(t *testing.T) {
		const env = "SOROTRAIL_TEST_PREREQS_SET"
		t.Setenv(env, "postgres://localhost/db")
		tool := Tool{Name: "Postgres", EnvVar: env}
		assert.True(t, tool.Present())
		assert.Equal(t, ActionContinue, tool.Decide())
	})

	t.Run("an empty environment variable means the dependency is missing", func(t *testing.T) {
		const env = "SOROTRAIL_TEST_PREREQS_EMPTY"
		t.Setenv(env, "")
		tool := Tool{Name: "Postgres", EnvVar: env}
		assert.False(t, tool.Present())
		assert.Equal(t, ActionSkip, tool.Decide())
	})

	t.Run("a required dependency fails instead of skipping", func(t *testing.T) {
		const requireEnv = "SOROTRAIL_TEST_PREREQS_REQUIRE"
		t.Setenv(requireEnv, "1")
		tool := Tool{
			Name:       "definitely-not-installed",
			Binary:     "definitely-not-installed",
			RequireEnv: requireEnv,
		}
		assert.True(t, tool.Required())
		assert.Equal(t, ActionFail, tool.Decide())
	})

	t.Run("a required dependency that is present still continues", func(t *testing.T) {
		const requireEnv = "SOROTRAIL_TEST_PREREQS_REQUIRE_PRESENT"
		t.Setenv(requireEnv, "1")
		tool := Tool{Name: "sh", Binary: "sh", RequireEnv: requireEnv}
		assert.Equal(t, ActionContinue, tool.Decide())
	})
}

// TestTool_Notice checks that the notice says everything a reader needs to
// act on it: what did not run, why, what the skip cost, and how to get the
// dependency. A notice that names only the tool leaves the reader guessing
// what coverage they just lost.
func TestTool_Notice(t *testing.T) {
	notice := Helm.Notice()

	assert.Contains(t, notice, "SKIPPED", "the notice must say the tests were skipped")
	assert.Contains(t, notice, Helm.Title, "the notice must name what did not run")
	assert.Contains(t, notice, Helm.Reason, "the notice must say why it did not run")
	assert.Contains(t, notice, Helm.Install, "the notice must say how to get the dependency")
	// The cost is wrapped across lines, so it is compared with the
	// whitespace normalised: the words are what matter, not where the
	// frame broke them.
	assert.Contains(t, normalizeSpace(notice), normalizeSpace(Helm.Cost),
		"the notice must say what the skip cost")
}

// normalizeSpace collapses every run of whitespace into a single space, so a
// wrapped sentence can be compared with the unwrapped original.
func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// TestNotice_WrapsLongLines keeps the notice readable as a block: a line
// that runs past the frame's width would break the frame and make the
// notice look like ordinary output.
func TestNotice_WrapsLongLines(t *testing.T) {
	notice := Helm.Notice()

	for _, line := range splitLines(notice) {
		assert.LessOrEqual(t, len([]rune(line)), noticeWidth,
			"the notice must not exceed its frame width: %q", line)
	}
}

// TestMissing returns only the dependencies that are not available, in the
// order they were given.
func TestMissing(t *testing.T) {
	const env = "SOROTRAIL_TEST_PREREQS_MISSING"
	t.Setenv(env, "")

	present := Tool{Name: "sh", Binary: "sh"}
	missing := Tool{Name: "Postgres", EnvVar: env}

	got := Missing(present, missing)
	require.Len(t, got, 1)
	assert.Equal(t, missing, got[0])
}

// TestDependencies covers every dependency the suite may skip on: the
// command the make targets run reports on exactly this list, so a
// dependency that is not registered here would never be reported.
func TestDependencies(t *testing.T) {
	deps := Dependencies()
	require.NotEmpty(t, deps)

	names := map[string]bool{}
	for _, tool := range deps {
		names[tool.Name] = true
		assert.NotEmpty(t, tool.Title, "every dependency must say what it skips")
		assert.NotEmpty(t, tool.Reason, "every dependency must say why it is missing")
		assert.NotEmpty(t, tool.Cost, "every dependency must say what the skip costs")
		assert.NotEmpty(t, tool.Install, "every dependency must say how to provide it")
	}
	assert.True(t, names["Helm"], "the Helm dependency must be reported")
	assert.True(t, names["Postgres"], "the database dependency must be reported")
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

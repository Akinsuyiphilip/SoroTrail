package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mergeScript resolves merge_coverage.sh next to this test file, so the
// tests run from any working directory.
func mergeScript(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	return filepath.Join(filepath.Dir(file), "merge_coverage.sh")
}

// writeProfile writes a coverage profile fixture into a temp directory and
// returns its path. Inputs are the profile's lines verbatim.
func writeProfile(t *testing.T, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	return path
}

// runMerge executes merge_coverage.sh with the given arguments and returns
// the combined output and exit code.
func runMerge(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{mergeScript(t)}, args...)...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		require.True(t, ok, "merge_coverage.sh did not run: %v", err)
		code = exitErr.ExitCode()
	}
	return string(out), code
}

// TestMergeCoverage_SumsOverlappingRanges: the same statement range from two
// runs must appear once in the merged profile with its hit counts summed, and
// ranges unique to either input must survive — that union is the point of
// merging the unit and integration-tagged runs.
func TestMergeCoverage_SumsOverlappingRanges(t *testing.T) {
	a := writeProfile(t, "a.out",
		"mode: atomic",
		"internal/store/store.go:10.20,12.5 3 1",
		"internal/store/store.go:20.2,22.9 2 4",
	)
	b := writeProfile(t, "b.out",
		"mode: atomic",
		"internal/store/store.go:10.20,12.5 3 2",
		"internal/ingester/ingester.go:5.1,8.2 7 1",
	)
	out := filepath.Join(t.TempDir(), "merged.out")

	stdout, code := runMerge(t, out, a, b)
	require.Equal(t, 0, code, "merge failed: %s", stdout)

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 4, "merged profile should hold the mode line plus three statement records")

	assert.Equal(t, "mode: atomic", lines[0], "the mode line must stay first so go tool cover accepts the file")

	records := map[string]string{}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		require.Len(t, fields, 3)
		records[fields[0]+" "+fields[1]] = fields[2]
	}
	assert.Equal(t, "3", records["internal/store/store.go:10.20,12.5 3"], "hit counts of the overlapping range must be summed (1+2)")
	assert.Equal(t, "4", records["internal/store/store.go:20.2,22.9 2"], "ranges unique to one run survive the merge")
	assert.Equal(t, "1", records["internal/ingester/ingester.go:5.1,8.2 7"], "the integration-only path survives the merge")
}

// TestMergeCoverage_RejectsMixedCovermodes: atomic and count profiles count
// differently, so a mix would produce a plausible-looking wrong total. It
// must fail instead.
func TestMergeCoverage_RejectsMixedCovermodes(t *testing.T) {
	a := writeProfile(t, "a.out", "mode: atomic", "pkg/x.go:1.1,2.2 1 1")
	b := writeProfile(t, "b.out", "mode: count", "pkg/x.go:1.1,2.2 1 1")
	out := filepath.Join(t.TempDir(), "merged.out")

	stdout, code := runMerge(t, out, a, b)
	assert.NotEqual(t, 0, code, "mixed covermodes must be rejected, output: %s", stdout)
	assert.Contains(t, stdout, "mixed covermodes")
}

// TestMergeCoverage_RejectsNonProfile: a file without a mode line is not a
// coverage profile — typically a stale glob or a misconfigured artifact
// download — and must fail rather than be silently concatenated.
func TestMergeCoverage_RejectsNonProfile(t *testing.T) {
	a := writeProfile(t, "a.out", "mode: atomic", "pkg/x.go:1.1,2.2 1 1")
	b := writeProfile(t, "b.out", "this is not a coverage profile")
	out := filepath.Join(t.TempDir(), "merged.out")

	stdout, code := runMerge(t, out, a, b)
	assert.NotEqual(t, 0, code, "non-profile input must be rejected, output: %s", stdout)
	assert.Contains(t, stdout, "not a coverage profile")
}

// TestMergeCoverage_RequiresAtLeastTwoInputs: one input is not a merge, and
// no output argument is a usage error rather than a crash.
func TestMergeCoverage_RequiresAtLeastTwoInputs(t *testing.T) {
	out := filepath.Join(t.TempDir(), "merged.out")
	a := writeProfile(t, "a.out", "mode: atomic", "pkg/x.go:1.1,2.2 1 1")

	stdout, code := runMerge(t, out)
	assert.Equal(t, 2, code, "missing inputs must be a usage error, output: %s", stdout)

	stdout, code = runMerge(t, out, a)
	assert.Equal(t, 2, code, "a single input must be a usage error, output: %s", stdout)
}

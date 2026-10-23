package testskip

import (
	"fmt"
	"os"
	"testing"
)

// Skip skips the current test because an external dependency is missing,
// after printing a notice that says so.
//
// The skip stays a skip: failing instead would break every contributor
// without the dependency, which is the wrong trade. But it is never silent
// — the notice names what did not run, why, and how to get the dependency.
//
// When the tool's RequireEnv variable is set, the skip becomes a failure.
// That is how a CI job asserts the tests really ran: it sets the variable, so
// a runner that lost the dependency fails the job instead of skipping the
// coverage and reporting success.
func Skip(t *testing.T, tool Tool) {
	t.Helper()
	switch tool.Decide() {
	case ActionContinue:
		return
	case ActionFail:
		t.Fatalf("%s is required for this test run but is not available: %s. %s",
			tool.Name, tool.Reason, tool.Install)
	}
	// The notice goes to stderr rather than through t.Log: t.Log output is
	// only shown in verbose mode, while stderr is shown by every harness
	// that surfaces the test binary's output.
	fmt.Fprint(os.Stderr, tool.Notice())
	t.Skipf("%s is not available: %s", tool.Name, tool.Reason)
}

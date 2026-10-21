// Command testprereqs reports the external dependencies the test suite needs
// but cannot find.
//
// A skipped dependency is invisible inside `go test`: Go discards the output
// of a package that passes, so a notice printed by the test binary itself
// only shows up under -v. This command is what the make targets run before
// the suite, so the notice appears in the default output of `make test`,
// `make ci` and every other full-gate target.
//
// It exits non-zero when a dependency the run demanded is missing: the
// RequireEnv variable on a tool turns the report into a failure, which is
// how CI asserts that the tests really ran.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/sorotrail/sorotrail/internal/testskip"
)

func main() {
	os.Exit(run(os.Stderr, testskip.Dependencies()...))
}

// run prints the notice for every missing dependency and reports whether
// the run failed. A dependency this run required and did not have is a
// failure; one it merely wanted is a notice.
func run(w io.Writer, tools ...testskip.Tool) int {
	missing := testskip.Missing(tools...)
	for _, tool := range missing {
		fmt.Fprint(w, tool.Notice())
	}
	for _, tool := range missing {
		if tool.Required() {
			fmt.Fprintf(w, "%s is required for this test run but is not available: %s. %s\n",
				tool.Name, tool.Reason, tool.Install)
			return 1
		}
	}
	return 0
}

// Package testskip gives the test suite one convention for an external
// dependency a test can run without.
//
// A test that needs a program or a server which may not be installed has to
// skip rather than fail: a contributor without Helm should not be blocked by
// the chart's golden tests. The trouble is that a skip prints nothing in the
// default `go test` output, so a green run says nothing about the coverage
// it did not exercise. A contributor who adds a configuration variable, runs
// the suite, sees green and opens a pull request never learns that the
// Helm chart cannot set the new variable.
//
// This package makes the skip loud. The notice names what did not run, why
// it did not run, what the skip cost and how to get the dependency. The same
// wording is used by the make targets (through cmd/testprereqs), so a
// contributor reads the same sentence whether they run `make test` or
// `go test ./...`.
package testskip

import (
	"os"
	"os/exec"
	"strings"
)

// noticeWidth is the width of the banner the notice is framed with. It is
// deliberately narrower than a standard terminal so the notice stays
// readable when the output is piped into a file.
const noticeWidth = 76

// Tool describes an external dependency a test needs but cannot always have:
// either a program on PATH or a server named by an environment variable.
type Tool struct {
	// Name is how the operator knows the dependency ("Helm", "Postgres").
	Name string

	// Binary is the executable to look up on PATH. It is empty for a
	// dependency that is not a program, and falls back to Name when it is
	// empty.
	Binary string

	// EnvVar, when set, names the environment variable that provides the
	// dependency; the dependency counts as present when the variable holds
	// a non-empty value. When it is empty, the dependency is a program
	// looked up on PATH under Binary.
	EnvVar string

	// Title names what did not run, in the operator's terms.
	Title string

	// Reason says, in a full sentence, why the dependency was not there.
	Reason string

	// Cost says what the skip costs: which coverage did not happen. A
	// reader who knows this cannot mistake a green run for full
	// coverage.
	Cost string

	// Install is the operator-facing hint for providing the dependency.
	Install string

	// RequireEnv, when set, names the environment variable that turns the
	// skip into a failure. A CI job that is supposed to run the tests
	// sets it, so the job cannot pass by quietly skipping them.
	RequireEnv string
}

// Helm is the Helm CLI, which the chart's golden tests need to render the
// chart. Without it the rendered manifests are never compared against the
// committed goldens.
var Helm = Tool{
	Name:   "Helm",
	Binary: "helm",
	Title:  "the Helm chart golden tests (deploy/helm/sorotrail)",
	Reason: "helm was not found on PATH",
	Cost: "the chart's rendered manifests are compared byte for byte against " +
		"the committed goldens, and every chart config value is checked against " +
		"the ConfigMap the chart renders — none of which ran",
	Install:    "install Helm (https://helm.sh) and re-run the suite",
	RequireEnv: "SOROTRAIL_REQUIRE_HELM",
}

// Database is the Postgres instance the store conformance suite and the
// integration-tagged tests need.
var Database = Tool{
	Name:   "Postgres",
	EnvVar: "TEST_DATABASE_URL",
	Title:  "the database-backed store tests (the Postgres conformance suite and the integration-tagged suite)",
	Reason: "TEST_DATABASE_URL is not set",
	Cost: "the store paths — migrations, reorg repair, the API's filter " +
		"tests — only run against a real database, so none of that ran",
	Install:    "start one with `make docker-up`, or point TEST_DATABASE_URL at your own, and re-run the suite",
	RequireEnv: "SOROTRAIL_REQUIRE_POSTGRES",
}

// Dependencies returns every external dependency the suite may skip on. The
// make targets report on exactly this list, so the notice a contributor sees
// and the skip a test records cannot describe different things.
func Dependencies() []Tool {
	return []Tool{Helm, Database}
}

// Present reports whether the dependency is available.
func (t Tool) Present() bool {
	if t.EnvVar != "" {
		return os.Getenv(t.EnvVar) != ""
	}
	_, err := exec.LookPath(t.binary())
	return err == nil
}

// binary returns the executable name to look up on PATH.
func (t Tool) binary() string {
	if t.Binary != "" {
		return t.Binary
	}
	return t.Name
}

// Required reports whether this run demanded the dependency: the environment
// variable that turns a skip into a failure is set.
func (t Tool) Required() bool {
	return t.RequireEnv != "" && os.Getenv(t.RequireEnv) != ""
}

// Action is what a test does about a dependency it may not have.
type Action int

const (
	// ActionContinue means the dependency is there: the test runs.
	ActionContinue Action = iota
	// ActionSkip means the dependency is missing and this run did not
	// require it, so the test skips after printing the notice.
	ActionSkip
	// ActionFail means the dependency is missing and this run required
	// it, so the test fails instead of skipping.
	ActionFail
)

// Decide reports what a test must do about the dependency. It is separate
// from the action so the decision can be asserted without running a test
// that skips or fails.
func (t Tool) Decide() Action {
	switch {
	case t.Present():
		return ActionContinue
	case t.Required():
		return ActionFail
	default:
		return ActionSkip
	}
}

// Notice renders the banner the skip prints. It is deliberately loud: the
// whole point is that a skipped dependency is impossible to miss in a
// scroll-back full of green package lines.
func (t Tool) Notice() string {
	rule := strings.Repeat("─", noticeWidth)
	var b strings.Builder
	b.WriteString("\n" + rule + "\n")
	b.WriteString(" NOTICE  a test dependency is missing — some tests were SKIPPED\n")
	b.WriteString(rule + "\n")
	writeWrapped(&b, "  Skipped: ", t.Title)
	writeWrapped(&b, "  Reason:  ", t.Reason)
	writeWrapped(&b, "  Cost:    ", t.Cost)
	writeWrapped(&b, "  To run:  ", t.Install)
	b.WriteString(rule + "\n")
	return b.String()
}

// writeWrapped writes an indented, wrapped paragraph. The continuation lines
// line up under the first one so the notice stays readable as a block.
func writeWrapped(b *strings.Builder, prefix, text string) {
	const width = noticeWidth - 2
	indent := strings.Repeat(" ", len(prefix))
	trimmed := strings.TrimSpace(prefix)
	line := prefix
	for _, word := range strings.Fields(text) {
		if strings.TrimSpace(line) == trimmed {
			// The first word continues the prefix, so the block's values
			// all start in the same column.
			line += word
			continue
		}
		if len(line)+1+len(word) > width {
			b.WriteString(strings.TrimRight(line, " ") + "\n")
			line = indent
		}
		line += " " + word
	}
	b.WriteString(strings.TrimRight(line, " ") + "\n")
}

// Missing returns the dependencies that are not available, in the order they
// were given.
func Missing(tools ...Tool) []Tool {
	var out []Tool
	for _, tool := range tools {
		if !tool.Present() {
			out = append(out, tool)
		}
	}
	return out
}

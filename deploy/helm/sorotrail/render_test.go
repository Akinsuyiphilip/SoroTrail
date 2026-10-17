// Package sorotrail_test covers the Helm chart's rendered output (issue
// #832). CI lints the chart and renders it once, but nothing asserted
// what the rendered manifests actually contain, so a template change
// could silently drop a probe or a resource limit.
//
// These tests render the chart with `helm template` and compare the
// output byte-for-byte against committed golden manifests under
// testdata/, plus a set of structural invariants (probes pointing at the
// real endpoints, DATABASE_URL coming from a Secret, resources present
// when configured). Any template change that alters the output fails the
// golden comparison — regenerating the goldens is a documented one-liner
// (see the TestRenderGolden_* doc comments).
//
// The tests skip when the helm binary is unavailable, so `go test ./...`
// stays green on machines without Helm; CI's helm job installs Helm with
// azure/setup-helm and lints and templates the chart, but the test job
// (go test ./...) has no Helm either, so these golden tests only run where a
// contributor has installed it. That is why the skip has to be loud: without
// the notice a green run looks like it verified the chart.
package sorotrail_test

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "regenerate golden files instead of comparing")

// requireHelmEnv turns the helm-less skip into a failure. CI sets it in the job
// that installs Helm, so the chart tests cannot quietly skip there and leave the
// job green: a missing or shadowed binary fails instead of reporting success for
// a suite that never rendered the chart. Locally it stays unset, because
// failing every contributor without Helm would break the suite for them (issue
// #1074).
const requireHelmEnv = "REQUIRE_HELM_CHART_TESTS"

// helmInstallHint names where to get the binary. It is quoted verbatim in the
// skip message, the failure message and the notice so the three cannot drift
// apart. The version matches the one CI's helm job pins.
const helmInstallHint = "install Helm v3.15.0 via https://helm.sh/docs/intro/install/"

// TestMain emits the skip notice on stderr. It lands in the output when the
// package fails and under -v, so a failing run says outright that the chart went
// unverified instead of leaving the reader to spot three SKIP lines.
//
// It cannot reach the default output of a clean `go test ./...`: the go command
// prints only the final "ok" summary line for a passing package and discards the
// test binary's output entirely (go help test), stderr included. So the notice a
// plain run needs is emitted from outside the test binary — `make test`,
// `make test-fast` and `make test-ci` run scripts/check_helm_chart_tests.sh for
// exactly that reason (issue #1074).
func TestMain(m *testing.M) {
	if notice := helmSkipNotice(); notice != "" {
		fmt.Fprintln(os.Stderr, notice)
	}
	os.Exit(m.Run())
}

// helmSkipNotice reports, as a single line, that this package's chart tests
// cannot run because helm is not on PATH. It returns "" when helm is available
// or when the tests are required, so the caller prints nothing in those cases.
func helmSkipNotice() string {
	if _, err := exec.LookPath("helm"); err == nil {
		return ""
	}
	if os.Getenv(requireHelmEnv) != "" {
		// helmPath fails the tests in this case; the notice would be noise
		// on top of the failure it already reports.
		return ""
	}
	return fmt.Sprintf("NOTICE: helm is not on PATH, so every test in deploy/helm/sorotrail was SKIPPED and the chart goldens were NOT verified (the same is true of the CI test job, which does not install Helm). To run them, %s. Set %s=1 to turn the skip into a failure.",
		helmInstallHint, requireHelmEnv)
}

// helmPath returns the helm binary. Without helm the tests skip, because
// failing would break the suite for every contributor who does not have it;
// with requireHelmEnv set the skip becomes a failure so a job that installs
// Helm can assert the chart tests really ran.
func helmPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("helm")
	if err != nil {
		if os.Getenv(requireHelmEnv) != "" {
			t.Fatalf("%s is set but helm is not on PATH; %s, or unset %s to skip the chart tests",
				requireHelmEnv, helmInstallHint, requireHelmEnv)
		}
		t.Skipf("helm not installed; %s, or set %s=1 to fail instead of skipping (see CONTRIBUTING.md)",
			helmInstallHint, requireHelmEnv)
	}
	return p
}

// chartDir resolves the chart directory relative to this test file, so
// the tests run from any working directory.
func chartDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}

// renderChart runs `helm template` with the given extra args and returns
// the rendered manifests. The database.url value is required by the
// chart (secret.yaml), so every render passes it unless the test
// overrides it via existingSecret.
func renderChart(t *testing.T, helm string, extraArgs ...string) string {
	t.Helper()
	args := append([]string{
		"template", "sorotrail", chartDir(),
		"--namespace", "sorotrail-test",
		"--set", "database.url=postgres://user:pass@host:5432/db",
	}, extraArgs...)
	out, err := exec.Command(helm, args...).CombinedOutput()
	require.NoError(t, err, "helm template failed: %s", out)
	return string(out)
}

// TestRenderGolden_DefaultValues pins the full render of the chart with
// the defaults from values.yaml (plus the required database.url). This
// is the "a template change silently drops something" guard: any drift
// in the rendered manifests fails the byte-for-byte comparison.
//
// Regenerate after an intentional template change with:
//
//	go test ./deploy/helm/sorotrail -run TestRenderGolden -update
func TestRenderGolden_DefaultValues(t *testing.T) {
	helm := helmPath(t)
	got := renderChart(t, helm)
	golden := filepath.Join(chartDir(), "testdata", "default.golden.yaml")
	assertGolden(t, golden, got)
}

// TestRenderGolden_RepresentativeOverrides pins a render with a
// representative override set: resource requests/limits, an existing
// database Secret (so the chart must NOT create its own and must not
// inline the URL), a ServiceMonitor, and a non-default ServiceAccount
// name. Each override exercises a different template branch.
//
// Regenerate with:
//
//	go test ./deploy/helm/sorotrail -run TestRenderGolden -update
func TestRenderGolden_RepresentativeOverrides(t *testing.T) {
	helm := helmPath(t)
	got := renderChart(t, helm,
		"--values", filepath.Join(chartDir(), "testdata", "overrides.yaml"),
	)
	golden := filepath.Join(chartDir(), "testdata", "overrides.golden.yaml")
	assertGolden(t, golden, got)
}

func normalizeManifest(s string) string {
	// Helm v4 emits an extra newline before document separators ("\n\n---\n")
	// whereas Helm v3 emits "\n---\n". Normalize them so golden tests pass
	// regardless of whether Helm v3 or Helm v4 is installed.
	for strings.Contains(s, "\n\n---\n") {
		s = strings.ReplaceAll(s, "\n\n---\n", "\n---\n")
	}
	return strings.TrimSpace(s) + "\n"
}

func assertGolden(t *testing.T, golden, got string) {
	t.Helper()
	if *update {
		require.NoError(t, os.WriteFile(golden, []byte(normalizeManifest(got)), 0o644))
		t.Logf("updated golden file %s", golden)
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("reading golden file %s: %v (run with -update to create it)", golden, err)
	}
	assert.Equal(t, normalizeManifest(string(want)), normalizeManifest(got),
		"rendered manifests drifted from %s; if the change is intentional, regenerate with:\n\tgo test ./deploy/helm/sorotrail -run TestRenderGolden -update", golden)
}

// TestRender_ManifestInvariants asserts the structural properties the
// operators rely on, independently of the goldens so a drift is reported
// with a precise message rather than a whole-file diff:
//
//   - probes point at the real endpoints (/health and /readyz)
//   - DATABASE_URL is mounted from a Secret, never inlined in the pod
//   - resource requests and limits land when configured
//   - the chart creates a Secret only when no existingSecret is given
func TestRender_ManifestInvariants(t *testing.T) {
	helm := helmPath(t)

	// Default render: chart-managed Secret, no resources.
	rendered := renderChart(t, helm)
	deployment := manifest(t, rendered, "Deployment")

	t.Run("probes hit the real endpoints", func(t *testing.T) {
		assert.Contains(t, deployment, "livenessProbe:", "liveness probe must exist")
		assert.Contains(t, deployment, "readinessProbe:", "readiness probe must exist")
		// The probes must point at the documented health endpoints, not
		// at a path the app never serves — a renamed route would make
		// every pod perpetually unhealthy without failing a deploy.
		assert.Contains(t, deployment, `path: /health`, "liveness probe must hit /health")
		assert.Contains(t, deployment, `path: /readyz`, "readiness probe must hit /readyz")
	})

	t.Run("database url comes from a secret, never inlined", func(t *testing.T) {
		assert.Contains(t, deployment, "secretKeyRef:", "DATABASE_URL must come from a Secret")
		assert.Contains(t, deployment, "DATABASE_URL", "DATABASE_URL env must be declared")
		assert.NotContains(t, deployment, "postgres://user:pass@host:5432/db",
			"the connection string must never be inlined into the pod spec")
		assert.NotContains(t, deployment, "value: \"postgres://",
			"no env var may carry the connection string as a plain value")
		assert.Contains(t, manifest(t, rendered, "Secret"), "stringData:",
			"default render must create the chart-managed Secret")
	})

	t.Run("resources land when configured", func(t *testing.T) {
		withResources := renderChart(t, helm,
			"--values", filepath.Join(chartDir(), "testdata", "overrides.yaml"),
		)
		dep := manifest(t, withResources, "Deployment")
		assert.Contains(t, dep, "requests:", "resource requests must render when configured")
		assert.Contains(t, dep, "limits:", "resource limits must render when configured")
		assert.Contains(t, dep, "cpu:", "CPU request/limit must render when configured")
		assert.Contains(t, dep, "memory:", "memory request/limit must render when configured")
		// The overrides file names an existing Secret: the chart must not
		// synthesize one, and the deployment must reference it by name.
		assert.NotContains(t, withResources, "kind: Secret",
			"an existingSecret must suppress the chart-managed Secret")
		assert.Contains(t, dep, "existing-db-secret", "deployment must reference the existing Secret")
	})

	t.Run("serviceMonitor renders when enabled", func(t *testing.T) {
		withMonitor := renderChart(t, helm,
			"--values", filepath.Join(chartDir(), "testdata", "overrides.yaml"),
		)
		sm := manifest(t, withMonitor, "ServiceMonitor")
		assert.Contains(t, sm, "path: /metrics", "ServiceMonitor must scrape /metrics")
		assert.Contains(t, sm, "interval: 15s", "ServiceMonitor must carry the configured interval")
	})

}

// manifest extracts the first block for the given Kind from a rendered
// chart output (manifests are separated by "---" lines).
func manifest(t *testing.T, rendered, kind string) string {
	t.Helper()
	for _, block := range strings.Split(rendered, "\n---\n") {
		if strings.Contains(block, "kind: "+kind) {
			return block
		}
	}
	t.Fatalf("rendered output contains no %s manifest", kind)
	return ""
}

// TestHelmSkipNotice pins the wording that makes the helm-less skip visible
// (issue #1074). It guards the copy in three places at once: the skip message,
// the REQUIRE_HELM_CHART_TESTS failure message, and the notice
// scripts/check_helm_chart_tests.sh repeats to make the gap visible in the
// default make output, where the test binary's own output does not survive. A
// regression here silently restores the "green run looks like chart coverage"
// problem the notice exists to prevent.
func TestHelmSkipNotice(t *testing.T) {
	if _, err := exec.LookPath("helm"); err == nil {
		t.Skip("helm is installed, so there is no skip notice to assert")
	}

	// The notice is what the plain, non-verbose run prints, so it must hold
	// up with the escape hatch unset. t.Setenv also fails the test if it
	// leaks into the suite's real environment.
	t.Setenv(requireHelmEnv, "")

	notice := helmSkipNotice()
	require.NotEmpty(t, notice, "helm is missing, so the package must announce the skip")
	assert.Contains(t, notice, requireHelmEnv,
		"the notice must name the variable that turns the skip into a failure")
	assert.Contains(t, notice, helmInstallHint,
		"the notice must quote the install hint, so it stays in step with the skip message")
	assert.NotContains(t, strings.TrimSpace(notice), "\n",
		"the notice is printed once per run and has to stay a single line")
}

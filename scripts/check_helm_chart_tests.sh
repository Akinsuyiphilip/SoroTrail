#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# check_helm_chart_tests.sh — announce that the Helm chart tests will skip.
#
# deploy/helm/sorotrail/render_test.go shells out to `helm template` and skips
# every test when the binary is not on PATH. A skipped test prints nothing in
# the default `go test` output, and the go command discards a passing package's
# output altogether, so `make test` reports success for a chart suite that never
# rendered anything. A contributor then adds a configuration variable, sees a
# green run, and the chart job fails because the chart cannot set it.
#
# The notice has to come from here rather than from the test binary: this script
# is not a test, so its output is ordinary make output and survives the run.
#
# The skip stays a skip. Requiring Helm would break the suite for every
# contributor who does not have it, so this script only ever warns — the one way
# it fails is when REQUIRE_HELM_CHART_TESTS is set and Helm is missing, which lets
# a job that installs Helm assert the chart tests really ran instead of trusting
# the job configuration.
#
# Usage:
#   scripts/check_helm_chart_tests.sh
#
# Exit code: always 0, except when REQUIRE_HELM_CHART_TESTS is set and Helm is
# missing, with the install command spelled out.
# ---------------------------------------------------------------------------
set -euo pipefail

# The version CI's helm job pins via azure/setup-helm. Keep in sync with
# .github/workflows/ci.yml and the wording in render_test.go.
pinned_helm_version="v3.15.0"
install_hint="install Helm ${pinned_helm_version} (https://helm.sh/docs/intro/install/)"

if command -v helm >/dev/null 2>&1; then
    echo "[PASS] helm found on PATH: $(helm version --short 2>/dev/null || echo 'version unknown')"
    echo "       deploy/helm/sorotrail's chart golden and render tests will run."
    exit 0
fi

if [ -n "${REQUIRE_HELM_CHART_TESTS:-}" ]; then
    echo "[FAIL] REQUIRE_HELM_CHART_TESTS is set but helm is not on PATH." >&2
    echo "       $install_hint, or unset REQUIRE_HELM_CHART_TESTS." >&2
    exit 1
fi

echo "NOTICE: helm is not on PATH, so every test in deploy/helm/sorotrail will SKIP." >&2
echo "        A green run here does NOT cover the chart: no manifest is rendered and" >&2
echo "        no golden file is compared, so a chart that cannot set a new value" >&2
echo "        passes locally and fails the chart job. To run the tests, $install_hint." >&2
echo "        To confirm this run skipped them, use make test-verbose, or set" >&2
echo "        REQUIRE_HELM_CHART_TESTS=1 to make the skip a hard failure." >&2
exit 0
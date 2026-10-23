package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sorotrail/sorotrail/internal/testskip"
)

// TestRun covers the three shapes the report can take: nothing missing, a
// dependency missing that the run did not require (a notice), and one the
// run required (a failure).
func TestRun(t *testing.T) {
	const presentEnv = "SOROTRAIL_TESTPREREQS_PRESENT"
	const missingEnv = "SOROTRAIL_TESTPREREQS_MISSING"

	t.Run("a present dependency is silent", func(t *testing.T) {
		t.Setenv(presentEnv, "value")
		var out bytes.Buffer

		code := run(&out, testskip.Tool{Name: "Postgres", EnvVar: presentEnv})

		assert.Equal(t, 0, code)
		assert.Empty(t, out.String(), "a dependency that is there must not produce a notice")
	})

	t.Run("a missing dependency is a notice", func(t *testing.T) {
		t.Setenv(missingEnv, "")
		var out bytes.Buffer

		code := run(&out, testskip.Tool{
			Name:    "Postgres",
			EnvVar:  missingEnv,
			Title:   "the database-backed store tests",
			Reason:  missingEnv + " is not set",
			Cost:    "the store paths did not run",
			Install: "start one and re-run",
		})

		assert.Equal(t, 0, code, "a dependency the run did not require must not fail it")
		assert.Contains(t, out.String(), "SKIPPED")
		assert.Contains(t, out.String(), "the database-backed store tests")
	})

	t.Run("a required dependency fails the run", func(t *testing.T) {
		const requireEnv = "SOROTRAIL_TESTPREREQS_REQUIRE"
		t.Setenv(missingEnv, "")
		t.Setenv(requireEnv, "1")
		var out bytes.Buffer

		code := run(&out, testskip.Tool{
			Name:       "Helm",
			EnvVar:     missingEnv,
			Title:      "the Helm chart golden tests",
			Reason:     missingEnv + " is not set",
			Cost:       "the chart's golden tests did not run",
			Install:    "install Helm and re-run",
			RequireEnv: requireEnv,
		})

		assert.Equal(t, 1, code, "a dependency the run required must fail it")
		assert.Contains(t, out.String(), "Helm is required for this test run")
	})
}

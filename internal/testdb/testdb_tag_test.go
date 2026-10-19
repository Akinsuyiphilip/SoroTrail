//go:build !integration

package testdb

import (
	"go/build"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHarnessRequiresIntegrationTag keeps this directory addressable in the
// unit suite: every other file is behind `//go:build integration`, so without
// this one `go test ./internal/testdb/...` reports "matched no packages".
// The property worth asserting is that the harness stays gated — a plain
// `go test ./...` must not pull in Docker or a Postgres dependency.
func TestHarnessRequiresIntegrationTag(t *testing.T) {
	matched, err := build.Default.MatchFile(".", "testdb.go")
	require.NoError(t, err)
	assert.False(t, matched, "testdb.go must remain behind //go:build integration")
}

//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/store"
	"github.com/sorotrail/sorotrail/internal/testdb"
)

// TestAPIKey_PostgresIntegration tests openStore, apikeyCreate, apikeyList,
// and apikeyRevoke against a real migrated Postgres database.
func TestAPIKey_PostgresIntegration(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping integration test against Postgres")
	}

	_ = testdb.Setup(t, store.Migrate)
	t.Setenv("DATABASE_URL", dbURL)

	ctx := context.Background()
	st, cleanup, err := openStore(ctx)
	require.NoError(t, err)
	defer cleanup()

	// 1. Create API key in real Postgres
	var buf bytes.Buffer
	err = apikeyCreateTo(&buf, []string{"--name", "integration-key"})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "created API key")
	assert.Contains(t, out, "integration-key")

	// 2. List API keys from real Postgres
	buf.Reset()
	err = apikeyListTo(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "integration-key")
	assert.Contains(t, buf.String(), "active")

	// 3. Revoke API key in real Postgres
	keys, err := st.ListAPIKeys(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, keys)
	keyID := keys[0].ID

	buf.Reset()
	err = apikeyRevokeTo(&buf, []string{strconv.FormatInt(keyID, 10)})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), fmt.Sprintf("revoked API key %d", keyID))

	// 4. Assert authentication rejection in Postgres
	keys, err = st.ListAPIKeys(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, keys)
	assert.True(t, keys[0].Revoked(), "key in database must be marked revoked")

	_, err = st.LookupAPIKeyByPrefix(ctx, keys[0].Prefix)
	assert.ErrorIs(t, err, store.ErrNotFound, "revoked key must be rejected by LookupAPIKeyByPrefix")
}

// TestStats_PostgresIntegration tests runStats against a real migrated Postgres database.
func TestStats_PostgresIntegration(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping integration test against Postgres")
	}

	_ = testdb.Setup(t, store.Migrate)
	t.Setenv("DATABASE_URL", dbURL)

	var buf bytes.Buffer
	err := runStatsTo(&buf, nil)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "METRIC")
	assert.Contains(t, out, "VALUE")
	assert.Regexp(t, `total events\s+0`, out)
}

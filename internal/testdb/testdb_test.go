//go:build integration

package testdb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/sorotrail/sorotrail/internal/store"
)

func TestIsDockerUnavailable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "docker socket missing",
			err:  errors.New("could not find a working docker socket"),
			want: true,
		},
		{
			name: "failed client creation",
			err:  errors.New("failed to create docker client: context deadline exceeded"),
			want: true,
		},
		{
			name: "permission denied",
			err:  errors.New("permission denied while trying to connect to the Docker daemon socket"),
			want: true,
		},
		{
			name: "cannot connect to docker daemon",
			err:  errors.New("cannot connect to the Docker daemon at unix:///var/run/docker.sock"),
			want: true,
		},
		{
			name: "wrapped docker error",
			err:  fmt.Errorf("starting postgres: %w", errors.New("cannot connect to the Docker daemon")),
			want: true,
		},
		{
			// Matching is on error text, so an unrelated error that merely
			// mentions the daemon must not be swallowed as "skip".
			name: "unrelated error",
			err:  errors.New("connection refused"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isDockerUnavailable(tt.err))
		})
	}
}

// TestSetupPrefersTestDatabaseURL pins the resolution order documented on
// Setup: when TEST_DATABASE_URL is set, the shared path must win over the
// container path. The migrate callback is the observable seam — setupShared
// hands it the configured URL before doing anything else, while setupContainer
// would hand it a random testcontainers-mapped URL.
func TestSetupPrefersTestDatabaseURL(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; the shared-branch precedence cannot be observed")
	}

	var migratedWith string
	pool := Setup(t, func(dbURL string) error {
		migratedWith = dbURL
		return store.Migrate(dbURL)
	})
	require.NotNil(t, pool)

	assert.Equal(t, url, migratedWith,
		"Setup must pass TEST_DATABASE_URL to migrate rather than starting a container")

	ctx := context.Background()
	var database string
	require.NoError(t, pool.QueryRow(ctx, "SELECT current_database()").Scan(&database))
	assert.NotEmpty(t, database)
}

// TestSetupStartsEphemeralContainer covers setupContainer directly. It clears
// TEST_DATABASE_URL so the container branch is exercised even on a CI runner
// that also provides a shared Postgres. On a host without Docker, Setup skips
// through setupContainer's own skip path and this test reports as skipped.
func TestSetupStartsEphemeralContainer(t *testing.T) {
	t.Setenv("TEST_DATABASE_URL", "")

	pool := Setup(t, store.Migrate)
	require.NotNil(t, pool)

	ctx := context.Background()
	var hasEvents bool
	require.NoError(t, pool.QueryRow(ctx, "SELECT to_regclass('public.events') IS NOT NULL").Scan(&hasEvents))
	assert.True(t, hasEvents, "setupContainer must migrate the ephemeral database before returning")

	// Truncation is unnecessary on the container path because nothing is
	// shared, but the returned pool must still be usable.
	_, err := pool.Exec(ctx, `INSERT INTO watched_contracts (contract_id) VALUES ('C_TESTDB_CONTAINER')`)
	require.NoError(t, err)
}

// TestSetupSkipsWhenDockerUnavailable makes the container runner fail with
// the error shape testcontainers returns on a host with no Docker daemon.
// Setup must report a skipped test rather than fatally failing the integration
// run. The seam is the only way to produce that error on a host that does
// have Docker, where testcontainers otherwise falls back to the real socket.
func TestSetupSkipsWhenDockerUnavailable(t *testing.T) {
	original := runPostgresContainer
	t.Cleanup(func() { runPostgresContainer = original })
	runPostgresContainer = func(context.Context, string, ...testcontainers.ContainerCustomizer) (*tcpostgres.PostgresContainer, error) {
		return nil, errors.New("could not find a working docker socket")
	}
	t.Setenv("TEST_DATABASE_URL", "")

	var returned bool
	t.Run("container start", func(t *testing.T) {
		Setup(t, store.Migrate)
		returned = true
	})

	assert.False(t, returned, "Setup must skip when Docker is unavailable")
}

// TestTruncateAllClearsEveryTable seeds one row into every table the test
// suite writes to, then asserts truncateAll leaves all of them empty. It also
// covers the one deliberate exception: tenants is re-seeded with the default
// tenant so later tests have an admin/wildcard row to work against.
func TestTruncateAllClearsEveryTable(t *testing.T) {
	pool := Setup(t, store.Migrate)
	ctx := context.Background()

	var tenantID int64
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO tenants (name) VALUES ('testdb-truncate') RETURNING id`).Scan(&tenantID))

	var subscriptionID int64
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO subscriptions (url, secret) VALUES ('https://example.test/hook', 'secret') RETURNING id`).Scan(&subscriptionID))

	seeds := []struct {
		table string
		query string
		args  []any
	}{
		{
			table: "events",
			query: `INSERT INTO events (network, id, contract_id, ledger, type, tx_hash)
				VALUES ('default', '0000000000000000042', 'C_TESTDB_TRUNCATE', 42, 'contract', 'hash')`,
		},
		{
			table: "ingestion_state",
			query: `INSERT INTO ingestion_state (network) VALUES ('truncate-test')`,
		},
		{
			table: "audit_state",
			query: `INSERT INTO audit_state (network) VALUES ('truncate-test')`,
		},
		{
			table: "audit_findings",
			query: `INSERT INTO audit_findings (network, from_ledger, to_ledger, expected_count, actual_count)
				VALUES ('truncate-test', 1, 2, 3, 4)`,
		},
		{
			table: "watched_contracts",
			query: `INSERT INTO watched_contracts (contract_id) VALUES ('C_TESTDB_TRUNCATE')`,
		},
		{
			table: "replay_state",
			query: `INSERT INTO replay_state (id) VALUES (1)`,
		},
		{
			table: "dead_letters",
			query: `INSERT INTO dead_letters (event_id, contract_id, ledger, type, error)
				VALUES ('event-truncate', 'C_TESTDB_TRUNCATE', 42, 'contract', 'boom')`,
		},
		{
			table: "api_keys",
			query: `INSERT INTO api_keys (prefix, key_hash) VALUES ('prefix-truncate', 'hash')`,
		},
		{
			table: "delivery_attempts",
			query: `INSERT INTO delivery_attempts (subscription_id, event_id, status)
				VALUES ($1, 'event-truncate', 'success')`,
			args: []any{subscriptionID},
		},
		{
			table: "backfill_state",
			query: `INSERT INTO backfill_state (id, contract_id, from_ledger, to_ledger)
				VALUES (1, 'C_TESTDB_TRUNCATE', 1, 2)`,
		},
		{
			table: "contract_cursors",
			query: `INSERT INTO contract_cursors (contract_id) VALUES ('C_TESTDB_TRUNCATE')`,
		},
		{
			table: "contract_meta",
			query: `INSERT INTO contract_meta (contract_id) VALUES ('C_TESTDB_TRUNCATE')`,
		},
		{
			table: "contract_specs",
			query: `INSERT INTO contract_specs (wasm_hash, contract_id) VALUES ('wasm-truncate', 'C_TESTDB_TRUNCATE')`,
		},
		{
			table: "contract_spec_overrides",
			query: `INSERT INTO contract_spec_overrides (contract_id, spec_json) VALUES ('C_TESTDB_TRUNCATE', '{}'::jsonb)`,
		},
		{
			table: "event_addresses",
			query: `INSERT INTO event_addresses (address, event_id, role) VALUES ('GADDRESS', 'event-truncate', 'topic[0]')`,
		},
		{
			table: "token_balances",
			query: `INSERT INTO token_balances (network, contract_id, address) VALUES ('default', 'C_TESTDB_TRUNCATE', 'GADDRESS')`,
		},
		{
			table: "token_balance_state",
			query: `INSERT INTO token_balance_state (network, contract_id) VALUES ('default', 'C_TESTDB_TRUNCATE')`,
		},
		{
			table: "archive_chunks",
			query: `INSERT INTO archive_chunks (ledger_start, ledger_end) VALUES (1, 100)`,
		},
		{
			table: "tenant_usage",
			query: `INSERT INTO tenant_usage (tenant_id, day) VALUES ($1, '2026-01-01')`,
			args:  []any{tenantID},
		},
		{
			table: "tenant_api_keys",
			query: `INSERT INTO tenant_api_keys (tenant_id, prefix, key_hash)
				VALUES ($1, 'tenant-prefix-truncate', decode('00', 'hex'))`,
			args: []any{tenantID},
		},
		{
			table: "tenant_watched_contracts",
			query: `INSERT INTO tenant_watched_contracts (tenant_id, contract_id)
				VALUES ($1, 'C_TESTDB_TRUNCATE')`,
			args: []any{tenantID},
		},
		{
			table: "tenant_contract_grants",
			query: `INSERT INTO tenant_contract_grants (tenant_id, contract_id)
				VALUES ($1, 'C_TESTDB_TRUNCATE')`,
			args: []any{tenantID},
		},
	}

	for _, seed := range seeds {
		t.Run("seed "+seed.table, func(t *testing.T) {
			_, err := pool.Exec(ctx, seed.query, seed.args...)
			require.NoError(t, err, "seeding %s", seed.table)
		})
	}

	require.NoError(t, truncateAll(ctx, pool))

	// subscriptions was seeded separately because delivery_attempts needs its
	// generated ID, so add it to the tables asserted empty.
	clearedTables := []string{"subscriptions"}
	for _, seed := range seeds {
		clearedTables = append(clearedTables, seed.table)
	}
	for _, table := range clearedTables {
		t.Run("cleared "+table, func(t *testing.T) {
			assert.Zero(t, tableCount(t, ctx, pool, table),
				"%s must be empty after truncateAll", table)
		})
	}

	// tenants is truncated too, but the helper immediately re-seeds the
	// default tenant; only the test's own row should be gone.
	t.Run("cleared tenants", func(t *testing.T) {
		assert.Equal(t, 1, tableCount(t, ctx, pool, "tenants"))

		var name string
		var wildcard, isAdmin, enabled bool
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT name, wildcard, is_admin, enabled FROM tenants`).Scan(&name, &wildcard, &isAdmin, &enabled))
		assert.Equal(t, "default", name)
		assert.True(t, wildcard)
		assert.True(t, isAdmin)
		assert.True(t, enabled)
	})
}

// TestTruncateAllPreservesSchemaAndMigrations asserts truncateAll is a data
// operation only: the migration version stays put and the tables remain. Its
// only DDL is resetting the events partition set — dropping range children a
// previous package left behind and restoring the default partition.
func TestTruncateAllPreservesSchemaAndMigrations(t *testing.T) {
	pool := Setup(t, store.Migrate)
	ctx := context.Background()

	versionBefore, dirtyBefore := schemaMigrationVersion(t, ctx, pool)
	require.False(t, dirtyBefore, "migrations must be clean before the test")

	// Mimic a package that widened the partition span: the range children it
	// leaves behind would overlap the next package's default partition.
	_, err := pool.Exec(ctx, `SELECT ensure_event_partitions(100000, 100100, 10)`)
	require.NoError(t, err)

	before := eventPartitionNames(t, ctx, pool)
	assert.Contains(t, before, "events_default")
	assert.Contains(t, before, "events_100000_100009")

	_, err = pool.Exec(ctx, `INSERT INTO events (network, id, contract_id, ledger, type, tx_hash)
		VALUES ('default', '000000000000100000', 'C_TESTDB_SCHEMA', 100000, 'contract', 'hash')`)
	require.NoError(t, err)

	// The store package's partition-span tests drop every child, including
	// events_default, when they rebuild the partition set. truncateAll must
	// restore the catch-all even when it was already missing.
	_, err = pool.Exec(ctx, `DROP TABLE IF EXISTS events_default`)
	require.NoError(t, err)

	require.NoError(t, truncateAll(ctx, pool))

	versionAfter, dirtyAfter := schemaMigrationVersion(t, ctx, pool)
	assert.Equal(t, versionBefore, versionAfter, "truncation must not move the schema version")
	assert.False(t, dirtyAfter, "truncation must leave migrations clean")

	after := eventPartitionNames(t, ctx, pool)
	assert.Equal(t, []string{"events_default"}, after,
		"truncateAll must drop leftover range partitions and restore the default one")

	var eventsExist bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT to_regclass('public.events') IS NOT NULL`).Scan(&eventsExist))
	assert.True(t, eventsExist, "the events table must survive truncation")

	assert.Zero(t, tableCount(t, ctx, pool, "events"))
	assert.Equal(t, 1, tableCount(t, ctx, pool, "schema_migrations"),
		"schema_migrations must still hold exactly its single version row")

	// The restored catch-all must accept events outside every range child.
	_, err = pool.Exec(ctx, `INSERT INTO events (network, id, contract_id, ledger, type, tx_hash)
		VALUES ('default', '0000000000000000042', 'C_TESTDB_SCHEMA', 42, 'contract', 'hash')`)
	require.NoError(t, err)
	assert.Equal(t, 1, tableCount(t, ctx, pool, "events"))
}

// TestSetupCleanupRunsAfterContextExpiry covers the cleanup registered by
// setupShared. Setup cancels its own setup context on return, so a cleanup
// that reused that context would silently fail to truncate. The marker is
// visible through the parent test's pool only if the subtest's cleanup did
// not run, which makes this a regression test for the background-context
// cleanup.
func TestSetupCleanupRunsAfterContextExpiry(t *testing.T) {
	observer := Setup(t, store.Migrate)
	ctx := context.Background()

	t.Run("subtest with expired context", func(t *testing.T) {
		pool := Setup(t, store.Migrate)
		_, err := pool.Exec(ctx,
			`INSERT INTO watched_contracts (contract_id) VALUES ('C_TESTDB_EXPIRED_CTX')`)
		require.NoError(t, err)

		expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		<-expired.Done()
	})

	assert.Zero(t, tableCountWhere(t, ctx, observer,
		"watched_contracts", "contract_id = 'C_TESTDB_EXPIRED_CTX'"),
		"cleanup must truncate with a live context after the test context expired")
}

func schemaMigrationVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (int64, bool) {
	t.Helper()
	var version int64
	var dirty bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty))
	return version, dirty
}

func eventPartitionNames(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname = 'events'
		ORDER BY c.relname`)
	require.NoError(t, err)
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	return names
}

func tableCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) int {
	t.Helper()
	return tableCountWhere(t, ctx, pool, table, "")
}

func tableCountWhere(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table, where string) int {
	t.Helper()
	query := "SELECT count(*) FROM " + table
	if where != "" {
		query += " WHERE " + where
	}
	var count int
	require.NoError(t, pool.QueryRow(ctx, query).Scan(&count))
	return count
}

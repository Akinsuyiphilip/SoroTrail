//go:build integration

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/store"
	"github.com/sorotrail/sorotrail/internal/testdb"
)

func TestSchemaInspection_PostgresIntegration(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Setup(t, store.Migrate)

	version, dirty, err := queryMigrationState(ctx, pool)
	require.NoError(t, err)
	require.Greater(t, version, 0)
	assert.False(t, dirty)

	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `UPDATE schema_migrations SET dirty = false`); err != nil {
			t.Logf("resetting schema_migrations dirty flag: %v", err)
		}
	})
	tag, err := pool.Exec(ctx, `UPDATE schema_migrations SET dirty = true`)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected())
	dirtyVersion, dirty, err := queryMigrationState(ctx, pool)
	require.NoError(t, err)
	assert.Equal(t, version, dirtyVersion)
	assert.True(t, dirty, "a failed migration must be reported as dirty")
	_, err = pool.Exec(ctx, `UPDATE schema_migrations SET dirty = false`)
	require.NoError(t, err)

	matched, err := querySchemaMatch(version)
	require.NoError(t, err)
	assert.True(t, matched.Matched)
	assert.Empty(t, matched.PendingMigrations)

	drifted, err := querySchemaMatch(version - 1)
	require.NoError(t, err)
	assert.False(t, drifted.Matched, "a database behind the embedded migration set must not match")
	assert.Equal(t, []uint{uint(version)}, drifted.PendingMigrations)

	partitionName := "events_schema_inspect_0_99"
	_, err = pool.Exec(ctx, `CREATE TABLE `+pgx.Identifier{partitionName}.Sanitize()+`
		PARTITION OF events FOR VALUES FROM (0) TO (100)`)
	require.NoError(t, err)
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+pgx.Identifier{partitionName}.Sanitize()); err != nil {
			t.Logf("dropping schema-inspection test partition: %v", err)
		}
	})
	_, err = pool.Exec(ctx, `
		INSERT INTO events (network, id, contract_id, ledger, type, tx_hash)
		VALUES ('default', 'schema-inspect-event', 'contract', 1, 'contract', 'tx')`)
	require.NoError(t, err)

	partitions, err := queryPartitions(ctx, pool)
	require.NoError(t, err)
	require.Len(t, partitions, 2)
	assert.Equal(t, "events_default", partitions[0].Name)
	assert.Equal(t, "events_schema_inspect_0_99", partitions[1].Name)
	assert.Equal(t, schemaPartition{Name: partitionName, From: 0, To: 100, RowCounts: 1}, partitions[1])

	sizes, err := queryTableSizes(ctx, pool)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(sizes), 4, "expected parent table, partitions, and indexes")
	assert.Equal(t, "events (parent)", sizes[0].Name)
	assert.Equal(t, "table", sizes[0].Kind)
	require.Equal(t, "events_default", sizes[1].Name)
	require.Equal(t, "partition", sizes[1].Kind)
	require.Equal(t, partitionName, sizes[2].Name)
	require.Equal(t, "partition", sizes[2].Kind)
	for _, size := range sizes {
		assert.GreaterOrEqual(t, size.Size, int64(0), "size for %s must be plausible", size.Name)
		assert.Equal(t, humanSize(size.Size), size.Human)
	}
	require.Equal(t, "index", sizes[3].Kind, "indexes follow the parent and partitions")
	require.NotEmpty(t, sizes[3].Name)

	report, err := collectSchemaReport(ctx, pool)
	require.NoError(t, err)
	assert.Equal(t, version, report.Migration.Version)
	assert.Equal(t, "clean", report.Migration.Status)
	assert.True(t, report.SchemaMatch.Matched)
	require.Len(t, report.Partitions, 2)
	assert.Equal(t, partitions, report.Partitions)
	assert.Equal(t, sizes, report.TableSizes)

	t.Run("collect reports which section failed", func(t *testing.T) {
		schema := fmt.Sprintf("schema_inspect_error_%d", time.Now().UnixNano())
		quotedSchema := pgx.Identifier{schema}.Sanitize()
		_, err := pool.Exec(ctx, "CREATE SCHEMA "+quotedSchema)
		require.NoError(t, err)
		t.Cleanup(func() {
			if _, err := pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE"); err != nil {
				t.Logf("dropping schema-inspection error-test schema: %v", err)
			}
		})

		config := pool.Config().Copy()
		config.ConnConfig.RuntimeParams["search_path"] = schema
		isolatedPool, err := pgxpool.NewWithConfig(ctx, config)
		require.NoError(t, err)
		t.Cleanup(isolatedPool.Close)

		partial, err := collectSchemaReport(ctx, isolatedPool)
		require.Error(t, err)
		assert.Nil(t, partial)
		assert.Contains(t, err.Error(), "reading migration state")
	})
}

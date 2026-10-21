//go:build integration

package api

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/store"
	"github.com/sorotrail/sorotrail/internal/testdb"
)

// TestEventMatches_ParityWithSQLQueryPath runs the shared parity table against
// a real Postgres: the corpus is seeded once, then every case is asked of both
// implementations — the store's SQL for that filter, and eventMatches over the
// same events — and the two ID sets must be equal.
//
// This is the assertion that makes the duplication in the filter language safe
// to keep. Neither side can be the oracle for the other, so each is judged
// against the same rows, and a predicate that exists in only one of them shows
// up as a concrete set difference: "SQL returned these events, the stream
// matched those". A stream that matches more than the query is the dangerous
// direction — a client filtering a live subscription sees rows it never asked
// for — but the reverse is a bug too, and this fails on both.
//
// Postgres is the reference for topic semantics, as it is the backend the
// documented `@>` containment behaviour belongs to. See topicMatches for the
// one place where the matcher is deliberately narrower than SQL, and why the
// corpus is written so the two still agree case by case.
func TestEventMatches_ParityWithSQLQueryPath(t *testing.T) {
	pool := testdb.Setup(t, store.Migrate)
	st := store.NewPostgres(pool)
	ctx := context.Background()

	corpus := parityCorpus()
	if _, err := st.UpsertEvents(ctx, corpus); err != nil {
		t.Fatalf("seeding parity corpus: %v", err)
	}

	// The unconstrained filter is the control: if the seed did not land, or the
	// corpus is being read through something other than the table it was
	// written to, every case below would compare two empty sets and pass.
	stored, _, err := st.QueryEvents(ctx, parityBase())
	require.NoError(t, err)
	require.Len(t, stored, len(corpus),
		"the seeded corpus must come back unfiltered before filter cases mean anything")

	// eventMatches must also agree with the store's own reading of the rows,
	// not just with its query. A field the ingest path does not persist (or
	// persists differently from the in-memory event — network defaults to
	// "default" on write) would otherwise pass the set comparison by being
	// wrong on both sides of it.
	returned := map[string]store.Event{}
	for _, e := range stored {
		returned[e.ID] = e
	}
	for _, e := range corpus {
		fromDB, ok := returned[e.ID]
		if !ok {
			continue // a case below reports the missing row
		}
		assert.Equal(t, e.Network, fromDB.Network, "network round-trips unchanged for %s", e.ID)
		assert.Equal(t, e.TxIndex, fromDB.TxIndex, "tx_index round-trips unchanged for %s", e.ID)
		assert.Equal(t, e.OpIndex, fromDB.OpIndex, "op_index round-trips unchanged for %s", e.ID)
		assert.Equal(t, e.InSuccessfulCall, fromDB.InSuccessfulCall, "in_successful_call round-trips unchanged for %s", e.ID)
		assert.Equal(t, len(e.Value) > 0, len(fromDB.Value) > 0, "value presence round-trips unchanged for %s", e.ID)
		assert.False(t, fromDB.CreatedAt.IsZero(), "created_at round-trips for %s", e.ID)
		if !e.CreatedAt.Equal(fromDB.CreatedAt) {
			t.Errorf("created_at for %s reads back as %s, not %s: a time filter compared against the "+
				"stored value rather than the in-memory one would disagree with SQL for the wrong reason",
				e.ID, fromDB.CreatedAt, e.CreatedAt)
		}
	}

	for _, c := range parityCases() {
		t.Run(c.name, func(t *testing.T) {
			f := parityFilter(c)

			sqlEvents, _, err := st.QueryEvents(ctx, f)
			require.NoError(t, err)

			sqlIDs := make([]string, 0, len(sqlEvents))
			for _, e := range sqlEvents {
				sqlIDs = append(sqlIDs, e.ID)
			}
			sort.Strings(sqlIDs)

			assert.Equal(t, paritySelected(f, corpus), sqlIDs,
				"the SQL query path and eventMatches disagree on this filter: the stream and GET /events "+
					"answer differently for the same query string")
		})
	}
}

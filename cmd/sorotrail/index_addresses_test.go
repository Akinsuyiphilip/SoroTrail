package main

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/store"
)

func TestRunIndexAddresses_Flags(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		expectError bool
	}{
		{"invalid batch size", []string{"--batch-size", "9999"}, true},
		{"inverted ledger range", []string{"--from", "200", "--to", "100"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runIndexAddresses(tt.args)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

type mockAddressStore struct {
	mu        sync.Mutex
	pages     [][]store.Event
	pageIndex int
	upserted  []store.AddressRef
}

func (m *mockAddressStore) QueryEvents(_ context.Context, filter store.EventFilter) ([]store.Event, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pageIndex >= len(m.pages) {
		return nil, "", nil
	}
	events := m.pages[m.pageIndex]
	m.pageIndex++
	nextCursor := ""
	if m.pageIndex < len(m.pages) {
		nextCursor = "cursor-next"
	}
	return events, nextCursor, nil
}

func (m *mockAddressStore) UpsertAddressRefs(_ context.Context, refs []store.AddressRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upserted = append(m.upserted, refs...)
	return nil
}

// TestAddressIndexer_Run_BuildsIndexOverKnownEvents asserts that addressIndexer.Run
// iterates over events, extracts addresses from topics and values, and persists
// address references into the store.
func TestAddressIndexer_Run_BuildsIndexOverKnownEvents(t *testing.T) {
	const addr1 = "GDQP2KPQGKIHYJGXNUIYOMHARUARCA7DJT5FO2FFOOKY3B2WSQHG4W37"
	const addr2 = "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4"

	ev1 := store.Event{
		ID:     "0000000100-0000000001",
		Ledger: 100,
		Topics: json.RawMessage(`["` + addr1 + `"]`),
		Value:  json.RawMessage(`"` + addr2 + `"`),
	}
	ev2 := store.Event{
		ID:     "0000000200-0000000001",
		Ledger: 200,
		Topics: json.RawMessage(`["transfer", "` + addr1 + `"]`),
		Value:  json.RawMessage(`{}`),
	}

	st := &mockAddressStore{
		pages: [][]store.Event{
			{ev1},
			{ev2},
		},
	}

	indexer := &addressIndexer{
		store:     st,
		batchSize: 10,
		from:      1,
		to:        250,
		log:       &testLogger{},
	}

	ctx := context.Background()
	summary, err := indexer.Run(ctx)

	require.NoError(t, err)
	assert.True(t, summary.completed)
	assert.Equal(t, int64(2), summary.pagesProcessed)
	assert.Equal(t, int64(2), summary.eventsScanned)
	assert.Equal(t, int64(3), summary.refsIndexed, "ev1 has 2 addresses, ev2 has 1 address")
	assert.Equal(t, int64(200), summary.throughLedger)

	st.mu.Lock()
	defer st.mu.Unlock()
	require.Len(t, st.upserted, 3)

	assert.Equal(t, addr1, st.upserted[0].Address)
	assert.Equal(t, ev1.ID, st.upserted[0].EventID)
	assert.Equal(t, "topic[0]", st.upserted[0].Role)

	assert.Equal(t, addr2, st.upserted[1].Address)
	assert.Equal(t, ev1.ID, st.upserted[1].EventID)
	assert.Equal(t, "value", st.upserted[1].Role)

	assert.Equal(t, addr1, st.upserted[2].Address)
	assert.Equal(t, ev2.ID, st.upserted[2].EventID)
	assert.Equal(t, "topic[1]", st.upserted[2].Role)
}

// TestAddressIndexer_Run_Idempotent asserts that running index_addresses.Run twice
// over the same events produces consistent output without errors or data corruption.
func TestAddressIndexer_Run_Idempotent(t *testing.T) {
	const addr = "GDQP2KPQGKIHYJGXNUIYOMHARUARCA7DJT5FO2FFOOKY3B2WSQHG4W37"
	ev := store.Event{
		ID:     "0000000100-0000000001",
		Ledger: 100,
		Topics: json.RawMessage(`["` + addr + `"]`),
	}

	st := &mockAddressStore{
		pages: [][]store.Event{{ev}},
	}

	indexer := &addressIndexer{
		store:     st,
		batchSize: 10,
		log:       &testLogger{},
	}

	s1, err1 := indexer.Run(context.Background())
	require.NoError(t, err1)
	assert.True(t, s1.completed)
	assert.Equal(t, int64(1), s1.refsIndexed)

	// Reset page index for second run over same store
	st.pageIndex = 0
	s2, err2 := indexer.Run(context.Background())
	require.NoError(t, err2)
	assert.True(t, s2.completed)
	assert.Equal(t, int64(1), s2.refsIndexed)
}

// TestAddressIndexer_Run_ContextCancelled asserts that cancelling context stops
// the indexer promptly without corrupting what was committed.
func TestAddressIndexer_Run_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel before run

	st := &mockAddressStore{
		pages: [][]store.Event{
			{{ID: "1", Ledger: 10}},
		},
	}

	indexer := &addressIndexer{
		store: st,
		log:   &testLogger{},
	}

	summary, err := indexer.Run(ctx)
	require.NoError(t, err)
	assert.False(t, summary.completed, "cancelled run must not mark completed")
	assert.Equal(t, int64(0), summary.eventsScanned)
	assert.Empty(t, st.upserted)
}

// TestAddressIndexer_Run_DryRun asserts that dryRun mode processes and counts
// events without writing any address refs to the store.
func TestAddressIndexer_Run_DryRun(t *testing.T) {
	const addr = "GDQP2KPQGKIHYJGXNUIYOMHARUARCA7DJT5FO2FFOOKY3B2WSQHG4W37"
	ev := store.Event{
		ID:     "0000000100-0000000001",
		Ledger: 100,
		Topics: json.RawMessage(`["` + addr + `"]`),
	}

	st := &mockAddressStore{
		pages: [][]store.Event{{ev}},
	}

	indexer := &addressIndexer{
		store:     st,
		batchSize: 10,
		dryRun:    true,
		log:       &testLogger{},
	}

	summary, err := indexer.Run(context.Background())
	require.NoError(t, err)
	assert.True(t, summary.completed)
	assert.Equal(t, int64(1), summary.eventsScanned)
	assert.Equal(t, int64(0), summary.refsIndexed, "dry-run must report 0 refs indexed")
	assert.Empty(t, st.upserted, "dry-run must not write to store")
}

// TestAddressIndexer_Run_EmptyRange asserts that when no events exist in range,
// the indexer cleanly exits with completed == true.
func TestAddressIndexer_Run_EmptyRange(t *testing.T) {
	st := &mockAddressStore{}
	indexer := &addressIndexer{
		store: st,
		log:   &testLogger{},
	}

	summary, err := indexer.Run(context.Background())
	require.NoError(t, err)
	assert.True(t, summary.completed)
	assert.Equal(t, int64(0), summary.eventsScanned)
	assert.Equal(t, int64(0), summary.refsIndexed)
}

// TestAddressIndexer_Run_PostgresIntegration runs against a real PostgreSQL
// instance when TEST_DATABASE_URL is set, and cleanly skips otherwise.
func TestAddressIndexer_Run_PostgresIntegration(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres-backed address index test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	defer pool.Close()

	st := store.NewPostgres(pool, 1000)
	indexer := &addressIndexer{
		store:     st,
		batchSize: 50,
		from:      1,
		to:        1000,
		log:       &testLogger{},
	}

	summary, err := indexer.Run(ctx)
	require.NoError(t, err)
	assert.True(t, summary.completed)
}

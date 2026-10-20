package ingester

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/rpc"
	"github.com/sorotrail/sorotrail/internal/store"
)

// loadtestStore wraps mockStore with the small surface RunLoadTest needs.
type loadtestStore struct {
	mockStore
}

func (s *loadtestStore) LoadTestEventCount(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.events)), nil
}

func (s *loadtestStore) LoadTestState(ctx context.Context) (store.IngestionState, error) {
	return s.GetIngestionState(ctx)
}

func TestSyntheticRPC_DeterministicFromSeed(t *testing.T) {
	makeRPC := func(seed uint64) *SyntheticRPC {
		return NewSyntheticRPC(LoadTestConfig{
			EventsPerLedger: 7,
			Ledgers:         50,
			PageLimit:       100,
			Seed:            seed,
		})
	}

	// Two RPCs built with the same seed must emit identical events.
	a, b := makeRPC(42), makeRPC(42)
	require.Equal(t, a.events, b.events, "same seed must produce identical events")
	require.Equal(t, a.LatestLedger(), b.LatestLedger())

	// The events must be deterministic across constructions: every page of
	// the first ledger must match between the two.
	pageA, err := a.GetEvents(context.Background(), rpc.GetEventsRequest{StartLedger: 2})
	require.NoError(t, err)
	pageB, err := b.GetEvents(context.Background(), rpc.GetEventsRequest{StartLedger: 2})
	require.NoError(t, err)
	require.Equal(t, pageA.Events, pageB.Events)

	// A different seed must produce different event values.
	c := makeRPC(7)
	different := false
	for i := range a.events {
		if string(a.events[i].ValueJSON) != string(c.events[i].ValueJSON) {
			different = true
			break
		}
	}
	assert.True(t, different, "different seeds must produce different events")
}

func TestSyntheticRPC_PaginationIsStable(t *testing.T) {
	r := NewSyntheticRPC(LoadTestConfig{
		EventsPerLedger: 3,
		Ledgers:         5,
		PageLimit:       4,
		Seed:            1,
	})
	expected := r.events

	var cursor string
	var got []rpc.Event
	for {
		resp, err := r.GetEvents(context.Background(), rpc.GetEventsRequest{
			StartLedger: 2,
			Pagination:  &rpc.Pagination{Cursor: cursor, Limit: 4},
		})
		require.NoError(t, err)
		got = append(got, resp.Events...)
		if resp.Cursor == "" {
			break
		}
		cursor = resp.Cursor
	}
	require.Equal(t, expected, got, "paginated walk must yield every event in order")
}

func TestLoadTestArithmetic(t *testing.T) {
	assert.Equal(t, int64(7), maxInt64(7, 3))
	assert.Equal(t, int64(3), maxInt64(2, 3))
	assert.Equal(t, int64(0), maxInt64(-5, 0))

	assert.Equal(t, 2.5, maxFloat(2.5, 1.0))
	assert.Equal(t, 1.0, maxFloat(0.5, 1.0))

	// EventsPerSecond is events divided by elapsed seconds, guarded against
	// a zero duration.
	report := LoadTestReport{Events: 1000, Duration: 2 * time.Second}
	assert.Equal(t, 500.0, float64(report.Events)/report.Duration.Seconds())

	zero := LoadTestReport{Events: 1000, Duration: 0}
	assert.Equal(t, 1000.0, float64(zero.Events)/maxFloat(zero.Duration.Seconds(), 1))
}

func TestRunLoadTest_RunsToCompletion(t *testing.T) {
	st := &loadtestStore{mockStore: *newMockStore()}
	cfg := LoadTestConfig{
		EventsPerLedger: 5,
		Ledgers:         20,
		PageLimit:       50,
		Duration:        10 * time.Second,
		Seed:            9,
	}

	report, err := RunLoadTest(context.Background(), st, &passthroughDecoder{}, cfg)
	require.NoError(t, err)
	assert.Greater(t, report.Events, int64(0), "some events must be ingested")
	assert.Equal(t, int64(cfg.Ledgers)*int64(cfg.EventsPerLedger), report.Events, "all synthetic events must be ingested")
	assert.Greater(t, report.Duration, time.Duration(0))
	assert.Equal(t, report.EventsPerSecond, float64(report.Events)/report.Duration.Seconds())
	assert.GreaterOrEqual(t, report.WriteLatencyP50, time.Duration(0))
	assert.GreaterOrEqual(t, report.WriteLatencyP99, report.WriteLatencyP50)
	assert.Greater(t, report.PeakMemoryBytes, int64(0))
}

func TestRunLoadTest_StopsOnDuration(t *testing.T) {
	st := &loadtestStore{mockStore: *newMockStore()}
	// A slow write makes each cycle exceed the context deadline, so the run
	// stops on cancellation rather than catching up.
	st.upsertDelay = time.Second
	cfg := LoadTestConfig{
		EventsPerLedger: 5,
		Ledgers:         1000,
		PageLimit:       50,
		Duration:        10 * time.Millisecond,
		Seed:            3,
	}

	_, err := RunLoadTest(context.Background(), st, &passthroughDecoder{}, cfg)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

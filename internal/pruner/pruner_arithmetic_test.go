package pruner

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrunerDeletionArithmeticCoverage(t *testing.T) {
	t.Run("age-based and ledger-floor bounds alone and combined", func(t *testing.T) {
		st := newMockStore()
		st.setIngestionState(100)
		now := time.Now()

		// Event 1: old and low ledger
		st.addEvent("e1", 30, now.Add(-48*time.Hour))
		// Event 2: old but high ledger
		st.addEvent("e2", 90, now.Add(-48*time.Hour))
		// Event 3: recent but low ledger
		st.addEvent("e3", 30, now.Add(-30*time.Minute))

		prn := New(st, slog.New(slog.NewTextHandler(nopWriter{}, nil)), Options{
			MinLedger: 50,
			MaxAge:    24 * time.Hour,
			BatchSize: 10,
		})
		require.True(t, prn.Enabled())

		total, err := prn.pruneOnce(context.Background())
		require.NoError(t, err)
		assert.Equal(t, int64(1), total)
	})

	t.Run("disabled pruner deletes nothing", func(t *testing.T) {
		st := newMockStore()
		st.setIngestionState(100)
		st.addEvent("e1", 30, time.Now().Add(-48*time.Hour))

		prn := New(st, slog.New(slog.NewTextHandler(nopWriter{}, nil)), Options{})
		require.False(t, prn.Enabled())

		// The disabled gate lives in Run. pruneOnce is the sweep itself and
		// assumes a caller that has already checked Enabled, so a disabled
		// pruner has to be driven through Run to prove it deletes nothing.
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		require.NoError(t, prn.Run(ctx))
		assert.Equal(t, 1, st.eventCount())
	})
}

func TestPrunerDeletionArithmetic_AgeAndLedgerBounds(t *testing.T) {
	now := time.Now()
	t.Run("ledger floor bound alone", func(t *testing.T) {
		st := newMockStore()
		st.setIngestionState(100)
		st.addEvent("e1", 40, now.Add(-time.Hour))
		st.addEvent("e2", 60, now.Add(-time.Hour))

		prn := New(st, slog.New(slog.NewTextHandler(nopWriter{}, nil)), Options{
			MinLedger: 50,
		})
		total, err := prn.pruneOnce(context.Background())
		require.NoError(t, err)
		assert.Equal(t, int64(1), total)
		assert.Equal(t, 1, st.eventCount())
	})

	t.Run("age bound alone", func(t *testing.T) {
		st := newMockStore()
		st.setIngestionState(100)
		st.addEvent("e1", 40, now.Add(-2*time.Hour))
		st.addEvent(
			"e2",
			45,
			now.Add(-30*time.Minute),
		)

		prn := New(st, slog.New(slog.NewTextHandler(nopWriter{}, nil)), Options{
			MaxAge: time.Hour,
		})
		total, err := prn.pruneOnce(context.Background())
		require.NoError(t, err)
		assert.Equal(t, int64(1), total)
		assert.Equal(t, 1, st.eventCount())
	})

	t.Run("combined bounds and conservative wins", func(t *testing.T) {
		st := newMockStore()
		st.setIngestionState(100)
		// e1: ledger < min (40 < 50) and old (-2h < -1h) -> deleted
		st.addEvent("e1", 40, now.Add(-2*time.Hour))
		// e2: ledger >= min (60 >= 50) but old -> conservative (keep because ledger >= min)
		st.addEvent("e2", 60, now.Add(-2*time.Hour))
		// e3: ledger < min (40 < 50) but recent -> conservative (keep because recent)
		st.addEvent("e3", 40, now.Add(-30*time.Minute))

		prn := New(st, slog.New(slog.NewTextHandler(nopWriter{}, nil)), Options{
			MinLedger: 50,
			MaxAge:    time.Hour,
		})
		total, err := prn.pruneOnce(context.Background())
		require.NoError(t, err)
		assert.Equal(t, int64(1), total)
		assert.Equal(t, 2, st.eventCount())
	})
}

func TestPrunerDeletionArithmetic_BatchingAndDisabled(t *testing.T) {
	t.Run("disabled pruner deletes nothing", func(t *testing.T) {
		st := newMockStore()
		st.setIngestionState(100)
		st.addEvent("e1", 30, time.Now().Add(-48*time.Hour))

		prn := New(st, slog.New(slog.NewTextHandler(nopWriter{}, nil)), Options{})
		require.False(t, prn.Enabled())

		// The disabled gate lives in Run. pruneOnce is the sweep itself and
		// assumes a caller that has already checked Enabled, so a disabled
		// pruner has to be driven through Run to prove it deletes nothing.
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		require.NoError(t, prn.Run(ctx))
		assert.Equal(t, 1, st.eventCount())
	})

	t.Run("batching stops at config size and resumes correctly", func(t *testing.T) {
		st := newMockStore()
		st.setIngestionState(100)
		for i := 0; i < 5; i++ {
			st.addEvent(string(rune('a'+i)), int64(10+i), time.Now().Add(-24*time.Hour))
		}

		prn := New(st, slog.New(slog.NewTextHandler(nopWriter{}, nil)), Options{
			MinLedger: 50,
			BatchSize: 2,
		})

		total, err := prn.pruneOnce(context.Background())
		require.NoError(t, err)
		assert.Equal(t, int64(5), total)
		assert.Equal(t, 0, st.eventCount())
		assert.GreaterOrEqual(t, st.deleteCalls, 3)
	})
}

func TestPrunerDeletionArithmetic_ReportedCountsAndPartialFailure(t *testing.T) {
	t.Run("reported counts match what was removed", func(t *testing.T) {
		st := newMockStore()
		st.setIngestionState(100)
		st.addEvent("e1", 10, time.Now().Add(-24*time.Hour))
		st.addEvent("e2", 20, time.Now().Add(-24*time.Hour))
		st.addEvent(
			"e3",
			30,
			time.Now().Add(-24*time.Hour),
		)

		prn := New(st, slog.New(slog.NewTextHandler(nopWriter{}, nil)), Options{
			MinLedger: 40,
		})
		total, err := prn.pruneOnce(context.Background())
		require.NoError(t, err)
		assert.Equal(t, int64(3), total)
	})

	t.Run("partial failure does not leave half-committed run", func(t *testing.T) {
		st := newMockStore()
		st.setIngestionState(100)
		st.deleteErr = errors.New("simulated delete failure")
		st.addEvent("e1", 10, time.Now().Add(-24*time.Hour))

		prn := New(st, slog.New(slog.NewTextHandler(nopWriter{}, nil)), Options{
			MinLedger: 40,
		})
		total, err := prn.pruneOnce(context.Background())
		assert.Error(t, err)
		assert.Equal(t, int64(0), total)
		assert.Equal(t, 1, st.eventCount())
	})
}

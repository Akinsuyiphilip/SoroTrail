package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/backfill"
	"github.com/sorotrail/sorotrail/internal/horizon"
)

type mockBackfillRunner struct {
	mu       sync.Mutex
	attempts int
	plan     []func(attempt int) (backfill.Summary, error)
}

func (m *mockBackfillRunner) Run(_ context.Context) (backfill.Summary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.attempts
	m.attempts++
	if idx < len(m.plan) {
		return m.plan[idx](idx + 1)
	}
	return backfill.Summary{Completed: true}, nil
}

type testLogger struct {
	mu   sync.Mutex
	logs []string
}

func (l *testLogger) Warn(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logs = append(l.logs, msg)
}

// TestRunBackfillWithRetry_TransientRetriedAndSucceeds asserts that a transient
// failure (e.g. rate limit or connection reset) is retried, the backfill resumes,
// and the operation eventually succeeds with its progress intact.
func TestRunBackfillWithRetry_TransientRetriedAndSucceeds(t *testing.T) {
	runner := &mockBackfillRunner{
		plan: []func(int) (backfill.Summary, error){
			func(int) (backfill.Summary, error) {
				return backfill.Summary{PagesFetched: 1, Inserted: 50}, horizon.ErrRateLimited
			},
			func(int) (backfill.Summary, error) {
				return backfill.Summary{PagesFetched: 2, Inserted: 100, Completed: true}, nil
			},
		},
	}

	origSleep := backfillSleep
	origBackoff := backfillInitialBackoff
	defer func() {
		backfillSleep = origSleep
		backfillInitialBackoff = origBackoff
	}()

	var sleeps []time.Duration
	backfillSleep = func(_ context.Context, d time.Duration) bool {
		sleeps = append(sleeps, d)
		return true
	}
	backfillInitialBackoff = time.Millisecond

	log := &testLogger{}
	sum, err := runBackfillWithRetry(context.Background(), runner, log)

	require.NoError(t, err)
	assert.True(t, sum.Completed)
	assert.Equal(t, int64(100), sum.Inserted)
	assert.Equal(t, 2, runner.attempts, "operation must be retried after transient failure")
	assert.Len(t, sleeps, 1, "exactly one backoff sleep should have been scheduled")
	assert.Len(t, log.logs, 1)
}

// TestRunBackfillWithRetry_PermanentErrorNotRetried asserts that a permanent
// failure is not retried, exiting immediately on the first attempt with an error
// naming the cause and mapped to exit code 1.
func TestRunBackfillWithRetry_PermanentErrorNotRetried(t *testing.T) {
	permanentErr := errors.New("permanent fatal database error: relation does not exist")
	runner := &mockBackfillRunner{
		plan: []func(int) (backfill.Summary, error){
			func(int) (backfill.Summary, error) {
				return backfill.Summary{}, permanentErr
			},
		},
	}

	origSleep := backfillSleep
	defer func() { backfillSleep = origSleep }()
	sleepCalled := false
	backfillSleep = func(_ context.Context, _ time.Duration) bool {
		sleepCalled = true
		return true
	}

	log := &testLogger{}
	sum, err := runBackfillWithRetry(context.Background(), runner, log)

	assert.ErrorIs(t, err, permanentErr)
	assert.False(t, sum.Completed)
	assert.Equal(t, 1, runner.attempts, "permanent error must not be retried")
	assert.False(t, sleepCalled, "must not enter sleep on permanent error")
	assert.Equal(t, 1, exitCode(err), "permanent error maps to exit code 1")
}

// TestRunBackfillWithRetry_BackoffGrowth asserts that backoff grows exponentially
// across successive retry attempts and is clamped at DefaultMaxBackoff.
func TestRunBackfillWithRetry_BackoffGrowth(t *testing.T) {
	const failures = 6
	runner := &mockBackfillRunner{}
	for i := 0; i < failures; i++ {
		runner.plan = append(runner.plan, func(int) (backfill.Summary, error) {
			return backfill.Summary{}, errors.New("connection reset by peer")
		})
	}
	runner.plan = append(runner.plan, func(int) (backfill.Summary, error) {
		return backfill.Summary{Completed: true}, nil
	})

	origSleep := backfillSleep
	origBackoff := backfillInitialBackoff
	defer func() {
		backfillSleep = origSleep
		backfillInitialBackoff = origBackoff
	}()

	var recordedSleeps []time.Duration
	backfillSleep = func(_ context.Context, d time.Duration) bool {
		recordedSleeps = append(recordedSleeps, d)
		return true
	}
	// Use 100ms base backoff to test scaling
	backfillInitialBackoff = 100 * time.Millisecond

	log := &testLogger{}
	sum, err := runBackfillWithRetry(context.Background(), runner, log)

	require.NoError(t, err)
	assert.True(t, sum.Completed)
	assert.Equal(t, failures+1, runner.attempts)
	assert.Len(t, recordedSleeps, failures)

	// Verify delays grow between attempts. Since jitter is applied, sleep is in [backoff/2, backoff]
	// with backoff doubling on each retry: 100ms, 200ms, 400ms, 800ms, 1600ms, 3200ms.
	currentBackoff := 100 * time.Millisecond
	for i, sleep := range recordedSleeps {
		minExpected := currentBackoff / 2
		maxExpected := currentBackoff
		if maxExpected > backfill.DefaultMaxBackoff {
			maxExpected = backfill.DefaultMaxBackoff
		}
		assert.GreaterOrEqual(t, sleep, minExpected, "attempt %d sleep must be >= half backoff", i+1)
		assert.LessOrEqual(t, sleep, maxExpected, "attempt %d sleep must be <= max backoff", i+1)
		currentBackoff *= 2
	}
}

// TestRunBackfillWithRetry_CancellationMidBackoff asserts that cancelling the context
// while sleeping in backoff causes runBackfillWithRetry to exit promptly without making
// any further retry attempt, returning partial progress.
func TestRunBackfillWithRetry_CancellationMidBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	runner := &mockBackfillRunner{
		plan: []func(int) (backfill.Summary, error){
			func(int) (backfill.Summary, error) {
				return backfill.Summary{PagesFetched: 1, Inserted: 25}, horizon.ErrRateLimited
			},
			func(int) (backfill.Summary, error) {
				t.Fatal("subsequent attempt should not be made after cancellation")
				return backfill.Summary{}, nil
			},
		},
	}

	origSleep := backfillSleep
	defer func() { backfillSleep = origSleep }()

	backfillSleep = func(c context.Context, _ time.Duration) bool {
		cancel() // Cancel context during sleep
		return sleepCtx(c, 5*time.Second)
	}

	log := &testLogger{}
	sum, err := runBackfillWithRetry(ctx, runner, log)

	require.NoError(t, err)
	assert.False(t, sum.Completed)
	assert.Equal(t, 1, runner.attempts, "only the initial attempt should have run")
}

// TestRunBackfillWithRetry_CancelledBeforeRun verifies that a pre-cancelled
// context immediately exits without retrying.
func TestRunBackfillWithRetry_CancelledBeforeRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runner := &mockBackfillRunner{
		plan: []func(int) (backfill.Summary, error){
			func(int) (backfill.Summary, error) {
				return backfill.Summary{PagesFetched: 1}, context.Canceled
			},
		},
	}

	log := &testLogger{}
	sum, err := runBackfillWithRetry(ctx, runner, log)
	require.NoError(t, err)
	assert.False(t, sum.Completed)
}

// TestSleepCtx_ReturnsPromptlyOnCancel verifies that sleepCtx returns false promptly
// when context is cancelled mid-backoff, rather than waiting out the full duration.
func TestSleepCtx_ReturnsPromptlyOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	// Ask for 10 seconds of sleep; should return in ~10ms when context cancels.
	ok := sleepCtx(ctx, 10*time.Second)
	elapsed := time.Since(start)

	assert.False(t, ok, "sleepCtx must return false when context is cancelled")
	assert.Less(t, elapsed, 500*time.Millisecond, "sleepCtx must return promptly on cancel")
}

// TestSleepCtx_CompletesWhenTimerFires verifies sleepCtx returns true when timer elapses.
func TestSleepCtx_CompletesWhenTimerFires(t *testing.T) {
	ctx := context.Background()
	ok := sleepCtx(ctx, 5*time.Millisecond)
	assert.True(t, ok, "sleepCtx must return true when duration elapses")
}

// TestSleepCtx_ZeroOrNegativeDuration verifies non-positive durations return immediately.
func TestSleepCtx_ZeroOrNegativeDuration(t *testing.T) {
	assert.True(t, sleepCtx(context.Background(), 0))
	assert.True(t, sleepCtx(context.Background(), -time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.False(t, sleepCtx(ctx, 0))
	assert.False(t, sleepCtx(ctx, -time.Second))
}

// TestRunBackfillWithRetry_ProgressNotLostOrDoubleCounted asserts that retried ranges
// are not double-counted or lost: an earlier batch's committed items remain accounted for
// and the second batch picks up seamlessly.
func TestRunBackfillWithRetry_ProgressNotLostOrDoubleCounted(t *testing.T) {
	runner := &mockBackfillRunner{
		plan: []func(int) (backfill.Summary, error){
			// First run fails after committing 50 events in page 1
			func(int) (backfill.Summary, error) {
				return backfill.Summary{
					PagesFetched: 1,
					Transactions: 50,
					Inserted:     50,
				}, errors.New("timeout reading from horizon")
			},
			// Second run resumes from saved checkpoint and ingests page 2
			func(int) (backfill.Summary, error) {
				return backfill.Summary{
					PagesFetched: 2,
					Transactions: 100,
					Inserted:     100,
					Completed:    true,
					Resumed:      true,
				}, nil
			},
		},
	}

	origSleep := backfillSleep
	origBackoff := backfillInitialBackoff
	defer func() {
		backfillSleep = origSleep
		backfillInitialBackoff = origBackoff
	}()
	backfillSleep = func(_ context.Context, _ time.Duration) bool { return true }
	backfillInitialBackoff = time.Millisecond

	sum, err := runBackfillWithRetry(context.Background(), runner, &testLogger{})
	require.NoError(t, err)
	assert.True(t, sum.Completed)
	assert.True(t, sum.Resumed)
	assert.Equal(t, int64(100), sum.Inserted)
	assert.Equal(t, int64(100), sum.Transactions)
	assert.Equal(t, int64(2), sum.PagesFetched)
}

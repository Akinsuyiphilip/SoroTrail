package replay

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/store"
)

// staleDecoding is what seedEvent stores before a replay: the lossless
// {"unknown": ...} fallback an older decoder emitted. currentDecoding is what
// improvedDecoder() writes in its place. Both are in snapshot() form.
const (
	staleDecoding   = `[{"unknown":{"type":"scvFoo"}}]|{"unknown":{"type":"scvFoo"}}`
	currentDecoding = `[{"symbol":"transfer"}]|{"i128":"1000"}`
)

// decodedEvent is a seedEvent whose decoded columns already hold what the
// current decoder produces, so a replay reports it and rewrites nothing.
func decodedEvent(n int, ledger int64) store.DecodedEvent {
	e := seedEvent(n, ledger)
	e.Topics = json.RawMessage(`[{"symbol":"transfer"}]`)
	e.Value = json.RawMessage(`{"i128":"1000"}`)
	return e
}

// TestReplayBatchAndProgressHandling walks the batch- and progress-handling
// cases issue #698 calls out as one table, so the counts each reports can be
// read side by side against the end state it leaves behind.
func TestReplayBatchAndProgressHandling(t *testing.T) {
	t.Parallel()

	fourEvents := []store.DecodedEvent{
		seedEvent(1, 100), seedEvent(2, 101), seedEvent(3, 102), seedEvent(4, 103),
	}
	fullRange := Options{FromLedger: 1, ToLedger: 1000, BatchSize: 2}

	tests := []struct {
		name    string
		events  []store.DecodedEvent
		decoder staticDecoder
		opts    Options
		// setup prepares the store before the measured run — an earlier
		// replay, or an interrupt armed on a commit. A non-nil return is the
		// context the measured run uses instead of context.Background.
		setup func(t *testing.T, st *fakeStore) context.Context

		wantProcessed int64
		wantChanged   int64
		wantSkipped   int64
		wantFailed    int64
		wantCompleted bool

		// check asserts the end state the counts alone don't capture. before
		// is the snapshot taken after setup and before the measured run.
		check func(t *testing.T, st *fakeStore, before map[string]string)
	}{
		{
			name:          "changed decoding rewrites the row",
			events:        fourEvents[:2],
			decoder:       improvedDecoder(),
			opts:          fullRange,
			wantProcessed: 2,
			wantChanged:   2,
			wantCompleted: true,
			check: func(t *testing.T, st *fakeStore, before map[string]string) {
				require.Equal(t, staleDecoding, before[eventID(1)])
				got := st.snapshot()
				assert.Equal(t, currentDecoding, got[eventID(1)])
				assert.Equal(t, currentDecoding, got[eventID(2)])
			},
		},
		{
			name:          "unchanged decoding is reported and not rewritten",
			events:        []store.DecodedEvent{decodedEvent(1, 100), decodedEvent(2, 101)},
			decoder:       improvedDecoder(),
			opts:          fullRange,
			wantProcessed: 2,
			wantChanged:   0,
			wantCompleted: true,
			check: func(t *testing.T, st *fakeStore, before map[string]string) {
				assert.Equal(t, before, st.snapshot(),
					"a row already holding the current decoding must not be rewritten")
			},
		},
		{
			name:    "second replay over the same range changes nothing",
			events:  fourEvents,
			decoder: improvedDecoder(),
			opts:    fullRange,
			setup: func(t *testing.T, st *fakeStore) context.Context {
				sum, err := newTestReplayer(st, improvedDecoder(), fullRange).Run(context.Background())
				require.NoError(t, err)
				require.EqualValues(t, 4, sum.Changed, "the first replay is the one that rewrites")
				return nil
			},
			wantProcessed: 4,
			wantChanged:   0,
			wantCompleted: true,
			check: func(t *testing.T, st *fakeStore, before map[string]string) {
				assert.Equal(t, before, st.snapshot(),
					"replaying an already-replayed range is a no-op")
			},
		},
		{
			name:          "decode failure is counted and skipped rather than fatal",
			events:        fourEvents[:2],
			decoder:       staticDecoder{out: map[string]string{}}, // every decode fails
			opts:          fullRange,
			wantProcessed: 2,
			wantFailed:    2,
			wantCompleted: true,
			check: func(t *testing.T, st *fakeStore, before map[string]string) {
				assert.Equal(t, before, st.snapshot(),
					"a row that would not decode keeps its stored decoding")
			},
		},
		{
			name:    "per-batch progress bounds the work lost to an interrupt",
			events:  fourEvents,
			decoder: improvedDecoder(),
			opts:    fullRange,
			setup: func(t *testing.T, st *fakeStore) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				// Interrupt once the first batch of two has committed.
				st.onCommit = func() {
					if st.commits == 1 {
						cancel()
					}
				}
				return ctx
			},
			wantProcessed: 2,
			wantChanged:   2,
			wantCompleted: false,
			check: func(t *testing.T, st *fakeStore, before map[string]string) {
				got := st.snapshot()
				assert.Equal(t, currentDecoding, got[eventID(2)], "the committed batch is durable")
				assert.Equal(t, staleDecoding, got[eventID(3)], "the uncommitted batch is untouched")

				state, err := st.GetReplayState(context.Background())
				require.NoError(t, err)
				assert.Equal(t, eventID(2), state.LastEventID,
					"progress stops at the last committed batch, so a re-run loses at most one batch")
				assert.False(t, state.Done())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			st := newFakeStore(tt.events...)
			ctx := context.Background()
			if tt.setup != nil {
				if c := tt.setup(t, st); c != nil {
					ctx = c
				}
			}
			before := st.snapshot()

			sum, err := newTestReplayer(st, tt.decoder, tt.opts).Run(ctx)
			require.NoError(t, err, "an interrupt or an undecodable row is not a run failure")

			assert.EqualValues(t, tt.wantProcessed, sum.Processed, "processed")
			assert.EqualValues(t, tt.wantChanged, sum.Changed, "changed")
			assert.EqualValues(t, tt.wantSkipped, sum.Skipped, "skipped")
			assert.EqualValues(t, tt.wantFailed, sum.Failed, "failed")
			assert.Equal(t, tt.wantCompleted, sum.Completed, "completed")

			if tt.check != nil {
				tt.check(t, st, before)
			}
		})
	}
}

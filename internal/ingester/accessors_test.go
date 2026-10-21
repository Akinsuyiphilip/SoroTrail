package ingester

// Direct tests for the small Ingester accessors that /stats and the startup
// log read, plus the broadcaster attachment. Their behavior is covered
// indirectly by the cycle tests, but a wrong accessor value is user-visible
// even when ingestion itself works, so the mapping from Options to accessor
// is pinned here.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/broadcast"
	"github.com/sorotrail/sorotrail/internal/rpc"
	"github.com/sorotrail/sorotrail/internal/store"
)

func TestOptionAccessors(t *testing.T) {
	tests := []struct {
		name                  string
		opts                  Options
		wantPageLimit         uint
		wantWriteBatchSize    uint
		wantMaxEventsPerCycle uint
		wantNetwork           string
	}{
		{
			name:               "applies documented defaults",
			opts:               Options{},
			wantPageLimit:      1000,
			wantWriteBatchSize: 1000,
			// Zero means the per-cycle cap is disabled, not 1000.
			wantMaxEventsPerCycle: 0,
			// Empty means the store default; callers normalize it.
			wantNetwork: "",
		},
		{
			name: "reports configured values",
			opts: Options{
				PageLimit:         250,
				WriteBatchSize:    25,
				MaxEventsPerCycle: 5000,
				Network:           "testnet",
			},
			wantPageLimit:         250,
			wantWriteBatchSize:    25,
			wantMaxEventsPerCycle: 5000,
			wantNetwork:           "testnet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ing := newTestIngester(&mockRPC{}, newMockStore(), tt.opts)

			assert.Equal(t, tt.wantPageLimit, ing.PageLimit())
			assert.Equal(t, tt.wantWriteBatchSize, ing.WriteBatchSize())
			assert.Equal(t, tt.wantMaxEventsPerCycle, ing.MaxEventsPerCycle())
			assert.Equal(t, tt.wantNetwork, ing.Network())
		})
	}
}

func TestWithBroadcasterPublishesPersistedEvents(t *testing.T) {
	client := &mockRPC{eventsResps: []rpc.GetEventsResponse{{
		Events:       []rpc.Event{rpcEvent("e1", 100)},
		LatestLedger: 500,
	}}}
	st := newMockStore()
	b := broadcast.New(8)
	sub := b.Subscribe(store.EventFilter{Scope: store.WildcardScope()})
	t.Cleanup(sub.Close)

	ing := newTestIngester(client, st, Options{StartLedger: 100, PageLimit: 100})
	require.Same(t, ing, ing.WithBroadcaster(b),
		"WithBroadcaster must return the ingester so callers can chain it")

	_, err := ing.runOnce(context.Background())
	require.NoError(t, err)
	require.Len(t, st.upserted, 1, "the event must be persisted before it is published")

	select {
	case ev := <-sub.Events():
		assert.Equal(t, "e1", ev.ID)
	case <-time.After(2 * time.Second):
		t.Fatal("persisted event was not published to the broadcaster")
	}
}

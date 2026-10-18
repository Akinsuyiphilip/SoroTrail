//go:build integration

package api_test

// GET /events filter combinations exercised end-to-end against a real
// Postgres and the actual HTTP handler (via httptest). Mocks would pass
// these tests but never catch a SQL drift: the column list in
// QueryEvents missing an index the API relies on, the topic containment
// operator receiving a different JSON shape, the cursor narrowing or
// expanding by one event when someone changes the ORDER BY clause.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/api"
	"github.com/sorotrail/sorotrail/internal/rpc"
	"github.com/sorotrail/sorotrail/internal/store"
	"github.com/sorotrail/sorotrail/internal/testdb"
)

// captureLogger buffers the server's log and prints it only when the test
// fails. Discarding it hides the one thing that explains a 500: handlers
// answer with a generic message and log the underlying SQL error, so a
// failing assertion on the status code otherwise tells you nothing.
func captureLogger(t *testing.T) *slog.Logger {
	t.Helper()
	buf := &lockedBuffer{}
	t.Cleanup(func() {
		if t.Failed() {
			if out := buf.String(); out != "" {
				t.Logf("server log:\n%s", out)
			}
		}
	})
	return slog.New(slog.NewTextHandler(buf, nil))
}

// lockedBuffer is written from the httptest server's handler goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

const (
	apiContractA = "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"
	apiContractB = "CBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
)

type healthOnlyRPC struct{}

func (healthOnlyRPC) GetEvents(context.Context, rpc.GetEventsRequest) (rpc.GetEventsResponse, error) {
	return rpc.GetEventsResponse{}, nil
}
func (healthOnlyRPC) GetLatestLedger(context.Context) (rpc.LatestLedger, error) {
	return rpc.LatestLedger{}, nil
}
func (healthOnlyRPC) GetHealth(context.Context) (rpc.Health, error) {
	return rpc.Health{Status: "healthy"}, nil
}
func (healthOnlyRPC) GetLedgerEntries(context.Context, rpc.GetLedgerEntriesRequest) (rpc.GetLedgerEntriesResponse, error) {
	return rpc.GetLedgerEntriesResponse{}, nil
}
func (healthOnlyRPC) SimulateTransaction(context.Context, rpc.SimulateTransactionRequest) (rpc.SimulateTransactionResponse, error) {
	return rpc.SimulateTransactionResponse{}, nil
}

func apiEventID(n int) string { return fmt.Sprintf("%020d-%010d", n, 0) }

// apiSeed builds a deterministic dataset: 10 events split across two
// contracts, event 3 marked diagnostic with a different topic, with
// staggered timestamps to make time-range filters meaningful.
func apiSeed() []store.Event {
	anchor := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	out := make([]store.Event, 0, 10)
	for i := 1; i <= 10; i++ {
		contract := apiContractA
		if i%2 == 0 {
			contract = apiContractB
		}
		e := store.Event{
			ID:               apiEventID(i),
			ContractID:       contract,
			Ledger:           int64(100 + i),
			Type:             "contract",
			TxHash:           "deadbeef",
			InSuccessfulCall: true,
			Topics:           json.RawMessage(`[{"symbol":"transfer"},{"u64":7}]`),
			Value:            json.RawMessage(`{"i128":"1000"}`),
			CreatedAt:        anchor.Add(time.Duration(i) * time.Hour),
		}
		if i == 3 {
			e.Type = "diagnostic"
			e.Topics = json.RawMessage(`[{"symbol":"mint"}]`)
		}
		out = append(out, e)
	}
	return out
}

// fromTimeBound is fixed half-way through the seed so the time-range
// assertion intersects events whose timestamps straddle it.
func fromTimeBound() string {
	return time.Date(2026, 7, 21, 17, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

// healthCheckOnly is the minimum rpc.Client the API needs at
// construction time; only /health uses it.
var _ rpc.Client = healthOnlyRPC{}

// TestListEvents_FilterCombinationsAgainstSeededData is the headline
// coverage that pins every documented filter combination against a
// real SQL filter plan.
func TestListEvents_FilterCombinationsAgainstSeededData(t *testing.T) {
	pool := testdb.Setup(t, store.Migrate)
	st := store.NewPostgres(pool)

	ctx := context.Background()
	if _, err := st.UpsertEvents(ctx, apiSeed()); err != nil {
		t.Fatalf("seeding api events: %v", err)
	}

	log := captureLogger(t)
	srv := httptest.NewServer(api.New(st, healthOnlyRPC{}, log, "test-key").Router())
	t.Cleanup(srv.Close)

	allTen := []string{
		apiEventID(1), apiEventID(2), apiEventID(3), apiEventID(4), apiEventID(5),
		apiEventID(6), apiEventID(7), apiEventID(8), apiEventID(9), apiEventID(10),
	}

	type tcase struct {
		name    string
		path    string
		wantIDs []string
		wantBad bool
	}
	// Event 3 is the deliberate odd one out in apiSeed: type=diagnostic with
	// topics [{"symbol":"mint"}]. Both topic filters below therefore match
	// the other nine, not all ten.
	allButThree := []string{
		apiEventID(1), apiEventID(2), apiEventID(4), apiEventID(5),
		apiEventID(6), apiEventID(7), apiEventID(8), apiEventID(9),
		apiEventID(10),
	}

	cases := []tcase{
		{"no filter", "/events", allTen, false},
		{"by contract A", "/events?contract_id=" + apiContractA,
			[]string{apiEventID(1), apiEventID(3), apiEventID(5), apiEventID(7), apiEventID(9)}, false},
		{"by contract B", "/events?contract_id=" + apiContractB,
			[]string{apiEventID(2), apiEventID(4), apiEventID(6), apiEventID(8), apiEventID(10)}, false},
		{"by ledger range", "/events?from_ledger=104&to_ledger=106",
			[]string{apiEventID(4), apiEventID(5), apiEventID(6)}, false},
		{"by type=diagnostic", "/events?type=diagnostic",
			[]string{apiEventID(3)}, false},
		{"topic match in second position", "/events?topic={\"u64\":7}", allButThree, false},
		{"topic match in first position", "/events?topic={\"symbol\":\"transfer\"}",
			allButThree, false},
		{"intersection: contract + ledger", "/events?contract_id=" + apiContractA + "&from_ledger=104&to_ledger=108",
			[]string{apiEventID(5), apiEventID(7)}, false},
		{"intersection: ledger range + time", "/events?from_ledger=104&to_ledger=106&from_time=" + fromTimeBound(),
			[]string{apiEventID(5), apiEventID(6)}, false},
		{"invalid type rejected", "/events?type=bogus", nil, true},
		{"invalid limit rejected", "/events?limit=99999", nil, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tc.path)
			require.NoError(t, err)
			defer resp.Body.Close()
			if tc.wantBad {
				assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
					"path %q must return 400", tc.path)
				return
			}
			require.Equal(t, http.StatusOK, resp.StatusCode, tc.path)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			var got struct {
				Events []store.Event `json:"events"`
				Cursor string        `json:"cursor"`
			}
			require.NoError(t, json.Unmarshal(body, &got), string(body))
			ids := make([]string, 0, len(got.Events))
			for _, e := range got.Events {
				ids = append(ids, e.ID)
			}
			assert.Equal(t, tc.wantIDs, ids,
				"filter %q returned wrong IDs; raw: %s", tc.path, string(body))
		})
	}
}

// TestContractStats pins GET /contracts/{id}/stats against a real
// Postgres: event_count must come from CountContractEvents, metadata
// must come from the contract_meta cache written by UpsertContractMeta,
// and the 404-on-zero-events rule must hold even when a contract_meta
// row exists (a row without events is still not a contract we expose).
func TestContractStats(t *testing.T) {
	pool := testdb.Setup(t, store.Migrate)
	st := store.NewPostgres(pool)

	ctx := context.Background()
	seed := apiSeed()
	if _, err := st.UpsertEvents(ctx, seed); err != nil {
		t.Fatalf("seeding api events: %v", err)
	}

	// Contract A emits 5 events (odd seeds): 4 "contract" + 1 "diagnostic".
	name, symbol := "Token A", "TKA"
	decimals := 7
	fullMeta := store.ContractMeta{
		ContractID: apiContractA,
		Name:       &name,
		Symbol:     &symbol,
		Decimals:   &decimals,
		IsToken:    true,
		FetchedAt:  time.Now().UTC(),
	}
	// Contract B emits 5 events but is a probed non-token: null metadata.
	negativeMeta := store.ContractMeta{ContractID: apiContractB, IsToken: false}
	// Cached metadata for a contract that has no events at all.
	orphanID := "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	orphanMeta := store.ContractMeta{ContractID: orphanID, IsToken: true, FetchedAt: time.Now().UTC()}
	for _, m := range []store.ContractMeta{fullMeta, negativeMeta, orphanMeta} {
		if err := st.UpsertContractMeta(ctx, m); err != nil {
			t.Fatalf("seeding contract_meta for %s: %v", m.ContractID, err)
		}
	}

	log := captureLogger(t)
	srv := httptest.NewServer(api.New(st, healthOnlyRPC{}, log, "test-key").Router())
	t.Cleanup(srv.Close)

	t.Run("with cached token metadata", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/contracts/" + apiContractA + "/stats")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		var got contractStatsResponse
		require.NoError(t, json.Unmarshal(body, &got), string(body))

		assert.Equal(t, apiContractA, got.ContractID)
		assert.Equal(t, int64(5), got.EventCount)
		require.NotNil(t, got.Name, "name must be returned for a cached token")
		assert.Equal(t, name, *got.Name)
		require.NotNil(t, got.Symbol, "symbol must be returned for a cached token")
		assert.Equal(t, symbol, *got.Symbol)
		require.NotNil(t, got.Decimals, "decimals must be returned for a cached token")
		assert.Equal(t, decimals, *got.Decimals)
	})

	t.Run("without metadata omits fields instead of nulls", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/contracts/" + apiContractB + "/stats")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		// Decode generically so we can tell "field omitted" from "field: null":
		// the API contract is omitempty, and a null would leak into clients
		// that distinguish missing from explicit null.
		var raw map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(body, &raw), string(body))
		for _, field := range []string{"name", "symbol", "decimals"} {
			v, ok := raw[field]
			assert.False(t, ok, "%s must be omitted entirely, got %q", field, string(v))
		}

		var got contractStatsResponse
		require.NoError(t, json.Unmarshal(body, &got), string(body))
		assert.Equal(t, apiContractB, got.ContractID)
		assert.Equal(t, int64(5), got.EventCount)
		assert.Nil(t, got.Name)
		assert.Nil(t, got.Symbol)
		assert.Nil(t, got.Decimals)
	})

	t.Run("no events returns 404 even with cached meta", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/contracts/" + orphanID + "/stats")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("unknown contract returns 404", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/contracts/" + apiContractA[:55] + "X/stats")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("invalid contract id returns 400", func(t *testing.T) {
		for _, id := range []string{"not-a-contract", "X" + apiContractA[1:], apiContractA[:55]} {
			resp, err := http.Get(srv.URL + "/contracts/" + id + "/stats")
			require.NoError(t, err)
			resp.Body.Close()
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "id %q must be rejected", id)
		}
	})
}

// contractStatsResponse mirrors the unexported api.contractStatsResponse
// so the test can assert the exact JSON wire shape.
type contractStatsResponse struct {
	ContractID    string                         `json:"contract_id"`
	Name          *string                        `json:"name,omitempty"`
	Symbol        *string                        `json:"symbol,omitempty"`
	Decimals      *int                           `json:"decimals,omitempty"`
	EventCount    int64                          `json:"event_count"`
	TypeBreakdown []store.ContractEventTypeCount `json:"type_breakdown,omitempty"`
}

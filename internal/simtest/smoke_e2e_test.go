package simtest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/api"
	"github.com/sorotrail/sorotrail/internal/decode"
	"github.com/sorotrail/sorotrail/internal/ingester"
	"github.com/sorotrail/sorotrail/internal/store"
)

// CountEvents implements store.Store on mockStore so API list handlers
// can compute pagination headers and totals without triggering a nil-dereference
// wiring panic.
func (m *mockStore) CountEvents(_ context.Context, f store.EventFilter) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var count int64
	for _, ev := range m.events {
		if f.ContractID != "" && ev.ContractID != f.ContractID {
			continue
		}
		if f.FromLedger > 0 && ev.Ledger < f.FromLedger {
			continue
		}
		if f.ToLedger > 0 && ev.Ledger > f.ToLedger {
			continue
		}
		count++
	}
	return count, nil
}

// smokeEnricher satisfies api.Enricher to simulate spec-driven enrichment
// of events during smoke test execution.
type smokeEnricher struct{}

func (e *smokeEnricher) EnrichEvents(_ context.Context, events []store.Event) []store.EnrichedEvent {
	out := make([]store.EnrichedEvent, len(events))
	for i, ev := range events {
		out[i] = store.EnrichedEvent{
			Event:   ev,
			Decoded: true,
			DecodedEvent: &store.DecodedEventResponse{
				Event: "Transfer",
				Fields: map[string]any{
					"amount": 1000,
					"to":     "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
				},
			},
		}
	}
	return out
}

func (e *smokeEnricher) DecodeStats() store.DecodeStats {
	return store.DecodeStats{Decodes: 1}
}

// errorStore simulates an infrastructure or wiring failure in the persistence layer.
type errorStore struct {
	mockStore
}

func (e *errorStore) QueryEvents(context.Context, store.EventFilter) ([]store.Event, string, error) {
	return nil, "", errors.New("simulated query failure")
}

func (e *errorStore) GetEvent(context.Context, string, store.Scope) (store.Event, error) {
	return store.Event{}, store.ErrNotFound
}

// TestSeededEndToEndSmoke exercises the end-to-end data lifecycle:
// seeding simulated RPC events -> ingesting through ingester engine ->
// persisting into store -> querying, decoding, and streaming via API layer.
func TestSeededEndToEndSmoke(t *testing.T) {
	ctx := context.Background()

	// 1. Setup deterministic virtual environment
	startTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := NewVirtualClock(startTime)
	chain := NewVirtualChain(clock, startTime, 100)
	st := newMockStore()

	targetLedger := uint32(5)
	eventID := "0000000005-0000000001"
	contractID := "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	// 2. Seed simulated RPC with event payload
	seededEvent := BuildEvent(eventID, targetLedger, contractID)
	chain.AddEvents(targetLedger, seededEvent)
	chain.AdvanceTo(targetLedger)

	// 3. Run ingester engine to pull from RPC and persist to store
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ing := ingester.New(
		chain,
		st,
		decode.XDRDecoder{},
		logger,
		ingester.Options{
			PollInterval:     time.Second,
			StartLedger:      2,
			RetentionLedgers: 100,
			PageLimit:        10,
			SweepWindow:      1000,
			Clock:            clock,
		},
	)

	caughtUp, err := ing.RunOnceForTest(ctx)
	require.NoError(t, err, "ingester should succeed without error")
	require.True(t, caughtUp, "ingester should catch up to chain head")

	// Verify persistence in store
	savedEvent, err := st.GetEvent(ctx, eventID, store.Scope{})
	require.NoError(t, err, "event should be present in store")
	require.Equal(t, eventID, savedEvent.ID)
	require.Equal(t, contractID, savedEvent.ContractID)

	// 4. Assemble API server on top of ingested store and RPC
	enricher := &smokeEnricher{}
	server := api.New(st, chain, logger, "smoke-api-key", enricher)
	server.WithAPIKeyAuth(false)
	router := server.Router()

	// Table-driven verification across query representations and wiring edge cases
	tests := []struct {
		name           string
		method         string
		path           string
		expectedStatus int
		validate       func(t *testing.T, resp *httptest.ResponseRecorder)
	}{
		{
			name:           "ListEvents_StoredRepresentation",
			method:         http.MethodGet,
			path:           "/events",
			expectedStatus: http.StatusOK,
			validate: func(t *testing.T, resp *httptest.ResponseRecorder) {
				var result struct {
					Events []store.Event `json:"events"`
				}
				err := json.Unmarshal(resp.Body.Bytes(), &result)
				require.NoError(t, err, "response should be valid JSON")
				require.Len(t, result.Events, 1, "should return seeded event")

				ev := result.Events[0]
				assert.Equal(t, eventID, ev.ID)
				assert.Equal(t, contractID, ev.ContractID)
				assert.Equal(t, int64(targetLedger), ev.Ledger)
				assert.Equal(t, "abc123", ev.TxHash)
			},
		},
		{
			name:           "ListEvents_DecodedAndEnrichedRepresentation",
			method:         http.MethodGet,
			path:           "/events?decoded=true",
			expectedStatus: http.StatusOK,
			validate: func(t *testing.T, resp *httptest.ResponseRecorder) {
				var result struct {
					Events []store.EnrichedEvent `json:"events"`
				}
				err := json.Unmarshal(resp.Body.Bytes(), &result)
				require.NoError(t, err, "response should be valid JSON")
				require.Len(t, result.Events, 1, "should return seeded event")

				ev := result.Events[0]
				assert.Equal(t, eventID, ev.ID)
				assert.Equal(t, contractID, ev.ContractID)
				assert.Equal(t, int64(targetLedger), ev.Ledger)
				assert.True(t, ev.Decoded, "event should be marked decoded")
				require.NotNil(t, ev.DecodedEvent, "decoded event payload must be present")
				assert.Equal(t, "Transfer", ev.DecodedEvent.Event)
			},
		},
		{
			name:           "GetEvent_SingleLookupByID",
			method:         http.MethodGet,
			path:           "/events/" + eventID,
			expectedStatus: http.StatusOK,
			validate: func(t *testing.T, resp *httptest.ResponseRecorder) {
				var ev store.Event
				err := json.Unmarshal(resp.Body.Bytes(), &ev)
				require.NoError(t, err, "response should be valid JSON")
				assert.Equal(t, eventID, ev.ID)
				assert.Equal(t, contractID, ev.ContractID)
				assert.Equal(t, int64(targetLedger), ev.Ledger)
			},
		},
		{
			name:           "GetEvent_DecodedRepresentation",
			method:         http.MethodGet,
			path:           "/events/" + eventID + "?decoded=true",
			expectedStatus: http.StatusOK,
			validate: func(t *testing.T, resp *httptest.ResponseRecorder) {
				var ev store.EnrichedEvent
				err := json.Unmarshal(resp.Body.Bytes(), &ev)
				require.NoError(t, err, "response should be valid JSON")
				assert.Equal(t, eventID, ev.ID)
				assert.True(t, ev.Decoded)
				require.NotNil(t, ev.DecodedEvent)
				assert.Equal(t, "Transfer", ev.DecodedEvent.Event)
			},
		},
		{
			name:           "FilterEvents_ContractMatching",
			method:         http.MethodGet,
			path:           "/events?contract_id=" + contractID,
			expectedStatus: http.StatusOK,
			validate: func(t *testing.T, resp *httptest.ResponseRecorder) {
				var result struct {
					Events []store.Event `json:"events"`
				}
				err := json.Unmarshal(resp.Body.Bytes(), &result)
				require.NoError(t, err)
				assert.Len(t, result.Events, 1)
			},
		},
		{
			name:           "FilterEvents_UnmatchedContractYieldsEmpty",
			method:         http.MethodGet,
			path:           "/events?contract_id=CBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
			expectedStatus: http.StatusOK,
			validate: func(t *testing.T, resp *httptest.ResponseRecorder) {
				var result struct {
					Events []store.Event `json:"events"`
				}
				err := json.Unmarshal(resp.Body.Bytes(), &result)
				require.NoError(t, err)
				assert.Empty(t, result.Events)
			},
		},
		{
			name:           "ContractExportNDJSON_RepresentationArrivesIntact",
			method:         http.MethodGet,
			path:           "/contracts/" + contractID + "/export?format=ndjson&from_ledger=1&to_ledger=10",
			expectedStatus: http.StatusOK,
			validate: func(t *testing.T, resp *httptest.ResponseRecorder) {
				body := resp.Body.String()
				assert.Contains(t, body, eventID)
				assert.Contains(t, body, contractID)
			},
		},
		{
			name:           "ContractExportCSV_RepresentationIncludesHeaderAndRow",
			method:         http.MethodGet,
			path:           "/contracts/" + contractID + "/export?format=csv&from_ledger=1&to_ledger=10",
			expectedStatus: http.StatusOK,
			validate: func(t *testing.T, resp *httptest.ResponseRecorder) {
				body := resp.Body.String()
				lines := strings.Split(strings.TrimSpace(body), "\n")
				require.GreaterOrEqual(t, len(lines), 2, "csv export must contain header and data row")
				assert.Contains(t, lines[0], "id", "header should include id column")
				assert.Contains(t, lines[1], eventID, "data row should include seeded event id")
				assert.Contains(t, lines[1], "abc123", "data row should include tx hash")
			},
		},
		{
			name:           "EventsCSV_EndpointReturnsCSVData",
			method:         http.MethodGet,
			path:           "/events.csv",
			expectedStatus: http.StatusOK,
			validate: func(t *testing.T, resp *httptest.ResponseRecorder) {
				body := resp.Body.String()
				lines := strings.Split(strings.TrimSpace(body), "\n")
				require.GreaterOrEqual(t, len(lines), 2, "events.csv must contain header and data row")
				assert.Contains(t, lines[0], "id")
				assert.Contains(t, lines[1], eventID)
			},
		},
		{
			name:           "GetEvent_NotFoundFailsLoudly",
			method:         http.MethodGet,
			path:           "/events/9999999999-9999999999",
			expectedStatus: http.StatusNotFound,
			validate: func(t *testing.T, resp *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusNotFound, resp.Code)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if tc.expectedStatus != rec.Code {
				t.Logf("[%s] expected %d got %d, body: %s", tc.name, tc.expectedStatus, rec.Code, rec.Body.String())
			}
			require.Equal(t, tc.expectedStatus, rec.Code, "unexpected http status code")
			if tc.validate != nil {
				tc.validate(t, rec)
			}
		})
	}

	// 5. Test wiring failure regression: store failure fails loudly
	t.Run("WiringFailure_FailsLoudlyOnStoreError", func(t *testing.T) {
		errSt := &errorStore{}
		brokenServer := api.New(errSt, chain, logger, "smoke-api-key")
		brokenServer.WithAPIKeyAuth(false)
		brokenRouter := brokenServer.Router()

		req := httptest.NewRequest(http.MethodGet, "/events", nil)
		rec := httptest.NewRecorder()
		brokenRouter.ServeHTTP(rec, req)

		// A broken store must return a 5xx error, never a quiet empty 200 OK
		assert.Equal(t, http.StatusInternalServerError, rec.Code, "store wiring failure must produce 500 status")
	})
}

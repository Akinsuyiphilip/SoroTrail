package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/store"
)

// TestFilterFromQuery_ParityAndBounds exercises FilterFromQuery with a table:
// single parameters, combinations, defaults, bounds, and malformed inputs.
func TestFilterFromQuery_ParityAndBounds(t *testing.T) {
	validC1 := validAddr('C')
	validC2 := otherContractID(t)

	tests := []struct {
		name        string
		query       url.Values
		wantErr     bool
		errContains string
		assertFn    func(t *testing.T, f store.EventFilter)
	}{
		{
			name:    "empty query uses defaults",
			query:   url.Values{},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				assert.Equal(t, 50, f.Limit, "default limit should be applied")
				assert.Empty(t, f.ContractID)
				assert.Empty(t, f.Types)
				assert.Zero(t, f.FromLedger)
				assert.Zero(t, f.ToLedger)
			},
		},
		{
			name:    "from_ledger alone",
			query:   url.Values{"from_ledger": []string{"100"}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				assert.Equal(t, int64(100), f.FromLedger)
			},
		},
		{
			name:    "to_ledger alone",
			query:   url.Values{"to_ledger": []string{"200"}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				assert.Equal(t, int64(200), f.ToLedger)
			},
		},
		{
			name: "from_ledger and to_ledger valid range",
			query: url.Values{
				"from_ledger": []string{"100"},
				"to_ledger":   []string{"200"},
			},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				assert.Equal(t, int64(100), f.FromLedger)
				assert.Equal(t, int64(200), f.ToLedger)
			},
		},
		{
			name: "from_ledger and to_ledger inverted bounds",
			query: url.Values{
				"from_ledger": []string{"200"},
				"to_ledger":   []string{"100"},
			},
			wantErr:     true,
			errContains: "from_ledger 200 is after to_ledger 100",
		},
		{
			name:    "from_time alone",
			query:   url.Values{"from_time": []string{"2026-01-01T00:00:00Z"}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				expected, _ := time.Parse(time.RFC3339, "2026-01-01T00:00:00Z")
				assert.Equal(t, expected, f.FromTime)
			},
		},
		{
			name:    "to_time alone",
			query:   url.Values{"to_time": []string{"2026-01-02T00:00:00Z"}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				expected, _ := time.Parse(time.RFC3339, "2026-01-02T00:00:00Z")
				assert.Equal(t, expected, f.ToTime)
			},
		},
		{
			name: "from_time and to_time inverted bounds",
			query: url.Values{
				"from_time": []string{"2026-01-02T00:00:00Z"},
				"to_time":   []string{"2026-01-01T00:00:00Z"},
			},
			wantErr:     true,
			errContains: "from_time",
		},
		{
			name:    "type single valid",
			query:   url.Values{"type": []string{"contract"}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				assert.Equal(t, []string{"contract"}, f.Types)
			},
		},
		{
			name:    "type multiple valid comma-separated",
			query:   url.Values{"type": []string{"contract,system"}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				assert.Equal(t, []string{"contract", "system"}, f.Types)
			},
		},
		{
			name:        "type invalid rejected",
			query:       url.Values{"type": []string{"invalid_event_type"}},
			wantErr:     true,
			errContains: "invalid",
		},
		{
			name:    "single contract_id",
			query:   url.Values{"contract_id": []string{validC1}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				assert.Equal(t, validC1, f.ContractID)
				assert.Empty(t, f.ContractIDs)
			},
		},
		{
			name:    "multiple contract_ids comma-separated",
			query:   url.Values{"contract_id": []string{validC1 + "," + validC2}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				assert.Empty(t, f.ContractID)
				assert.Equal(t, []string{validC1, validC2}, f.ContractIDs)
			},
		},
		{
			name:        "contract_id invalid strkey",
			query:       url.Values{"contract_id": []string{"invalid_strkey"}},
			wantErr:     true,
			errContains: "contract_id",
		},
		{
			name:    "contract_id_prefix alone",
			query:   url.Values{"contract_id_prefix": []string{"CAAA"}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				assert.Equal(t, "CAAA", f.ContractIDPrefix)
			},
		},
		{
			name:    "topic single JSON value",
			query:   url.Values{"topic": []string{`"transfer"`}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				assert.Equal(t, json.RawMessage(`"transfer"`), f.Topic)
			},
		},
		{
			name: "positional topics topic0..topic3",
			query: url.Values{
				"topic0": []string{`"transfer"`},
				"topic1": []string{`"from_addr"`},
				"topic2": []string{`"to_addr"`},
				"topic3": []string{`"100"`},
			},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				assert.Equal(t, json.RawMessage(`"transfer"`), f.Topic0)
				assert.Equal(t, json.RawMessage(`"from_addr"`), f.Topic1)
				assert.Equal(t, json.RawMessage(`"to_addr"`), f.Topic2)
				assert.Equal(t, json.RawMessage(`"100"`), f.Topic3)
			},
		},
		{
			name:    "topic_contains",
			query:   url.Values{"topic_contains": []string{`"my_topic"`}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				assert.Equal(t, json.RawMessage(`"my_topic"`), f.TopicContains)
			},
		},
		{
			name:    "tx_hash alone",
			query:   url.Values{"tx_hash": []string{"aabbccddeeff00112233"}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				assert.Equal(t, "aabbccddeeff00112233", f.TxHash)
			},
		},
		{
			name:    "tx_index valid",
			query:   url.Values{"tx_index": []string{"2"}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				require.NotNil(t, f.TxIndex)
				assert.Equal(t, int32(2), *f.TxIndex)
			},
		},
		{
			name:        "tx_index negative",
			query:       url.Values{"tx_index": []string{"-1"}},
			wantErr:     true,
			errContains: "tx_index",
		},
		{
			name:    "op_index valid",
			query:   url.Values{"op_index": []string{"0"}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				require.NotNil(t, f.OpIndex)
				assert.Equal(t, int32(0), *f.OpIndex)
			},
		},
		{
			name:        "op_index non-integer",
			query:       url.Values{"op_index": []string{"abc"}},
			wantErr:     true,
			errContains: "op_index",
		},
		{
			name:    "in_successful_call true",
			query:   url.Values{"in_successful_call": []string{"true"}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				require.NotNil(t, f.InSuccessfulCall)
				assert.True(t, *f.InSuccessfulCall)
			},
		},
		{
			name:    "in_successful_call false",
			query:   url.Values{"in_successful_call": []string{"false"}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				require.NotNil(t, f.InSuccessfulCall)
				assert.False(t, *f.InSuccessfulCall)
			},
		},
		{
			name:        "in_successful_call invalid",
			query:       url.Values{"in_successful_call": []string{"maybe"}},
			wantErr:     true,
			errContains: "in_successful_call",
		},
		{
			name:    "has_value true",
			query:   url.Values{"has_value": []string{"true"}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				require.NotNil(t, f.HasValue)
				assert.True(t, *f.HasValue)
			},
		},
		{
			name:    "has_value false",
			query:   url.Values{"has_value": []string{"false"}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				require.NotNil(t, f.HasValue)
				assert.False(t, *f.HasValue)
			},
		},
		{
			name:        "has_value invalid",
			query:       url.Values{"has_value": []string{"invalid"}},
			wantErr:     true,
			errContains: "has_value",
		},
		{
			name:    "limit valid within range",
			query:   url.Values{"limit": []string{"150"}},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				assert.Equal(t, 150, f.Limit)
			},
		},
		{
			name:        "limit out of bounds zero",
			query:       url.Values{"limit": []string{"0"}},
			wantErr:     true,
			errContains: "limit",
		},
		{
			name:        "limit out of bounds above max",
			query:       url.Values{"limit": []string{"501"}},
			wantErr:     true,
			errContains: "limit",
		},
		{
			name: "order and order_by",
			query: url.Values{
				"order":    []string{"desc"},
				"order_by": []string{"ledger"},
			},
			wantErr: false,
			assertFn: func(t *testing.T, f store.EventFilter) {
				assert.Equal(t, "desc", f.Order)
				assert.Equal(t, "ledger", f.OrderBy)
			},
		},
		{
			name:        "malformed from_ledger naming parameter",
			query:       url.Values{"from_ledger": []string{"abc"}},
			wantErr:     true,
			errContains: "from_ledger",
		},
		{
			name:        "malformed to_ledger naming parameter",
			query:       url.Values{"to_ledger": []string{"def"}},
			wantErr:     true,
			errContains: "to_ledger",
		},
		{
			name:        "malformed from_time naming parameter",
			query:       url.Values{"from_time": []string{"invalid-date"}},
			wantErr:     true,
			errContains: "from_time",
		},
		{
			name:        "malformed to_time naming parameter",
			query:       url.Values{"to_time": []string{"invalid-date"}},
			wantErr:     true,
			errContains: "to_time",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/events?"+tt.query.Encode(), nil)
			req = req.WithContext(WithPrincipal(req.Context(), Principal{Scope: store.WildcardScope(), Untenanted: true}))
			filter, err := FilterFromQuery(req)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
				if tt.assertFn != nil {
					tt.assertFn(t, filter)
				}
			}
		})
	}
}

// TestHandleAddressSummary exercises handleAddressSummary across:
// - an address with activity (returns 200 with fixture numbers)
// - an address with no activity (returns 404 per store.ErrNotFound)
// - malformed addresses (returns 400 naming format requirement)
// - store error (returns 500 without leaking raw error)
func TestHandleAddressSummary(t *testing.T) {
	const activeAddr = "GDQP2KPQGKIHYJGXNUIYOMHARUARCA7DJT5FO2FFOOKY3B2WSQHG4W37"
	const emptyAddr = "GAAA2KPQGKIHYJGXNUIYOMHARUARCA7DJT5FO2FFOOKY3B2WSQHG4W37"
	const contractAddr = "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4"

	fixture := store.AddressSummary{
		Address:           activeAddr,
		FirstSeenLedger:   100,
		LastSeenLedger:    500,
		EventCount:        42,
		DistinctContracts: []string{"CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4"},
	}

	t.Run("address with activity returns 200 and matches fixture", func(t *testing.T) {
		st := &stubStore{
			addressSummary: fixture,
		}
		srv := newTestServer(st, nil)

		resp, body := doGet(t, srv, "/addresses/"+activeAddr+"/summary")
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))

		var got store.AddressSummary
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, fixture.Address, got.Address)
		assert.Equal(t, fixture.FirstSeenLedger, got.FirstSeenLedger)
		assert.Equal(t, fixture.LastSeenLedger, got.LastSeenLedger)
		assert.Equal(t, fixture.EventCount, got.EventCount)
		assert.Equal(t, fixture.DistinctContracts, got.DistinctContracts)
	})

	t.Run("contract address format is accepted", func(t *testing.T) {
		contractFixture := store.AddressSummary{
			Address:           contractAddr,
			FirstSeenLedger:   200,
			LastSeenLedger:    300,
			EventCount:        10,
			DistinctContracts: []string{contractAddr},
		}
		st := &stubStore{addressSummary: contractFixture}
		srv := newTestServer(st, nil)

		resp, body := doGet(t, srv, "/addresses/"+contractAddr+"/summary")
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var got store.AddressSummary
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, contractAddr, got.Address)
	})

	t.Run("address with no activity returns 404", func(t *testing.T) {
		st := &stubStore{
			addressSummaryErr: store.ErrNotFound,
		}
		srv := newTestServer(st, nil)

		resp, body := doGet(t, srv, "/addresses/"+emptyAddr+"/summary")
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		assert.Contains(t, string(body), "not found")
	})

	t.Run("malformed address returns 400", func(t *testing.T) {
		st := &stubStore{}
		srv := newTestServer(st, nil)

		badAddresses := []string{
			"invalid-address",
			"BDQP2KPQGKIHYJGXNUIYOMHARUARCA7DJT5FO2FFOOKY3B2WSQHG4W37",   // wrong prefix
			"GDQP2KPQGKIHYJGXNUIYOMHARUARCA7DJT5FO2FFOOKY3B2WSQHG4W",     // too short
			"GDQP2KPQGKIHYJGXNUIYOMHARUARCA7DJT5FO2FFOOKY3B2WSQHG4W3799", // too long
			"GDQP2KPQGKIHYJGXNUIYOMHARUARCA7DJT5FO2FFOOKY3B2WSQHG4W89",   // illegal chars
		}

		for _, addr := range badAddresses {
			resp, body := doGet(t, srv, "/addresses/"+addr+"/summary")
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "address %q should be rejected", addr)
			assert.Contains(t, string(body), "invalid address")
		}
	})

	t.Run("store error propagates as 500 without leaking raw error text", func(t *testing.T) {
		rawErr := "pgx connection reset: connection terminated unexpectedly by peer"
		st := &stubStore{
			addressSummaryErr: errors.New(rawErr),
		}
		srv := newTestServer(st, nil)

		resp, body := doGet(t, srv, "/addresses/"+activeAddr+"/summary")
		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		assert.Contains(t, string(body), "loading address summary failed")
		assert.NotContains(t, string(body), rawErr, "internal error text must not leak to caller")
	})
}

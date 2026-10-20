package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/store"
)

// requireContractStatsOK performs the request and asserts the parts every
// successful stats response shares: 200 and the cacheability class the handler
// promises. The JSON is decoded into the handler's own response type so the
// field names are pinned by the compiler as well as the tags.
func requireContractStatsOK(t *testing.T, st *stubStore) contractStatsResponse {
	t.Helper()
	resp, body := doGet(t, newTestServer(st, nil), "/contracts/"+testContract+"/stats")
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
	assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))

	var got contractStatsResponse
	require.NoError(t, json.Unmarshal(body, &got))
	return got
}

func TestContractStats_RejectsMalformedID(t *testing.T) {
	resp, body := doGet(t, newTestServer(&stubStore{}, nil), "/contracts/not-a-contract/stats")

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, string(body), "invalid contract ID")
}

func TestContractStats_ZeroEventsIsNotFound(t *testing.T) {
	st := &stubStore{contractEventCount: 0}

	resp, body := doGet(t, newTestServer(st, nil), "/contracts/"+testContract+"/stats")

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Contains(t, string(body), "not found")
}

func TestContractStats_CountErrorIsInternal(t *testing.T) {
	st := &stubStore{contractEventCountErr: errors.New("db timeout")}

	resp, body := doGet(t, newTestServer(st, nil), "/contracts/"+testContract+"/stats")

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assert.Contains(t, string(body), "loading contract stats failed")
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
}

func TestContractStats_BasicResponseAndCacheControl(t *testing.T) {
	st := &stubStore{contractEventCount: 12}

	got := requireContractStatsOK(t, st)

	assert.Equal(t, testContract, got.ContractID)
	assert.Equal(t, int64(12), got.EventCount)
	assert.Nil(t, got.Name)
	assert.Nil(t, got.Symbol)
	assert.Nil(t, got.Decimals)
	assert.Empty(t, got.TypeBreakdown)
}

func TestContractStats_Metadata(t *testing.T) {
	name, symbol, decimals := "USD Coin", "USDC", 6
	full := &store.ContractMeta{
		ContractID: testContract,
		Name:       &name,
		Symbol:     &symbol,
		Decimals:   &decimals,
		IsToken:    true,
	}

	tests := []struct {
		name     string
		meta     *store.ContractMeta
		metaErr  error
		wantName *string
		wantSym  *string
		wantDec  *int
	}{
		{name: "present", meta: full, wantName: &name, wantSym: &symbol, wantDec: &decimals},
		{name: "absent", meta: nil},
		{name: "error is swallowed", metaErr: errors.New("meta lookup failed")},
		// HasMetadata requires IsToken and all three fields; a partial row
		// must not produce a response with some metadata attached.
		{name: "present but incomplete", meta: &store.ContractMeta{Name: &name, IsToken: true}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := &stubStore{
				contractEventCount: 5,
				contractMeta:       tc.meta,
				contractMetaErr:    tc.metaErr,
			}

			got := requireContractStatsOK(t, st)

			assert.Equal(t, int64(5), got.EventCount, "metadata must never replace the required stats")
			if tc.wantName == nil {
				assert.Nil(t, got.Name)
			} else {
				require.NotNil(t, got.Name)
				assert.Equal(t, *tc.wantName, *got.Name)
			}
			if tc.wantSym == nil {
				assert.Nil(t, got.Symbol)
			} else {
				require.NotNil(t, got.Symbol)
				assert.Equal(t, *tc.wantSym, *got.Symbol)
			}
			if tc.wantDec == nil {
				assert.Nil(t, got.Decimals)
			} else {
				require.NotNil(t, got.Decimals)
				assert.Equal(t, *tc.wantDec, *got.Decimals)
			}
		})
	}
}

func TestContractStats_TypeBreakdown(t *testing.T) {
	types := []store.ContractEventTypeCount{
		{Type: "transfer", Count: 4},
		{Type: "mint", Count: 1},
	}

	t.Run("present", func(t *testing.T) {
		st := &stubStore{contractEventCount: 5, eventTypeCounts: types}

		got := requireContractStatsOK(t, st)

		assert.Equal(t, int64(5), got.EventCount)
		assert.Equal(t, types, got.TypeBreakdown)
	})

	// Both a backend that cannot aggregate (SQLite/ClickHouse) and a real
	// aggregation failure are non-fatal: the endpoint still returns the
	// counts it was asked for.
	for _, aggErr := range []error{store.ErrUnsupported, errors.New("aggregate failed")} {
		t.Run("error is swallowed: "+aggErr.Error(), func(t *testing.T) {
			st := &stubStore{
				contractEventCount: 5,
				eventTypeCounts:    types,
				eventTypeCountsErr: aggErr,
			}

			got := requireContractStatsOK(t, st)

			assert.Equal(t, int64(5), got.EventCount)
			assert.Empty(t, got.TypeBreakdown)
		})
	}
}

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/store"
)

// doDeleteWithAuth performs a DELETE request against the test server with an
// optional API key header.
func doDeleteWithAuth(t *testing.T, s *Server, path, apiKey string) (testResponse, []byte) {
	t.Helper()
	srv := httptest.NewServer(s.Router())
	defer srv.Close()
	req, err := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
	require.NoError(t, err)
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	return testResponse{StatusCode: resp.StatusCode, Header: resp.Header}, body
}

// TestDeleteDeadLetter_Auth verifies that DELETE /dead-letters/{id} enforces
// authentication: missing or invalid keys are rejected, and an unconfigured server
// returns 503 (#1096).
func TestDeleteDeadLetter_Auth(t *testing.T) {
	const validKey = "operator-secret-key"
	st := &stubStore{}
	ts := newTestServerWithKey(st, nil, validKey)

	t.Run("missing API key returns 401", func(t *testing.T) {
		resp, body := doDeleteWithAuth(t, ts, "/dead-letters/42", "")
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		var errResp map[string]string
		require.NoError(t, json.Unmarshal(body, &errResp))
		assert.Contains(t, strings.ToLower(errResp["error"]), "x-api-key")
	})

	t.Run("invalid or under-privileged key returns 401", func(t *testing.T) {
		resp, body := doDeleteWithAuth(t, ts, "/dead-letters/42", "wrong-key")
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		var errResp map[string]string
		require.NoError(t, json.Unmarshal(body, &errResp))
		assert.Contains(t, strings.ToLower(errResp["error"]), "x-api-key")
	})

	t.Run("unconfigured server returns 503", func(t *testing.T) {
		unconfiguredTS := newTestServerWithKey(st, nil, "")
		resp, body := doDeleteWithAuth(t, unconfiguredTS, "/dead-letters/42", validKey)
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		var errResp map[string]string
		require.NoError(t, json.Unmarshal(body, &errResp))
		assert.Contains(t, errResp["error"], "API_KEY")
	})
}

// TestDeleteDeadLetter_Validation verifies ID parsing and parameter validation (#1096).
func TestDeleteDeadLetter_Validation(t *testing.T) {
	const validKey = "operator-key"
	ts := newTestServerWithKey(&stubStore{}, nil, validKey)

	invalidIDs := []struct {
		name string
		path string
	}{
		{"non-numeric string", "/dead-letters/not-a-number"},
		{"zero id", "/dead-letters/0"},
		{"negative id", "/dead-letters/-5"},
		{"float id", "/dead-letters/3.14"},
	}

	for _, tc := range invalidIDs {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doDeleteWithAuth(t, ts, tc.path, validKey)
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
			var errResp map[string]string
			require.NoError(t, json.Unmarshal(body, &errResp))
			assert.Contains(t, errResp["error"], "must be a positive integer")
		})
	}
}

// TestDeleteDeadLetter_StoreError verifies that internal store errors surface
// as 500 without leaking raw store/database error text (#1096).
func TestDeleteDeadLetter_StoreError(t *testing.T) {
	const validKey = "operator-key"
	const rawSecretError = "pq: disk full or relation dead_letters I/O error at sector 0xDEADBEEF"

	st := &stubStore{
		deleteDeadLetterErr: errors.New(rawSecretError),
	}
	ts := newTestServerWithKey(st, nil, validKey)

	resp, body := doDeleteWithAuth(t, ts, "/dead-letters/10", validKey)
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)

	var errResp map[string]string
	require.NoError(t, json.Unmarshal(body, &errResp))
	assert.Equal(t, "deleting dead letter failed", errResp["error"])
	assert.NotContains(t, string(body), rawSecretError, "raw store error text must not be leaked")
}

// TestDeleteDeadLetter_NotFoundAndIdempotency verifies that deleting a nonexistent ID
// returns 404, and calling delete twice on the same ID returns 404 on the second call (#1096).
func TestDeleteDeadLetter_NotFoundAndIdempotency(t *testing.T) {
	const validKey = "operator-key"
	deleted := false
	st := &stubStore{
		deleteDeadLetterFn: func(ctx context.Context, id int64) error {
			if id == 42 && !deleted {
				deleted = true
				return nil
			}
			return store.ErrNotFound
		},
	}
	ts := newTestServerWithKey(st, nil, validKey)

	t.Run("first delete succeeds with 204 and empty body", func(t *testing.T) {
		resp, body := doDeleteWithAuth(t, ts, "/dead-letters/42", validKey)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		assert.Empty(t, body, "successful delete must return empty body")
		assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	})

	t.Run("second delete of same ID returns 404", func(t *testing.T) {
		resp, body := doDeleteWithAuth(t, ts, "/dead-letters/42", validKey)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		var errResp map[string]string
		require.NoError(t, json.Unmarshal(body, &errResp))
		assert.Contains(t, errResp["error"], "dead letter 42 not found")
	})

	t.Run("delete nonexistent ID returns 404", func(t *testing.T) {
		resp, body := doDeleteWithAuth(t, ts, "/dead-letters/999", validKey)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		var errResp map[string]string
		require.NoError(t, json.Unmarshal(body, &errResp))
		assert.Contains(t, errResp["error"], "dead letter 999 not found")
	})
}

// TestDeleteDeadLetter_SurroundingRowsAndListing verifies:
// deleting a target row removes only that row, leaves surrounding rows intact,
// does not echo raw XDR in the response, and is reflected in the GET /dead-letters listing (#1096).
func TestDeleteDeadLetter_SurroundingRowsAndListing(t *testing.T) {
	rawXDR1 := "AAAAEgAAAAFAAAA..."
	rawXDR2 := "BBBBEgAAAAFBBBB..."
	rawXDR3 := "CCCCEgAAAAFCCCC..."

	dl1 := store.DeadLetter{
		ID:         1,
		EventID:    "evt-001",
		ContractID: testContract,
		Ledger:     100,
		Type:       "contract",
		Error:      "decode failure 1",
		ValueXDR:   rawXDR1,
		CreatedAt:  time.Now(),
	}
	dl2 := store.DeadLetter{
		ID:         2,
		EventID:    "evt-002",
		ContractID: "CBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		Ledger:     101,
		Type:       "system",
		Error:      "decode failure 2",
		ValueXDR:   rawXDR2,
		CreatedAt:  time.Now(),
	}
	dl3 := store.DeadLetter{
		ID:         3,
		EventID:    "evt-003",
		ContractID: testContract,
		Ledger:     102,
		Type:       "diagnostic",
		Error:      "decode failure 3",
		ValueXDR:   rawXDR3,
		CreatedAt:  time.Now(),
	}

	letters := map[int64]store.DeadLetter{
		dl1.ID: dl1,
		dl2.ID: dl2,
		dl3.ID: dl3,
	}
	var mu sync.Mutex

	st := &stubStore{
		listDeadLettersFn: func(ctx context.Context, contractID string, limit int, cursor string) ([]store.DeadLetter, string, error) {
			mu.Lock()
			defer mu.Unlock()
			var ids []int64
			for id := range letters {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			var out []store.DeadLetter
			for _, id := range ids {
				out = append(out, letters[id])
			}
			return out, "", nil
		},
		deleteDeadLetterFn: func(ctx context.Context, id int64) error {
			mu.Lock()
			defer mu.Unlock()
			if _, ok := letters[id]; !ok {
				return store.ErrNotFound
			}
			delete(letters, id)
			return nil
		},
	}

	const validKey = "admin-secret-key"
	srv := newTestServerWithKey(st, nil, validKey)

	// 1. Verify all 3 dead letters exist via GET /dead-letters
	listResp, listBody := doGetWithAuth(t, srv, "/dead-letters", validKey)
	require.Equal(t, http.StatusOK, listResp.StatusCode)
	var listBefore deadLetterListResponse
	require.NoError(t, json.Unmarshal(listBody, &listBefore))
	require.Len(t, listBefore.DeadLetters, 3)

	// 2. Delete the middle dead letter (dl2)
	delResp, delBody := doDeleteWithAuth(t, srv, fmt.Sprintf("/dead-letters/%d", dl2.ID), validKey)
	assert.Equal(t, http.StatusNoContent, delResp.StatusCode)
	assert.Empty(t, delBody, "204 delete response must have empty body")
	assert.NotContains(t, string(delBody), rawXDR2, "raw XDR must never be echoed in delete response")

	// 3. Verify GET /dead-letters listing no longer shows dl2, but dl1 and dl3 remain intact
	listRespAfter, listBodyAfter := doGetWithAuth(t, srv, "/dead-letters", validKey)
	require.Equal(t, http.StatusOK, listRespAfter.StatusCode)
	var listAfter deadLetterListResponse
	require.NoError(t, json.Unmarshal(listBodyAfter, &listAfter))
	require.Len(t, listAfter.DeadLetters, 2)

	// Ensure dl2 is gone and dl1, dl3 are preserved with exact IDs and fields
	ids := []int64{listAfter.DeadLetters[0].ID, listAfter.DeadLetters[1].ID}
	assert.NotContains(t, ids, dl2.ID, "deleted dead letter must not appear in listing")
	assert.Contains(t, ids, dl1.ID, "surrounding dead letter 1 must be preserved")
	assert.Contains(t, ids, dl3.ID, "surrounding dead letter 3 must be preserved")

	for _, item := range listAfter.DeadLetters {
		if item.ID == dl1.ID {
			assert.Equal(t, "evt-001", item.EventID)
			assert.Equal(t, testContract, item.ContractID)
			assert.Equal(t, "decode failure 1", item.Error)
			assert.Equal(t, rawXDR1, item.ValueXDR)
		}
		if item.ID == dl3.ID {
			assert.Equal(t, "evt-003", item.EventID)
			assert.Equal(t, testContract, item.ContractID)
			assert.Equal(t, "decode failure 3", item.Error)
			assert.Equal(t, rawXDR3, item.ValueXDR)
		}
	}

	// 4. Repeated delete of dl2 returns 404
	delAgainResp, delAgainBody := doDeleteWithAuth(t, srv, fmt.Sprintf("/dead-letters/%d", dl2.ID), validKey)
	assert.Equal(t, http.StatusNotFound, delAgainResp.StatusCode)
	var notFoundResp map[string]string
	require.NoError(t, json.Unmarshal(delAgainBody, &notFoundResp))
	assert.Contains(t, notFoundResp["error"], fmt.Sprintf("dead letter %d not found", dl2.ID))

	// 5. Delete nonexistent row returns 404
	nonExistentResp, nonExistentBody := doDeleteWithAuth(t, srv, "/dead-letters/999999", validKey)
	assert.Equal(t, http.StatusNotFound, nonExistentResp.StatusCode)
	var nonExistentErr map[string]string
	require.NoError(t, json.Unmarshal(nonExistentBody, &nonExistentErr))
	assert.Contains(t, nonExistentErr["error"], "dead letter 999999 not found")
}

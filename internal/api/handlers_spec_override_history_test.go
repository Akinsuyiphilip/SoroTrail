package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/store"
)

// specOverrideHistoryBody is the JSON body of the history listing.
type specOverrideHistoryBody struct {
	Versions []store.ContractSpecOverrideVersion `json:"versions"`
	Count    int                                 `json:"count"`
	Cursor   string                              `json:"cursor"`
}

// putOverride uploads an override and returns the response.
func putOverride(t *testing.T, s *Server, body string) (testResponse, []byte) {
	t.Helper()
	return doReq(t, s, http.MethodPut, "/contracts/"+testContract+"/spec", body,
		map[string]string{"X-Api-Key": "test-key"})
}

// listHistory reads the override history.
func listHistory(t *testing.T, s *Server, query string) (testResponse, []byte) {
	t.Helper()
	return doReq(t, s, http.MethodGet, "/contracts/"+testContract+"/spec/history"+query, "",
		map[string]string{"X-Api-Key": "test-key"})
}

// TestPutContractSpecOverride_AppendsHistory proves every PUT records a
// history entry carrying the spec, the key prefix that made the change, and
// a timestamp — so "did someone change the override?" is answerable.
func TestPutContractSpecOverride_AppendsHistory(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	resp, body := putOverride(t, srv, specOverrideBody)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	require.Len(t, st.specOverrideHistory, 1, "a PUT must append exactly one history entry")
	entry := st.specOverrideHistory[0]
	assert.Equal(t, testContract, entry.ContractID)
	assert.JSONEq(t, specOverrideBody, string(entry.Spec), "the entry must carry the stored spec")
	assert.NotEmpty(t, entry.APIKeyPrefix, "the entry must name the key that made the change")
	assert.False(t, entry.CreatedAt.IsZero(), "the entry must be timestamped")
}

// TestDeleteContractSpecOverride_AppendsTombstone proves a DELETE records a
// tombstone entry: the history keeps the fact that the override went away,
// which is what makes "the override disappeared" answerable.
func TestDeleteContractSpecOverride_AppendsTombstone(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	_, _ = putOverride(t, srv, specOverrideBody)
	resp, body := doReq(t, srv, http.MethodDelete, "/contracts/"+testContract+"/spec", "",
		map[string]string{"X-Api-Key": "test-key"})
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	require.Len(t, st.specOverrideHistory, 2, "the delete must append a second entry")
	tombstone := st.specOverrideHistory[1]
	assert.Equal(t, testContract, tombstone.ContractID)
	assert.Empty(t, tombstone.Spec, "a tombstone carries no spec")
	assert.NotEmpty(t, tombstone.APIKeyPrefix)
}

// TestListContractSpecOverrideHistory_NewestFirst proves the listing is
// newest first, which is the order an operator reads it in.
func TestListContractSpecOverrideHistory_NewestFirst(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	_, _ = putOverride(t, srv, `{"events":[{"name":"first"}]}`)
	_, _ = putOverride(t, srv, `{"events":[{"name":"second"}]}`)
	_, _ = putOverride(t, srv, `{"events":[{"name":"third"}]}`)

	resp, body := listHistory(t, srv, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	var got specOverrideHistoryBody
	require.NoError(t, json.Unmarshal(body, &got))
	require.Len(t, got.Versions, 3)
	assert.Equal(t, 3, got.Count)
	assert.JSONEq(t, `{"events":[{"name":"third"}]}`, string(got.Versions[0].Spec))
	assert.JSONEq(t, `{"events":[{"name":"second"}]}`, string(got.Versions[1].Spec))
	assert.JSONEq(t, `{"events":[{"name":"first"}]}`, string(got.Versions[2].Spec))
	// The version ids increase with each change.
	assert.Greater(t, got.Versions[0].ID, got.Versions[1].ID)
	assert.Greater(t, got.Versions[1].ID, got.Versions[2].ID)
}

// TestListContractSpecOverrideHistory_CursorPagination proves the listing
// pages: the first page carries a cursor, and following it yields the older
// entries exactly once.
func TestListContractSpecOverrideHistory_CursorPagination(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	for i := 0; i < 3; i++ {
		_, _ = putOverride(t, srv, `{"events":[{"name":"v"}]}`)
	}

	resp, body := listHistory(t, srv, "?limit=2")
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
	var first specOverrideHistoryBody
	require.NoError(t, json.Unmarshal(body, &first))
	require.Len(t, first.Versions, 2)
	assert.NotEmpty(t, first.Cursor, "a partial page must carry a cursor")

	resp, body = listHistory(t, srv, "?limit=2&cursor="+first.Cursor)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
	var second specOverrideHistoryBody
	require.NoError(t, json.Unmarshal(body, &second))
	require.Len(t, second.Versions, 1, "the second page holds the remaining entry")
	assert.Empty(t, second.Cursor, "the last page carries no cursor")

	// The two pages together must cover every entry, newest first, with no
	// repeats.
	ids := []int64{first.Versions[0].ID, first.Versions[1].ID, second.Versions[0].ID}
	require.Len(t, ids, 3)
	assert.Greater(t, ids[0], ids[1])
	assert.Greater(t, ids[1], ids[2])
}

// TestListContractSpecOverrideHistory_EmptyForUnknownContract proves the
// listing of a contract that never had an override is an empty page, not an
// error: an operator asking about an untouched contract gets "nothing
// happened", not a failure.
func TestListContractSpecOverrideHistory_EmptyForUnknownContract(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	resp, body := listHistory(t, srv, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
	var got specOverrideHistoryBody
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Empty(t, got.Versions)
	assert.Equal(t, 0, got.Count)
}

// TestListContractSpecOverrideHistory_RejectsBadLimit proves the page size
// is validated rather than clamped.
func TestListContractSpecOverrideHistory_RejectsBadLimit(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	resp, body := listHistory(t, srv, "?limit=0")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", body)
}

// TestRevertContractSpecOverride_RestoresPreviousVersion proves a revert
// puts a previous version back and is itself recorded, so the history stays
// append-only and keeps the way back.
func TestRevertContractSpecOverride_RestoresPreviousVersion(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	_, _ = putOverride(t, srv, `{"events":[{"name":"original"}]}`)
	_, _ = putOverride(t, srv, `{"events":[{"name":"replacement"}]}`)

	// The first entry is the original spec.
	_, body := listHistory(t, srv, "")
	var listing specOverrideHistoryBody
	require.NoError(t, json.Unmarshal(body, &listing))
	require.Len(t, listing.Versions, 2)
	original := listing.Versions[1]

	resp, body := doReq(t, srv, http.MethodPost,
		"/contracts/"+testContract+"/spec/revert", `{"version":`+strconv.FormatInt(original.ID, 10)+`}`,
		map[string]string{"X-Api-Key": "test-key"})
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	// The stored override is the original spec again.
	assert.JSONEq(t, `{"events":[{"name":"original"}]}`, string(st.specOverride),
		"the revert must restore the previous spec")

	// And the revert is recorded as a third entry.
	require.Len(t, st.specOverrideHistory, 3, "a revert must be recorded as a new entry")
	assert.JSONEq(t, `{"events":[{"name":"original"}]}`, string(st.specOverrideHistory[2].Spec))
}

// TestRevertContractSpecOverride_UnknownVersion proves the not-found path:
// naming a version that is not in the history is a 404, and nothing is
// written.
func TestRevertContractSpecOverride_UnknownVersion(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	_, _ = putOverride(t, srv, specOverrideBody)
	before := len(st.specOverrideHistory)

	resp, body := doReq(t, srv, http.MethodPost,
		"/contracts/"+testContract+"/spec/revert", `{"version":9999}`,
		map[string]string{"X-Api-Key": "test-key"})
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "body: %s", body)
	assert.Len(t, st.specOverrideHistory, before, "a failed revert must not write history")
}

// TestRevertContractSpecOverride_RequiresVersion proves the version is
// mandatory: a revert with no target is a client error, not a silent no-op.
func TestRevertContractSpecOverride_RequiresVersion(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	resp, body := doReq(t, srv, http.MethodPost, "/contracts/"+testContract+"/spec/revert", `{}`,
		map[string]string{"X-Api-Key": "test-key"})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", body)
	assert.Contains(t, string(body), "version")
}

// TestRevertContractSpecOverride_RejectsBadVersion proves a non-positive
// version is rejected.
func TestRevertContractSpecOverride_RejectsBadVersion(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	resp, body := doReq(t, srv, http.MethodPost,
		"/contracts/"+testContract+"/spec/revert", `{"version":0}`,
		map[string]string{"X-Api-Key": "test-key"})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", body)
}

// TestSpecOverrideHistory_RequiresTheManagementKey proves the new routes
// sit behind the same gate as the override itself: a spec override silently
// changes how events decode, so its history is a management surface too.
func TestSpecOverrideHistory_RequiresTheManagementKey(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/contracts/" + testContract + "/spec/history"},
		{http.MethodPost, "/contracts/" + testContract + "/spec/revert"},
	} {
		resp, body := doReq(t, srv, tc.method, tc.path, "", nil)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
			"%s %s must be gated: %s", tc.method, tc.path, body)
	}
}

// recordingInvalidationPublisher captures the contract IDs the API publishes
// for the other replicas to evict.
type recordingInvalidationPublisher struct {
	mu        sync.Mutex
	published []string
}

func (p *recordingInvalidationPublisher) PublishSpecInvalidation(_ context.Context, contractID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published = append(p.published, contractID)
	return nil
}

func (p *recordingInvalidationPublisher) list() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.published...)
}

func (p *recordingInvalidationPublisher) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published = nil
}

// TestPutContractSpecOverride_PublishesInvalidation proves a write that
// changes what a contract decodes to is published for the other replicas.
//
// The spec cache is per-process, so without this the replicas that did not
// serve the write keep serving the old spec until their own cache expires.
func TestPutContractSpecOverride_PublishesInvalidation(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)
	publisher := &recordingInvalidationPublisher{}
	srv.SetSpecInvalidationPublisher(publisher)

	resp, body := putOverride(t, srv, specOverrideBody)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
	assert.Equal(t, []string{testContract}, publisher.list(),
		"the write must be published for the other replicas")
}

// TestDeleteContractSpecOverride_PublishesInvalidation proves a delete is
// published too: the other replicas hold the override as well.
func TestDeleteContractSpecOverride_PublishesInvalidation(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)
	publisher := &recordingInvalidationPublisher{}
	srv.SetSpecInvalidationPublisher(publisher)

	_, _ = putOverride(t, srv, specOverrideBody)
	publisher.reset()

	resp, body := doReq(t, srv, http.MethodDelete, "/contracts/"+testContract+"/spec", "",
		map[string]string{"X-Api-Key": "test-key"})
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
	assert.Equal(t, []string{testContract}, publisher.list(),
		"the delete must be published for the other replicas")
}

// TestSpecOverrideWithoutAPublisherStillWorks proves a single-process
// deployment — no publisher wired — keeps working.
func TestSpecOverrideWithoutAPublisherStillWorks(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	resp, body := putOverride(t, srv, specOverrideBody)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
}

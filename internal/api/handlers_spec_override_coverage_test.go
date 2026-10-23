package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetContractSpecOverride_Found(t *testing.T) {
	st := &stubStore{specOverride: []byte(specOverrideBody)}
	srv := newTestServer(st, nil)

	resp, body := doReq(t, srv, http.MethodGet, "/contracts/"+testContract+"/spec", "",
		map[string]string{"X-Api-Key": "test-key"})

	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	var got specOverrideResponse
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, testContract, got.ContractID)
	assert.JSONEq(t, specOverrideBody, string(got.Spec))
}

func TestGetContractSpecOverride_NotFound(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	resp, body := doReq(t, srv, http.MethodGet, "/contracts/"+testContract+"/spec", "",
		map[string]string{"X-Api-Key": "test-key"})

	require.Equal(t, http.StatusNotFound, resp.StatusCode, "body: %s", body)
	assert.Contains(t, string(body), "no spec override")
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
}

func TestGetContractSpecOverride_MalformedContractID(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	resp, body := doReq(t, srv, http.MethodGet, "/contracts/not-a-contract/spec", "",
		map[string]string{"X-Api-Key": "test-key"})

	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", body)
	assert.Contains(t, string(body), "invalid contract id")
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
}

func TestGetContractSpecOverride_StoreError(t *testing.T) {
	st := &stubStore{specOverrideErr: errors.New("boom")}
	srv := newTestServer(st, nil)

	resp, body := doReq(t, srv, http.MethodGet, "/contracts/"+testContract+"/spec", "",
		map[string]string{"X-Api-Key": "test-key"})

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode, "body: %s", body)
	assert.Contains(t, string(body), "loading contract spec override failed")
	assert.NotContains(t, string(body), "boom")
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
}

func TestDeleteContractSpecOverride_Existing(t *testing.T) {
	st := &stubStore{specOverride: []byte(specOverrideBody)}
	srv := newTestServer(st, nil)

	resp, body := doReq(t, srv, http.MethodDelete, "/contracts/"+testContract+"/spec", "",
		map[string]string{"X-Api-Key": "test-key"})

	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	var got specOverrideDeleteResponse
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, testContract, got.ContractID)
	assert.True(t, got.Deleted)
}

func TestDeleteContractSpecOverride_AbsentStill200(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	resp, body := doReq(t, srv, http.MethodDelete, "/contracts/"+testContract+"/spec", "",
		map[string]string{"X-Api-Key": "test-key"})

	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	var got specOverrideDeleteResponse
	require.NoError(t, json.Unmarshal(body, &got))
	assert.True(t, got.Deleted)
}

func TestDeleteContractSpecOverride_MalformedContractID(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	resp, body := doReq(t, srv, http.MethodDelete, "/contracts/not-a-contract/spec", "",
		map[string]string{"X-Api-Key": "test-key"})

	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", body)
	assert.Contains(t, string(body), "invalid contract id")
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
}

func TestDeleteContractSpecOverride_StoreError(t *testing.T) {
	st := &stubStore{specOverrideErr: errors.New("boom")}
	srv := newTestServer(st, nil)

	resp, body := doReq(t, srv, http.MethodDelete, "/contracts/"+testContract+"/spec", "",
		map[string]string{"X-Api-Key": "test-key"})

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode, "body: %s", body)
	assert.Contains(t, string(body), "deleting contract spec override failed")
	assert.NotContains(t, string(body), "boom")
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
}

func TestPutContractSpecOverride_ValidWrappedSpec(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	resp, body := doReq(t, srv, http.MethodPut, "/contracts/"+testContract+"/spec",
		`{"spec": `+specOverrideBody+`}`, map[string]string{"X-Api-Key": "test-key"})

	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	assert.Equal(t, testContract, st.specOverrideID)
	assert.JSONEq(t, specOverrideBody, string(st.specOverride))
}

func TestPutContractSpecOverride_InvalidSpecRejected(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	resp, body := doReq(t, srv, http.MethodPut, "/contracts/"+testContract+"/spec",
		`{"spec": {}}`, map[string]string{"X-Api-Key": "test-key"})

	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", body)
	assert.Contains(t, string(body), "events\\\" array is required")
	assert.Empty(t, st.specOverride)
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
}

func TestPutContractSpecOverride_EmptyBody(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)

	resp, body := doReq(t, srv, http.MethodPut, "/contracts/"+testContract+"/spec", "",
		map[string]string{"X-Api-Key": "test-key"})

	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", body)
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
}

func TestPutContractSpecOverride_OversizedBody(t *testing.T) {
	st := &stubStore{}
	srv := newTestServer(st, nil)
	srv.SetHTTPRequestBodyLimit(4096)

	reqBody := `{"spec":{"events":[{"name":"` + strings.Repeat("x", 6000) + `"}]}}`
	resp, body := doReq(t, srv, http.MethodPut, "/contracts/"+testContract+"/spec", reqBody,
		map[string]string{"X-Api-Key": "test-key"})

	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode, "body: %s", body)
	assert.Contains(t, string(body), "request body exceeds")
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
}

func TestSpecOverrideEndpoints_RequireAdminKey(t *testing.T) {
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/contracts/" + testContract + "/spec", ""},
		{http.MethodDelete, "/contracts/" + testContract + "/spec", ""},
		{http.MethodPut, "/contracts/" + testContract + "/spec", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.method+" missing key", func(t *testing.T) {
			st := &stubStore{}
			srv := newTestServer(st, nil)
			resp, body := doReq(t, srv, tc.method, tc.path, tc.body, nil)
			require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "body: %s", body)
			assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
		})
		t.Run(tc.method+" wrong key", func(t *testing.T) {
			st := &stubStore{}
			srv := newTestServer(st, nil)
			resp, body := doReq(t, srv, tc.method, tc.path, tc.body,
				map[string]string{"X-Api-Key": "wrong"})
			require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "body: %s", body)
			assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
		})
		t.Run(tc.method+" unconfigured key", func(t *testing.T) {
			st := &stubStore{}
			srv := newTestServerWithKey(st, nil, "")
			resp, body := doReq(t, srv, tc.method, tc.path, tc.body,
				map[string]string{"X-Api-Key": "anything"})
			require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "body: %s", body)
			assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
		})
	}
}

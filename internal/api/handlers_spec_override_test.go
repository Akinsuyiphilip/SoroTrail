package api

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// specOverrideBody is a minimal valid override document: one event with a
// name and nothing else. spec.ParseOverrideSpec accepts an event without
// topic or value specs, so this keeps the tests about the transport shape
// rather than about spec validation.
const specOverrideBody = `{"events": [{"name": "transfer"}]}`

func specOverridePut(t *testing.T, s *Server, body string) (testResponse, []byte) {
	t.Helper()
	return doReq(t, s, http.MethodPut, "/contracts/"+testContract+"/spec", body,
		map[string]string{"X-Api-Key": "test-key"})
}

// TestPutContractSpecOverride_AcceptsBareSpecForm pins the documented bare
// shape: {"events": [...]} without the {"spec": ...} wrapper. The handler
// used to retry decodeJSONBody against the already-drained r.Body, so this
// request always came back 400 and the bare form could never be stored.
func TestPutContractSpecOverride_AcceptsBareSpecForm(t *testing.T) {
	st := &stubStore{}

	resp, body := specOverridePut(t, newTestServer(st, nil), specOverrideBody)

	require.Equal(t, http.StatusOK, resp.StatusCode, "bare spec form must be accepted, got: %s", body)
	assert.Equal(t, testContract, st.specOverrideID)
	assert.JSONEq(t, specOverrideBody, string(st.specOverride),
		"the store must receive the bare document unchanged")
}

// TestPutContractSpecOverride_KeepsAcceptingTheWrappedForm is the regression
// guard for the shape that already worked: {"spec": {...}} must still win
// over the bare fallback and store only the inner document.
func TestPutContractSpecOverride_KeepsAcceptingTheWrappedForm(t *testing.T) {
	st := &stubStore{}

	resp, body := specOverridePut(t, newTestServer(st, nil), `{"spec": `+specOverrideBody+`}`)

	require.Equal(t, http.StatusOK, resp.StatusCode, "wrapped spec form must keep working, got: %s", body)
	assert.JSONEq(t, specOverrideBody, string(st.specOverride),
		"only the wrapped spec field is stored, not the envelope")
}

// TestPutContractSpecOverride_NeitherFormIsRejected covers the third shape:
// a body that is neither a wrapped nor a bare spec is a client error, and
// the message reports the decode failure of the wrapped attempt.
func TestPutContractSpecOverride_NeitherFormIsRejected(t *testing.T) {
	st := &stubStore{}

	resp, body := specOverridePut(t, newTestServer(st, nil), `{"events": [}`)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, string(body), "invalid JSON body",
		"the 400 must carry the wrapped decode error")
	assert.Empty(t, st.specOverride, "nothing may reach the store")
}

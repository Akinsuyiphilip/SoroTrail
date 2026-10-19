package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sorotrail/sorotrail/internal/store"
)

// A backend that does not implement an operation answers 501 Not Implemented, not an empty 200
// or a server fault.
func TestUnsupportedBackendOperationIsNotImplemented(t *testing.T) {
	unsupported := fmt.Errorf("list contracts: not supported by the test backend: %w", store.ErrUnsupported)

	t.Run("list contracts", func(t *testing.T) {
		st := &stubStore{listContractsErr: unsupported}
		resp, _ := doGet(t, newTestServer(st, nil), "/contracts")
		assert.Equal(t, http.StatusNotImplemented, resp.StatusCode)
	})

	t.Run("contract summary", func(t *testing.T) {
		st := &stubStore{contractSummaryErr: unsupported}
		resp, _ := doGet(t, newTestServer(st, nil), "/contracts/"+testContract)
		assert.Equal(t, http.StatusNotImplemented, resp.StatusCode)
	})
}

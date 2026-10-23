package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetMetricsEnabled_DisablesMetricsEndpoint(t *testing.T) {
	s := newTestServer(&stubStore{}, nil)
	s.SetMetricsEnabled(false)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestSetMetricsEnabled_EnabledByDefault(t *testing.T) {
	s := newTestServer(&stubStore{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

	require.NotEqual(t, http.StatusServiceUnavailable, rec.Code)
}

func TestUsage_ReturnsRecorderWhenMultiTenant(t *testing.T) {
	ts := newFakeTenants()
	s := newMultiTenantAuthServer(t, ts)
	require.NotNil(t, s.Usage())
}

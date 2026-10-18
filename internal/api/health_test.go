package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/rpc"
	"github.com/sorotrail/sorotrail/internal/store"
)

// This file is the coverage for "make the health endpoints report
// per-dependency detail": each dependency failing in isolation, each
// check timing out on its own budget, liveness staying independent of
// dependencies, and degraded being distinguishable from unready by
// documented thresholds. Every test asserts on the per-check entries,
// not just the envelope, so a regression that flattens the detail back
// into one coarse answer fails even when the status code still matches.

// decodeHealth parses a health-family envelope.
func decodeHealth(t *testing.T, body []byte) healthResponse {
	t.Helper()
	var got healthResponse
	require.NoError(t, json.Unmarshal(body, &got), string(body))
	return got
}

// checkStates projects the envelope onto name -> status so tables can
// assert whole sets of states at once.
func checkStates(h healthResponse) map[string]string {
	out := make(map[string]string, len(h.Checks))
	for name, c := range h.Checks {
		out[name] = c.Status
	}
	return out
}

// healthCase is one dependency-failure combination and the states every
// endpoint must report for it.
type healthCase struct {
	name string

	pingErr      error
	rpcErr       error
	rpcStatus    string // getHealth status when rpcErr is nil
	rpcLatest    uint32 // chain head reported by getHealth
	lastIngested int64  // LastIngestedLedger staged in the store
	ingestionErr error  // GetIngestionState error (e.g. store.ErrNotFound)

	wantCode   int
	wantStatus string
	// wantReadyz are the exact check states /readyz must report; every
	// non-ok one must also carry a detail explaining itself.
	wantReadyz map[string]string
}

// depCases is the shared matrix: each dependency failing in isolation,
// threshold boundaries, and combinations. The healthy dependencies must
// report ok in every row — that is the per-dependency-detail promise.
func depCases() []healthCase {
	return []healthCase{
		{
			name:         "all healthy",
			rpcStatus:    "healthy",
			rpcLatest:    1000,
			lastIngested: 1000,
			wantCode:     http.StatusOK,
			wantStatus:   healthOK,
			wantReadyz: map[string]string{
				"database": healthOK, "rpc": healthOK, "ingestion_lag": healthOK,
			},
		},
		{
			name:         "database down in isolation",
			pingErr:      errors.New("connection refused"),
			rpcStatus:    "healthy",
			rpcLatest:    1000,
			lastIngested: 1000,
			wantCode:     http.StatusServiceUnavailable,
			wantStatus:   healthUnready,
			wantReadyz: map[string]string{
				"database": healthUnready, "rpc": healthOK, "ingestion_lag": healthOK,
			},
		},
		{
			name:         "rpc unreachable in isolation",
			rpcErr:       errors.New("rpc unreachable"),
			lastIngested: 1000,
			wantCode:     http.StatusServiceUnavailable,
			wantStatus:   healthUnready,
			wantReadyz: map[string]string{
				// ingestion_lag needs the chain head from the same RPC, so
				// an unreachable RPC fails it too; the database, which does
				// not depend on the RPC, must still report ok.
				"database": healthOK, "rpc": healthUnready, "ingestion_lag": healthUnready,
			},
		},
		{
			name:         "rpc degraded upstream in isolation",
			rpcStatus:    "stopping",
			rpcLatest:    1000,
			lastIngested: 1000,
			wantCode:     http.StatusServiceUnavailable,
			wantStatus:   healthDegraded,
			wantReadyz: map[string]string{
				"database": healthOK, "rpc": healthDegraded, "ingestion_lag": healthOK,
			},
		},
		{
			name:         "ingestion past the lag threshold",
			rpcStatus:    "healthy",
			rpcLatest:    1000,
			lastIngested: 850, // 150 behind: strictly greater than the threshold
			wantCode:     http.StatusServiceUnavailable,
			wantStatus:   healthDegraded,
			wantReadyz: map[string]string{
				"database": healthOK, "rpc": healthOK, "ingestion_lag": healthDegraded,
			},
		},
		{
			name:         "ingestion exactly at the lag threshold stays ok",
			rpcStatus:    "healthy",
			rpcLatest:    1000,
			lastIngested: 900, // 100 behind: the threshold is strictly greater-than
			wantCode:     http.StatusOK,
			wantStatus:   healthOK,
			wantReadyz: map[string]string{
				"database": healthOK, "rpc": healthOK, "ingestion_lag": healthOK,
			},
		},
		{
			name:      "no ingestion state yet is a cold start, not a failure",
			rpcStatus: "healthy",
			rpcLatest: 1000,
			// Nothing ingested: there is no lag to judge, and the detail
			// says so rather than leaving an operator wondering.
			ingestionErr: store.ErrNotFound,
			wantCode:     http.StatusOK,
			wantStatus:   healthOK,
			wantReadyz: map[string]string{
				"database": healthOK, "rpc": healthOK, "ingestion_lag": healthOK,
			},
		},
		{
			name:      "zeroed ingestion state with a chain head stays ok",
			rpcStatus: "healthy",
			rpcLatest: 5000,
			// lastIngested=0 means ingestion has not committed anything yet,
			// so the huge arithmetic lag must not fail readiness (the guard
			// is part of the documented threshold).
			wantCode:   http.StatusOK,
			wantStatus: healthOK,
			wantReadyz: map[string]string{
				"database": healthOK, "rpc": healthOK, "ingestion_lag": healthOK,
			},
		},
		{
			name:         "unready dominates degraded in the envelope",
			pingErr:      errors.New("connection refused"),
			rpcStatus:    "stopping",
			rpcLatest:    1000,
			lastIngested: 1000,
			wantCode:     http.StatusServiceUnavailable,
			wantStatus:   healthUnready,
			wantReadyz: map[string]string{
				"database": healthUnready, "rpc": healthDegraded, "ingestion_lag": healthOK,
			},
		},
		{
			name:         "both dependencies failing",
			pingErr:      errors.New("connection refused"),
			rpcErr:       errors.New("rpc unreachable"),
			lastIngested: 1000,
			wantCode:     http.StatusServiceUnavailable,
			wantStatus:   healthUnready,
			wantReadyz: map[string]string{
				"database": healthUnready, "rpc": healthUnready, "ingestion_lag": healthUnready,
			},
		},
	}
}

func stubsFor(c healthCase) (*stubStore, *stubRPC) {
	st := &stubStore{
		pingErr:      c.pingErr,
		ingestionErr: c.ingestionErr,
		ingestion:    store.IngestionState{LastIngestedLedger: c.lastIngested},
	}
	rc := &stubRPC{
		health:    rpc.Health{Status: c.rpcStatus, LatestLedger: c.rpcLatest},
		healthErr: c.rpcErr,
	}
	return st, rc
}

// assertReadyzEnvelope checks the whole /readyz body: exact check set,
// exact states, detail on every non-ok state, and the latency field on
// every check.
func assertReadyzEnvelope(t *testing.T, body []byte, wantStatus string, wantChecks map[string]string) healthResponse {
	t.Helper()
	h := decodeHealth(t, body)
	assert.Equal(t, wantStatus, h.Status, "envelope status; body: %s", body)
	require.NotNil(t, h.Checks, "checks must always be present; body: %s", body)
	assert.Equal(t, wantChecks, checkStates(h), "per-check states; body: %s", body)
	for name, c := range h.Checks {
		if c.Status != healthOK {
			assert.NotEmptyf(t, c.Detail, "check %q must say why it isn't ok; body: %s", name, body)
		}
		// The latency field is always emitted; assert on the raw body so
		// an omitempty regression is caught rather than a zero value.
		assert.Containsf(t, string(body), `"latency_ms"`, "check %q must report its own latency", name)
	}
	return h
}

// TestReadiness_ReportsEachDependency walks the failure matrix on
// /readyz: readiness reports each dependency's state individually, and
// each dependency failing in isolation leaves the others untouched.
func TestReadiness_ReportsEachDependency(t *testing.T) {
	for _, c := range depCases() {
		t.Run(c.name, func(t *testing.T) {
			st, rc := stubsFor(c)
			resp, body := doGet(t, newTestServer(st, rc), "/readyz")

			require.Equal(t, c.wantCode, resp.StatusCode, string(body))
			assertReadyzEnvelope(t, body, c.wantStatus, c.wantReadyz)
		})
	}
}

// TestHealth_ReportsEachDependency runs the same matrix on /health,
// which grades the two external dependencies but never ingestion
// freshness — that asymmetry is part of its contract.
func TestHealth_ReportsEachDependency(t *testing.T) {
	for _, c := range depCases() {
		t.Run(c.name, func(t *testing.T) {
			st, rc := stubsFor(c)
			resp, body := doGet(t, newTestServer(st, rc), "/health")

			// /health grades only what /health runs: a lag-only degradation
			// is invisible here, so the expected code follows database+rpc.
			wantCode := http.StatusOK
			if c.wantReadyz["database"] != healthOK || c.wantReadyz["rpc"] != healthOK {
				wantCode = http.StatusServiceUnavailable
			}
			require.Equal(t, wantCode, resp.StatusCode, string(body))
			h := decodeHealth(t, body)
			want := map[string]string{
				"database": c.wantReadyz["database"],
				"rpc":      c.wantReadyz["rpc"],
			}
			wantStatus := healthOK
			if want["database"] == healthUnready || want["rpc"] == healthUnready {
				wantStatus = healthUnready
			} else if want["database"] == healthDegraded || want["rpc"] == healthDegraded {
				wantStatus = healthDegraded
			}
			assert.Equal(t, wantStatus, h.Status, "envelope status; body: %s", body)
			assert.Equal(t, want, checkStates(h), "/health checks database and rpc only; body: %s", body)
			for name, check := range h.Checks {
				if check.Status != healthOK {
					assert.NotEmptyf(t, check.Detail, "check %q must say why it isn't ok; body: %s", name, body)
				}
			}
		})
	}
}

// TestLiveness_NeverTouchesDependencies: /livez must answer for the
// process alone. With both dependencies broken it still answers 200,
// reports only its own process check, and provably contacts nothing —
// if it did, a dead database would get the binary restarted instead of
// the instance simply leaving rotation via /readyz.
func TestLiveness_NeverTouchesDependencies(t *testing.T) {
	st := &stubStore{
		pingErr:      errors.New("connection refused"),
		ingestionErr: errors.New("store down"),
		ingestion:    store.IngestionState{LastIngestedLedger: 42},
	}
	rc := &stubRPC{healthErr: errors.New("rpc unreachable")}
	s := newTestServer(st, rc)

	resp, body := doGet(t, s, "/livez")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	h := decodeHealth(t, body)
	assert.Equal(t, healthOK, h.Status, "liveness is about the process, not its dependencies")
	require.NotNil(t, h.Checks)
	assert.Equal(t, map[string]string{"process": healthOK}, checkStates(h),
		"/livez must report the process check and no dependency checks")

	assert.Zero(t, st.pingCalls.Load(), "/livez must not touch the database")
	assert.Zero(t, st.ingestionStateCalls.Load(), "/livez must not read ingestion state")
	assert.Zero(t, rc.getHealthCalls.Load(), "/livez must not touch the RPC")

	// The same server, asked for readiness, does see the breakage:
	// independence is scoped to liveness, not spread over the family.
	resp, body = doGet(t, s, "/readyz")
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, string(body))
	assert.Equal(t, healthUnready, decodeHealth(t, body).Status)
	assert.Positive(t, st.pingCalls.Load(), "/readyz must probe the database")
}

// TestHealthChecks_TimeoutIndependently: every check gets its own
// budget, so a hung dependency reports its own timeout while the checks
// around it report their own (healthy) state — and when every check
// hangs the endpoint costs one budget, not one budget each in series.
func TestHealthChecks_TimeoutIndependently(t *testing.T) {
	const budget = 120 * time.Millisecond

	tests := []struct {
		name          string
		hangDatabase  bool
		hangRPC       bool
		wantDBStatus  string
		wantRPCStatus string
	}{
		{
			name:          "database hangs, the rest answer",
			hangDatabase:  true,
			wantDBStatus:  healthUnready,
			wantRPCStatus: healthOK,
		},
		{
			name:          "rpc hangs, the rest answer",
			hangRPC:       true,
			wantDBStatus:  healthOK,
			wantRPCStatus: healthUnready,
		},
		{
			name:          "everything hangs",
			hangDatabase:  true,
			hangRPC:       true,
			wantDBStatus:  healthUnready,
			wantRPCStatus: healthUnready,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &stubStore{ingestion: store.IngestionState{LastIngestedLedger: 1000}}
			if tt.hangDatabase {
				st.pingFn = hangOnContextDone
			}
			rc := &stubRPC{health: rpc.Health{Status: "healthy", LatestLedger: 1000}}
			if tt.hangRPC {
				rc.getHealthFn = hangForHealth
			}

			s := newTestServer(st, rc)
			s.checkTimeout = budget

			start := time.Now()
			resp, body := doGet(t, s, "/readyz")
			elapsed := time.Since(start)

			// A check with no answer within budget is unready, so readiness
			// fails — with the timeout named as the cause, not the generic
			// context error the deadline surfaces.
			require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, string(body))
			h := decodeHealth(t, body)
			assert.Equal(t, healthUnready, h.Status, string(body))
			assert.Equal(t, tt.wantDBStatus, h.Checks["database"].Status, string(body))
			assert.Equal(t, tt.wantRPCStatus, h.Checks["rpc"].Status, string(body))

			if tt.hangDatabase {
				assert.Contains(t, h.Checks["database"].Detail, "timed out",
					"a hung check must report the timeout as its cause")
			}
			if tt.hangRPC {
				assert.Contains(t, h.Checks["rpc"].Detail, "timed out",
					"a hung check must report the timeout as its cause")
			}
			// When only one dependency hangs, the others must still report
			// their own healthy state: the hang doesn't bleed into them or
			// consume their budget.
			if tt.hangDatabase && !tt.hangRPC {
				assert.Equal(t, healthOK, h.Checks["rpc"].Status, string(body))
				assert.Equal(t, healthOK, h.Checks["ingestion_lag"].Status, string(body))
			}
			if tt.hangRPC && !tt.hangDatabase {
				assert.Equal(t, healthOK, h.Checks["database"].Status, string(body))
			}
			if tt.hangDatabase && tt.hangRPC {
				assert.Contains(t, h.Checks["ingestion_lag"].Detail, "timed out",
					"the lag check gets its own budget too")
				// Concurrent independent budgets: N hung checks cost one
				// budget, not N in series. The bound sits well below
				// 2x budget so a serial regression cannot slip under it,
				// and well above 1x budget so scheduler noise cannot
				// trip it.
				assert.Less(t, elapsed, 2*budget-30*time.Millisecond,
					"checks must not run in series against a shared clock (took %s)", elapsed)
			}
		})
	}
}

// hangOnContextDone lets Ping block until its own deadline expires, the
// way a wedged database connection behaves in production.
func hangOnContextDone(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// hangForHealth is hangOnContextDone for the RPC probe.
func hangForHealth(ctx context.Context) (rpc.Health, error) {
	<-ctx.Done()
	return rpc.Health{}, ctx.Err()
}

// TestOpenAPIDocumentsTheHealthShape: the response shape is the operator
// contract, so it lives in the OpenAPI spec — this test fails if the
// spec stops documenting an endpoint, drops a state from the vocabulary,
// or loses the thresholds the handlers enforce.
func TestOpenAPIDocumentsTheHealthShape(t *testing.T) {
	var doc struct {
		Paths map[string]struct {
			Get struct {
				Description string `json:"description"`
			} `json:"get"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(openapiSpec, &doc), "the embedded spec must parse") // schemaProbe decodes only the keys this test reads; sibling schemas
	// shape their properties differently, so a stricter shared struct
	// would choke on schemas this test doesn't care about.
	type schemaProbe struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	probe := func(name string) schemaProbe {
		t.Helper()
		raw, ok := doc.Components.Schemas[name]
		require.Truef(t, ok, "the spec must define schema %s", name)
		var s schemaProbe
		require.NoErrorf(t, json.Unmarshal(raw, &s), "schema %s must decode", name)
		return s
	}
	propEnum := func(s schemaProbe, prop string) []string {
		t.Helper()
		raw, ok := s.Properties[prop]
		require.Truef(t, ok, "schema must document property %q", prop)
		var p struct {
			Enum []string `json:"enum"`
		}
		require.NoError(t, json.Unmarshal(raw, &p))
		return p.Enum
	}

	states := []string{"ok", "degraded", "unready"}
	for _, schema := range []string{"HealthResponse", "DependencyCheck"} {
		s := probe(schema)
		require.Contains(t, s.Properties, "status", "%s must document status", schema)
		assert.Equalf(t, states, propEnum(s, "status"),
			"%s.status must document the full ok/degraded/unready vocabulary", schema)
	}
	// The per-check detail fields are the point of the change.
	dep := probe("DependencyCheck")
	assert.Contains(t, dep.Properties, "detail", "the spec must document the per-check detail")
	assert.Contains(t, dep.Properties, "latency_ms", "the spec must document the per-check latency")
	assert.Contains(t, dep.Required, "status")
	assert.Contains(t, dep.Required, "latency_ms")
	// checks values are structured, not strings.
	health := probe("HealthResponse")
	checksRaw, ok := health.Properties["checks"]
	require.True(t, ok, "HealthResponse must document checks")
	var checksProp struct {
		AdditionalProperties json.RawMessage `json:"additionalProperties"`
	}
	require.NoError(t, json.Unmarshal(checksRaw, &checksProp))
	var addl struct {
		Ref string `json:"$ref"`
	}
	require.NoError(t, json.Unmarshal(checksProp.AdditionalProperties, &addl),
		"HealthResponse.checks must declare additionalProperties")
	assert.Equal(t, "#/components/schemas/DependencyCheck", addl.Ref,
		"HealthResponse.checks must point at DependencyCheck")

	for _, path := range []string{"/health", "/livez", "/readyz"} {
		assert.Containsf(t, doc.Paths, path, "the spec must document %s", path)
	}
	// Thresholds are part of the contract the spec carries.
	assert.Contains(t, doc.Paths["/readyz"].Get.Description, "100 ledgers",
		"the readiness spec must document the ingestion-lag threshold")
	assert.Contains(t, doc.Paths["/readyz"].Get.Description, "5-second",
		"the readiness spec must document the per-check timeout")
	assert.Contains(t, doc.Paths["/livez"].Get.Description, "Never touches",
		"the liveness spec must document its independence from dependencies")
}

// TestHealth_PrettyPrintKeepsShape: operators curl these endpoints by
// hand, so ?pretty=true must carry exactly the same per-dependency
// detail as the compact form — only whitespace may differ.
func TestHealth_PrettyPrintKeepsShape(t *testing.T) {
	st, rc := stubsFor(depCases()[1]) // database down
	s := newTestServer(st, rc)

	resp, plain := doGet(t, s, "/readyz")
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, string(plain))
	resp, pretty := doGet(t, s, "/readyz?pretty=true")
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, string(pretty))

	assert.Contains(t, string(pretty), "\n", "?pretty=true should indent the envelope")
	assert.JSONEq(t, string(plain), string(pretty),
		"pretty output must carry byte-identical data once whitespace is ignored")
	h := decodeHealth(t, pretty)
	assert.Equal(t, healthUnready, h.Checks["database"].Status)
	assert.Contains(t, h.Checks["database"].Detail, "connection refused")
}

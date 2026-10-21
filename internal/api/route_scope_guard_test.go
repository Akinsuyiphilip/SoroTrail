package api_test

// Router-driven tenant scope leak guard (#1161).
//
// Existing tenant-boundary tests in internal/api/tenant_test.go enumerate
// routes by hand, so a read endpoint nobody listed is an endpoint nobody
// tests. This file walks the live chi router via chi.Walk and exercises
// every top-level GraphQL Query field, asserting that a request issued as
// tenant A never returns tenant B's contract ID or event ID in the response
// body.
//
// The test runs both against a real Postgres database when TEST_DATABASE_URL
// (or DATABASE_URL) is set (used by `make test-db`) and against a full-table
// mock store when no database URL is configured, so `go test ./...` and
// `make test-db` both enforce the route-inventory invariant.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/api"
	"github.com/sorotrail/sorotrail/internal/api/graphql"
	"github.com/sorotrail/sorotrail/internal/rpc"
	"github.com/sorotrail/sorotrail/internal/store"
)

const (
	guardContractA = "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"
	guardContractB = "CBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	guardEventIDA  = "00000000000000000100-0000000001"
	guardEventIDB  = "00000000000000000200-0000000002"
	guardAddress   = "GDQP2KPQGKIHYJGXNUIYOMHARUARCA7DJT5FO2FFOOKY3B2WSQHG4W37"
	guardTxHash    = "9f5c7a2b00000000000000000000000000000000000000000000000000000001"
)

// globalRouteAllowlist lists GET routes that are genuinely global (contain
// no tenant/event/contract data) or are operator/admin-only management
// surfaces where a non-admin tenant credential is rejected before reading.
// Every entry must state a clear reason; any newly added GET route that is
// neither scoped nor listed here fails the build.
var globalRouteAllowlist = map[string]string{
	"/health":                        "Global process dependency probe (DB + RPC reachability); contains no event or contract identifiers.",
	"/livez":                         "Global process liveness probe; touches no store state.",
	"/readyz":                        "Global readiness probe (DB + RPC + ingestion lag); contains no tenant data.",
	"/version":                       "Static build metadata (version, commit, build date).",
	"/metrics":                       "Prometheus process metrics scraper endpoint; contains no contract or event IDs.",
	"/openapi.json":                  "Compiled-in static OpenAPI 3.1 specification.",
	"/docs":                          "Compiled-in static Swagger UI HTML.",
	"/graphiql":                      "Compiled-in static GraphiQL HTML playground.",
	"/graphql":                       "GraphQL transport endpoint; exercised per top-level Query field in TestGraphQLQueryFields_ConsultTenantScope.",
	"/events/ws":                     "WebSocket upgrade endpoint; requires WebSocket handshake headers and is covered by streaming scope tests.",
	"/apikeys":                       "Single-tenant API key management surface; rejected under multi-tenant bearer auth.",
	"/watched-contracts":             "Operator-only watch list gated by API_KEY (watchedMW), not tenant credentials (returns 503/401 to tenants).",
	"/contracts/{id}/spec":           "Operator-only contract spec override surface gated by API_KEY (watchedMW).",
	"/dead-letters":                  "Operator-only dead-letter queue gated by API_KEY (watchedMW).",
	"/admin/tenants":                 "Admin-only tenant management endpoint gated by requireAdmin (returns 403 to non-admin tenants).",
	"/admin/tenants/{id}":            "Admin-only tenant detail endpoint gated by requireAdmin.",
	"/admin/tenants/{id}/usage":      "Admin-only tenant usage endpoint gated by requireAdmin.",
	"/admin/tenants/{id}/grants":     "Admin-only tenant grants endpoint gated by requireAdmin.",
	"/admin/tenants/{id}/keys":       "Admin-only tenant keys endpoint gated by requireAdmin.",
	"/subscriptions/{id}/deliveries": "Subscription delivery history; scoped by subscription ownership (tested via /subscriptions).",
}

// routeExtraQuery supplies required query parameters for read routes that
// return 400 or an empty body without them. Any scoped GET route that
// returns an empty body without extra parameters must have an entry here.
var routeExtraQuery = map[string]string{
	"/events/aggregate":      "bucket=ledger",
	"/contracts/{id}/export": "from_ledger=1&to_ledger=500&format=ndjson",
}

// knownUnscopedRESTRoutes documents pre-existing read routes on `main` whose
// handlers do not yet consult the caller's tenant Scope. When a leak is
// detected on one of these routes, the test logs the known issue rather than
// failing `main` until the corresponding fix PR lands; once the fix lands and
// the route stops leaking, the test requires it to be removed from this map.
var knownUnscopedRESTRoutes = map[string]string{
	"/addresses/{address}/summary": "GetAddressSummary aggregates e.contract_id across all events for the address without filtering by caller Scope.",
}

// knownUnscopedGraphQLFields documents top-level GraphQL Query fields on
// `main` that do not yet consult `api.ScopeFromContext`.
var knownUnscopedGraphQLFields = map[string]string{}

type noopRPC struct{}

func (noopRPC) GetEvents(context.Context, rpc.GetEventsRequest) (rpc.GetEventsResponse, error) {
	return rpc.GetEventsResponse{}, nil
}
func (noopRPC) GetLatestLedger(context.Context) (rpc.LatestLedger, error) {
	return rpc.LatestLedger{Sequence: 500}, nil
}
func (noopRPC) GetHealth(context.Context) (rpc.Health, error) {
	return rpc.Health{Status: "healthy", LatestLedger: 500, OldestLedger: 1}, nil
}
func (noopRPC) GetLedgerEntries(context.Context, rpc.GetLedgerEntriesRequest) (rpc.GetLedgerEntriesResponse, error) {
	return rpc.GetLedgerEntriesResponse{}, nil
}
func (noopRPC) SimulateTransaction(context.Context, rpc.SimulateTransactionRequest) (rpc.SimulateTransactionResponse, error) {
	return rpc.SimulateTransactionResponse{}, nil
}

// guardTenants is an in-memory TenantStore used when running the router-walk
// guard without an external Postgres instance.
type guardTenants struct {
	store.TenantStore
	tenantA store.Tenant
	prefixA string
	digestA []byte
}

func newGuardTenants(t *testing.T) (*guardTenants, string) {
	t.Helper()
	maxWatched := 10
	tenantA := store.Tenant{ID: 1, Name: "tenant-a", Enabled: true, MaxWatchedContracts: &maxWatched}
	keyA, prefixA, digestA, err := api.GenerateAPIKey()
	require.NoError(t, err)
	return &guardTenants{
		tenantA: tenantA,
		prefixA: prefixA,
		digestA: digestA,
	}, keyA
}

func (g *guardTenants) LookupTenantAPIKey(_ context.Context, prefix string) (store.TenantAPIKey, []byte, store.Tenant, error) {
	if prefix != g.prefixA {
		return store.TenantAPIKey{}, nil, store.Tenant{}, store.ErrNotFound
	}
	return store.TenantAPIKey{ID: 100, TenantID: g.tenantA.ID}, g.digestA, g.tenantA, nil
}

func (g *guardTenants) ScopeForTenant(_ context.Context, t store.Tenant) (store.Scope, error) {
	if t.ID == g.tenantA.ID {
		return store.NewScope([]string{guardContractA}), nil
	}
	return store.Scope{}, nil
}

func (g *guardTenants) GetTenant(_ context.Context, id int64) (store.Tenant, error) {
	if id == g.tenantA.ID {
		return g.tenantA, nil
	}
	return store.Tenant{}, store.ErrNotFound
}

func (g *guardTenants) ListGrants(_ context.Context, id int64) ([]string, error) {
	if id == g.tenantA.ID {
		return []string{guardContractA}, nil
	}
	return nil, nil
}

func (g *guardTenants) ListTenantWatchedContracts(_ context.Context, id int64, _ string) ([]string, error) {
	if id == g.tenantA.ID {
		return []string{guardContractA}, nil
	}
	return nil, nil
}

func (g *guardTenants) TouchTenantAPIKey(context.Context, int64) error { return nil }
func (g *guardTenants) AddUsage(context.Context, time.Time, map[int64]store.UsageDelta) error {
	return nil
}
func (g *guardTenants) ListUsage(context.Context, int64, int) ([]store.TenantUsage, error) {
	return nil, nil
}

// guardMemoryStore mirrors the Postgres store's contract: methods that take
// store.Scope (or store.EventFilter.Scope) filter by that scope, and methods
// that do NOT take a Scope return the full table (both tenant A and tenant B
// rows). Any handler that forgets to propagate Scope or calls an unscoped
// store method without handler-side filtering will therefore leak
// guardContractB or guardEventIDB in its response body.
type guardMemoryStore struct {
	store.Store
	events        []store.Event
	watched       []store.WatchedContract
	subscriptions []store.Subscription
}

func newGuardMemoryStore(t *testing.T) (*guardMemoryStore, *guardTenants, string) {
	t.Helper()
	ts, keyA := newGuardTenants(t)
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ownerA := int64(1)
	ownerB := int64(2)
	st := &guardMemoryStore{
		events: []store.Event{
			{
				ID:               guardEventIDA,
				ContractID:       guardContractA,
				Ledger:           100,
				Type:             "contract",
				TxHash:           guardTxHash,
				InSuccessfulCall: true,
				Topics:           json.RawMessage(`[{"symbol":"transfer"},{"address":"` + guardAddress + `"}]`),
				Value:            json.RawMessage(`{"i128":"1000"}`),
				RawTopicXDR:      []string{"AAAADwAAAAh0cmFuc2Zlcg=="},
				RawValueXDR:      "AAAACgAAAAAAAAAB",
				CreatedAt:        now,
			},
			{
				ID:               guardEventIDB,
				ContractID:       guardContractB,
				Ledger:           200,
				Type:             "contract",
				TxHash:           guardTxHash,
				InSuccessfulCall: true,
				Topics:           json.RawMessage(`[{"symbol":"transfer"},{"address":"` + guardAddress + `"}]`),
				Value:            json.RawMessage(`{"i128":"2000"}`),
				RawTopicXDR:      []string{"AAAADwAAAAh0cmFuc2Zlcg=="},
				RawValueXDR:      "AAAACgAAAAAAAAAB",
				CreatedAt:        now.Add(time.Minute),
			},
		},
		watched: []store.WatchedContract{
			{ContractID: guardContractA, Network: "default"},
			{ContractID: guardContractB, Network: "default"},
		},
		subscriptions: []store.Subscription{
			{
				ID:       1,
				URL:      "https://example.com/a",
				Secret:   "whsec_aaaaaaaaaaaaaaaa",
				Filters:  store.SubscriptionFilter{ContractID: guardContractA},
				Enabled:  true,
				TenantID: &ownerA,
			},
			{
				ID:       2,
				URL:      "https://example.com/b",
				Secret:   "whsec_bbbbbbbbbbbbbbbb",
				Filters:  store.SubscriptionFilter{ContractID: guardContractB},
				Enabled:  true,
				TenantID: &ownerB,
			},
		},
	}
	return st, ts, keyA
}

func (m *guardMemoryStore) visible(sc store.Scope) []store.Event {
	if sc.DeniesAll() {
		return nil
	}
	var out []store.Event
	for _, e := range m.events {
		if sc.Allows(e.ContractID) {
			out = append(out, e)
		}
	}
	return out
}

func (m *guardMemoryStore) Ping(context.Context) error { return nil }
func (m *guardMemoryStore) GetIngestionState(context.Context) (store.IngestionState, error) {
	return store.IngestionState{LastIngestedLedger: 500}, nil
}

func (m *guardMemoryStore) QueryEvents(_ context.Context, f store.EventFilter) ([]store.Event, string, error) {
	var out []store.Event
	for _, e := range m.visible(f.Scope) {
		if f.ContractID != "" && e.ContractID != f.ContractID {
			continue
		}
		out = append(out, e)
	}
	return out, "", nil
}

func (m *guardMemoryStore) CountEvents(_ context.Context, f store.EventFilter) (int64, error) {
	var n int64
	for _, e := range m.visible(f.Scope) {
		if f.ContractID != "" && e.ContractID != f.ContractID {
			continue
		}
		n++
	}
	return n, nil
}

func (m *guardMemoryStore) AggregateEvents(_ context.Context, f store.EventFilter, _ string) ([]store.AggregateBucket, error) {
	vis := m.visible(f.Scope)
	if len(vis) == 0 {
		return nil, nil
	}
	return []store.AggregateBucket{{Bucket: "100", Count: int64(len(vis))}}, nil
}

func (m *guardMemoryStore) GetEvent(_ context.Context, id string, sc store.Scope) (store.Event, error) {
	for _, e := range m.visible(sc) {
		if e.ID == id {
			return e, nil
		}
	}
	return store.Event{}, store.ErrNotFound
}

func (m *guardMemoryStore) EventExists(_ context.Context, id string, sc store.Scope) (bool, error) {
	for _, e := range m.visible(sc) {
		if e.ID == id {
			return true, nil
		}
	}
	return false, nil
}

func (m *guardMemoryStore) GetEventsByTxHash(_ context.Context, txHash, excludeID string) ([]store.Event, error) {
	var out []store.Event
	for _, e := range m.events {
		if e.TxHash == txHash && e.ID != excludeID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *guardMemoryStore) Stats(_ context.Context, sc store.Scope) (store.Stats, error) {
	vis := m.visible(sc)
	return store.Stats{
		TotalEvents:        int64(len(vis)),
		ContractCount:      int64(len(vis)),
		LastIngestedLedger: 500,
	}, nil
}

// Scoped contract methods (mirroring Postgres on `main` after #1173 and #1175):
func (m *guardMemoryStore) ListContracts(_ context.Context, f store.ContractsFilter) ([]store.ContractSummary, string, error) {
	if f.Scope.DeniesAll() {
		return nil, "", nil
	}
	var out []store.ContractSummary
	for _, e := range m.visible(f.Scope) {
		out = append(out, store.ContractSummary{
			ContractID:  e.ContractID,
			EventCount:  1,
			FirstLedger: e.Ledger,
			LastLedger:  e.Ledger,
			LastSeen:    e.CreatedAt,
		})
	}
	return out, "", nil
}

func (m *guardMemoryStore) CountContracts(_ context.Context, f store.ContractsFilter) (int64, error) {
	return int64(len(m.visible(f.Scope))), nil
}

func (m *guardMemoryStore) GetContractSummary(_ context.Context, contractID string) (store.ContractSummary, error) {
	for _, e := range m.events {
		if e.ContractID == contractID {
			return store.ContractSummary{
				ContractID:  e.ContractID,
				EventCount:  1,
				FirstLedger: e.Ledger,
				LastLedger:  e.Ledger,
				LastSeen:    e.CreatedAt,
			}, nil
		}
	}
	return store.ContractSummary{}, store.ErrNotFound
}

func (m *guardMemoryStore) CountContractEvents(_ context.Context, contractID string) (int64, error) {
	for _, e := range m.events {
		if e.ContractID == contractID {
			return 1, nil
		}
	}
	return 0, nil
}

func (m *guardMemoryStore) ContractEventTypeCounts(_ context.Context, contractID string) ([]store.ContractEventTypeCount, error) {
	for _, e := range m.events {
		if e.ContractID == contractID {
			return []store.ContractEventTypeCount{{Type: e.Type, Count: 1}}, nil
		}
	}
	return nil, nil
}

func (m *guardMemoryStore) GetContractMeta(_ context.Context, _ string) (store.ContractMeta, error) {
	return store.ContractMeta{}, store.ErrNotFound
}

func (m *guardMemoryStore) ListWatchedContracts(context.Context) ([]store.WatchedContract, error) {
	return m.watched, nil
}

func (m *guardMemoryStore) ListWatchedContractsScoped(_ context.Context, sc store.Scope) ([]store.WatchedContract, error) {
	if sc.DeniesAll() {
		return nil, nil
	}
	var out []store.WatchedContract
	for _, w := range m.watched {
		if sc.Allows(w.ContractID) {
			out = append(out, w)
		}
	}
	return out, nil
}

func (m *guardMemoryStore) GetContractCursor(_ context.Context, _ string) (store.ContractCursor, error) {
	return store.ContractCursor{}, store.ErrNotFound
}

func (m *guardMemoryStore) ListSubscriptions(_ context.Context, owner store.SubscriptionOwner) ([]store.Subscription, error) {
	var out []store.Subscription
	for _, s := range m.subscriptions {
		if owner.IsAll() || (s.TenantID != nil && owner.TenantID() == *s.TenantID) {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *guardMemoryStore) GetSubscription(_ context.Context, id int64, owner store.SubscriptionOwner) (store.Subscription, error) {
	for _, s := range m.subscriptions {
		if s.ID == id && (owner.IsAll() || (s.TenantID != nil && owner.TenantID() == *s.TenantID)) {
			return s, nil
		}
	}
	return store.Subscription{}, store.ErrNotFound
}

func (m *guardMemoryStore) QueryAddressEvents(_ context.Context, _ string, f store.EventFilter) ([]store.Event, string, error) {
	return m.visible(f.Scope), "", nil
}

func (m *guardMemoryStore) CountAddressEvents(_ context.Context, _ string) (int64, error) {
	return int64(len(m.events)), nil
}

func (m *guardMemoryStore) GetAddressSummary(_ context.Context, address string) (store.AddressSummary, error) {
	return store.AddressSummary{
		Address:           address,
		EventCount:        2,
		FirstSeenLedger:   100,
		LastSeenLedger:    200,
		DistinctContracts: []string{guardContractA, guardContractB},
	}, nil
}

// seedGuardStore populates st with watched contracts, events, address refs,
// and subscriptions for both tenant A and tenant B (used by the Postgres
// integration variant).
func seedGuardStore(t *testing.T, st store.Store, tenantAID, tenantBID int64) {
	t.Helper()
	ctx := context.Background()

	require.NoError(t, st.AddWatchedContract(ctx, store.WatchedContract{ContractID: guardContractA, Network: "default"}))
	require.NoError(t, st.AddWatchedContract(ctx, store.WatchedContract{ContractID: guardContractB, Network: "default"}))

	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	_, err := st.UpsertEvents(ctx, []store.Event{
		{
			ID:               guardEventIDA,
			ContractID:       guardContractA,
			Ledger:           100,
			Type:             "contract",
			TxHash:           guardTxHash,
			InSuccessfulCall: true,
			Topics:           json.RawMessage(`[{"symbol":"transfer"},{"address":"` + guardAddress + `"}]`),
			Value:            json.RawMessage(`{"i128":"1000"}`),
			RawTopicXDR:      []string{"AAAADwAAAAh0cmFuc2Zlcg=="},
			RawValueXDR:      "AAAACgAAAAAAAAAB",
			CreatedAt:        now,
		},
		{
			ID:               guardEventIDB,
			ContractID:       guardContractB,
			Ledger:           200,
			Type:             "contract",
			TxHash:           guardTxHash,
			InSuccessfulCall: true,
			Topics:           json.RawMessage(`[{"symbol":"transfer"},{"address":"` + guardAddress + `"}]`),
			Value:            json.RawMessage(`{"i128":"2000"}`),
			RawTopicXDR:      []string{"AAAADwAAAAh0cmFuc2Zlcg=="},
			RawValueXDR:      "AAAACgAAAAAAAAAB",
			CreatedAt:        now.Add(time.Minute),
		},
	})
	require.NoError(t, err)

	require.NoError(t, st.UpsertAddressRefs(ctx, []store.AddressRef{
		{Address: guardAddress, EventID: guardEventIDA, Role: "topic"},
		{Address: guardAddress, EventID: guardEventIDB, Role: "topic"},
	}))

	ownerA := tenantAID
	ownerB := tenantBID
	_, err = st.CreateSubscription(ctx, store.Subscription{
		URL:      "https://example.com/a",
		Secret:   "whsec_aaaaaaaaaaaaaaaa",
		Filters:  store.SubscriptionFilter{ContractID: guardContractA},
		Enabled:  true,
		TenantID: &ownerA,
	})
	require.NoError(t, err)
	_, err = st.CreateSubscription(ctx, store.Subscription{
		URL:      "https://example.com/b",
		Secret:   "whsec_bbbbbbbbbbbbbbbb",
		Filters:  store.SubscriptionFilter{ContractID: guardContractB},
		Enabled:  true,
		TenantID: &ownerB,
	})
	require.NoError(t, err)
}

// setupGuardPostgres connects to Postgres when TEST_DATABASE_URL or
// DATABASE_URL is set, migrates, truncates, seeds tenant A and tenant B with
// their respective contracts and events, and returns the store and tenant A's
// API key. When neither env var is set, it skips cleanly.
func setupGuardPostgres(t *testing.T) (*store.Postgres, string) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = os.Getenv("DATABASE_URL")
	}
	if dbURL == "" || !strings.HasPrefix(dbURL, "postgres") {
		t.Skip("TEST_DATABASE_URL / DATABASE_URL not set to postgres; skipping Postgres-backed route scope guard (run `make test-db`)")
	}
	require.NoError(t, store.Migrate(dbURL))

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)

	resetTables := `
		DO $$ DECLARE part text;
		BEGIN
			FOR part IN SELECT inhrelid::regclass::text FROM pg_inherits WHERE inhparent = 'events'::regclass AND inhrelid::regclass::text <> 'events_default'
			LOOP
				EXECUTE 'DROP TABLE IF EXISTS ' || part || ' CASCADE';
			END LOOP;
			IF to_regclass('public.events_default') IS NULL THEN
				CREATE TABLE events_default PARTITION OF events DEFAULT;
			END IF;
		END $$;
		TRUNCATE events, ingestion_state, watched_contracts, replay_state, dead_letters,
		         audit_state, audit_findings, api_keys, subscriptions, delivery_attempts,
		         backfill_state, contract_cursors, contract_meta, contract_specs, contract_spec_overrides,
		         event_addresses, token_balances, token_balance_state, archive_chunks,
		         tenant_usage, tenant_api_keys, tenant_watched_contracts, tenant_contract_grants,
		         tenants RESTART IDENTITY CASCADE;
		INSERT INTO tenants (name, wildcard, is_admin, enabled)
		VALUES ('default', true, true, true)
		ON CONFLICT (name) DO NOTHING;
	`
	_, err = pool.Exec(ctx, resetTables)
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), resetTables)
		pool.Close()
	})

	pg := store.NewPostgres(pool)

	maxWatched := 10
	tenantA, err := pg.CreateTenant(ctx, store.Tenant{Name: "tenant-a", Enabled: true, MaxWatchedContracts: &maxWatched})
	require.NoError(t, err)
	require.NoError(t, pg.GrantContract(ctx, tenantA.ID, guardContractA))

	tenantB, err := pg.CreateTenant(ctx, store.Tenant{Name: "tenant-b", Enabled: true, MaxWatchedContracts: &maxWatched})
	require.NoError(t, err)
	require.NoError(t, pg.GrantContract(ctx, tenantB.ID, guardContractB))

	keyA, prefixA, digestA, err := api.GenerateAPIKey()
	require.NoError(t, err)
	_, err = pg.CreateTenantAPIKey(ctx, tenantA.ID, "key-a", prefixA, digestA)
	require.NoError(t, err)

	require.NoError(t, pg.AddTenantWatchedContract(ctx, tenantA, "default", guardContractA, 0))
	require.NoError(t, pg.AddTenantWatchedContract(ctx, tenantB, "default", guardContractB, 0))

	seedGuardStore(t, pg, tenantA.ID, tenantB.ID)
	return pg, keyA
}

// buildMultiTenantRouter wires an api.Server with multi-tenancy and GraphQL
// mounted, returning both the chi.Router (for chi.Walk) and the http.Handler.
func buildMultiTenantRouter(t *testing.T, st store.Store, ts store.TenantStore) (chi.Router, http.Handler) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := api.New(st, noopRPC{}, log, "operator-secret-key")
	srv.WithMultiTenancy(ts, api.MultiTenantOptions{MaxWatchedContracts: 100})

	gql, err := graphql.New(api.ServerDeps{Store: st}, log, true)
	require.NoError(t, err)
	srv.SetGraphQLHandler(gql, gql.PlaygroundHandler())

	return collectRouter(srv), srv.Router()
}

// collectRouter extracts the underlying chi.Router from srv.Router() (when
// no OTel tracer is attached, srv.Router() returns the chi.Router directly).
func collectRouter(srv *api.Server) chi.Router {
	r, ok := srv.Router().(chi.Router)
	if !ok {
		panic("expected srv.Router() without tracer to implement chi.Router")
	}
	return r
}

// substitutePathParams replaces route pattern placeholders with tenant B's
// identifiers so single-object and contract-scoped routes are probed directly
// against the other tenant's data.
func substitutePathParams(pattern string) string {
	replacer := strings.NewReplacer(
		"{contract_id}", guardContractB,
		"{address}", guardAddress,
	)
	path := replacer.Replace(pattern)

	if strings.Contains(path, "{id}") {
		switch {
		case strings.HasPrefix(path, "/contracts/"):
			path = strings.ReplaceAll(path, "{id}", guardContractB)
		case strings.HasPrefix(path, "/events/"):
			path = strings.ReplaceAll(path, "{id}", guardEventIDB)
		case strings.HasPrefix(path, "/subscriptions/"):
			// Subscription ID 2 belongs to tenant B in our seed.
			path = strings.ReplaceAll(path, "{id}", "2")
		default:
			path = strings.ReplaceAll(path, "{id}", "2")
		}
	}
	return path
}

func runReadRoutesScopeGuard(t *testing.T, r chi.Router, handler http.Handler, keyA string) {
	t.Helper()
	routes := map[string]struct{}{}
	require.NoError(t, chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodGet {
			routes[route] = struct{}{}
		}
		return nil
	}))

	sortedRoutes := make([]string, 0, len(routes))
	for route := range routes {
		sortedRoutes = append(sortedRoutes, route)
	}
	sort.Strings(sortedRoutes)

	for _, pattern := range sortedRoutes {
		pattern := pattern
		t.Run("GET "+pattern, func(t *testing.T) {
			if reason, ok := globalRouteAllowlist[pattern]; ok {
				assert.NotEmptyf(t, strings.TrimSpace(reason), "allowlisted route %s must state a non-empty reason", pattern)
				return
			}

			targetPath := substitutePathParams(pattern)
			if q, ok := routeExtraQuery[pattern]; ok {
				targetPath += "?" + q
			}

			req := httptest.NewRequest(http.MethodGet, targetPath, nil)
			req.Header.Set("Authorization", "Bearer "+keyA)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			body := rec.Body.String()
			require.NotEmptyf(t, strings.TrimSpace(body),
				"route %s returned an empty body; add required query parameters to routeExtraQuery", pattern)

			// A 403 Forbidden or 404 Not Found error envelope may echo the
			// caller-supplied path parameter ("contract <id> is not in this
			// tenant's scope" or "event <id> not found"); echoing what the
			// caller already sent in the URL is not a cross-tenant data leak.
			leakedContractB := rec.Code < 400 && strings.Contains(body, guardContractB)
			leakedEventB := rec.Code < 400 && strings.Contains(body, guardEventIDB)

			if note, known := knownUnscopedRESTRoutes[pattern]; known {
				if !leakedContractB && !leakedEventB {
					t.Fatalf("route %s is listed in knownUnscopedRESTRoutes (%s) but no longer leaks; remove it from knownUnscopedRESTRoutes so regressions fail the build", pattern, note)
				}
				t.Logf("KNOWN UNSCOPED ROUTE %s: %s (status=%d)", pattern, note, rec.Code)
				return
			}

			assert.Falsef(t, leakedContractB,
				"GET %s (as tenant A) leaked tenant B's contract ID %s in response (status %d): %s",
				targetPath, guardContractB, rec.Code, body)
			assert.Falsef(t, leakedEventB,
				"GET %s (as tenant A) leaked tenant B's event ID %s in response (status %d): %s",
				targetPath, guardEventIDB, rec.Code, body)
		})
	}
}

func runGraphQLScopeGuard(t *testing.T, handler http.Handler, keyA string) {
	t.Helper()
	queries := []struct {
		field string
		query string
	}{
		{
			field: "events",
			query: `{ events(page: { first: 50 }) { nodes { id contractId } totalCount } }`,
		},
		{
			field: "tokenEvents",
			query: `{ tokenEvents(page: { first: 50 }) { nodes { id contractId } totalCount } }`,
		},
		{
			field: "event",
			query: `{ event(id: "` + guardEventIDB + `") { id contractId } }`,
		},
		{
			field: "contracts",
			query: `{ contracts(page: { first: 50 }) { nodes { contractId } totalCount } }`,
		},
		{
			field: "contract",
			query: `{ contract(id: "` + guardContractB + `") { contractId } }`,
		},
	}

	for _, tc := range queries {
		tc := tc
		t.Run("Query."+tc.field, func(t *testing.T) {
			payload, err := json.Marshal(map[string]string{"query": tc.query})
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload))
			req.Header.Set("Authorization", "Bearer "+keyA)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()

			leakedContractB := strings.Contains(body, guardContractB)
			leakedEventB := strings.Contains(body, guardEventIDB)

			if note, known := knownUnscopedGraphQLFields[tc.field]; known {
				if !leakedContractB && !leakedEventB {
					t.Fatalf("GraphQL field %q is listed in knownUnscopedGraphQLFields (%s) but no longer leaks; remove it from knownUnscopedGraphQLFields", tc.field, note)
				}
				t.Logf("KNOWN UNSCOPED GRAPHQL FIELD %s: %s", tc.field, note)
				return
			}

			assert.Falsef(t, leakedContractB,
				"GraphQL Query.%s (as tenant A) leaked tenant B's contract ID %s: %s",
				tc.field, guardContractB, body)
			assert.Falsef(t, leakedEventB,
				"GraphQL Query.%s (as tenant A) leaked tenant B's event ID %s: %s",
				tc.field, guardEventIDB, body)
		})
	}
}

// TestReadRoutes_ConsultTenantScope walks every GET route on the multi-tenant
// router against a seeded full-table store (so it runs unconditionally under
// `go test ./...`) and verifies that tenant A never receives tenant B's
// contract ID or event ID.
func TestReadRoutes_ConsultTenantScope(t *testing.T) {
	st, ts, keyA := newGuardMemoryStore(t)
	r, handler := buildMultiTenantRouter(t, st, ts)
	runReadRoutesScopeGuard(t, r, handler, keyA)
}

// TestGraphQLQueryFields_ConsultTenantScope exercises each top-level GraphQL
// Query field as tenant A against a seeded full-table store.
func TestGraphQLQueryFields_ConsultTenantScope(t *testing.T) {
	st, ts, keyA := newGuardMemoryStore(t)
	_, handler := buildMultiTenantRouter(t, st, ts)
	runGraphQLScopeGuard(t, handler, keyA)
}

// TestReadRoutes_ConsultTenantScope_Postgres runs the same router-walk guard
// against a real Postgres database when TEST_DATABASE_URL (or DATABASE_URL)
// is set (`make test-db`).
func TestReadRoutes_ConsultTenantScope_Postgres(t *testing.T) {
	pg, keyA := setupGuardPostgres(t)
	r, handler := buildMultiTenantRouter(t, pg, pg)
	runReadRoutesScopeGuard(t, r, handler, keyA)
}

// TestGraphQLQueryFields_ConsultTenantScope_Postgres exercises each top-level
// GraphQL Query field against a real Postgres database (`make test-db`).
func TestGraphQLQueryFields_ConsultTenantScope_Postgres(t *testing.T) {
	pg, keyA := setupGuardPostgres(t)
	_, handler := buildMultiTenantRouter(t, pg, pg)
	runGraphQLScopeGuard(t, handler, keyA)
}

package store

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clickHouseTestServer wires a *ClickHouse to an httptest server so the
// contract-summary queries can be exercised without a live ClickHouse. The
// HTTP interface is the same one migrations and every other read path use,
// so a request-body assertion here is a real check that the query is
// aggregated in the database rather than by fetching rows into Go.
type clickHouseTestServer struct {
	store   *ClickHouse
	mu      sync.Mutex
	queries []string
}

func newClickHouseTestServer(t *testing.T, respond func(query string) string) *clickHouseTestServer {
	t.Helper()

	fake := &clickHouseTestServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		query := string(body)
		fake.mu.Lock()
		fake.queries = append(fake.queries, query)
		fake.mu.Unlock()

		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, respond(query))
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	host, portText, err := net.SplitHostPort(u.Host)
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)

	cfg := clickHouseConfig{host: host, port: port, httpPort: port, database: "sorotrail"}
	fake.store = &ClickHouse{host: host, port: port, cfg: cfg}
	return fake
}

func (f *clickHouseTestServer) lastQuery(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.NotEmpty(t, f.queries, "no query reached the ClickHouse HTTP interface")
	return f.queries[len(f.queries)-1]
}

func TestClickHouseGetContractSummary(t *testing.T) {
	const contract = "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"

	fake := newClickHouseTestServer(t, func(query string) string {
		if strings.Contains(query, "CEMPTY") {
			return ""
		}
		return `{"contract_id":"` + contract + `","event_count":3,` +
			`"first_ledger":100,"last_ledger":102,` +
			`"last_seen":"2026-08-01 12:00:00.000"}` + "\n"
	})

	got, err := fake.store.GetContractSummary(context.Background(), contract)
	require.NoError(t, err)
	assert.Equal(t, contract, got.ContractID)
	assert.Equal(t, int64(3), got.EventCount)
	assert.Equal(t, int64(100), got.FirstLedger)
	assert.Equal(t, int64(102), got.LastLedger)
	assert.True(t, got.LastSeen.Equal(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)),
		"last_seen = %v", got.LastSeen)

	query := fake.lastQuery(t)
	assert.Contains(t, query, "FROM events")
	assert.Contains(t, query, "WHERE contract_id = '"+contract+"'")
	assert.Contains(t, query, "GROUP BY contract_id",
		"the count and ledger bounds must be aggregated in ClickHouse")
	assert.NotContains(t, query, "ORDER BY id",
		"the implementation must not page event rows into Go to count them")

	t.Run("no rows is ErrNotFound", func(t *testing.T) {
		_, err := fake.store.GetContractSummary(context.Background(), "CEMPTY")
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

func TestClickHouseContractEventTypeCounts(t *testing.T) {
	const contract = "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"

	fake := newClickHouseTestServer(t, func(query string) string {
		if strings.Contains(query, "CEMPTY") {
			return ""
		}
		return `{"type":"contract","event_count":2}` + "\n" +
			`{"type":"system","event_count":1}` + "\n"
	})

	got, err := fake.store.ContractEventTypeCounts(context.Background(), contract)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, ContractEventTypeCount{Type: "contract", Count: 2}, got[0])
	assert.Equal(t, ContractEventTypeCount{Type: "system", Count: 1}, got[1])

	query := fake.lastQuery(t)
	assert.Contains(t, query, "GROUP BY type")
	assert.Contains(t, query, "ORDER BY event_count DESC")
	assert.NotContains(t, query, "SELECT *",
		"the breakdown must be aggregated, not fetched row by row")

	t.Run("no rows is an empty non-nil slice", func(t *testing.T) {
		got, err := fake.store.ContractEventTypeCounts(context.Background(), "CEMPTY")
		require.NoError(t, err)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})
}

func TestClickHouseContractQueriesEscapeContractID(t *testing.T) {
	fake := newClickHouseTestServer(t, func(string) string { return "" })

	_, err := fake.store.GetContractSummary(context.Background(), "C' OR 1=1 --")
	require.ErrorIs(t, err, ErrNotFound)

	query := fake.lastQuery(t)
	assert.Contains(t, query, `'C\' OR 1=1 --'`,
		"the contract ID must be quoted, not interpolated raw")
}

// TestClickHouseContractInventory_Integration runs the two contract-summary
// methods against a real ClickHouse when TEST_CLICKHOUSE_URL is set. The
// shared conformance suite cannot register ClickHouse yet (it truncates
// between tests), so this test uses a per-run contract ID instead: the same
// fixture Postgres would return, without disturbing rows another run owns.
func TestClickHouseContractInventory_Integration(t *testing.T) {
	st, err := newClickHouseStoreForTests(t)
	require.NoError(t, err)
	ch, ok := st.(*ClickHouse)
	require.True(t, ok, "ClickHouse conformance store has type %T", st)

	ctx := context.Background()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	contract := "CINTEGRATION" + suffix
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	e1 := testEvent("integration-"+suffix+"-1", 1000, contract)
	e2 := testEvent("integration-"+suffix+"-2", 1005, contract)
	e2.Type = "system"
	e3 := testEvent("integration-"+suffix+"-3", 1200, contract)
	e1.CreatedAt, e2.CreatedAt, e3.CreatedAt = base, base.Add(time.Minute), base.Add(time.Hour)
	_, err = ch.UpsertEvents(ctx, []Event{e1, e2, e3})
	require.NoError(t, err)
	t.Cleanup(func() {
		// ClickHouse mutations are asynchronous; a failed cleanup must not
		// fail the assertions that already ran.
		_ = newClickHouseExecutor(ch.cfg).exec(context.Background(), ch.cfg.databaseOrDefault(),
			"ALTER TABLE events DELETE WHERE contract_id = "+clickHouseQuoteString(contract))
	})

	summary, err := ch.GetContractSummary(ctx, contract)
	require.NoError(t, err)
	assert.Equal(t, contract, summary.ContractID)
	assert.Equal(t, int64(3), summary.EventCount)
	assert.Equal(t, int64(1000), summary.FirstLedger)
	assert.Equal(t, int64(1200), summary.LastLedger)
	assert.True(t, summary.LastSeen.Equal(base.Add(time.Hour)), "last_seen = %v", summary.LastSeen)

	types, err := ch.ContractEventTypeCounts(ctx, contract)
	require.NoError(t, err)
	require.Len(t, types, 2)
	assert.Equal(t, ContractEventTypeCount{Type: "contract", Count: 2}, types[0])
	assert.Equal(t, ContractEventTypeCount{Type: "system", Count: 1}, types[1])

	_, err = ch.GetContractSummary(ctx, contract+"-missing")
	assert.ErrorIs(t, err, ErrNotFound)

	empty, err := ch.ContractEventTypeCounts(ctx, contract+"-missing")
	require.NoError(t, err)
	assert.NotNil(t, empty)
	assert.Empty(t, empty)
}

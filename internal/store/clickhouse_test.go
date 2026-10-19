package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseClickHouseURL(t *testing.T) {
	cfg, err := parseClickHouseConfig("clickhouse://user:pass@localhost:9000/sorotrail?sslmode=disable")
	require.NoError(t, err)
	assert.Equal(t, "localhost", cfg.host)
	assert.Equal(t, 9000, cfg.port)
	assert.Equal(t, "user", cfg.username)
	assert.Equal(t, "pass", cfg.password)
	assert.Equal(t, "sorotrail", cfg.database)
	assert.False(t, cfg.ssl)
}

func TestNewStoreFromURL(t *testing.T) {
	postgresStore, err := NewStoreFromURL("postgres://user:pass@localhost:5432/sorotrail")
	require.NoError(t, err)
	assert.IsType(t, &Postgres{}, postgresStore)

	clickhouseStore, err := NewStoreFromURL("clickhouse://user:pass@localhost:9000/sorotrail")
	require.NoError(t, err)
	assert.IsType(t, &ClickHouse{}, clickhouseStore)
}

// --- fake ClickHouse HTTP interface ---
//
// CI has no ClickHouse server: every server-backed test gates on
// TEST_CLICKHOUSE_URL and skips there. The contracts issues #523 and #524
// pin — ingestion state surviving a save/load cycle, and Ping performing a
// real round trip — therefore run against an in-process stand-in for the
// HTTP interface the backend actually speaks. The fake records every
// statement it receives, keeps INSERTed rows in insertion order, and answers
// the ingestion_state FINAL read the way ReplacingMergeTree(updated_at)
// resolves it: the newest version wins, the later insertion wins a tie.

type fakeClickHouseHTTP struct {
	server *httptest.Server

	mu         sync.Mutex
	statements []string
	ingestion  []map[string]any
}

func newFakeClickHouseHTTP(t *testing.T) *fakeClickHouseHTTP {
	t.Helper()
	f := &fakeClickHouseHTTP{}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeClickHouseHTTP) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stmt := string(body)

	f.mu.Lock()
	f.statements = append(f.statements, stmt)
	f.mu.Unlock()

	switch {
	case strings.HasPrefix(stmt, "SELECT 1 FORMAT TabSeparated"):
		_, _ = io.WriteString(w, "1\n")
	case strings.HasPrefix(stmt, "INSERT INTO ingestion_state "):
		if err := f.recordIngestionInsert(stmt); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	case strings.Contains(stmt, "FROM ingestion_state FINAL"):
		if row := f.latestIngestionRow(stmt); row != nil {
			encoded, err := json.Marshal(row)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(encoded)
		}
	default:
		http.Error(w, "fake clickhouse: unexpected statement: "+truncateForError(stmt),
			http.StatusInternalServerError)
	}
}

// recordIngestionInsert decodes the JSONEachRow lines that follow the
// INSERT header, mirroring what the real server would persist.
func (f *fakeClickHouseHTTP) recordIngestionInsert(stmt string) error {
	_, rowsJSON, found := strings.Cut(stmt, "\n")
	if !found {
		return fmt.Errorf("insert statement carries no rows: %s", truncateForError(stmt))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, line := range strings.Split(strings.TrimSpace(rowsJSON), "\n") {
		if line == "" {
			continue
		}
		var row map[string]any
		dec := json.NewDecoder(strings.NewReader(line))
		dec.UseNumber()
		if err := dec.Decode(&row); err != nil {
			return fmt.Errorf("decoding inserted row: %w", err)
		}
		f.ingestion = append(f.ingestion, row)
	}
	return nil
}

// latestIngestionRow answers the FINAL read for the network the statement
// names, collapsing appended versions the way ReplacingMergeTree(updated_at)
// does.
func (f *fakeClickHouseHTTP) latestIngestionRow(stmt string) map[string]any {
	network := "default"
	if _, after, ok := strings.Cut(stmt, "network = '"); ok {
		if name, _, ok := strings.Cut(after, "'"); ok {
			network = name
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var newest map[string]any
	newestVersion := ""
	for _, row := range f.ingestion {
		if row["network"] != network {
			continue
		}
		version, _ := row["updated_at"].(string)
		if newest == nil || version >= newestVersion {
			newest = row
			newestVersion = version
		}
	}
	return newest
}

func (f *fakeClickHouseHTTP) ingestionRows() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.ingestion))
	copy(out, f.ingestion)
	return out
}

func (f *fakeClickHouseHTTP) statementsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.statements))
	copy(out, f.statements)
	return out
}

func (f *fakeClickHouseHTTP) newStore(t *testing.T) *ClickHouse {
	t.Helper()
	return newClickHouseStoreAt(t, f.server.Listener.Addr().String())
}

// newClickHouseStoreAt builds the backend exactly the way NewStoreFromURL
// does for a real deployment, pointed at addr's HTTP port (the interface
// every ClickHouse method in this package talks to).
func newClickHouseStoreAt(t *testing.T, addr string) *ClickHouse {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	st, err := NewStoreFromURL("clickhouse://" + addr + "/default?http_port=" + port)
	require.NoError(t, err)
	ch, ok := st.(*ClickHouse)
	require.True(t, ok, "a clickhouse:// URL must select the ClickHouse backend")
	return ch
}

// TestClickHouse_IngestionStateRoundTrip pins issue #523's "done" list:
// state written by SaveIngestionState reads back through GetIngestionState,
// survives a simulated process restart so a mid-ingest restart resumes from
// the persisted position, and a fresh instance reports ErrNotFound instead
// of a fabricated zero position it could mistake for "ledger 0".
func TestClickHouse_IngestionStateRoundTrip(t *testing.T) {
	fake := newFakeClickHouseHTTP(t)
	st := fake.newStore(t)
	ctx := context.Background()

	_, err := st.GetIngestionState(ctx)
	require.ErrorIs(t, err, ErrNotFound,
		"a fresh instance must cold-start rather than resume from a zero position")

	poll := time.Date(2026, 10, 19, 9, 30, 0, 0, time.UTC)
	require.NoError(t, st.SaveIngestionState(ctx, IngestionState{
		LastIngestedLedger: 42,
		LastCursor:         "ledger=42-cursor",
		LastSuccessfulPoll: &poll,
	}))

	got, err := st.GetIngestionState(ctx)
	require.NoError(t, err)
	assert.Equal(t, "default", got.Network, "an empty Network must round-trip as the default network")
	assert.Equal(t, int64(42), got.LastIngestedLedger)
	assert.Equal(t, "ledger=42-cursor", got.LastCursor)
	require.NotNil(t, got.LastSuccessfulPoll)
	assert.True(t, got.LastSuccessfulPoll.Equal(poll),
		"last_successful_poll must survive the round trip: got %v, want %v", got.LastSuccessfulPoll, poll)
	assert.False(t, got.UpdatedAt.IsZero(), "updated_at must be stamped on write")

	// A restart discards every in-memory value; whatever a brand-new store
	// reads back is the position ingestion will resume from.
	restarted := fake.newStore(t)
	got, err = restarted.GetIngestionState(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(42), got.LastIngestedLedger,
		"a restarted process must resume from the persisted position, not the retention window")

	// Advancing overwrites the singleton: FINAL must collapse the appended
	// versions to the newest save, and SaveIngestionState must replace every
	// field the way the Postgres upsert does.
	require.NoError(t, restarted.SaveIngestionState(ctx, IngestionState{
		LastIngestedLedger: 43,
		LastCursor:         "ledger=43-cursor",
	}))
	got, err = st.GetIngestionState(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(43), got.LastIngestedLedger, "the newest save must win under FINAL")
	assert.Equal(t, "ledger=43-cursor", got.LastCursor)
	assert.Nil(t, got.LastSuccessfulPoll, "an unset poll must overwrite the previous value, like Postgres")

	rows := fake.ingestionRows()
	require.Len(t, rows, 2, "each save appends exactly one version")
	firstRaw, ok := rows[0]["updated_at"].(string)
	require.True(t, ok, "updated_at must be written as a timestamp string")
	secondRaw, ok := rows[1]["updated_at"].(string)
	require.True(t, ok, "updated_at must be written as a timestamp string")
	first, err := parseClickHouseTime(firstRaw)
	require.NoError(t, err)
	second, err := parseClickHouseTime(secondRaw)
	require.NoError(t, err)
	assert.True(t, second.After(first),
		"back-to-back saves must carry strictly increasing versions so FINAL cannot pick the older row: %v then %v",
		first, second)
}

// TestClickHouse_PingIssuesRealRoundTrip pins issue #524's first criterion:
// Ping must put an actual statement on the wire to the HTTP interface every
// query uses, not open a socket (or nothing at all) and report success.
func TestClickHouse_PingIssuesRealRoundTrip(t *testing.T) {
	fake := newFakeClickHouseHTTP(t)
	st := fake.newStore(t)

	require.NoError(t, st.Ping(context.Background()))

	seen := fake.statementsSeen()
	require.Len(t, seen, 1, "Ping must issue exactly one statement per probe")
	assert.Contains(t, seen[0], "SELECT 1",
		"the probe must be a query the server answers, so a dead interface fails it")
}

// TestClickHouse_PingFailsWhenUnreachable pins the readiness contract: a
// database nothing answers for — and a backend with no configuration at
// all — must make Ping return an error, never nil.
func TestClickHouse_PingFailsWhenUnreachable(t *testing.T) {
	ctx := context.Background()

	// Reserve a port, then release it so the address is guaranteed closed:
	// nothing will accept the probe's connection.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	st := newClickHouseStoreAt(t, addr)
	require.Error(t, st.Ping(ctx),
		"an unreachable database must fail the probe so /readyz can drop the instance")

	require.ErrorContains(t, (&ClickHouse{}).Ping(ctx), "not configured",
		"an unconfigured backend must refuse to probe rather than report healthy")
}

// TestClickHouse_PingHonorsContextDeadline pins the probe's third
// criterion: a server that accepts the connection but never answers must
// not stall the probe past the caller's deadline — that deadline is what
// bounds each check behind /readyz.
func TestClickHouse_PingHonorsContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	defer srv.Close()

	st := newClickHouseStoreAt(t, srv.Listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := st.Ping(ctx)
	elapsed := time.Since(start)

	require.ErrorIs(t, err, context.DeadlineExceeded,
		"the caller's deadline must surface as the probe's failure")
	assert.Less(t, elapsed, 10*time.Second,
		"the probe must return at the caller's deadline, not wait out the unresponsive server")
}

// TestClickHouseIngestionStateVersionIsMonotonic covers the version mint
// behind SaveIngestionState directly: two saves inside one millisecond (or
// a wall clock stepping backwards) must still stamp strictly newer
// ReplacingMergeTree versions, otherwise FINAL may collapse back onto the
// older resume position.
func TestClickHouseIngestionStateVersionIsMonotonic(t *testing.T) {
	base := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)

	first := clickHouseIngestionStateVersion(base)
	second := clickHouseIngestionStateVersion(base)
	third := clickHouseIngestionStateVersion(base.Add(-time.Hour))

	assert.True(t, second.After(first),
		"two saves in the same millisecond must not tie: %v then %v", first, second)
	assert.True(t, third.After(second),
		"a regressing wall clock must not rewind the version: %v then %v", second, third)
}

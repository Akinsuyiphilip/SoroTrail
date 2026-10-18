package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/store"
)

func int64p(v int64) *int64 { return &v }

// TestStatsRows verifies the metric rows rendered for each shape of
// store.Stats: a fully populated report, one where the backend has no
// chain head yet, and a fresh store that has never polled.
func TestStatsRows(t *testing.T) {
	t.Parallel()
	fixedTime := time.Date(2026, 9, 24, 12, 30, 0, 0, time.UTC)
	tests := []struct {
		name  string
		stats store.Stats
		want  map[string]string
	}{
		{
			name: "fully populated",
			stats: store.Stats{
				TotalEvents:           123456,
				LastIngestedLedger:    987654,
				VerifiedThroughLedger: 987000,
				OldestStoredLedger:    100000,
				ChainHeadLedger:       int64p(987700),
				IngestLagLedgers:      int64p(46),
				LastSuccessfulPoll:    &fixedTime,
				ContractCount:         12,
				WatchedContracts:      5,
				TableSizeBytes:        134744064, // 128.5 MiB
			},
			want: map[string]string{
				"total events":            "123456",
				"last ingested ledger":    "987654",
				"verified through ledger": "987000",
				"oldest stored ledger":    "100000",
				"chain head ledger":       "987700",
				"ingest lag":              "46 ledgers",
				"last successful poll":    "2026-09-24 12:30:00",
				"contracts":               "12",
				"watched contracts":       "5",
				"events table size":       "128.5 MiB",
			},
		},
		{
			name: "no chain head yet",
			stats: store.Stats{
				TotalEvents:        1,
				LastIngestedLedger: 42,
				OldestStoredLedger: 42,
				TableSizeBytes:     512,
			},
			want: map[string]string{
				"chain head ledger":    "-",
				"ingest lag":           "-",
				"last successful poll": "never",
				"events table size":    "512 B",
			},
		},
		{
			name:  "fresh empty store",
			stats: store.Stats{},
			want: map[string]string{
				"total events":         "0",
				"last ingested ledger": "0",
				"chain head ledger":    "-",
				"ingest lag":           "-",
				"last successful poll": "never",
				"events table size":    "0 B",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rows := statsRows(tt.stats)
			got := make(map[string]string, len(rows))
			for _, r := range rows {
				got[r.metric] = r.value
			}
			for metric, want := range tt.want {
				assert.Equal(t, want, got[metric], "metric %q", metric)
			}
		})
	}
}

// TestStatsRowsHasExpectedMetrics guards the row set: every metric an
// operator expects from `sorotrail stats` must be present exactly once.
func TestStatsRowsHasExpectedMetrics(t *testing.T) {
	t.Parallel()
	want := []string{
		"total events",
		"last ingested ledger",
		"verified through ledger",
		"oldest stored ledger",
		"chain head ledger",
		"ingest lag",
		"last successful poll",
		"contracts",
		"watched contracts",
		"events table size",
	}
	rows := statsRows(store.Stats{})
	require.Equal(t, len(want), len(rows), "statsRows must emit one row per expected metric")
	for i, r := range rows {
		assert.Equal(t, want[i], r.metric, "row %d", i)
	}
}

// TestRenderStatsTable verifies the table header and that values land
// in the output stream.
func TestRenderStatsTable(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	err := renderStatsTable(&buf, store.Stats{
		TotalEvents:        7,
		LastIngestedLedger: 1000,
		TableSizeBytes:     2048,
	})
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "METRIC")
	assert.Contains(t, out, "VALUE")
	assert.Contains(t, out, "total events")
	assert.Contains(t, out, "7")
	assert.Contains(t, out, "2.0 KiB")
}

// TestRunStatsArgs covers the subcommand's flag handling: --help and
// unexpected arguments must be resolved before any database connection
// is attempted.
func TestRunStatsArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "help short", args: []string{"-h"}},
		{name: "help long", args: []string{"--help"}},
		{name: "unexpected arg", args: []string{"extra"}, wantErr: true},
		{name: "unknown flag", args: []string{"--bogus"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			err := runStatsTo(&buf, tt.args)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestStatsUsageMentionsReadOnly checks the help text documents the
// read-only contract and the process-local-counter exclusion.
func TestStatsUsageMentionsReadOnly(t *testing.T) {
	t.Parallel()
	assert.Contains(t, statsUsage, "usage: sorotrail stats")
	assert.Contains(t, statsUsage, "Read-only")
	assert.Contains(t, statsUsage, "/stats endpoint")
}

type stubStatsStore struct {
	stats store.Stats
	err   error
}

func (s *stubStatsStore) Stats(context.Context, store.Scope) (store.Stats, error) {
	return s.stats, s.err
}

// TestRunStats_KnownContents verifies that runStats against a database with known
// contents outputs the exact fixture numbers.
func TestRunStats_KnownContents(t *testing.T) {
	orig := openStatsStore
	defer func() { openStatsStore = orig }()

	fixedTime := time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC)
	stub := &stubStatsStore{
		stats: store.Stats{
			TotalEvents:           54321,
			LastIngestedLedger:    123456,
			VerifiedThroughLedger: 123400,
			OldestStoredLedger:    1000,
			ChainHeadLedger:       int64p(123460),
			IngestLagLedgers:      int64p(4),
			LastSuccessfulPoll:    &fixedTime,
			ContractCount:         8,
			WatchedContracts:      3,
			TableSizeBytes:        1048576, // 1.0 MiB
		},
	}
	openStatsStore = func(context.Context) (statsStore, func(), error) {
		return stub, func() {}, nil
	}

	var buf bytes.Buffer
	err := runStatsTo(&buf, nil)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "METRIC")
	assert.Contains(t, out, "VALUE")
	assert.Regexp(t, `total events\s+54321`, out)
	assert.Regexp(t, `last ingested ledger\s+123456`, out)
	assert.Regexp(t, `verified through ledger\s+123400`, out)
	assert.Regexp(t, `oldest stored ledger\s+1000`, out)
	assert.Regexp(t, `chain head ledger\s+123460`, out)
	assert.Regexp(t, `ingest lag\s+4 ledgers`, out)
	assert.Regexp(t, `last successful poll\s+2026-10-15 14:00:00`, out)
	assert.Regexp(t, `contracts\s+8`, out)
	assert.Regexp(t, `watched contracts\s+3`, out)
	assert.Regexp(t, `events table size\s+1.0 MiB`, out)
}

// TestRunStats_EmptyDatabase verifies that an empty database reports zeroes clearly
// and exits 0 rather than erroring or printing a partial report.
func TestRunStats_EmptyDatabase(t *testing.T) {
	orig := openStatsStore
	defer func() { openStatsStore = orig }()

	stub := &stubStatsStore{
		stats: store.Stats{},
	}
	openStatsStore = func(context.Context) (statsStore, func(), error) {
		return stub, func() {}, nil
	}

	var buf bytes.Buffer
	err := runStatsTo(&buf, nil)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "METRIC")
	assert.Contains(t, out, "VALUE")
	assert.Regexp(t, `total events\s+0`, out)
	assert.Regexp(t, `last ingested ledger\s+0`, out)
	assert.Regexp(t, `verified through ledger\s+0`, out)
	assert.Regexp(t, `oldest stored ledger\s+0`, out)
	assert.Regexp(t, `chain head ledger\s+-`, out)
	assert.Regexp(t, `ingest lag\s+-`, out)
	assert.Regexp(t, `last successful poll\s+never`, out)
	assert.Regexp(t, `contracts\s+0`, out)
	assert.Regexp(t, `watched contracts\s+0`, out)
	assert.Regexp(t, `events table size\s+0 B`, out)
}

// TestRunStats_DatabaseUnreachable verifies that failing to open the store
// exits non-zero, distinguishable from a successful report of zero rows.
func TestRunStats_DatabaseUnreachable(t *testing.T) {
	orig := openStatsStore
	defer func() { openStatsStore = orig }()

	dbErr := errors.New("connection to postgres refused")
	openStatsStore = func(context.Context) (statsStore, func(), error) {
		return nil, nil, dbErr
	}

	var buf bytes.Buffer
	err := runStatsTo(&buf, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, dbErr)
	assert.Equal(t, 1, exitCode(err), "database unreachable must map to exit code 1")
	assert.Empty(t, buf.String(), "no stats table should be rendered on connection failure")
}

// TestRunStats_StatsQueryError verifies that an error reading stats from the
// store returns an error and maps to non-zero exit code.
func TestRunStats_StatsQueryError(t *testing.T) {
	orig := openStatsStore
	defer func() { openStatsStore = orig }()

	queryErr := errors.New("relation events does not exist")
	stub := &stubStatsStore{err: queryErr}
	openStatsStore = func(context.Context) (statsStore, func(), error) {
		return stub, func() {}, nil
	}

	var buf bytes.Buffer
	err := runStatsTo(&buf, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading store stats: relation events does not exist")
	assert.Equal(t, 1, exitCode(err))
}

// TestRunStatsDirect verifies that calling runStats directly writes to stdout
// and completes without error.
func TestRunStatsDirect(t *testing.T) {
	orig := openStatsStore
	defer func() { openStatsStore = orig }()

	stub := &stubStatsStore{
		stats: store.Stats{TotalEvents: 99},
	}
	openStatsStore = func(context.Context) (statsStore, func(), error) {
		return stub, func() {}, nil
	}

	r, w, err := os.Pipe()
	require.NoError(t, err)
	oldStdout := os.Stdout
	os.Stdout = w

	runErr := runStats(nil)
	w.Close()
	os.Stdout = oldStdout

	require.NoError(t, runErr)
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	assert.Contains(t, buf.String(), "METRIC")
	assert.Contains(t, buf.String(), "total events")
	assert.Contains(t, buf.String(), "99")
}

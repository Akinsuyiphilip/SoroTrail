package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sorotrail/sorotrail/internal/store"
)

func TestPrintMigrationStatus(t *testing.T) {
	tests := []struct {
		name   string
		status store.MigrationStatus
		want   string
	}{
		{
			name:   "no migrations applied",
			status: store.MigrationStatus{Pending: []uint{1, 2}},
			want:   "migration status: no migrations applied (2 pending)\n",
		},
		{
			name: "dirty with pending migrations",
			status: store.MigrationStatus{
				Version: 12,
				Dirty:   true,
				Pending: []uint{13, 14},
			},
			want: "migration status: version=12 (DIRTY — manual repair required), 2 pending\n  pending versions: [13 14]\n",
		},
		{
			name: "clean with no pending migrations",
			status: store.MigrationStatus{
				Version: 28,
				Pending: []uint{},
			},
			want: "migration status: version=28 (clean), 0 pending\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := captureStdout(t, func() {
				printMigrationStatus(tt.status)
			})
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRunMigrateStatusHelp(t *testing.T) {
	// --help should not return an error.
	err := runMigrateStatus([]string{"--help"})
	assert.NoError(t, err, "--help should not return an error")
}

func TestRunMigrateStatusUnknownFlag(t *testing.T) {
	err := runMigrateStatus([]string{"--unknown-flag"})
	assert.Error(t, err, "unknown flag should return an error")
}

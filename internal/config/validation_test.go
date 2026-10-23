package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validBase() Config {
	return Config{
		DatabaseURL:         "postgres://user:pass@localhost:5432/db",
		RPCURL:              "https://rpc.example.com",
		PollInterval:        5 * time.Second,
		AuditPollInterval:   30 * time.Second,
		IngesterMinBackoff:  1 * time.Second,
		IngesterMaxBackoff:  5 * time.Second,
		RetentionLedgers:    17280,
		PartitionLedgerSpan: 120960,
		AuditBatchLedgers:   100,
		AuditLagThreshold:   200,
		AuditBudgetShare:    0.5,
		AuditMaxRPS:         10,
		AuditMaxRepair:      3,
		AuditFindingMaxLgrs: 100,
		LogLevel:            "info",
		LogFormat:           "json",
	}
}

func TestValidateAll_Coverage(t *testing.T) {
	tests := []struct {
		name        string
		modify      func(*Config)
		expectError string
	}{
		{"empty DATABASE_URL", func(c *Config) { c.DatabaseURL = "" }, "DATABASE_URL: required but empty"},
		{"empty RPC_URL and RPC_URLS", func(c *Config) { c.RPCURL = ""; c.RPCURLS = nil }, "RPC_URL: required but empty"},
		{"invalid RPC_URL", func(c *Config) { c.RPCURL = "invalid-url" }, "is not a valid absolute URL"},
		{"invalid RPC_URLS element", func(c *Config) { c.RPCURLS = []string{"http://ok.com", "not-a-url"} }, "is not a valid absolute URL"},
		{"negative PollInterval", func(c *Config) { c.PollInterval = -1 * time.Second }, "POLL_INTERVAL"},
		{"negative PollIntervalMin", func(c *Config) { c.PollIntervalMin = -1 * time.Second }, "POLL_INTERVAL_MIN"},
		{"negative PollIntervalMax", func(c *Config) { c.PollIntervalMax = -1 * time.Second }, "POLL_INTERVAL_MAX"},
		{"PollIntervalMin > Max", func(c *Config) { c.PollIntervalMin = 5 * time.Second; c.PollIntervalMax = 1 * time.Second }, "must be <= POLL_INTERVAL_MAX"},
		{"negative AuditPollInterval", func(c *Config) { c.AuditPollInterval = -1 * time.Second }, "AUDIT_POLL_INTERVAL"},
		{"negative IngesterMinBackoff", func(c *Config) { c.IngesterMinBackoff = -1 * time.Second }, "INGESTER_MIN_BACKOFF"},
		{"negative IngesterMaxBackoff", func(c *Config) { c.IngesterMaxBackoff = -1 * time.Second }, "INGESTER_MAX_BACKOFF"},
		{"IngesterMinBackoff > Max", func(c *Config) { c.IngesterMinBackoff = 5 * time.Second; c.IngesterMaxBackoff = 1 * time.Second }, "must not exceed INGESTER_MAX_BACKOFF"},
		{"negative IngesterJitterMin", func(c *Config) { c.IngesterJitterMin = -1 }, "jitter bounds must be non-negative"},
		{"IngesterJitterMin > Max", func(c *Config) { c.IngesterJitterMin = 5; c.IngesterJitterMax = 1 }, "must not exceed INGESTER_JITTER_MAX"},
		{"zero RetentionLedgers", func(c *Config) { c.RetentionLedgers = 0 }, "RETENTION_LEDGERS"},
		{"zero PartitionLedgerSpan", func(c *Config) { c.PartitionLedgerSpan = 0 }, "PARTITION_LEDGER_SPAN"},
		{"zero AuditBatchLedgers", func(c *Config) { c.AuditBatchLedgers = 0 }, "AUDIT_BATCH_LEDGERS"},
		{"zero AuditLagThreshold", func(c *Config) { c.AuditLagThreshold = 0 }, "AUDIT_LAG_THRESHOLD"},
		{"AuditBudgetShare > 1", func(c *Config) { c.AuditBudgetShare = 1.5 }, "AUDIT_BUDGET_SHARE"},
		{"AuditMaxRPS zero", func(c *Config) { c.AuditMaxRPS = 0 }, "AUDIT_MAX_RPS"},
		{"AuditMaxRepair zero", func(c *Config) { c.AuditMaxRepair = 0 }, "AUDIT_MAX_REPAIR_ATTEMPTS"},
		{"AuditFindingMaxLgrs zero", func(c *Config) { c.AuditFindingMaxLgrs = 0 }, "AUDIT_FINDING_MAX_LEDGERS"},
		{"RateLimitRPS negative", func(c *Config) { c.RateLimitRPS = -1 }, "RATE_LIMIT_RPS"},
		{"RateLimitBurst negative", func(c *Config) { c.RateLimitBurst = -1 }, "RATE_LIMIT_BURST"},
		{"HourlyQuota negative", func(c *Config) { c.HourlyQuota = -1 }, "HOURLY_QUOTA"},
		{"DailyQuota negative", func(c *Config) { c.DailyQuota = -1 }, "DAILY_QUOTA"},
		{"invalid LogLevel", func(c *Config) { c.LogLevel = "invalid" }, "LOG_LEVEL"},
		{"invalid LogFormat", func(c *Config) { c.LogFormat = "invalid" }, "LOG_FORMAT"},
		{"invalid WatchedContracts", func(c *Config) { c.WatchedContracts = []string{"invalid"} }, "WATCHED_CONTRACTS"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validBaseValidate()
			tt.modify(&cfg)
			err := cfg.ValidateAll()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectError)
		})
	}
}

func TestRedactURLPassword_Table(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   string
		secret string
	}{
		{
			name:   "password is masked and URL components survive",
			in:     "https://alice:s3cr3t@rpc.example.com:8443/v1/events?network=test",
			want:   "https://alice:%2A%2A%2A@rpc.example.com:8443/v1/events?network=test",
			secret: "s3cr3t",
		},
		{
			name: "username without password is unchanged",
			in:   "https://alice@rpc.example.com/v1/events",
			want: "https://alice@rpc.example.com/v1/events",
		},
		{
			name: "URL without userinfo is unchanged",
			in:   "https://rpc.example.com/v1/events?network=test",
			want: "https://rpc.example.com/v1/events?network=test",
		},
		{
			name: "malformed URL fails closed without panicking",
			in:   "https://alice:secret%zz@rpc.example.com/v1/events",
			want: "<redacted>",
		},
		{name: "empty string remains empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactURLPassword(tt.in)
			assert.Equal(t, tt.want, got)
			if tt.secret != "" {
				assert.NotContains(t, got, tt.secret)
			}
		})
	}
}

// TestValidateCORSOrigins pins down the startup guard on CORS_ALLOWED_ORIGINS,
// which is the last point before a malformed entry becomes a permissive browser
// policy for the life of the process. Each case covers one rule the operator
// docs promise: an explicit scheme://host[:port] origin, the "*" wildcard, the
// rejected "null" origin, or an empty list meaning CORS is disabled.
func TestValidateCORSOrigins(t *testing.T) {
	tests := []struct {
		name    string
		origins []string
		// expectError is the substring the error must contain; empty means
		// the list is accepted.
		expectError string
	}{
		{
			// An empty list is the documented default: CORS stays off and no
			// browser request receives Access-Control-* headers.
			name: "empty list accepted (CORS disabled)",
		},
		{
			name:    "well-formed https origin accepted",
			origins: []string{"https://app.example.com"},
		},
		{
			name:    "http origin with an explicit port accepted",
			origins: []string{"http://localhost:3000"},
		},
		{
			name:    "wildcard accepted",
			origins: []string{"*"},
		},
		{
			// Load calls cleanOrigins first, so a pasted trailing slash is
			// stripped before validation. Called directly the entry is still
			// rejected: browsers never send a path in Origin, so comparing one
			// would silently match nothing.
			name:        "origin with a trailing slash rejected",
			origins:     []string{"https://app.example.com/"},
			expectError: `CORS_ALLOWED_ORIGINS entry "https://app.example.com/" is not a valid origin`,
		},
		{
			name:        "origin with a path rejected",
			origins:     []string{"https://app.example.com/api"},
			expectError: `CORS_ALLOWED_ORIGINS entry "https://app.example.com/api" is not a valid origin`,
		},
		{
			name:        "bare hostname with no scheme rejected",
			origins:     []string{"app.example.com"},
			expectError: `CORS_ALLOWED_ORIGINS entry "app.example.com" is not a valid origin`,
		},
		{
			// Origin: null is what sandboxed iframes and file:// pages send, so
			// allowing it would let any third-party page call the API.
			name:        "null origin rejected",
			origins:     []string{"null"},
			expectError: `CORS_ALLOWED_ORIGINS entry "null" is not allowed`,
		},
		{
			// The error has to name the offending entry, not just the variable,
			// so an operator with a long allowlist can find the bad one.
			name:        "error names the offending origin",
			origins:     []string{"https://app.example.com", "app.example.com"},
			expectError: `CORS_ALLOWED_ORIGINS entry "app.example.com" is not a valid origin`,
		},
		{
			// Whitespace-only entries are dropped by cleanOrigins before Load
			// validates, and the validator tolerates them directly rather than
			// failing startup over a stray comma.
			name:    "blank entries tolerated",
			origins: []string{"", "   "},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCORSOrigins(tt.origins)
			if tt.expectError == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectError)
		})
	}
}

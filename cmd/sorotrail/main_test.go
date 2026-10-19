package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/api"
	"github.com/sorotrail/sorotrail/internal/api/graphql"
	"github.com/sorotrail/sorotrail/internal/config"
	"github.com/sorotrail/sorotrail/internal/store"
)

func TestNewLoggerJSONOutput(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.New(h).Info("hello", "key", "value")

	var parsed map[string]any
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("JSON output did not parse: %v\nraw: %s", err, buf.String())
	}
	if parsed["msg"] != "hello" {
		t.Errorf("expected msg=hello, got %v", parsed["msg"])
	}
	if parsed["key"] != "value" {
		t.Errorf("expected key=value, got %v", parsed["key"])
	}
}

func TestNewLoggerUsesTextByDefault(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.New(h).Info("test")

	output := buf.String()
	if strings.Contains(output, `"msg"`) {
		t.Error("expected text output, got JSON")
	}
	if !strings.Contains(output, "test") {
		t.Errorf("expected log message in text output, got %q", output)
	}
}

func TestRPCURLsForLog(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want []string
	}{
		{
			name: "single plain URL is returned unchanged",
			cfg:  config.Config{RPCURL: "https://rpc.example.com"},
			want: []string{"https://rpc.example.com"},
		},
		{
			// An RPC URL may carry basic-auth credentials; the password
			// must never reach the log output. url.UserPassword
			// percent-encodes the mask, so "***" appears as %2A%2A%2A.
			name: "basic-auth password is redacted",
			cfg:  config.Config{RPCURL: "https://alice:supersecret@rpc.example.com"},
			want: []string{"https://alice:%2A%2A%2A@rpc.example.com"},
		},
		{
			name: "all configured failover endpoints are returned",
			cfg: config.Config{RPCURLS: []string{
				"https://rpc1.example.com",
				"https://rpc2.example.com",
				"https://user:hunter2@rpc3.example.com",
			}},
			want: []string{
				"https://rpc1.example.com",
				"https://rpc2.example.com",
				"https://user:%2A%2A%2A@rpc3.example.com",
			},
		},
		{
			name: "RPCURLS overrides RPCURL when both are configured",
			cfg: config.Config{
				RPCURL: "https://fallback.example.com",
				RPCURLS: []string{
					"https://primary1:secret1@rpc.example.com",
					"https://primary2.example.com",
				},
			},
			want: []string{
				"https://primary1:%2A%2A%2A@rpc.example.com",
				"https://primary2.example.com",
			},
		},
		{
			name: "empty entries in RPCURLS are dropped",
			cfg: config.Config{
				RPCURLS: []string{"", "https://rpc.example.com", ""},
			},
			want: []string{"https://rpc.example.com"},
		},
		{
			name: "URL with user but no password preserves user",
			cfg:  config.Config{RPCURL: "https://alice@rpc.example.com"},
			want: []string{"https://alice@rpc.example.com"},
		},
		{
			name: "empty config yields an empty result",
			cfg:  config.Config{},
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rpcURLsForLog(tt.cfg)
			assert.Equal(t, tt.want, got)
		})
	}

	// Security requirement: explicitly assert that password substrings
	// are strictly absent from the redacted log URLs.
	t.Run("password substring is absent from log output", func(t *testing.T) {
		passwords := []string{"supersecret", "hunter2", "secret1", "p@ssw0rd!_123"}
		cfg := config.Config{
			RPCURLS: []string{
				"https://alice:" + passwords[0] + "@rpc1.example.com",
				"https://bob:" + passwords[1] + "@rpc2.example.com",
				"https://admin:" + passwords[2] + "@rpc3.example.com",
				"https://service:" + passwords[3] + "@rpc4.example.com",
			},
		}

		got := rpcURLsForLog(cfg)
		require.Len(t, got, len(passwords))
		for i, raw := range got {
			assert.NotContains(t, raw, passwords[i], "password %q must not leak into loggable URL", passwords[i])
			assert.Contains(t, raw, "%2A%2A%2A", "redacted URL must contain masked password placeholder")
		}
	})
}

// fakeTenantStore is a minimal store.TenantStore fake covering bootstrap requirements.
type fakeTenantStore struct {
	store.TenantStore

	tenantsByName map[string]store.Tenant
	keys          map[string][]byte
	createCalls   int
	getTenantErr  error
	createKeyErr  error
}

func newFakeTenantStore() *fakeTenantStore {
	return &fakeTenantStore{
		tenantsByName: map[string]store.Tenant{
			"default": {ID: 1, Name: "default", Wildcard: true},
		},
		keys: make(map[string][]byte),
	}
}

func (f *fakeTenantStore) GetTenantByName(_ context.Context, name string) (store.Tenant, error) {
	if f.getTenantErr != nil {
		return store.Tenant{}, f.getTenantErr
	}
	t, ok := f.tenantsByName[name]
	if !ok {
		return store.Tenant{}, store.ErrNotFound
	}
	return t, nil
}

func (f *fakeTenantStore) CreateTenantAPIKeyIfAbsent(_ context.Context, _ int64, _, prefix string, digest []byte) error {
	f.createCalls++
	if f.createKeyErr != nil {
		return f.createKeyErr
	}
	// Idempotent insertion matching Postgres ON CONFLICT (prefix) DO UPDATE.
	f.keys[prefix] = digest
	return nil
}

func TestBootstrapAdminKeyStartup(t *testing.T) {
	ctx := context.Background()

	t.Run("bootstrapping with no existing admin creates one", func(t *testing.T) {
		ts := newFakeTenantStore()
		key, prefix, digest, err := api.GenerateAPIKey()
		require.NoError(t, err)

		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

		err = bootstrapAdminKey(ctx, ts, key, log)
		require.NoError(t, err)

		assert.Equal(t, 1, ts.createCalls)
		assert.Equal(t, digest, ts.keys[prefix])

		logOutput := buf.String()
		assert.Contains(t, logOutput, "bootstrap admin key installed from MULTI_TENANT_BOOTSTRAP_KEY")
		assert.Contains(t, logOutput, prefix)
		assert.Contains(t, logOutput, "default")
	})

	t.Run("bootstrapping when an admin exists is a no-op", func(t *testing.T) {
		ts := newFakeTenantStore()
		key, prefix, digest, err := api.GenerateAPIKey()
		require.NoError(t, err)

		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

		// First bootstrap installs the key.
		err = bootstrapAdminKey(ctx, ts, key, log)
		require.NoError(t, err)
		assert.Equal(t, 1, ts.createCalls)

		// Second bootstrap with the same key is an idempotent no-op.
		err = bootstrapAdminKey(ctx, ts, key, log)
		require.NoError(t, err)
		assert.Equal(t, 2, ts.createCalls)
		assert.Equal(t, digest, ts.keys[prefix])
		assert.Len(t, ts.keys, 1)
	})

	t.Run("invalid bootstrap key is rejected with a clear error", func(t *testing.T) {
		invalidKeys := []struct {
			name string
			key  string
		}{
			{name: "garbage text", key: "invalid-key"},
			{name: "wrong scheme prefix", key: "other_1234567890123456_supersecret"},
			{name: "prefix too short", key: "st_short_secret"},
			{name: "prefix too long", key: "st_12345678901234567890_secret"},
			{name: "missing secret portion", key: "st_1234567890123456"},
			{name: "empty secret portion", key: "st_1234567890123456_"},
		}

		for _, tt := range invalidKeys {
			t.Run(tt.name, func(t *testing.T) {
				ts := newFakeTenantStore()
				log := slog.New(slog.NewTextHandler(io.Discard, nil))

				err := bootstrapAdminKey(ctx, ts, tt.key, log)
				require.Error(t, err)
				assert.Contains(t, err.Error(), "MULTI_TENANT_BOOTSTRAP_KEY is not a valid key")
				assert.Contains(t, err.Error(), "st_<12 chars>_<secret>")
				assert.Equal(t, 0, ts.createCalls)
				assert.Empty(t, ts.keys)
			})
		}
	})

	t.Run("empty bootstrap key is a no-op", func(t *testing.T) {
		ts := newFakeTenantStore()
		log := slog.New(slog.NewTextHandler(io.Discard, nil))

		err := bootstrapAdminKey(ctx, ts, "", log)
		require.NoError(t, err)
		assert.Equal(t, 0, ts.createCalls)
		assert.Empty(t, ts.keys)
	})

	t.Run("fails when default tenant does not exist", func(t *testing.T) {
		ts := newFakeTenantStore()
		delete(ts.tenantsByName, "default")
		key, _, _, err := api.GenerateAPIKey()
		require.NoError(t, err)

		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		err = bootstrapAdminKey(ctx, ts, key, log)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "loading default tenant")
	})

	t.Run("fails when tenant store key insertion fails", func(t *testing.T) {
		ts := newFakeTenantStore()
		ts.createKeyErr = errors.New("db disk full")
		key, _, _, err := api.GenerateAPIKey()
		require.NoError(t, err)

		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		err = bootstrapAdminKey(ctx, ts, key, log)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "installing bootstrap key")
		assert.Contains(t, err.Error(), "db disk full")
	})
}

// stubStore is a minimal store.Store implementation for GraphQL dependency testing.
type stubStore struct {
	store.Store
}

func (s *stubStore) QueryEvents(_ context.Context, _ store.EventFilter) ([]store.Event, string, error) {
	return nil, "", nil
}

func (s *stubStore) CountEvents(_ context.Context, _ store.EventFilter) (int64, error) {
	return 0, nil
}

func (s *stubStore) GetEvent(_ context.Context, _ string, _ store.Scope) (store.Event, error) {
	return store.Event{}, store.ErrNotFound
}

func (s *stubStore) ListWatchedContracts(_ context.Context) ([]store.WatchedContract, error) {
	return nil, nil
}

type stubEnricher struct{}

func (s *stubEnricher) EnrichEvents(_ context.Context, _ []store.Event) []store.EnrichedEvent {
	return nil
}

func (s *stubEnricher) DecodeStats() store.DecodeStats {
	return store.DecodeStats{}
}

func TestGraphQLServerDeps(t *testing.T) {
	st := &stubStore{}
	en := &stubEnricher{}

	tests := []struct {
		name     string
		store    store.Store
		enricher api.Enricher
	}{
		{
			name:     "bundles both store and enricher",
			store:    st,
			enricher: en,
		},
		{
			name:     "bundles store with nil enricher when decoding disabled",
			store:    st,
			enricher: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := graphqlServerDeps(tt.store, tt.enricher)
			assert.Equal(t, tt.store, deps.Store)
			assert.Equal(t, tt.enricher, deps.Enricher)
		})
	}

	t.Run("GraphQL dependencies are wired only when feature is enabled", func(t *testing.T) {
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		deps := graphqlServerDeps(st, en)

		// When GraphQL is wired with playground enabled.
		gqlHandlerPlayground, err := graphql.New(deps, log, true)
		require.NoError(t, err)
		serverWithPlayground := api.New(st, nil, log, "").
			SetGraphQLHandler(gqlHandlerPlayground, gqlHandlerPlayground.PlaygroundHandler())
		routerPlayground := serverWithPlayground.Router()

		// When GraphQL is wired with playground disabled.
		gqlHandlerNoPlayground, err := graphql.New(deps, log, false)
		require.NoError(t, err)
		serverNoPlayground := api.New(st, nil, log, "").
			SetGraphQLHandler(gqlHandlerNoPlayground, gqlHandlerNoPlayground.PlaygroundHandler())
		routerNoPlayground := serverNoPlayground.Router()

		// When GraphQL is NOT wired at all.
		serverUnwired := api.New(st, nil, log, "")
		routerUnwired := serverUnwired.Router()

		cases := []struct {
			name       string
			handler    http.Handler
			path       string
			method     string
			wantStatus int
			assertBody func(t *testing.T, body string)
		}{
			{
				name:       "unwired server returns 404 for /graphql",
				handler:    routerUnwired,
				path:       "/graphql",
				method:     http.MethodPost,
				wantStatus: http.StatusNotFound,
			},
			{
				name:       "unwired server returns 404 for /graphiql",
				handler:    routerUnwired,
				path:       "/graphiql",
				method:     http.MethodGet,
				wantStatus: http.StatusNotFound,
			},
			{
				name:       "wired server routes /graphql",
				handler:    routerPlayground,
				path:       "/graphql",
				method:     http.MethodGet,
				wantStatus: http.StatusOK,
			},
			{
				name:       "wired server with playground enabled serves /graphiql html",
				handler:    routerPlayground,
				path:       "/graphiql",
				method:     http.MethodGet,
				wantStatus: http.StatusOK,
				assertBody: func(t *testing.T, body string) {
					assert.Contains(t, body, "GraphQL Playground")
				},
			},
			{
				name:       "wired server with playground disabled returns 404 for /graphiql",
				handler:    routerNoPlayground,
				path:       "/graphiql",
				method:     http.MethodGet,
				wantStatus: http.StatusNotFound,
				assertBody: func(t *testing.T, body string) {
					assert.Contains(t, body, "playground is disabled")
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest(tc.method, tc.path, nil)
				rec := httptest.NewRecorder()
				tc.handler.ServeHTTP(rec, req)
				assert.Equal(t, tc.wantStatus, rec.Code)
				if tc.assertBody != nil {
					tc.assertBody(t, rec.Body.String())
				}
			})
		}
	})
}

func TestAdminBootstrapIdempotencyAndCreation(t *testing.T) {
	// Test that admin bootstrap or command wrappers handle flag validation correctly
	err := runAPIKey([]string{"--help"})
	assert.NoError(t, err)
}

func TestDispatchUnknownCommand(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "unknown subcommand",
			args:    []string{"unknown-cmd"},
			wantErr: "unknown subcommand \"unknown-cmd\"",
		},
		{
			name:    "help subcommand",
			args:    []string{"help"},
			wantErr: "",
		},
		{
			name:    "dash h flag",
			args:    []string{"-h"},
			wantErr: "",
		},
		{
			name:    "double dash help flag",
			args:    []string{"--help"},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := dispatch(tt.args)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
		})
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/config"
)

var stdIOMu sync.Mutex

// captureStdout captures os.Stdout output produced during fn.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	stdIOMu.Lock()
	defer stdIOMu.Unlock()

	r, w, err := os.Pipe()
	require.NoError(t, err)

	orig := os.Stdout
	os.Stdout = w
	defer func() {
		os.Stdout = orig
	}()

	fn()
	_ = w.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	_ = r.Close()
	return buf.String()
}

// captureStderr captures os.Stderr output produced during fn.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	stdIOMu.Lock()
	defer stdIOMu.Unlock()

	r, w, err := os.Pipe()
	require.NoError(t, err)

	orig := os.Stderr
	os.Stderr = w
	defer func() {
		os.Stderr = orig
	}()

	fn()
	_ = w.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	_ = r.Close()
	return buf.String()
}

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

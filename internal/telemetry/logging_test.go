package telemetry

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCanonicalLogFields(t *testing.T) {
	assert.Equal(t, "contract_id", FieldContractID)
	assert.Equal(t, "ledger", FieldLedger)
	assert.Equal(t, "duration_ms", FieldDurationMs)
	assert.Equal(t, "error", FieldError)
	assert.Equal(t, "url", FieldURL)
	assert.Equal(t, "tx_hash", FieldTxHash)
}

func TestSanitizeURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain url unchanged",
			input: "https://rpc.example.com",
			want:  "https://rpc.example.com",
		},
		{
			name:  "basic auth password redacted",
			input: "https://user:secretpassword@rpc.example.com/path",
			want:  "https://user:%2A%2A%2A@rpc.example.com/path",
		},
		{
			name:  "username only, no password",
			input: "https://user@rpc.example.com/path",
			want:  "https://user@rpc.example.com/path",
		},
		{
			name:  "password with special chars that need escaping",
			input: "https://user:p%40ss:w0rd@rpc.example.com/path",
			want:  "https://user:%2A%2A%2A@rpc.example.com/path",
		},
		{
			name:  "url with no userinfo at all",
			input: "https://rpc.example.com/path?query=value",
			want:  "https://rpc.example.com/path?query=value",
		},
		{
			name:  "credentials in query string rather than userinfo",
			input: "https://rpc.example.com/path?api_key=secret123",
			want:  "https://rpc.example.com/path?api_key=secret123",
		},
		// A password does not have to be exotic to defeat url.Parse, and the
		// old fallback returned the string verbatim — so the one input most
		// likely to hold a secret was the one that got logged in full.
		{
			name:  "unparseable url is redacted, not passed through",
			input: "https://user:sekret%pass@rpc.example.com/path",
			want:  "<redacted>",
		},
		{
			name:  "space in userinfo is redacted",
			input: "https://user:has space@rpc.example.com/path",
			want:  "<redacted>",
		},
		{
			name:  "malformed url with brackets in userinfo",
			input: "https://user:[password@rpc.example.com/path",
			want:  "<redacted>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeURL(tt.input)
			assert.Equal(t, tt.want, got)
			// Ensure the secret is absent from the whole output string
			if tt.input != tt.want {
				assert.NotContains(t, got, "secret")
				assert.NotContains(t, got, "password")
				assert.NotContains(t, got, "sekret")
				assert.NotContains(t, got, "api_key")
			}
		})
	}
}

func TestLogValueSanitized(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		val     string
		wantKey string
		wantVal string
	}{
		{
			name:    "url with password gets sanitized",
			key:     "rpc_url",
			val:     "https://user:secret@rpc.example.com/path",
			wantKey: "rpc_url",
			wantVal: "https://user:%2A%2A%2A@rpc.example.com/path",
		},
		{
			name:    "non-url value passed through unchanged",
			key:     "some_key",
			val:     "just a string",
			wantKey: "some_key",
			wantVal: "just a string",
		},
		{
			name:    "url without password passed through",
			key:     "rpc_url",
			val:     "https://rpc.example.com/path",
			wantKey: "rpc_url",
			wantVal: "https://rpc.example.com/path",
		},
		{
			name:    "url with username only passed through",
			key:     "rpc_url",
			val:     "https://user@rpc.example.com/path",
			wantKey: "rpc_url",
			wantVal: "https://user@rpc.example.com/path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attr := LogValueSanitized(tt.key, tt.val)
			assert.Equal(t, tt.wantKey, attr.Key)
			assert.Equal(t, tt.wantVal, attr.Value.String())
		})
	}
}

func TestAttrHelpers(t *testing.T) {
	cAttr := WithContractID("CA")
	assert.Equal(t, FieldContractID, cAttr.Key)
	assert.Equal(t, "CA", cAttr.Value.String())

	lAttr := WithLedger(42)
	assert.Equal(t, FieldLedger, lAttr.Key)
	assert.Equal(t, int64(42), lAttr.Value.Int64())

	dAttr := WithDurationMs(150)
	assert.Equal(t, FieldDurationMs, dAttr.Key)
	assert.Equal(t, int64(150), dAttr.Value.Int64())

	eAttr := WithError(errors.New("failed"))
	assert.Equal(t, FieldError, eAttr.Key)
	assert.Equal(t, "failed", eAttr.Value.String())

	// nil error returns empty string
	eAttrNil := WithError(nil)
	assert.Equal(t, FieldError, eAttrNil.Key)
	assert.Equal(t, "", eAttrNil.Value.String())
}

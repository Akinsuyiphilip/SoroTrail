package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sorotrail/sorotrail/internal/horizon"
)

// TestIsRetryableBackfillErr_Table pins the backfill's retry decision.
//
// The inputs are the errors the Go runtime and Horizon actually produce —
// a syscall errno behind a *net.OpError, a *net.DNSError, io.EOF — rather
// than strings copied out of the implementation. The transport
// classification is shared with the RPC client (rpc.IsTransientHTTP), so a
// DNS blip that the ingester backs off from is also retried here instead of
// aborting the backfill.
func TestIsRetryableBackfillErr_Table(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			// Horizon's 429 signal: slow down and try again.
			name: "HTTP 429 rate limit",
			err:  horizon.ErrRateLimited,
			want: true,
		},
		{
			name: "wrapped HTTP 429 rate limit",
			err:  fmt.Errorf("fetching page 3: %w", horizon.ErrRateLimited),
			want: true,
		},
		{
			// A 404 is a permanent answer: the resource is not there, and
			// asking again changes nothing.
			name: "horizon not found",
			err:  horizon.ErrNotFound,
			want: false,
		},
		{
			name: "wrapped horizon not found",
			err:  fmt.Errorf("fetching page 3: %w", horizon.ErrNotFound),
			want: false,
		},
		{
			name: "HTTP 5xx response is not a transport failure",
			err:  errors.New("horizon returned HTTP 503: service unavailable"),
			want: false,
		},
		{
			name: "HTTP 4xx response other than 429 is permanent",
			err:  errors.New("horizon returned HTTP 400: bad request"),
			want: false,
		},
		{
			// The behaviour that used to differ from the RPC path: a
			// transient DNS failure was fatal here while the ingester
			// retried it. One classifier means one answer.
			name: "dns lookup failure",
			err:  &net.DNSError{Err: "no such host", Name: "horizon.example", IsNotFound: true},
			want: true,
		},
		{
			name: "connection refused",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
			want: true,
		},
		{
			name: "connection reset",
			err:  &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET},
			want: true,
		},
		{
			name: "wrapped refused connection behind the transport's wrapper",
			err: fmt.Errorf("fetching page 3: %w", &url.Error{
				Op: "Get", URL: "https://horizon.example", Err: &net.OpError{
					Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED,
				},
			}),
			want: true,
		},
		{
			name: "unexpected EOF",
			err:  io.ErrUnexpectedEOF,
			want: true,
		},
		{
			name: "expired deadline",
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			// The caller asked for the work to stop; retrying would ignore
			// that.
			name: "cancelled context",
			err:  context.Canceled,
			want: false,
		},
		{
			// A real bug must surface immediately rather than be retried
			// into a longer failure.
			name: "permanent failure",
			err:  errors.New("permanent fatal database error: relation does not exist"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isRetryableBackfillErr(tt.err))
		})
	}
}

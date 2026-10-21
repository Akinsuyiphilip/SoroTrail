package rpc

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
)

// IsTransientHTTP reports whether err is a transport-level failure that is
// worth retrying: a refused connection, a DNS failure, a reset, a timeout or
// an unexpected EOF.
//
// The classification is structural rather than textual. The operating system
// renders the same failure differently — "connection refused" on POSIX,
// "No connection could be made because the target machine actively refused
// it" on Windows — so matching on message text made the retry behaviour
// depend on the platform the binary happened to run on, and a Go release
// that reworded a network error would change it silently. Every condition
// below has a typed cause in the standard library, and errors.As / errors.Is
// reach it through the layers a transport error arrives in (*url.Error around
// *net.OpError around a syscall errno).
//
// The substring fallback at the end is a last resort for errors that carry no
// typed cause at all. A case the structural checks already handle never
// reaches it, so it cannot be the mechanism that decides a retry.
//
// The stance is deliberately permissive: a misclassified non-retryable error
// only gets retried once and surfaces on the second failure, while a
// misclassified retryable error stops ingestion.
func IsTransientHTTP(err error) bool {
	if err == nil {
		return false
	}

	// A cancelled context is the caller asking for the work to stop, not a
	// transport failure, so it is never transient — and it is checked
	// before the *net.OpError branch below, because a cancelled request
	// arrives wrapped in one.
	if errors.Is(err, context.Canceled) {
		return false
	}
	// An expired deadline is a timeout, which is exactly what the retry
	// layer exists to absorb. The caller that owns the deadline decides
	// separately whether to give up (see isContextErr).
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// DNS failures. The resolver reports a name that does not resolve as
	// IsNotFound ("no such host") and a resolver that gave up as
	// IsTimeout; every DNS failure is worth another attempt, because the
	// name may resolve a moment later.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	// Socket-level failures: refused, reset, unreachable, timed out. The
	// *net.OpError wrapper is what net/http puts around them, and the
	// syscall errno underneath it is what says which one it was.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}

	// Timeouts reported through the net.Error interface, which both
	// *url.Error and *net.OpError implement by delegating to the cause.
	// The client's own timeout arrives this way, and it is transient even
	// though no errno is involved. The interface's Temporary method is
	// deliberately not consulted: it is deprecated, and the errno and the
	// timeout above already cover what it used to signal.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}

	// The errno values themselves, for errors that reach us without the
	// net package's wrappers.
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}

	// An EOF on an HTTP body is genuinely ambiguous: it can mean the
	// response was truncated or that the body ended cleanly. The behaviour
	// is unchanged — an EOF has always been treated as transient — so a
	// clean end costs one extra request rather than a failed call.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	// Last resort: an error with no typed cause at all. The fragments are
	// the ones the Go runtime and the common resolvers use; they are only
	// consulted when the structural checks above found nothing.
	msg := err.Error()
	for _, frag := range []string{
		"connection reset",
		"connection refused",
		"EOF",
		"timeout",
		"temporary failure",
		"no such host",
	} {
		if strings.Contains(msg, frag) {
			return true
		}
	}
	return false
}

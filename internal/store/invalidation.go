package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// SpecInvalidationChannel is the Postgres channel the spec-cache
// invalidations are published on.
//
// The spec cache is per-process, so a change that alters what a contract
// decodes to (an override write, a detected contract upgrade) is only
// visible to the process that made it. Publishing the contract ID on a
// channel lets every other replica evict its own copy, so the same request
// does not decode differently depending on which replica served it.
//
// Postgres is already a shared dependency and LISTEN/NOTIFY is the standard
// way to fan a message out without new infrastructure.
const SpecInvalidationChannel = "sorotrail_spec_invalidation"

// SpecInvalidationPublisher publishes a contract ID whose cached spec must
// be evicted in every process.
//
// The spec package defines its own equivalent interface so it does not have
// to depend on this package's concrete types.
type SpecInvalidationPublisher interface {
	PublishSpecInvalidation(ctx context.Context, contractID string) error
}

// PublishSpecInvalidation notifies every other replica that the contract's
// cached spec (and its override) must be evicted.
//
// The payload is the bare contract ID: it is a public on-chain identifier,
// so nothing sensitive travels over the channel, and it keeps the payload
// small enough for the 8000-byte NOTIFY limit.
func (p *Postgres) PublishSpecInvalidation(ctx context.Context, contractID string) error {
	_, err := p.pool.Exec(ctx, `SELECT pg_notify($1, $2)`, SpecInvalidationChannel, contractID)
	if err != nil {
		return fmt.Errorf("publishing spec invalidation for %s: %w", contractID, err)
	}
	return nil
}

// SpecInvalidationListener subscribes to the spec invalidation channel.
//
// The subscription uses a dedicated connection rather than one from the
// pool: a LISTEN blocks its connection until a notification arrives, so
// borrowing a pooled connection would hold it for the life of the process.
type SpecInvalidationListener struct {
	// DatabaseURL is the connection string the listener dials.
	DatabaseURL string
	// OnInvalidation is called for every contract ID received. It runs on
	// the listener's goroutine, so it must not block for long.
	OnInvalidation func(contractID string)
	// OnReconnect is called after every successful (re)subscription. A
	// reconnect may have missed notifications, so the cache is cleared.
	OnReconnect func()
	// Logger receives the listener's diagnostics. Optional.
	Logger *slog.Logger
	// Backoff is the initial delay before a reconnect attempt. It doubles
	// up to maxBackoff.
	Backoff time.Duration
	// MaxBackoff caps the reconnect delay.
	MaxBackoff time.Duration
}

// listenBackoff is the initial reconnect delay, and the floor the backoff
// grows from. A dropped connection usually means a database restart, so the
// listener retries quickly at first and then settles into a slower poll.
const (
	listenBackoff    = 100 * time.Millisecond
	listenMaxBackoff = 5 * time.Second
)

// Run subscribes to the invalidation channel and delivers every contract ID
// to the listener's OnInvalidation callback. It blocks until ctx is done.
//
// A dropped connection is not fatal: the listener reconnects with an
// exponential backoff and, after every successful (re)subscription, calls
// OnReconnect — because a reconnect may have missed notifications, the
// subscriber is expected to clear its whole cache at that point.
func (l *SpecInvalidationListener) Run(ctx context.Context) error {
	backoff := l.Backoff
	if backoff <= 0 {
		backoff = listenBackoff
	}
	maxBackoff := l.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = listenMaxBackoff
	}
	log := l.logger()

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := l.listenOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Warn("spec invalidation listener disconnected; reconnecting",
			"error", err,
			"backoff", backoff,
		)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// listenOnce holds one subscription open until it fails or ctx is done.
func (l *SpecInvalidationListener) listenOnce(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, l.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connecting for LISTEN: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	if _, err := conn.Exec(ctx, "LISTEN "+SpecInvalidationChannel); err != nil {
		return fmt.Errorf("subscribing to %s: %w", SpecInvalidationChannel, err)
	}
	l.logger().Debug("subscribed to the spec invalidation channel",
		"channel", SpecInvalidationChannel,
	)

	// A (re)subscription may have missed notifications, so the subscriber
	// is told to clear everything it holds.
	if l.OnReconnect != nil {
		l.OnReconnect()
	}

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("waiting for a notification: %w", err)
		}
		if l.OnInvalidation != nil {
			l.OnInvalidation(notification.Payload)
		}
	}
}

// logger returns the listener's logger, or the default one.
func (l *SpecInvalidationListener) logger() *slog.Logger {
	if l.Logger != nil {
		return l.Logger
	}
	return slog.Default()
}

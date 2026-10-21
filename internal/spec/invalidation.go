package spec

import (
	"context"
	"log/slog"
)

// Invalidator publishes a contract whose cached spec must be evicted in
// every process.
//
// The store package provides the Postgres implementation; the interface is
// declared here so the spec package does not have to depend on it.
type Invalidator interface {
	PublishSpecInvalidation(ctx context.Context, contractID string) error
}

// WithInvalidationPublisher wires the publisher used when this process
// detects that a contract's decoded shape changed on its own — a contract
// upgrade, which changes the wasm hash the cached spec was keyed by.
//
// Without a publisher the change stays local, and the other replicas keep
// serving the old spec until their own expiry.
func WithInvalidationPublisher(pub Invalidator) Option {
	return func(c *Cache) { c.publisher = pub }
}

// WithLogger sets the logger the cache uses for its own diagnostics.
func WithLogger(log *slog.Logger) Option {
	return func(c *Cache) { c.log = log }
}

// ApplyInvalidation evicts everything the cache holds for a contract: the
// spec fetched from the RPC and the user-supplied override.
//
// It is what a replica does when another replica tells it that the
// contract's decoded shape changed.
func (c *Cache) ApplyInvalidation(contractID string) {
	c.InvalidateContract(contractID)
	c.InvalidateOverride(contractID)
}

// Clear empties the cache.
//
// It is what a replica does after its invalidation listener reconnects: a
// reconnect may have missed notifications, so the only safe state is to
// hold nothing rather than to hold something that might be stale.
func (c *Cache) Clear() {
	c.mu.Lock()
	c.mem = make(map[string]*entry)
	c.hashes = make(map[string]hashEntry)
	c.mu.Unlock()
}

// InvalidationsReceived counts the invalidations this process applied from
// the shared channel.
func (c *Cache) InvalidationsReceived() uint64 {
	return c.invalidationsReceived.Load()
}

// recordInvalidationReceived counts an invalidation that arrived from
// another replica.
func (c *Cache) recordInvalidationReceived() {
	c.invalidationsReceived.Add(1)
}

// publishInvalidation tells the other replicas that the contract's cached
// spec must be evicted.
//
// A failure is not fatal: this process's own cache is already correct, and
// the other replicas fall back to their own expiry. It is logged rather
// than swallowed so the fan-out is observable when it breaks.
func (c *Cache) publishInvalidation(ctx context.Context, contractID string) {
	if c.publisher == nil {
		return
	}
	if err := c.publisher.PublishSpecInvalidation(ctx, contractID); err != nil {
		c.logger().Warn("publishing the spec invalidation failed",
			"contract_id", contractID,
			"error", err,
		)
	}
}

// ApplyRemoteInvalidation evicts everything the cache holds for a contract
// and counts the invalidation as one received from another replica.
//
// It is the single call a subscriber makes when a notification arrives, so
// the counter cannot be forgotten at a call site.
func (c *Cache) ApplyRemoteInvalidation(contractID string) {
	c.ApplyInvalidation(contractID)
	c.recordInvalidationReceived()
}

// logger returns the cache's logger, or the default one.
func (c *Cache) logger() *slog.Logger {
	if c.log != nil {
		return c.log
	}
	return slog.Default()
}

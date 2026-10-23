package spec

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingInvalidator captures the contract IDs the cache publishes.
type recordingInvalidator struct {
	mu        sync.Mutex
	published []string
	err       error
}

func (p *recordingInvalidator) PublishSpecInvalidation(_ context.Context, contractID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published = append(p.published, contractID)
	return p.err
}

func (p *recordingInvalidator) list() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.published...)
}

// changingResolver returns a wasm hash the test can change.
type changingResolver struct {
	mu   sync.Mutex
	hash string
}

func (r *changingResolver) FetchWasmHash(context.Context, string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hash, nil
}

func (r *changingResolver) set(hash string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hash = hash
}

// TestCache_PublishesInvalidationOnContractUpgrade proves a detected contract
// upgrade is published for the other replicas.
//
// The cache is per-process, so a replica that never learns about the
// upgrade keeps decoding the contract the old way until its own expiry —
// the same request would then decode differently depending on which
// replica served it.
func TestCache_PublishesInvalidationOnContractUpgrade(t *testing.T) {
	resolver := &changingResolver{hash: "hash-1"}
	publisher := &recordingInvalidator{}
	cache := NewCache(nil,
		WithWasmHashResolver(resolver),
		WithInvalidationPublisher(publisher),
		// A tiny hash TTL makes the mapping stale immediately, so the
		// second lookup re-resolves without a wall-clock wait.
		WithHashTTL(time.Nanosecond),
	)
	ctx := context.Background()
	const contractID = "C1"

	// The first lookup records the mapping and publishes nothing: nothing
	// changed yet.
	cache.GetForContract(ctx, contractID)
	assert.Empty(t, publisher.list(), "the first lookup must not publish an invalidation")

	// The contract upgrades. The next lookup notices the changed hash and
	// tells the other replicas.
	time.Sleep(time.Millisecond)
	resolver.set("hash-2")
	cache.GetForContract(ctx, contractID)
	assert.Equal(t, []string{contractID}, publisher.list(),
		"a detected contract upgrade must be published")
}

// TestCache_WithoutAPublisherDoesNotPanic proves a single-process
// deployment — no publisher wired — keeps working.
func TestCache_WithoutAPublisherDoesNotPanic(t *testing.T) {
	resolver := &changingResolver{hash: "hash-1"}
	cache := NewCache(nil, WithWasmHashResolver(resolver), WithHashTTL(time.Nanosecond))
	ctx := context.Background()

	cache.GetForContract(ctx, "C1")
	time.Sleep(time.Millisecond)
	resolver.set("hash-2")
	assert.NotPanics(t, func() { cache.GetForContract(ctx, "C1") })
}

// TestCache_ApplyRemoteInvalidationEvicts proves an invalidation that
// arrived from another replica empties the contract's entries, including the
// override, and is counted.
func TestCache_ApplyRemoteInvalidationEvicts(t *testing.T) {
	cache := NewCache(nil)
	const contractID = "C1"

	// Set registers the contract → hash mapping, so the fetched spec is
	// reachable by contract ID; Put stores the override under its own key.
	require.NoError(t, cache.Set(context.Background(),
		&ContractSpec{WasmHash: "hash-1", ContractID: contractID}))
	cache.Put(overrideKeyPrefix+contractID, &ContractSpec{WasmHash: "override", ContractID: contractID})

	cache.ApplyRemoteInvalidation(contractID)

	assert.Nil(t, cache.Get("hash-1"), "the fetched spec must be evicted")
	assert.Nil(t, cache.Get(overrideKeyPrefix+contractID), "the override must be evicted")
	assert.Equal(t, uint64(1), cache.InvalidationsReceived(),
		"an applied remote invalidation must be counted")
}

// TestCache_ClearEmptiesEverything proves the reconnect path drops the whole
// cache: a reconnect may have missed notifications, so nothing the cache
// holds can be trusted.
func TestCache_ClearEmptiesEverything(t *testing.T) {
	cache := NewCache(nil)
	cache.Put("hash-1", &ContractSpec{WasmHash: "hash-1", ContractID: "C1"})
	cache.Put("hash-2", &ContractSpec{WasmHash: "hash-2", ContractID: "C2"})

	cache.Clear()

	assert.Nil(t, cache.Get("hash-1"))
	assert.Nil(t, cache.Get("hash-2"))
	assert.Equal(t, 0, cache.Stats().CachedSpecs)
}

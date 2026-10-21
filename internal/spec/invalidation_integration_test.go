//go:build integration

package spec

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/store"
	"github.com/sorotrail/sorotrail/internal/testdb"
)

// TestSpecInvalidation_CrossReplicaOverrideWrite proves that an override
// written through one replica reaches another within a bounded time.
//
// The spec cache is per-process, so without the shared invalidation channel
// the second replica keeps decoding the contract the old way until its own
// cache expires — the same request would then decode differently depending
// on which replica served it.
func TestSpecInvalidation_CrossReplicaOverrideWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool := testdb.Setup(t, store.Migrate)
	databaseURL := pool.Config().ConnString()

	writer := store.NewPostgres(pool)
	reader := store.NewPostgres(pool)

	// The reader holds a cached override, as it would after serving one
	// request for the contract.
	readerCache := NewCache(nil)
	const contractID = "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	readerCache.Put(overrideKeyPrefix+contractID, &ContractSpec{WasmHash: "stale", ContractID: contractID})
	require.NotNil(t, readerCache.Get(overrideKeyPrefix+contractID),
		"the reader must start with a cached override")

	// The reader subscribes to the invalidation channel.
	applied := make(chan string, 8)
	listener := &store.SpecInvalidationListener{
		DatabaseURL: databaseURL,
		OnInvalidation: func(contractID string) {
			readerCache.ApplyRemoteInvalidation(contractID)
			applied <- contractID
		},
		OnReconnect: func() { readerCache.Clear() },
	}
	listenerDone := make(chan struct{})
	go func() {
		defer close(listenerDone)
		_ = listener.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-listenerDone:
		case <-time.After(5 * time.Second):
		}
	})

	// The notification is published repeatedly until the reader has it: the
	// listener subscribes asynchronously, so the first notification can
	// land before the subscription is in place.
	deadline := time.Now().Add(30 * time.Second)
	for {
		// The override is written through the writer replica, exactly as
		// the API handler does it, and the write is published.
		require.NoError(t, writer.SetContractSpecOverrideWithHistory(ctx, contractID,
			[]byte(`{"events":[{"name":"transfer"}]}`), "prefix"))
		require.NoError(t, writer.PublishSpecInvalidation(ctx, contractID))

		select {
		case got := <-applied:
			assert.Equal(t, contractID, got)
			assert.Nil(t, readerCache.Get(overrideKeyPrefix+contractID),
				"the reader must have evicted its cached override")
			assert.Equal(t, uint64(1), readerCache.InvalidationsReceived(),
				"the reader must count the invalidation it applied")

			// The write itself is in the shared database, so the reader
			// reads the new spec rather than the one it had cached.
			specJSON, err := reader.GetContractSpecOverride(ctx, contractID)
			require.NoError(t, err)
			assert.Contains(t, string(specJSON), "transfer")
			return
		case <-time.After(200 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("the invalidation did not reach the reader in time")
		}
	}
}

// TestSpecInvalidation_ListenerReconnectClearsTheCache proves the reconnect
// path: after the listener reconnects it may have missed notifications, so
// the cache is emptied rather than left holding something that might be
// stale.
func TestSpecInvalidation_ListenerReconnectClearsTheCache(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool := testdb.Setup(t, store.Migrate)
	databaseURL := pool.Config().ConnString()

	cache := NewCache(nil)
	cache.Put("hash-1", &ContractSpec{WasmHash: "hash-1", ContractID: "C1"})
	require.NotNil(t, cache.Get("hash-1"))

	reconnects := make(chan struct{}, 4)
	listener := &store.SpecInvalidationListener{
		DatabaseURL: databaseURL,
		OnInvalidation: func(string) {},
		OnReconnect: func() {
			cache.Clear()
			reconnects <- struct{}{}
		},
	}
	go func() { _ = listener.Run(ctx) }()

	// The first subscription clears the cache: a reconnect may have
	// missed notifications, so nothing the cache holds can be trusted.
	select {
	case <-reconnects:
	case <-time.After(30 * time.Second):
		t.Fatal("the listener never subscribed")
	}
	assert.Nil(t, cache.Get("hash-1"),
		"a (re)subscription must clear the cache")
}

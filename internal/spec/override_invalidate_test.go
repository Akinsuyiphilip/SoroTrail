package spec

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// overrideStoreStub serves one contract's override document from memory.
type overrideStoreStub struct {
	spec []byte
}

func (s *overrideStoreStub) GetContractSpecOverride(context.Context, string) ([]byte, error) {
	if len(s.spec) == 0 {
		return nil, errors.New("no override")
	}
	return s.spec, nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestEnricher_InvalidateContractEvictsTheOverride proves a write to the
// override reaches the cache. Without the eviction the cached override would
// keep decoding events the old way for the life of the process: an override
// never expires on its own, because it is authoritative until it is
// replaced.
func TestEnricher_InvalidateContractEvictsTheOverride(t *testing.T) {
	const contractID = "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	store := &overrideStoreStub{spec: []byte(`{"events":[{"name":"transfer"}]}`)}
	cache := NewCache(nil)
	e := NewEnricher(nil, cache, quietLogger(), store)

	got := e.getOverride(context.Background(), contractID)
	require.NotNil(t, got, "the override must be read from the store")
	assert.Equal(t, "transfer", got.Events[0].Name)
	assert.NotNil(t, cache.Get(overrideKeyPrefix+contractID), "the override must be cached")

	e.InvalidateContract(contractID)

	assert.Nil(t, cache.Get(overrideKeyPrefix+contractID),
		"the cached override must be evicted by the invalidation")
}

// TestCache_InvalidateOverrideLeavesFetchedSpecsAlone proves the override
// eviction is targeted: a fetched spec for the same contract is a different
// thing and must survive.
func TestCache_InvalidateOverrideLeavesFetchedSpecsAlone(t *testing.T) {
	cache := NewCache(nil)
	fetched := &ContractSpec{WasmHash: "abc123", ContractID: "C1"}
	cache.Put("abc123", fetched)
	cache.Put(overrideKeyPrefix+"C1", &ContractSpec{WasmHash: "override", ContractID: "C1"})

	cache.InvalidateOverride("C1")

	assert.NotNil(t, cache.Get("abc123"), "the fetched spec must survive")
	assert.Nil(t, cache.Get(overrideKeyPrefix+"C1"), "the override must be evicted")
}

// TestEnricher_InvalidateContractWithoutACache proves the invalidation is
// safe on an enricher with no cache wired in.
func TestEnricher_InvalidateContractWithoutACache(t *testing.T) {
	var e *Enricher
	e.InvalidateContract("C1") // must not panic

	e = NewEnricher(nil, nil, quietLogger(), nil)
	e.InvalidateContract("C1") // must not panic
}

package simtest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/rpc"
)

// newTestChain builds a chain at a fixed genesis so ledger arithmetic in
// these tests is exact and reproducible.
func newTestChain(t *testing.T, retention uint32) (*VirtualChain, *VirtualClock, time.Time) {
	t.Helper()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := NewVirtualClock(now)
	return NewVirtualChain(clock, now, retention), clock, now
}

// TestVirtualChain_AdvanceBy pins the two effects of AdvanceBy: the head
// moves forward by exactly the requested number of ledgers, and the shared
// virtual clock advances by the matching wall time, so a scenario that
// interleaves AdvanceBy with AddEvents lands on deterministic ledgers.
func TestVirtualChain_AdvanceBy(t *testing.T) {
	chain, clock, genesis := newTestChain(t, 100)

	chain.AdvanceBy(7)
	assert.Equal(t, uint32(7), chain.LatestLedger(),
		"the head must move by exactly the requested count")
	assert.Equal(t, genesis.Add(7*LedgerDuration), clock.Now(),
		"the clock must advance one LedgerDuration per ledger")

	chain.AdvanceBy(3)
	assert.Equal(t, uint32(10), chain.LatestLedger(),
		"counts accumulate across calls")

	chain.AdvanceBy(0)
	assert.Equal(t, uint32(10), chain.LatestLedger(),
		"advancing by zero is a no-op for the head")
	assert.Equal(t, genesis.Add(10*LedgerDuration), clock.Now(),
		"and for the clock")
}

// TestVirtualChain_AdvanceByRevealsEvents covers the visibility half: once
// the head has been advanced past an event's ledger, GetEvents returns it.
// Without this the fault scenarios could "advance" past events and never
// notice the events were never served.
func TestVirtualChain_AdvanceByRevealsEvents(t *testing.T) {
	chain, _, _ := newTestChain(t, 100)
	chain.AddEvents(10, BuildEvent("0000000010-000000000", 10, "CAAAA"))

	chain.AdvanceBy(10)

	resp, err := chain.GetEvents(context.Background(), rpc.GetEventsRequest{StartLedger: 2})
	require.NoError(t, err)
	require.Len(t, resp.Events, 1, "events at ledgers within the advanced head must be visible")
	assert.Equal(t, uint32(10), resp.Events[0].Ledger)
	assert.Equal(t, chain.LatestLedger(), resp.LatestLedger)
}

// TestVirtualChain_CallCountCountsGetEventsOnly pins what the counter is
// for: the fault scheduler injects at GetEvents call indices, so the
// other RPC methods the ingester polls must not consume those indices.
func TestVirtualChain_CallCountCountsGetEventsOnly(t *testing.T) {
	chain, _, _ := newTestChain(t, 100)
	chain.AdvanceTo(50)

	assert.Equal(t, 0, chain.CallCount(), "a fresh chain has seen no GetEvents")

	_, err := chain.GetHealth(context.Background())
	require.NoError(t, err)
	_, err = chain.GetLatestLedger(context.Background())
	require.NoError(t, err)
	_, err = chain.GetLedgerEntries(context.Background(), rpc.GetLedgerEntriesRequest{})
	require.NoError(t, err)
	_, err = chain.SimulateTransaction(context.Background(), rpc.SimulateTransactionRequest{})
	require.NoError(t, err)
	assert.Equal(t, 0, chain.CallCount(),
		"health, ledger and simulation polls must not advance the fault index")

	_, err = chain.GetEvents(context.Background(), rpc.GetEventsRequest{StartLedger: 2})
	require.NoError(t, err)
	assert.Equal(t, 1, chain.CallCount(), "each GetEvents call counts exactly once")

	_, err = chain.GetEvents(context.Background(), rpc.GetEventsRequest{StartLedger: 2})
	require.NoError(t, err)
	assert.Equal(t, 2, chain.CallCount())

	// A call that fails still counts: the fault scheduler keys off call
	// order, so a failed fetch must not shift the injection indices.
	_, err = chain.GetEvents(context.Background(), rpc.GetEventsRequest{StartLedger: 1})
	require.Error(t, err, "startLedger below retention must fail")
	assert.Equal(t, 3, chain.CallCount(), "failed GetEvents calls still advance the fault index")
}

// TestVirtualChain_AllEvents covers order and copy semantics: the oracle
// compares AllEvents against what the store holds, so it must see every
// added event, in the order they were added (callers add in ledger order),
// and mutating the returned slice must not rewrite the chain's history.
func TestVirtualChain_AllEvents(t *testing.T) {
	chain, _, _ := newTestChain(t, 100)
	chain.AddEvents(5, BuildEvent("0000000005-000000000", 5, "CAAAA"))
	chain.AddEvents(10, BuildEvent("0000000010-000000000", 10, "CBBBB"))

	got := chain.AllEvents()
	require.Len(t, got, 2, "every added event must be reported")
	assert.Equal(t, uint32(5), got[0].Ledger, "events keep the ledger order they were added in")
	assert.Equal(t, uint32(10), got[1].Ledger)
	assert.Equal(t, "0000000005-000000000", got[0].Event.ID)

	got[0].Event.ID = "rewritten"
	assert.Equal(t, "0000000005-000000000", chain.AllEvents()[0].Event.ID,
		"the returned slice must be a copy, not a view onto chain history")

	chain.AdvanceBy(50)
	assert.Len(t, chain.AllEvents(), 2,
		"advancing the head must not drop events from the authoritative set")
}

// TestVirtualChain_GetLatestLedgerAgreesWithHead pins the RPC view against
// the chain's own head after both advance paths: the ingester bootstraps
// its cursor from this response, so disagreement silently skips ledgers.
func TestVirtualChain_GetLatestLedgerAgreesWithHead(t *testing.T) {
	chain, _, _ := newTestChain(t, 100)

	ledger, err := chain.GetLatestLedger(context.Background())
	require.NoError(t, err)
	assert.Equal(t, chain.LatestLedger(), ledger.Sequence)
	assert.Equal(t, "000", ledger.ID, "the id echoes the sequence with the 00 prefix")

	chain.AdvanceTo(50)
	ledger, err = chain.GetLatestLedger(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint32(50), ledger.Sequence)
	assert.Equal(t, "0050", ledger.ID)
	assert.Equal(t, chain.LatestLedger(), ledger.Sequence)

	chain.AdvanceBy(4)
	ledger, err = chain.GetLatestLedger(context.Background())
	require.NoError(t, err)
	assert.Equal(t, chain.LatestLedger(), ledger.Sequence,
		"AdvanceBy must be reflected in the RPC view too")
}

// TestVirtualChain_LedgerStubsReturnEmpty pins the documented stub
// behaviour: the simulator models events only, so ledger lookups and
// transaction simulation answer with zero values and no error rather than
// fabricating data the oracle would then assert against.
func TestVirtualChain_LedgerStubsReturnEmpty(t *testing.T) {
	chain, _, _ := newTestChain(t, 100)

	entries, err := chain.GetLedgerEntries(context.Background(), rpc.GetLedgerEntriesRequest{})
	require.NoError(t, err, "the stub must not invent failures")
	assert.Equal(t, rpc.GetLedgerEntriesResponse{}, entries,
		"no ledger state is modelled, so the response must be empty")

	sim, err := chain.SimulateTransaction(context.Background(), rpc.SimulateTransactionRequest{})
	require.NoError(t, err, "the stub must not invent failures")
	assert.Equal(t, rpc.SimulateTransactionResponse{}, sim,
		"contract simulation is not modelled, so the response must be empty")
}

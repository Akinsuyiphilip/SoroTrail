package simtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/rpc"
)

// installedSchedule copies the package-level fault schedule while holding the
// same lock the scheduler writes it under, so a test sees exactly what a
// running simulation would see rather than an unsynchronised guess.
func installedSchedule() []faultRecord {
	faultMu.RLock()
	defer faultMu.RUnlock()
	return append([]faultRecord(nil), faultSchedule...)
}

// withSchedule installs a schedule for the duration of one test and always
// clears it afterwards: the schedule is package state shared by every
// simulation in the process, so a failing assertion must not leak faults into
// whatever test runs next.
func withSchedule(t *testing.T, faults []FaultDescriptor) {
	t.Helper()
	setFaultSchedule(faults)
	t.Cleanup(clearFaultSchedule)
}

// TestSetFaultSchedule_MapsDescriptorsToRecords pins how each descriptor kind
// becomes an injectable record: which RPC call the fault lands on, and what
// error the client observes. A wrong mapping here would inject the wrong fault
// while every scenario still "ran", which is the silent happy-path failure the
// simulation suite exists to prevent.
func TestSetFaultSchedule_MapsDescriptorsToRecords(t *testing.T) {
	tests := []struct {
		name       string
		fault      FaultDescriptor
		wantTarget string
		// wantErr is matched exactly for plain errors and as a substring for
		// rpc.Error, whose message carries provider detail around it.
		wantErr      string
		wantRPCCode  int
		wantRecorded bool
	}{
		{
			name:         "timeout targets GetEvents with a deadline error",
			fault:        FaultDescriptor{Kind: FaultTimeout, CallIndex: 3},
			wantTarget:   "GetEvents",
			wantErr:      "context deadline exceeded",
			wantRecorded: true,
		},
		{
			name:         "rate limit targets GetEvents with the RPC 429 error",
			fault:        FaultDescriptor{Kind: FaultRateLimit, CallIndex: 3},
			wantTarget:   "GetEvents",
			wantErr:      "rate limited",
			wantRPCCode:  -32000,
			wantRecorded: true,
		},
		{
			name:         "malformed page targets GetEvents with a decode error",
			fault:        FaultDescriptor{Kind: FaultMalformedPage, CallIndex: 3},
			wantTarget:   "GetEvents",
			wantErr:      "unexpected end of JSON input",
			wantRecorded: true,
		},
		{
			name:         "out of range targets GetEvents with a retention-window error",
			fault:        FaultDescriptor{Kind: FaultOutOfRange, CallIndex: 3},
			wantTarget:   "GetEvents",
			wantErr:      "startLedger outside of retention window",
			wantRPCCode:  -32600,
			wantRecorded: true,
		},
		{
			name:         "truncated page targets GetEvents with a truncation error",
			fault:        FaultDescriptor{Kind: FaultTruncatedPage, CallIndex: 3},
			wantTarget:   "GetEvents",
			wantErr:      "truncated response",
			wantRecorded: true,
		},
		{
			// The moving-back fault has no error to return: the chain has to
			// move its head backwards, so the record only marks the call that
			// should observe it.
			name:         "moving head targets GetEvents with no error",
			fault:        FaultDescriptor{Kind: FaultRPCMovingBack, CallIndex: 3},
			wantTarget:   "GetEvents",
			wantRecorded: true,
		},
		{
			name:         "health error targets GetHealth",
			fault:        FaultDescriptor{Kind: FaultGetHealthError, CallIndex: 3},
			wantTarget:   "GetHealth",
			wantErr:      "getHealth: connection refused",
			wantRecorded: true,
		},
		{
			// Crashes are scheduled by step, not by call, and are consumed by
			// crashAfterStep, so they must not land in the call-indexed
			// schedule where they could never match.
			name:         "crash is not a call-indexed fault",
			fault:        FaultDescriptor{Kind: FaultCrash, AfterStep: 2},
			wantRecorded: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withSchedule(t, []FaultDescriptor{tt.fault})

			schedule := installedSchedule()
			if !tt.wantRecorded {
				assert.Empty(t, schedule, "descriptor should not be recorded in the call-indexed schedule")
				return
			}

			require.Len(t, schedule, 1)
			record := schedule[0]
			assert.Equal(t, tt.wantTarget, record.target)
			assert.Equal(t, tt.fault.CallIndex, record.callIndex, "the nominated call index must survive the mapping")

			if tt.wantErr == "" {
				assert.NoError(t, record.err)
				return
			}
			if tt.wantRPCCode != 0 {
				var rpcErr *rpc.Error
				require.ErrorAs(t, record.err, &rpcErr)
				assert.Equal(t, tt.wantRPCCode, rpcErr.Code)
				assert.Contains(t, rpcErr.Message, tt.wantErr)
				return
			}
			assert.EqualError(t, record.err, tt.wantErr)
		})
	}
}

// TestFaultForCall_FiresOnNominatedCallOnly verifies the scheduler's core
// promise: the fault is armed for exactly one GetEvents call index. Both
// failure directions matter — firing early or on every call corrupts a
// scenario just as much as never firing at all.
func TestFaultForCall_FiresOnNominatedCallOnly(t *testing.T) {
	const nominated = 3
	withSchedule(t, []FaultDescriptor{{Kind: FaultTimeout, CallIndex: nominated}})

	for _, call := range []int{1, 2, 4, 5} {
		t.Run(fmt.Sprintf("call_%d_is_not_faulted", call), func(t *testing.T) {
			injected, ok := faultForCall(call)
			assert.False(t, ok)
			assert.Nil(t, injected)
		})
	}

	t.Run("nominated_call_is_faulted", func(t *testing.T) {
		injected, ok := faultForCall(nominated)
		require.True(t, ok)
		require.NotNil(t, injected)
		assert.EqualError(t, injected.err, "context deadline exceeded")
	})
}

// TestFaultForCall_IgnoresNonGetEventsTargets checks the target filter: a
// health fault shares the schedule but is addressed by step, so a GetEvents
// call must never pick it up and return a nonsense error.
func TestFaultForCall_IgnoresNonGetEventsTargets(t *testing.T) {
	withSchedule(t, []FaultDescriptor{
		{Kind: FaultGetHealthError, CallIndex: 1},
		{Kind: FaultCrash, AfterStep: 1},
	})

	schedule := installedSchedule()
	require.Len(t, schedule, 1, "only the health fault belongs in the call-indexed schedule")
	assert.Equal(t, "GetHealth", schedule[0].target)

	for call := 1; call <= 3; call++ {
		_, ok := faultForCall(call)
		assert.False(t, ok, "call %d must not match a GetHealth fault", call)
	}
}

// TestFaultForCall_NoSchedule covers the default state, including calls made
// before a harness has installed anything: an unscheduled process must run
// clean rather than fault on a leftover index.
func TestFaultForCall_NoSchedule(t *testing.T) {
	withSchedule(t, nil)
	assert.Empty(t, installedSchedule())

	for call := 1; call <= 5; call++ {
		injected, ok := faultForCall(call)
		assert.False(t, ok, "call %d must not be faulted without a schedule", call)
		assert.Nil(t, injected)
	}
}

// TestClearFaultSchedule_StopsAllFurtherFaults covers the teardown path the
// harness defers after every run. A clear that only emptied some records would
// leave the next scenario inheriting this one's faults.
func TestClearFaultSchedule_StopsAllFurtherFaults(t *testing.T) {
	withSchedule(t, []FaultDescriptor{
		{Kind: FaultTimeout, CallIndex: 1},
		{Kind: FaultRateLimit, CallIndex: 2},
		{Kind: FaultTruncatedPage, CallIndex: 3},
	})

	_, ok := faultForCall(2)
	require.True(t, ok, "the schedule must be armed before the clear is meaningful")
	require.Len(t, installedSchedule(), 3)

	clearFaultSchedule()
	assert.Empty(t, installedSchedule())
	for call := 1; call <= 3; call++ {
		_, ok := faultForCall(call)
		assert.False(t, ok, "call %d must not fault after the schedule is cleared", call)
	}

	// Clearing twice is what a nested harness or a repeated test run does;
	// it must stay a no-op rather than panic or resurrect records.
	clearFaultSchedule()
	assert.Empty(t, installedSchedule())
}

// TestSetFaultSchedule_ReplacesPreviousSchedule pins that installing a
// schedule starts from scratch. Appending instead of replacing would make a
// second run in the same process inherit the first run's faults, so scenarios
// would fail or pass depending on test order.
func TestSetFaultSchedule_ReplacesPreviousSchedule(t *testing.T) {
	withSchedule(t, []FaultDescriptor{{Kind: FaultTimeout, CallIndex: 2}})
	require.Len(t, installedSchedule(), 1)

	setFaultSchedule([]FaultDescriptor{{Kind: FaultRateLimit, CallIndex: 4}})

	schedule := installedSchedule()
	require.Len(t, schedule, 1, "a new schedule must replace the old one, not extend it")

	_, ok := faultForCall(2)
	assert.False(t, ok, "the previous run's fault must not survive")

	injected, ok := faultForCall(4)
	require.True(t, ok)
	assert.Contains(t, injected.err.Error(), "rate limited")

	// An empty descriptor list is how a run without faults is expressed, so it
	// must leave nothing armed.
	setFaultSchedule(nil)
	assert.Empty(t, installedSchedule())
}

// TestCrashAfterStep_ReturnsNominatedStep covers the helper the harness reads
// to decide where a simulated restart happens. The harness crashes on the
// first step after this value, so an off-by-one or a scan that ignores the
// crash descriptor turns the crash scenarios into plain runs.
//
// The restart itself is deliberately not observable from outside: the harness
// builds the new ingester over the same store, so only persisted state
// survives and the run is meant to continue exactly as if nothing happened.
// That is why the step choice is pinned here rather than inferred from a
// finished run.
func TestCrashAfterStep_ReturnsNominatedStep(t *testing.T) {
	tests := []struct {
		name   string
		faults []FaultDescriptor
		want   int
	}{
		{
			name:   "no faults means no crash",
			faults: nil,
			want:   0,
		},
		{
			name:   "crash on step 3",
			faults: []FaultDescriptor{{Kind: FaultCrash, AfterStep: 3}},
			want:   3,
		},
		{
			name: "crash alongside RPC faults",
			faults: []FaultDescriptor{
				{Kind: FaultTimeout, CallIndex: 1},
				{Kind: FaultCrash, AfterStep: 2},
				{Kind: FaultRateLimit, CallIndex: 2},
			},
			want: 2,
		},
		{
			name: "RPC and health faults alone never crash",
			faults: []FaultDescriptor{
				{Kind: FaultTimeout, CallIndex: 1},
				{Kind: FaultGetHealthError, AfterStep: 2},
				{Kind: FaultRPCMovingBack, AfterStep: 3, Ledger: 5},
			},
			want: 0,
		},
		{
			// The harness crashes once and clears its own marker, so the first
			// crash descriptor decides the step even when a later one names an
			// earlier step.
			name: "first crash descriptor decides the step",
			faults: []FaultDescriptor{
				{Kind: FaultCrash, AfterStep: 4},
				{Kind: FaultCrash, AfterStep: 1},
			},
			want: 4,
		},
		{
			name:   "crash without a step means no crash",
			faults: []FaultDescriptor{{Kind: FaultCrash}},
			want:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, crashAfterStep(tt.faults))
		})
	}
}

// TestVirtualChain_GetEvents_InjectsScheduledFault exercises the helper where
// it is actually consumed: the chain increments its call count and asks the
// scheduler per GetEvents call, so this is the wiring a unit test on
// faultForCall alone would not catch.
func TestVirtualChain_GetEvents_InjectsScheduledFault(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	newChain := func() *VirtualChain {
		chain := NewVirtualChain(NewVirtualClock(now), now, 100)
		chain.AdvanceTo(50)
		return chain
	}
	req := rpc.GetEventsRequest{StartLedger: 2}

	t.Run("nominated call returns the scheduled error", func(t *testing.T) {
		withSchedule(t, []FaultDescriptor{{Kind: FaultRateLimit, CallIndex: 2}})
		chain := newChain()

		_, err := chain.GetEvents(context.Background(), req)
		require.NoError(t, err, "call 1 is not the nominated index")

		_, err = chain.GetEvents(context.Background(), req)
		var rpcErr *rpc.Error
		require.ErrorAs(t, err, &rpcErr)
		assert.Equal(t, -32000, rpcErr.Code)

		_, err = chain.GetEvents(context.Background(), req)
		require.NoError(t, err, "the fault is armed for one call, not every call after it")
	})

	t.Run("cleared schedule leaves the chain clean", func(t *testing.T) {
		setFaultSchedule([]FaultDescriptor{{Kind: FaultTimeout, CallIndex: 1}})
		clearFaultSchedule()
		chain := newChain()

		_, err := chain.GetEvents(context.Background(), req)
		require.NoError(t, err, "a cleared schedule must not inject on the first call")
	})
}

// TestFaultSchedule_DeterministicForSeed covers the promise that makes a
// failing randomized run reproducible: replaying a seed has to install the
// identical schedule, including each fault's target, index and error text.
func TestFaultSchedule_DeterministicForSeed(t *testing.T) {
	withSchedule(t, nil)
	faultedSeeds := 0

	for seed := uint64(1); seed <= 50; seed++ {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			setFaultSchedule(RandomScenario(seed).Faults)
			first := installedSchedule()

			setFaultSchedule(RandomScenario(seed).Faults)
			second := installedSchedule()

			assert.Equal(t, first, second, "the same seed must install the same schedule")
			if len(first) > 0 {
				faultedSeeds++
			}
		})
	}

	require.Greater(t, faultedSeeds, 0, "no seed produced a fault, so determinism was never exercised")
}

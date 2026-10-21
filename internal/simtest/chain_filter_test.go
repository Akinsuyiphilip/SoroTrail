package simtest

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sorotrail/sorotrail/internal/rpc"
)

// TestMatchesFilter pins the simulated chain's filter, which stands in for the
// RPC's own. Nothing else in the tree exercises it, and every scenario that
// reads events back through the virtual chain rests on it: if it ever drifts
// from real filtering, those scenarios keep passing while asserting against
// events the real getEvents would never have returned.
//
// The function is pure, so each case is only an event and a filter. No chain,
// no store, and no fixtures beyond the values below.
func TestMatchesFilter(t *testing.T) {
	// The simulator compares filter topics against the event's raw JSON topics
	// (rpc.Event.TopicJSON), so these are spelled the way an event carries them
	// rather than the way a contract emits them.
	var (
		transfer  = json.RawMessage(`"transfer"`)
		symbolABC = json.RawMessage(`{"symbol":"ABC"}`)
		symbolXYZ = json.RawMessage(`{"symbol":"XYZ"}`)
		amountOne = json.RawMessage(`{"u32":1}`)
	)

	// Two events from different contracts that share a topic list shape but not
	// their contract ID or symbol, so each filter dimension can be varied on its
	// own without the others accidentally deciding the case.
	byA := rpc.Event{
		ID:         "0000000007-0000000001",
		Type:       "contract",
		Ledger:     7,
		ContractID: "CAAA",
		TopicJSON:  []json.RawMessage{transfer, symbolABC, amountOne},
	}
	byB := rpc.Event{
		ID:         "0000000007-0000000002",
		Type:       "contract",
		Ledger:     7,
		ContractID: "CBBB",
		TopicJSON:  []json.RawMessage{transfer, symbolXYZ, amountOne},
	}
	// An event with no topics at all, so a topic filter cannot be satisfied
	// vacuously by the absence of anything to compare against.
	bare := rpc.Event{ID: "0000000008-0000000001", Type: "contract", Ledger: 8, ContractID: "CAAA"}

	// Topic matching here is by presence: makeTopicStrings flattens TopicJSON,
	// so an entry in f.Topics is satisfied by any of the event's topics at any
	// index rather than only by the topic in the slot it names. Real RPC
	// filtering is positional. These cases pin what the simulator does today, so
	// a move to strict positional matching registers as a deliberate change here
	// instead of sliding through unnoticed.
	tests := []struct {
		name   string
		event  rpc.Event
		filter rpc.EventFilter
		want   bool
		why    string
	}{
		{
			name:   "empty filter matches the first event",
			event:  byA,
			filter: rpc.EventFilter{},
			want:   true,
			why:    "an empty filter is how a caller asks for every event, so it must not narrow anything",
		},
		{
			name:   "empty filter matches an unrelated event too",
			event:  byB,
			filter: rpc.EventFilter{},
			want:   true,
			why:    "the same empty filter has to admit every event, not just the one it was written against",
		},
		{
			name:   "contract id selects its own contract",
			event:  byA,
			filter: rpc.EventFilter{ContractIDs: []string{"CAAA"}},
			want:   true,
			why:    "the filter names the emitting contract, so its event qualifies",
		},
		{
			name:   "contract id rejects another contract",
			event:  byB,
			filter: rpc.EventFilter{ContractIDs: []string{"CAAA"}},
			want:   false,
			why:    "a filter naming one contract must not hand back another contract's events",
		},
		{
			name:   "contract id list admits any listed contract",
			event:  byB,
			filter: rpc.EventFilter{ContractIDs: []string{"CAAA", "CBBB"}},
			want:   true,
			why:    "contractIds is any-of at the RPC, so a listed contract other than the first still matches",
		},
		{
			name:   "type selects the matching event",
			event:  byA,
			filter: rpc.EventFilter{Type: "contract"},
			want:   true,
			why:    "the event is of the requested type",
		},
		{
			name:   "type rejects a different type",
			event:  byA,
			filter: rpc.EventFilter{Type: "system"},
			want:   false,
			why:    "a type filter is a constraint, not a hint: getEvents must not return contract events for it",
		},
		{
			name:   "topic filter matches the first topic",
			event:  byA,
			filter: rpc.EventFilter{Topics: [][]string{{`"transfer"`}}},
			want:   true,
			why:    "one filter entry describing the event's leading topic is enough to match it",
		},
		{
			name:   "topic filter matches across two positions",
			event:  byA,
			filter: rpc.EventFilter{Topics: [][]string{{`"transfer"`}, {`{"symbol":"ABC"}`}}},
			want:   true,
			why:    "every filter entry is another condition, and this event carries both",
		},
		{
			name:   "topic alternatives admit either value in one position",
			event:  byA,
			filter: rpc.EventFilter{Topics: [][]string{{`"mint"`, `"transfer"`}}},
			want:   true,
			why:    "the inner list is any-of, so matching one of its alternatives satisfies that position",
		},
		{
			name:   "topic filter rejects an event missing one of its topics",
			event:  byA,
			filter: rpc.EventFilter{Topics: [][]string{{`"transfer"`}, {`{"symbol":"XYZ"}`}}},
			want:   false,
			why:    "the event carries the first topic but not the second, and every entry must match",
		},
		{
			name:   "topic filter rejects an event with no topics",
			event:  bare,
			filter: rpc.EventFilter{Topics: [][]string{{`"transfer"`}}},
			want:   false,
			why:    "an empty topic list has nothing that could satisfy the filter, so this must not fall through to a match",
		},
		{
			name:  "combined filter matches when every dimension agrees",
			event: byA,
			filter: rpc.EventFilter{
				Type:        "contract",
				ContractIDs: []string{"CAAA"},
				Topics:      [][]string{{`"transfer"`}, {`{"symbol":"ABC"}`}},
			},
			want: true,
			why:  "type, contract, and topics all line up, which is the one shape the RPC would return",
		},
		{
			name:  "combined filter rejects a disagreeing contract",
			event: byB,
			filter: rpc.EventFilter{
				Type:        "contract",
				ContractIDs: []string{"CAAA"},
				Topics:      [][]string{{`"transfer"`}},
			},
			want: false,
			why:  "the contract disagrees while type and topic agree, so a filter that leaked on any one dimension would show up here",
		},
		{
			name:  "combined filter rejects a disagreeing type",
			event: byA,
			filter: rpc.EventFilter{
				Type:        "diagnostic",
				ContractIDs: []string{"CAAA"},
				Topics:      [][]string{{`"transfer"`}},
			},
			want: false,
			why:  "only the type disagrees, which is enough to reject the event",
		},
		{
			name:  "combined filter rejects a disagreeing topic",
			event: byA,
			filter: rpc.EventFilter{
				Type:        "contract",
				ContractIDs: []string{"CAAA"},
				Topics:      [][]string{{`"transfer"`}, {`{"symbol":"XYZ"}`}},
			},
			want: false,
			why:  "only a topic disagrees, which proves the topic check is part of the conjunction rather than decorative",
		},
		{
			name:   "filter matching nothing yields no match",
			event:  byA,
			filter: rpc.EventFilter{ContractIDs: []string{"CNOPE"}},
			want:   false,
			why:    "getEvents skips the events this reports false for, so a query that matches nothing becomes an empty page; the function has no error return, and that is the whole of the no-error contract",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesFilter(tt.event, tt.filter)
			assert.Equal(t, tt.want, got, tt.why)
		})
	}
}

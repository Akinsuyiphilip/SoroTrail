package api

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/store"
)

// Event filtering is written twice: internal/store turns a [store.EventFilter]
// into SQL for the REST endpoints, and [Broker.eventMatches] applies the same
// struct to every event pushed onto a live stream. Two implementations of one
// language drift, and the direction this one drifts in is not symmetric — a
// predicate that exists only in SQL leaves the stream delivering events the
// equivalent query would have excluded, so a client that asked for one thing
// and used the stream to fill its gaps receives rows it never selected.
//
// The table below is the single definition of "what a filter means" that both
// halves are judged against:
//
//   - [TestEventMatches_ParityCasesNarrowTheCorpus] runs it through the matcher
//     with no database at all, which is what fails when a case exists but the
//     matcher ignores the field it names.
//   - [TestEventMatches_CoversEveryFilterField] enumerates the fields of
//     [store.EventFilter] by reflection, so a field added to the struct cannot
//     be quietly left out of the table.
//   - TestEventMatches_ParityWithSQLQueryPath (integration tag) seeds the same
//     corpus into a real Postgres and asserts, case by case, that the rows SQL
//     selects are exactly the rows the matcher selects. That is the half which
//     catches a predicate implemented in one language only, and it is the one
//     that a reviewer cannot infer from either file alone.
//
// Both the corpus and the cases are written so that every filter field splits
// the corpus into a group that matches and a group that does not. A case whose
// filter selects everything would pass the SQL comparison while proving
// nothing, so the corpus is part of the assertion, not fixture decoration.

const (
	// parityContractA, parityContractB and parityUnknownContract are 56-character
	// IDs shaped like contract Strkeys; the store writes contract_id as text and
	// does not decode it, so they need no valid checksum. A is the only one that
	// starts with "CDLZ", which is what gives the contract_id_prefix case
	// something to separate, and parityUnknownContract is the ID no corpus event
	// carries.
	parityContractA       = "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"
	parityContractB       = "CBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	parityUnknownContract = "CDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"

	parityTxHashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	parityTxHashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	parityTxHashC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// The topic arrays the corpus stores. Every value used by a topic case below is
// one of these *complete elements*, never a partial object: Postgres `@>` reads
// a right-hand object as a subset requirement, while the matcher compares whole
// elements, and the two operators only agree when the needle is the entire
// stored element. See topicMatches for why that asymmetry is not pinned here.
var (
	parityTopicsTransfer = json.RawMessage(`[{"symbol":"transfer"},{"address":"GA"},{"u64":7}]`)
	parityTopicsMint     = json.RawMessage(`[{"symbol":"mint"},{"u64":7}]`)
	parityTopicsBurn     = json.RawMessage(`[{"symbol":"burn"}]`)
	parityTopicsClaim    = json.RawMessage(`[{"symbol":"claim"},{"u64":1},{"address":"GB"},{"i128":"50"}]`)
	parityTopicsStrings  = json.RawMessage(`["transfer","to_addr"]`)
)

// parityAnchor is the timestamp of the first corpus event. Every event sits a
// whole number of hours after it and every time case sits on one of those
// hours, so a comparison is never decided by the sub-second precision Postgres
// keeps and Go does not.
var parityAnchor = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

// parityEventID mirrors the store's TOID-shaped primary key: zero-padded so
// lexicographic order is insertion order, and offset well away from the IDs the
// other API integration fixtures insert.
func parityEventID(n int) string {
	return fmt.Sprintf("%020d-%010d", 9000+n, 0)
}

// parityCorpus is the row set both filter implementations are judged against.
//
// Read it as a coverage table rather than a plausible event log: the columns
// exist so that each field of [store.EventFilter] separates the corpus. Two
// networks, two contracts, three event types, three transaction hashes,
// distinct ledger numbers and one hour per event, indices that repeat, four
// events with no value payload, and five topic shapes covering the positional
// filters up to array index 3.
func parityCorpus() []store.Event {
	specs := []struct {
		contract string
		network  string
		kind     string
		txHash   string
		topics   json.RawMessage
		value    string
		ledger   int64
		hour     int64
		txIndex  int32
		opIndex  int32
		success  bool
	}{
		{parityContractA, "testnet", "contract", parityTxHashA, parityTopicsTransfer, `{"i128":"1000"}`, 1001, 0, 0, 0, true},
		{parityContractB, "testnet", "contract", parityTxHashB, parityTopicsMint, `{"i128":"1001"}`, 1002, 1, 1, 1, true},
		{parityContractA, "testnet", "diagnostic", parityTxHashA, parityTopicsBurn, "", 1003, 2, 0, 0, false},
		{parityContractB, "futurenet", "contract", parityTxHashC, parityTopicsClaim, `{"i128":"1003"}`, 1004, 3, 2, 0, true},
		{parityContractA, "testnet", "contract", parityTxHashB, parityTopicsTransfer, `{"i128":"1004"}`, 1005, 4, 1, 1, false},
		{parityContractB, "testnet", "system", parityTxHashA, parityTopicsStrings, "", 1006, 5, 0, 1, true},
		{parityContractA, "futurenet", "contract", parityTxHashC, parityTopicsMint, `{"i128":"1006"}`, 1007, 6, 1, 0, true},
		{parityContractB, "testnet", "contract", parityTxHashB, parityTopicsClaim, `{"i128":"1007"}`, 1008, 7, 2, 1, false},
		{parityContractA, "testnet", "diagnostic", parityTxHashA, parityTopicsTransfer, "", 1009, 8, 0, 0, true},
		{parityContractB, "futurenet", "contract", parityTxHashC, parityTopicsBurn, `{"i128":"1009"}`, 1010, 9, 1, 1, true},
		{parityContractA, "testnet", "contract", parityTxHashA, parityTopicsStrings, `{"i128":"1010"}`, 1011, 10, 2, 0, false},
		{parityContractB, "testnet", "system", parityTxHashB, parityTopicsClaim, "", 1012, 11, 0, 1, true},
	}

	out := make([]store.Event, 0, len(specs))
	for i, s := range specs {
		e := store.Event{
			ID:               parityEventID(i + 1),
			Network:          s.network,
			ContractID:       s.contract,
			Ledger:           s.ledger,
			Type:             s.kind,
			TxHash:           s.txHash,
			TxIndex:          s.txIndex,
			OpIndex:          s.opIndex,
			InSuccessfulCall: s.success,
			Topics:           s.topics,
			CreatedAt:        parityAnchor.Add(time.Duration(s.hour) * time.Hour),
		}
		// An empty spec value means the event carries no payload at all, which
		// the store writes as SQL NULL — the thing the has_value filter reads.
		if s.value != "" {
			e.Value = json.RawMessage(s.value)
		}
		out = append(out, e)
	}
	return out
}

// parityBase is the filter every case starts from.
//
// Scope is pinned to [store.WildcardScope] because its zero value is an
// authorization boundary that denies everything, not the absence of a
// constraint: without it the SQL side would return no rows for every case and
// the comparison would pass vacuously. A wildcard scope emits no predicate, so
// the two paths are still comparing the same user filters.
//
// Limit is far above the size of the corpus so no case is cut short by
// pagination, which would look like a filter disagreement.
func parityBase() store.EventFilter {
	return store.EventFilter{
		Scope: store.WildcardScope(),
		Limit: 100,
	}
}

// parityCase is one filter to judge, plus what it is supposed to prove.
type parityCase struct {
	name string
	// field names the single [store.EventFilter] field this case constrains,
	// and is what [TestEventMatches_CoversEveryFilterField] matches against the
	// struct's own field list. Empty means a combination case, which is checked
	// for agreement only and not for coverage.
	field string
	// mutate applies the constraint on top of [parityBase]. Kept as a function
	// rather than a literal filter so the base stays the single source of the
	// scope and page-size conventions above.
	mutate func(f *store.EventFilter)
	// expectNone marks a case whose filter is supposed to select nothing. Both
	// paths agreeing on an empty result is worth asserting — an unimplemented
	// predicate shows up as "SQL zero, stream everything" — but it must be
	// deliberate, because a case that selects nothing by accident proves the
	// same nothing as a case that was never designed.
	expectNone bool
}

// parityCases returns the table every filter assertion runs over: one case per
// narrowing field of [store.EventFilter], then combinations of them.
func parityCases() []parityCase {
	yes := true
	no := false
	one := int32(1)
	return []parityCase{
		{
			name:  "network",
			field: "Network",
			mutate: func(f *store.EventFilter) {
				f.Network = "futurenet"
			},
		},
		{
			name:  "contract id",
			field: "ContractID",
			mutate: func(f *store.EventFilter) {
				f.ContractID = parityContractA
			},
		},
		{
			name:  "contract id list",
			field: "ContractIDs",
			mutate: func(f *store.EventFilter) {
				// One ID present in the corpus, one absent: the list is a union,
				// so an unknown ID must not narrow it further.
				f.ContractIDs = []string{parityContractB, parityUnknownContract}
			},
		},
		{
			name:  "contract id prefix",
			field: "ContractIDPrefix",
			mutate: func(f *store.EventFilter) {
				f.ContractIDPrefix = "CDLZ"
			},
		},
		{
			name:  "types",
			field: "Types",
			mutate: func(f *store.EventFilter) {
				f.Types = []string{"diagnostic", "system"}
			},
		},
		{
			name:  "topic any position",
			field: "Topic",
			mutate: func(f *store.EventFilter) {
				f.Topic = json.RawMessage(`{"symbol":"mint"}`)
			},
		},
		{
			name:  "tx hash",
			field: "TxHash",
			mutate: func(f *store.EventFilter) {
				f.TxHash = parityTxHashA
			},
		},
		{
			name:  "in successful call false",
			field: "InSuccessfulCall",
			mutate: func(f *store.EventFilter) {
				f.InSuccessfulCall = &no
			},
		},
		{
			name:  "topic 0",
			field: "Topic0",
			mutate: func(f *store.EventFilter) {
				f.Topic0 = json.RawMessage(`{"symbol":"transfer"}`)
			},
		},
		{
			// The same value as the Topic case above, at a fixed position:
			// transfer events carry {"u64":7} at index 2 and mint events at
			// index 1, so a positional filter must select fewer than the
			// any-position one.
			name:  "topic 1",
			field: "Topic1",
			mutate: func(f *store.EventFilter) {
				f.Topic1 = json.RawMessage(`{"u64":7}`)
			},
		},
		{
			name:  "topic 2",
			field: "Topic2",
			mutate: func(f *store.EventFilter) {
				f.Topic2 = json.RawMessage(`{"u64":7}`)
			},
		},
		{
			name:  "topic 3",
			field: "Topic3",
			mutate: func(f *store.EventFilter) {
				f.Topic3 = json.RawMessage(`{"i128":"50"}`)
			},
		},
		{
			name:  "topic contains",
			field: "TopicContains",
			mutate: func(f *store.EventFilter) {
				f.TopicContains = json.RawMessage(`[{"symbol":"transfer"},{"u64":7}]`)
			},
		},
		{
			name:  "has value false",
			field: "HasValue",
			mutate: func(f *store.EventFilter) {
				f.HasValue = &no
			},
		},
		{
			name:  "tx index",
			field: "TxIndex",
			mutate: func(f *store.EventFilter) {
				f.TxIndex = &one
			},
		},
		{
			name:  "op index",
			field: "OpIndex",
			mutate: func(f *store.EventFilter) {
				f.OpIndex = &one
			},
		},
		{
			name:  "from ledger",
			field: "FromLedger",
			mutate: func(f *store.EventFilter) {
				f.FromLedger = 1007
			},
		},
		{
			name:  "to ledger",
			field: "ToLedger",
			mutate: func(f *store.EventFilter) {
				f.ToLedger = 1004
			},
		},
		{
			name:  "from time",
			field: "FromTime",
			mutate: func(f *store.EventFilter) {
				f.FromTime = parityAnchor.Add(6 * time.Hour)
			},
		},
		{
			name:  "to time",
			field: "ToTime",
			mutate: func(f *store.EventFilter) {
				f.ToTime = parityAnchor.Add(3 * time.Hour)
			},
		},

		// Combinations. These are the filters real requests send, and where a
		// difference between the two implementations shows up fastest: the
		// store ANDs predicates into one WHERE clause, while a matcher that
		// only knows some of them degrades an exact query into a superset.
		{
			name: "contract and type and ledger range",
			mutate: func(f *store.EventFilter) {
				f.ContractID = parityContractA
				f.Types = []string{"contract"}
				f.FromLedger = 1002
				f.ToLedger = 1011
			},
		},
		{
			name: "topic position with value presence",
			mutate: func(f *store.EventFilter) {
				f.Topic0 = json.RawMessage(`{"symbol":"claim"}`)
				f.HasValue = &yes
			},
		},
		{
			name: "successful call with tx index and time window",
			mutate: func(f *store.EventFilter) {
				f.InSuccessfulCall = &yes
				f.TxIndex = &one
				f.FromTime = parityAnchor.Add(5 * time.Hour)
				f.ToTime = parityAnchor.Add(10 * time.Hour)
			},
		},

		// Deliberate empty results: agreement on zero rows is agreement, and a
		// predicate missing from one side turns these into "SQL excludes
		// everything, the stream delivers everything".
		{
			name:  "unknown contract",
			field: "",
			mutate: func(f *store.EventFilter) {
				f.ContractID = parityUnknownContract
			},
			expectNone: true,
		},
		{
			name: "contract and tx hash that never co-occur",
			mutate: func(f *store.EventFilter) {
				f.ContractID = parityContractB
				f.TxHash = parityTxHashC
				f.FromLedger = 1011
			},
			expectNone: true,
		},
		{
			name: "topic that no event carries",
			mutate: func(f *store.EventFilter) {
				f.Topic = json.RawMessage(`{"symbol":"clawback"}`)
			},
			expectNone: true,
		},
	}
}

// paritySelected applies the matcher to the corpus and returns the selected IDs
// in ascending order, so a disagreement says which events it is about instead
// of which slice index differed.
func paritySelected(f store.EventFilter, corpus []store.Event) []string {
	ids := make([]string, 0, len(corpus))
	for _, e := range corpus {
		if eventMatches(f, e) {
			ids = append(ids, e.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// parityFilter applies a case's constraint to the base filter.
func parityFilter(c parityCase) store.EventFilter {
	f := parityBase()
	c.mutate(&f)
	return f
}

// TestEventMatches_ParityCasesNarrowTheCorpus checks the matcher side of the
// table without a database: every case must actually do something to a filter,
// and must do it to the corpus.
//
// The name of the failure it prevents is in the assertion message. A case whose
// field the matcher never reads selects the whole corpus, exactly like the
// unconstrained filter does, and would still "agree" with any SQL that selects
// the whole corpus — which is what a store-side predicate that nobody
// implements would look like on the day it was added.
func TestEventMatches_ParityCasesNarrowTheCorpus(t *testing.T) {
	corpus := parityCorpus()
	require.Len(t, corpus, 12, "the parity corpus is sized deliberately; see parityCorpus")

	ids := make([]string, 0, len(corpus))
	seen := make(map[string]struct{}, len(corpus))
	for _, e := range corpus {
		require.NotEmpty(t, e.Topics, "a topic-less corpus event makes every topic case vacuous")
		_, dup := seen[e.ID]
		require.False(t, dup, "corpus IDs must be unique for the set comparisons to mean anything")
		seen[e.ID] = struct{}{}
		ids = append(ids, e.ID)
	}

	all := parityBase()
	require.Equal(t, ids, paritySelected(all, corpus),
		"the base filter must select the whole corpus; a case cannot prove narrowing against a partial baseline")

	for _, c := range parityCases() {
		t.Run(c.name, func(t *testing.T) {
			got := paritySelected(parityFilter(c), corpus)
			if c.expectNone {
				assert.Empty(t, got,
					"this case is written to select nothing; if the corpus changed so that it now matches, "+
						"the case has stopped testing the agreement on an empty result")
				return
			}
			assert.NotEmpty(t, got,
				"filter case %q selects no corpus event, so agreeing on it proves nothing; "+
					"the corpus has to contain at least one event this filter matches", c.name)
			if c.field != "" {
				assert.NotEqual(t, ids, got,
					"%s is constrained but the matcher selected every event anyway: eventMatches does not read "+
						"that field, so the stream delivers rows the equivalent SQL query would have excluded",
					c.field)
			}
		})
	}
}

// parityNotAFilterField names the EventFilter fields that decide *which page*
// of a result a caller sees rather than *which events* are in the result. They
// belong to the query, not to the match predicate, so neither the matcher nor a
// parity case is expected to touch them.
//
// Scope is the one field here that is a constraint on rows: it is the tenant
// authorization boundary, and it is applied out of band on the stream so that a
// revoked grant stops flowing without the client reconnecting (see
// internal/broadcast.Subscription.SetScope). It is in this list because the
// matcher is not where that decision belongs, not because it narrows nothing —
// and a wildcard Scope is pinned in [parityBase], so the SQL path applies no
// boundary predicate either and the two paths stay comparable.
var parityNotAFilterField = map[string]string{
	"Cursor":  "keyset pagination: chooses where a page starts",
	"Limit":   "page size",
	"Order":   "sort direction",
	"OrderBy": "sort column",
	"Scope":   "tenant authorization boundary, applied by the stream layer rather than the matcher",
}

// TestEventMatches_CoversEveryFilterField is the half of the parity that needs
// no database and no case to be remembered.
//
// It enumerates the fields of [store.EventFilter] themselves and requires each
// one to be either a single-field case in [parityCases] or an entry in
// [parityNotAFilterField] explaining why it is not a match predicate. The point
// is the day someone adds a filter: the REST side gets its predicate, the
// struct gets its field, and the stream keeps delivering the old, wider set —
// a change that is invisible to every test that only exercises the query path.
// This fails on that commit, and its message says which of the two things to do
// about it.
func TestEventMatches_CoversEveryFilterField(t *testing.T) {
	tabled := make(map[string]struct{}, len(parityCases()))
	for _, c := range parityCases() {
		if c.field != "" {
			tabled[c.field] = struct{}{}
		}
	}

	typ := reflect.TypeOf(store.EventFilter{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name

		if reason, ok := parityNotAFilterField[name]; ok {
			_, also := tabled[name]
			require.False(t, also,
				"%s is listed as not a match predicate (%s) but parityCases has a case for it; "+
					"the two lists contradict each other", name, reason)
			continue
		}

		_, ok := tabled[name]
		require.True(t, ok,
			"store.EventFilter has a %s field with no parity case: add a single-field case to parityCases "+
				"(and the matching predicate to eventMatches and buildEventWhereClause), or explain in "+
				"parityNotAFilterField why it is not a filter on rows", name)
	}

	// And the reverse direction: a case naming a field that no longer exists
	// would otherwise linger as coverage of nothing. Whether the matcher really
	// consults each of these fields is asserted by
	// TestEventMatches_ParityCasesNarrowTheCorpus, which requires a
	// single-field case to disagree with the unconstrained filter somewhere on
	// the corpus.
	for name := range tabled {
		_, found := typ.FieldByName(name)
		assert.True(t, found, "parityCases constrains %s, which store.EventFilter no longer has", name)
	}
}

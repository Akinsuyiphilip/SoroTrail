// Package api serves stored events over HTTP, including a Server-Sent Events
// subscription endpoint that pushes newly ingested events to clients in
// real time.
package api

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"

	"github.com/sorotrail/sorotrail/internal/store"
)

// subscriberBufferSize bounds the event channel per subscriber. When a
// subscriber's buffer is full, publishes to it are dropped (non-blocking).
const subscriberBufferSize = 256

// Broker fans out newly ingested events to subscribers with matching filters.
// It is the seam between the ingester (which calls Publish) and the SSE
// endpoint (which calls Subscribe). A nil *Broker is safe: all methods no-op.
type Broker struct {
	mu          sync.RWMutex
	subscribers map[int64]*subscriber
	nextID      int64
	closed      bool
}

type subscriber struct {
	id     int64
	filter store.EventFilter
	ch     chan store.Event
}

// NewBroker creates a Broker ready for use.
func NewBroker() *Broker {
	return &Broker{
		subscribers: make(map[int64]*subscriber),
	}
}

// Subscribe registers a filter and returns a receive-only channel of
// store.Event values plus a cancel function that removes the subscriber.
// The channel has a bounded buffer; when it overflows, the subscriber is
// dropped and the channel is closed. Callers must call cancel() to clean up.
func (b *Broker) Subscribe(filter store.EventFilter) (<-chan store.Event, func()) {
	if b == nil {
		ch := make(chan store.Event)
		close(ch)
		return ch, func() {}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		ch := make(chan store.Event)
		close(ch)
		return ch, func() {}
	}

	id := b.nextID
	b.nextID++
	sub := &subscriber{
		id:     id,
		filter: filter,
		ch:     make(chan store.Event, subscriberBufferSize),
	}
	b.subscribers[id] = sub

	cancel := func() {
		if b == nil {
			return
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if s, ok := b.subscribers[id]; ok {
			close(s.ch)
			delete(b.subscribers, id)
		}
	}
	return sub.ch, cancel
}

// Publish sends events to all subscribers whose filters match. Sends are
// non-blocking: a subscriber whose buffer is full is dropped (its channel is
// closed and it is removed). Publish runs in the ingester goroutine and must
// return quickly — it never blocks on a slow consumer.
func (b *Broker) Publish(events []store.Event) {
	if b == nil || len(events) == 0 {
		return
	}
	b.mu.RLock()
	if b.closed || len(b.subscribers) == 0 {
		b.mu.RUnlock()
		return
	}

	var dropped []int64
	for _, s := range b.subscribers {
		for _, e := range events {
			if !eventMatches(s.filter, e) {
				continue
			}
			select {
			case s.ch <- e:
			default:
				// Buffer full: drop this subscriber.
				dropped = append(dropped, s.id)
				goto nextSub // break out of the inner event loop
			}
		}
	nextSub:
	}
	b.mu.RUnlock()

	if len(dropped) > 0 {
		b.mu.Lock()
		for _, id := range dropped {
			if s, ok := b.subscribers[id]; ok {
				close(s.ch)
				delete(b.subscribers, id)
			}
		}
		b.mu.Unlock()
	}
}

// Shutdown closes all subscriber channels and removes them.
func (b *Broker) Shutdown() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, s := range b.subscribers {
		close(s.ch)
		delete(b.subscribers, id)
	}
}

// containsString reports whether want appears in values. It backs the
// set-membership filters, which the SQL side generates as `= ANY(...)`.
func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// eventMatches reports whether e satisfies every constraint in f. Zero-value
// fields mean "unconstrained"; the pointer fields (InSuccessfulCall, HasValue,
// TxIndex, OpIndex) opt in only when non-nil, because their zero value is a
// meaningful constraint.
//
// This is the stream-side half of the filter language. The REST half is the SQL
// that internal/store's buildEventWhereClause generates, and the two are
// asserted case by case against a real Postgres by
// TestEventMatches_ParityWithSQLQueryPath. Every constraint below therefore
// mirrors a predicate there: filtering twice, once in SQL and once in Go, is
// only safe while they mean the same thing, and the failure mode of a
// predicate that lands on one side alone is a stream that quietly
// over-delivers rows the equivalent query would have excluded.
//
// [store.EventFilter.Scope] is the one field not consulted. It is the tenant
// authorization boundary rather than a user filter, and the live stream applies
// it separately so a revoked grant stops flowing without the client
// reconnecting (see internal/broadcast.Subscription.SetScope); this Broker has
// no scope plumbing of its own, and its empty filter means "unconstrained", so
// an unset Scope must not be read as "deny everything" here. The parity table
// pins Scope to [store.WildcardScope] in every case, which generates no SQL
// predicate either, so the two paths still agree on the rows they select.
func eventMatches(f store.EventFilter, e store.Event) bool {
	if f.Network != "" && e.Network != f.Network {
		return false
	}
	// ContractID and ContractIDs are a union, the way a single
	// `contract_id = ANY(...)` built from both of them is on the SQL side.
	if len(f.ContractIDs) > 0 {
		if !containsString(f.ContractIDs, e.ContractID) && e.ContractID != f.ContractID {
			return false
		}
	} else if f.ContractID != "" && e.ContractID != f.ContractID {
		return false
	}
	if f.ContractIDPrefix != "" && !strings.HasPrefix(e.ContractID, f.ContractIDPrefix) {
		return false
	}
	if len(f.Types) > 0 && !containsString(f.Types, e.Type) {
		return false
	}
	if f.TxHash != "" && e.TxHash != f.TxHash {
		return false
	}
	if f.InSuccessfulCall != nil && e.InSuccessfulCall != *f.InSuccessfulCall {
		return false
	}
	if len(f.Topic) > 0 && !topicMatches(e.Topics, f.Topic) {
		return false
	}
	if len(f.TopicContains) > 0 && !topicsContain(e.Topics, f.TopicContains) {
		return false
	}
	for i, want := range []json.RawMessage{f.Topic0, f.Topic1, f.Topic2, f.Topic3} {
		if len(want) == 0 {
			continue
		}
		if !topicAtMatches(e.Topics, i, want) {
			return false
		}
	}
	// An absent value payload is an empty json.RawMessage, which is what the
	// store writes as SQL NULL and reads back as nil; a literal JSON `null`
	// payload is 4 bytes and is a value, as `value IS NOT NULL` also reads it.
	if f.HasValue != nil && (len(e.Value) > 0) != *f.HasValue {
		return false
	}
	if f.TxIndex != nil && e.TxIndex != *f.TxIndex {
		return false
	}
	if f.OpIndex != nil && e.OpIndex != *f.OpIndex {
		return false
	}
	if f.FromLedger > 0 && e.Ledger < f.FromLedger {
		return false
	}
	if f.ToLedger > 0 && e.Ledger > f.ToLedger {
		return false
	}
	if !f.FromTime.IsZero() && e.CreatedAt.Before(f.FromTime) {
		return false
	}
	if !f.ToTime.IsZero() && e.CreatedAt.After(f.ToTime) {
		return false
	}
	return true
}

// compactJSON returns b with insignificant whitespace removed, or the empty
// string when b is not valid JSON. Both sides of every topic comparison go
// through it, so a value that arrived as a query parameter with spaces still
// compares equal to the compact form the store keeps.
func compactJSON(b json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return ""
	}
	return buf.String()
}

// topicsElements decodes an events.topics value into its array elements. A
// topics value that is not a JSON array has no elements, and every topic filter
// the API accepts addresses array positions, so a non-array matches nothing.
func topicsElements(topics json.RawMessage) []json.RawMessage {
	if len(topics) == 0 {
		return nil
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(topics, &elements); err != nil {
		return nil
	}
	return elements
}

// topicMatches reports whether a JSON topic value appears anywhere in a JSON
// topics array.
//
// The comparison is whole-element equality, which is narrower than the Postgres
// `topics @> '[value]'::jsonb` the REST path generates: jsonb containment
// treats a right-hand object as a subset requirement, so
// {"symbol":"transfer"} also matches a stored
// {"symbol":"transfer","to":"G..."} element. That asymmetry is a real gap and
// is deliberately not pinned here — asserting equality would freeze the wrong
// behaviour once the containment semantics are settled. The shared parity table
// therefore constrains topics with values that are complete stored elements,
// where the two operators agree.
func topicMatches(topics, topic json.RawMessage) bool {
	want := compactJSON(topic)
	if want == "" {
		return false
	}
	for _, element := range topicsElements(topics) {
		if compactJSON(element) == want {
			return true
		}
	}
	return false
}

// topicAtMatches reports whether the element at a specific position of a JSON
// topics array equals want. Out-of-range positions match nothing, which is how
// `topics->N = $1::jsonb` reads a missing element as SQL NULL and therefore
// never true.
func topicAtMatches(topics json.RawMessage, position int, want json.RawMessage) bool {
	needle := compactJSON(want)
	if needle == "" {
		return false
	}
	elements := topicsElements(topics)
	if position >= len(elements) {
		return false
	}
	return compactJSON(elements[position]) == needle
}

// topicsContain reports whether every value in a topic_contains needle appears
// among a JSON topics array's elements. The needle is passed through without
// array-wrapping (unlike Topic), and the documented shape for it is an array,
// so an array needle requires each of its elements to be present — the same
// subset test `topics @> $1::jsonb` performs on two arrays. A bare object is
// treated as the one-element needle it describes.
func topicsContain(topics, needle json.RawMessage) bool {
	var want []json.RawMessage
	if err := json.Unmarshal(needle, &want); err != nil {
		want = []json.RawMessage{needle}
	}
	for _, value := range want {
		if !topicMatches(topics, value) {
			return false
		}
	}
	return true
}

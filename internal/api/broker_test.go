package api

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/store"
)

func makeTestEvent(id string, contractID string, ledger int64, eventType string, topics string) store.Event {
	return store.Event{
		ID:         id,
		ContractID: contractID,
		Ledger:     ledger,
		Type:       eventType,
		Topics:     json.RawMessage(topics),
	}
}

// TestBroker_FanOut verifies that published events are broadcast to multiple
// subscribers rather than handed off to only one.
func TestBroker_FanOut(t *testing.T) {
	b := NewBroker()
	defer b.Shutdown()

	ch1, cancel1 := b.Subscribe(store.EventFilter{})
	defer cancel1()
	ch2, cancel2 := b.Subscribe(store.EventFilter{})
	defer cancel2()

	ev1 := makeTestEvent("evt-1", "CAAA", 100, "contract", `["topic1"]`)
	ev2 := makeTestEvent("evt-2", "CBBB", 101, "system", `["topic2"]`)

	b.Publish([]store.Event{ev1, ev2})

	recv1 := readEvents(t, ch1, 2, time.Second)
	recv2 := readEvents(t, ch2, 2, time.Second)

	assert.Equal(t, []store.Event{ev1, ev2}, recv1, "sub1 must receive both events")
	assert.Equal(t, []store.Event{ev1, ev2}, recv2, "sub2 must receive both events in fan-out")
}

// TestBroker_DeliveryOrdering asserts that events are delivered to subscribers
// in the exact order they were published (FIFO).
func TestBroker_DeliveryOrdering(t *testing.T) {
	b := NewBroker()
	defer b.Shutdown()

	ch, cancel := b.Subscribe(store.EventFilter{})
	defer cancel()

	const count = 50
	events := make([]store.Event, count)
	for i := 0; i < count; i++ {
		events[i] = makeTestEvent(fmt.Sprintf("evt-%d", i), "CAAA", int64(100+i), "contract", `["test"]`)
	}

	b.Publish(events)

	got := readEvents(t, ch, count, time.Second)
	require.Len(t, got, count)
	for i := 0; i < count; i++ {
		assert.Equal(t, events[i].ID, got[i].ID, "event order at index %d must match publication order", i)
	}
}

// TestBroker_SlowSubscriberDropped verifies that a subscriber that does not drain
// its buffer is dropped when its buffer overflows (subscriberBufferSize = 256),
// closing its channel and removing it, without blocking Publish or other subscribers.
func TestBroker_SlowSubscriberDropped(t *testing.T) {
	b := NewBroker()
	defer b.Shutdown()

	slowCh, slowCancel := b.Subscribe(store.EventFilter{})
	defer slowCancel()

	fastCh, fastCancel := b.Subscribe(store.EventFilter{})
	defer fastCancel()

	// Drain fast subscriber concurrently so its buffer never overflows.
	var fastCount atomic.Int64
	fastDone := make(chan struct{})
	go func() {
		defer close(fastDone)
		for range fastCh {
			fastCount.Add(1)
		}
	}()

	// Publish 300 events in batches with a brief sleep so fast consumer drains
	// while slow consumer (which never reads) accumulates and overflows subscriberBufferSize (256).
	const totalEvents = 300
	publishDone := make(chan struct{})
	go func() {
		for i := 0; i < totalEvents; i += 20 {
			end := i + 20
			if end > totalEvents {
				end = totalEvents
			}
			batch := make([]store.Event, end-i)
			for j := i; j < end; j++ {
				batch[j-i] = makeTestEvent(fmt.Sprintf("evt-%03d", j), "CAAA", int64(j+1), "contract", `["data"]`)
			}
			b.Publish(batch)
			time.Sleep(500 * time.Microsecond)
		}
		close(publishDone)
	}()

	select {
	case <-publishDone:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on slow consumer")
	}

	// Drain slow consumer channel; since it was dropped, the channel is closed.
	slowCount := 0
	drainSlowDone := make(chan struct{})
	go func() {
		for range slowCh {
			slowCount++
		}
		close(drainSlowDone)
	}()

	select {
	case <-drainSlowDone:
	case <-time.After(time.Second):
		t.Fatal("slow consumer channel was never closed after overflow")
	}

	assert.Equal(t, subscriberBufferSize, slowCount, "slow consumer must have buffered exactly subscriberBufferSize events before being dropped")

	// Fast consumer should have received all published events.
	fastCancel()
	<-fastDone
	assert.Equal(t, int64(totalEvents), fastCount.Load(), "fast consumer draining buffer should receive all events")

	// Verify slow subscriber is removed from b.subscribers.
	b.mu.RLock()
	assert.Empty(t, b.subscribers, "subscribers should only contain active members")
	b.mu.RUnlock()
}

// TestBroker_UnsubscribeReleasesResources asserts that calling cancel removes
// the subscriber from the broker and closes its channel.
func TestBroker_UnsubscribeReleasesResources(t *testing.T) {
	b := NewBroker()
	defer b.Shutdown()

	ch, cancel := b.Subscribe(store.EventFilter{})

	b.mu.RLock()
	require.Len(t, b.subscribers, 1, "subscriber should be registered")
	b.mu.RUnlock()

	cancel()

	// Channel should be closed promptly.
	select {
	case _, ok := <-ch:
		assert.False(t, ok, "channel must be closed upon cancel")
	case <-time.After(time.Second):
		t.Fatal("channel not closed on cancel")
	}

	// Verify subscriber was removed and resources released.
	b.mu.RLock()
	assert.Empty(t, b.subscribers, "b.subscribers map must be empty after cancel")
	b.mu.RUnlock()

	// Calling cancel a second time is a safe no-op.
	assert.NotPanics(t, func() { cancel() })
}

// TestBroker_ContextCancellation asserts that a subscriber managed by context
// lifecycle removes the subscription and releases resources when context cancels.
func TestBroker_ContextCancellation(t *testing.T) {
	b := NewBroker()
	defer b.Shutdown()

	ctx, cancelCtx := context.WithCancel(context.Background())
	ch, cancelSub := b.Subscribe(store.EventFilter{})

	// Simulate subscription handler goroutine.
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		defer cancelSub()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
			}
		}
	}()

	b.mu.RLock()
	require.Len(t, b.subscribers, 1)
	b.mu.RUnlock()

	cancelCtx()

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("handler did not stop on context cancellation")
	}

	b.mu.RLock()
	assert.Empty(t, b.subscribers, "subscriber must be removed on context cancellation")
	b.mu.RUnlock()
}

// TestBroker_ShutdownClosesSubscribersAndReturnsBounded verifies that Shutdown closes
// all subscriber channels and returns within a bounded time, even if a subscriber
// is not draining.
func TestBroker_ShutdownClosesSubscribersAndReturnsBounded(t *testing.T) {
	b := NewBroker()

	ch1, _ := b.Subscribe(store.EventFilter{})
	ch2, _ := b.Subscribe(store.EventFilter{})

	shutdownDone := make(chan struct{})
	go func() {
		b.Shutdown()
		close(shutdownDone)
	}()

	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("Shutdown hung or failed to complete within bounded time")
	}

	// Verify all channels closed.
	for i, ch := range []<-chan store.Event{ch1, ch2} {
		select {
		case _, ok := <-ch:
			assert.False(t, ok, "subscriber %d channel must be closed on shutdown", i+1)
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d channel was not closed on shutdown", i+1)
		}
	}

	b.mu.RLock()
	assert.Empty(t, b.subscribers, "all subscribers must be deleted on shutdown")
	b.mu.RUnlock()

	// Calling Shutdown again is a safe no-op.
	assert.NotPanics(t, func() { b.Shutdown() })
}

// TestBroker_PublishAfterShutdown asserts that publishing after Shutdown
// does not panic (no send on closed channel, no send on nil map).
func TestBroker_PublishAfterShutdown(t *testing.T) {
	b := NewBroker()
	ch, _ := b.Subscribe(store.EventFilter{})
	b.Shutdown()

	select {
	case _, ok := <-ch:
		require.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("channel not closed")
	}

	assert.NotPanics(t, func() {
		b.Publish([]store.Event{makeTestEvent("evt-after", "CAAA", 200, "contract", `["t"]`)})
	})
}

// TestBroker_SubscribeAfterShutdown asserts that subscribing after Shutdown
// returns a closed channel and a safe no-op cancel function.
func TestBroker_SubscribeAfterShutdown(t *testing.T) {
	b := NewBroker()
	b.Shutdown()

	ch, cancel := b.Subscribe(store.EventFilter{})
	select {
	case _, ok := <-ch:
		assert.False(t, ok, "channel returned after shutdown must be closed")
	case <-time.After(time.Second):
		t.Fatal("channel not closed")
	}

	assert.NotPanics(t, func() { cancel() })
}

// TestBroker_ConcurrentLifecycleRace exercises concurrent Subscribes, Publishes,
// cancels, and Shutdown from multiple goroutines under the Go race detector.
func TestBroker_ConcurrentLifecycleRace(t *testing.T) {
	b := NewBroker()

	const workers = 8
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var publishedCount atomic.Int64

	// Publisher goroutines
	for i := 0; i < workers/2; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			seq := 0
			for {
				select {
				case <-ctx.Done():
					return
				default:
					ev := makeTestEvent(
						fmt.Sprintf("evt-w%d-%d", workerID, seq),
						"CAAA",
						int64(seq+1),
						"contract",
						`["topic"]`,
					)
					b.Publish([]store.Event{ev})
					publishedCount.Add(1)
					seq++
					time.Sleep(50 * time.Microsecond)
				}
			}
		}(i)
	}

	// Subscriber goroutines that repeatedly subscribe, read a few items, and cancel
	for i := 0; i < workers/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					ch, cancelSub := b.Subscribe(store.EventFilter{ContractID: "CAAA"})
					// Read up to 5 events or timeout
					for r := 0; r < 5; r++ {
						select {
						case _, ok := <-ch:
							if !ok {
								break
							}
						case <-time.After(100 * time.Microsecond):
						}
					}
					cancelSub()
					time.Sleep(50 * time.Microsecond)
				}
			}
		}()
	}

	// Mid-flight shutdown near the end of test duration
	time.Sleep(500 * time.Millisecond)
	b.Shutdown()

	wg.Wait()
	assert.Greater(t, publishedCount.Load(), int64(0), "must have executed publishes concurrently")
}

// TestBroker_NilSafety verifies that calling any method on a nil *Broker is safe.
func TestBroker_NilSafety(t *testing.T) {
	var b *Broker

	assert.NotPanics(t, func() {
		b.Publish([]store.Event{makeTestEvent("evt", "C", 1, "t", "[]")})
		b.Shutdown()
	})

	ch, cancel := b.Subscribe(store.EventFilter{})
	select {
	case _, ok := <-ch:
		assert.False(t, ok, "nil broker subscribe channel should be closed")
	default:
		t.Fatal("nil broker subscribe channel should be immediately closed")
	}

	assert.NotPanics(t, func() { cancel() })
}

// TestBroker_FilterMatching asserts that eventMatches correctly filters events
// based on ContractID, Types, Ledger bounds, and Topic JSON match.
func TestBroker_FilterMatching(t *testing.T) {
	b := NewBroker()
	defer b.Shutdown()

	tests := []struct {
		name      string
		filter    store.EventFilter
		event     store.Event
		wantMatch bool
	}{
		{
			name:      "unconstrained matches everything",
			filter:    store.EventFilter{},
			event:     makeTestEvent("e1", "CAAA", 100, "contract", `["hello"]`),
			wantMatch: true,
		},
		{
			name:      "contractID match",
			filter:    store.EventFilter{ContractID: "CAAA"},
			event:     makeTestEvent("e2", "CAAA", 100, "contract", `["hello"]`),
			wantMatch: true,
		},
		{
			name:      "contractID mismatch",
			filter:    store.EventFilter{ContractID: "CAAA"},
			event:     makeTestEvent("e3", "CBBB", 100, "contract", `["hello"]`),
			wantMatch: false,
		},
		{
			name:      "type match",
			filter:    store.EventFilter{Types: []string{"contract", "system"}},
			event:     makeTestEvent("e4", "CAAA", 100, "contract", `["hello"]`),
			wantMatch: true,
		},
		{
			name:      "type mismatch",
			filter:    store.EventFilter{Types: []string{"diagnostic"}},
			event:     makeTestEvent("e5", "CAAA", 100, "contract", `["hello"]`),
			wantMatch: false,
		},
		{
			name:      "fromLedger match",
			filter:    store.EventFilter{FromLedger: 100},
			event:     makeTestEvent("e6", "CAAA", 100, "contract", `["hello"]`),
			wantMatch: true,
		},
		{
			name:      "fromLedger before bounds",
			filter:    store.EventFilter{FromLedger: 100},
			event:     makeTestEvent("e7", "CAAA", 99, "contract", `["hello"]`),
			wantMatch: false,
		},
		{
			name:      "toLedger match",
			filter:    store.EventFilter{ToLedger: 200},
			event:     makeTestEvent("e8", "CAAA", 200, "contract", `["hello"]`),
			wantMatch: true,
		},
		{
			name:      "toLedger after bounds",
			filter:    store.EventFilter{ToLedger: 200},
			event:     makeTestEvent("e9", "CAAA", 201, "contract", `["hello"]`),
			wantMatch: false,
		},
		{
			name:      "topic JSON match",
			filter:    store.EventFilter{Topic: json.RawMessage(`"transfer"`)},
			event:     makeTestEvent("e10", "CAAA", 100, "contract", `["transfer", "to_addr"]`),
			wantMatch: true,
		},
		{
			name:      "topic JSON mismatch",
			filter:    store.EventFilter{Topic: json.RawMessage(`"burn"`)},
			event:     makeTestEvent("e11", "CAAA", 100, "contract", `["transfer", "to_addr"]`),
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantMatch, eventMatches(tt.filter, tt.event))
		})
	}
}

// TestBroker_NoGoroutineLeak verifies that a full subscribe, publish, and shutdown
// lifecycle creates and closes channels cleanly without leaking goroutines.
func TestBroker_NoGoroutineLeak(t *testing.T) {
	// Let existing goroutines settle
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	initialGoroutines := runtime.NumGoroutine()

	b := NewBroker()
	ch1, cancel1 := b.Subscribe(store.EventFilter{})
	_, _ = b.Subscribe(store.EventFilter{})

	b.Publish([]store.Event{makeTestEvent("e1", "CAAA", 10, "contract", `["t"]`)})

	// Drain ch1, leave ch2 undrained
	select {
	case <-ch1:
	case <-time.After(time.Second):
		t.Fatal("timed out draining ch1")
	}

	cancel1()
	b.Shutdown()

	// Wait for cleanup
	runtime.GC()
	time.Sleep(30 * time.Millisecond)

	finalGoroutines := runtime.NumGoroutine()
	// Allow for small runtime fluctuations, but assert no unbounded goroutine growth.
	assert.LessOrEqual(t, finalGoroutines, initialGoroutines+2,
		"broker lifecycle must not leak goroutines: before=%d, after=%d", initialGoroutines, finalGoroutines)
}

func readEvents(t *testing.T, ch <-chan store.Event, count int, timeout time.Duration) []store.Event {
	t.Helper()
	var events []store.Event
	deadline := time.After(timeout)
	for len(events) < count {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed unexpectedly after %d events (wanted %d)", len(events), count)
			}
			events = append(events, ev)
		case <-deadline:
			t.Fatalf("timed out after reading %d events (wanted %d)", len(events), count)
		}
	}
	return events
}

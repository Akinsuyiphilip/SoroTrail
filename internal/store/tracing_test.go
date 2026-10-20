package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/sorotrail/sorotrail/internal/tracing/tracingtest"
)

// onlySpan returns the single span exported for the call under test. Every
// wrapper method is expected to emit exactly one, so anything else is a bug in
// the wrapper rather than a tolerable difference.
func onlySpan(t *testing.T, exp *tracetest.InMemoryExporter) tracetest.SpanStub {
	t.Helper()
	spans := exp.GetSpans()
	require.Len(t, spans, 1, "wrapper must emit exactly one span per call")
	return spans[0]
}

// spanAttrs flattens the recorded attributes so a test can assert the whole
// set, not just the keys it remembered to check. A stray attribute carrying a
// filter or credential therefore fails the exact-map comparison.
func spanAttrs(span tracetest.SpanStub) map[string]any {
	out := make(map[string]any, len(span.Attributes))
	for _, kv := range span.Attributes {
		out[string(kv.Key)] = kv.Value.AsInterface()
	}
	return out
}

// tracingFakeStore embeds Store and overrides only the eight methods the
// wrapper decorates. The embedded nil interface means a call to an unwrapped
// method panics, which is the point: the test records every delegation it
// expects and nothing else can quietly happen.
type tracingFakeStore struct {
	Store

	calls []string

	upsertInserted int64
	upsertErr      error
	replaceErr     error
	event          Event
	eventErr       error
	exists         bool
	existsErr      error
	events         []Event
	cursor         string
	queryErr       error
	ingestion      IngestionState
	ingestionErr   error
	saveErr        error
	networkState   IngestionState
	networkErr     error

	spanInCtx  bool
	gotEvents  []Event
	gotFrom    int64
	gotTo      int64
	gotID      string
	gotScope   Scope
	gotFilter  EventFilter
	gotState   IngestionState
	gotNetwork string
}

func (f *tracingFakeStore) note(ctx context.Context, name string) {
	f.calls = append(f.calls, name)
	f.spanInCtx = trace.SpanFromContext(ctx).SpanContext().IsValid()
}

func (f *tracingFakeStore) UpsertEvents(ctx context.Context, events []Event) (int64, error) {
	f.note(ctx, "UpsertEvents")
	f.gotEvents = events
	return f.upsertInserted, f.upsertErr
}

func (f *tracingFakeStore) ReplaceEventsInRange(ctx context.Context, events []Event, fromLedger, toLedger int64) error {
	f.note(ctx, "ReplaceEventsInRange")
	f.gotEvents = events
	f.gotFrom = fromLedger
	f.gotTo = toLedger
	return f.replaceErr
}

func (f *tracingFakeStore) GetEvent(ctx context.Context, id string, sc Scope) (Event, error) {
	f.note(ctx, "GetEvent")
	f.gotID = id
	f.gotScope = sc
	return f.event, f.eventErr
}

func (f *tracingFakeStore) EventExists(ctx context.Context, id string, sc Scope) (bool, error) {
	f.note(ctx, "EventExists")
	f.gotID = id
	f.gotScope = sc
	return f.exists, f.existsErr
}

func (f *tracingFakeStore) QueryEvents(ctx context.Context, filter EventFilter) ([]Event, string, error) {
	f.note(ctx, "QueryEvents")
	f.gotFilter = filter
	return f.events, f.cursor, f.queryErr
}

func (f *tracingFakeStore) GetIngestionState(ctx context.Context) (IngestionState, error) {
	f.note(ctx, "GetIngestionState")
	return f.ingestion, f.ingestionErr
}

func (f *tracingFakeStore) GetIngestionStateForNetwork(ctx context.Context, network string) (IngestionState, error) {
	f.note(ctx, "GetIngestionStateForNetwork")
	f.gotNetwork = network
	return f.networkState, f.networkErr
}

func (f *tracingFakeStore) SaveIngestionState(ctx context.Context, state IngestionState) error {
	f.note(ctx, "SaveIngestionState")
	f.gotState = state
	return f.saveErr
}

// tracingDefaultOnlyStore is a store without the optional
// GetIngestionStateForNetwork method, so the wrapper has to fall back to the
// default network's state.
type tracingDefaultOnlyStore struct {
	Store

	calls     int
	spanInCtx bool
	state     IngestionState
	err       error
}

func (f *tracingDefaultOnlyStore) GetIngestionState(ctx context.Context) (IngestionState, error) {
	f.calls++
	f.spanInCtx = trace.SpanFromContext(ctx).SpanContext().IsValid()
	return f.state, f.err
}

func TestTracingStoreDelegates(t *testing.T) {
	ctx := context.Background()

	t.Run("UpsertEvents", func(t *testing.T) {
		exp := tracingtest.Setup(t)
		fake := &tracingFakeStore{upsertInserted: 3}
		ts := NewTracingStore(fake, otel.Tracer("test")).(*TracingStore)

		events := []Event{{ID: "event-1"}, {ID: "event-2"}}
		inserted, err := ts.UpsertEvents(ctx, events)

		require.NoError(t, err)
		assert.Equal(t, int64(3), inserted)
		assert.Equal(t, []string{"UpsertEvents"}, fake.calls)
		assert.Equal(t, events, fake.gotEvents)
		assert.True(t, fake.spanInCtx, "inner store must receive the span context")

		span := onlySpan(t, exp)
		assert.Equal(t, "store.UpsertEvents", span.Name)
		assert.Equal(t, map[string]any{"store.event_count": int64(2)}, spanAttrs(span))
		assert.Equal(t, codes.Unset, span.Status.Code)
	})

	t.Run("ReplaceEventsInRange", func(t *testing.T) {
		exp := tracingtest.Setup(t)
		fake := &tracingFakeStore{}
		ts := NewTracingStore(fake, otel.Tracer("test")).(*TracingStore)

		events := []Event{{ID: "event-1"}}
		err := ts.ReplaceEventsInRange(ctx, events, 100, 200)

		require.NoError(t, err)
		assert.Equal(t, []string{"ReplaceEventsInRange"}, fake.calls)
		assert.Equal(t, events, fake.gotEvents)
		assert.Equal(t, int64(100), fake.gotFrom)
		assert.Equal(t, int64(200), fake.gotTo)

		span := onlySpan(t, exp)
		assert.Equal(t, "store.ReplaceEventsInRange", span.Name)
		assert.Equal(t, map[string]any{
			"store.from_ledger": int64(100),
			"store.to_ledger":   int64(200),
			"store.event_count": int64(1),
		}, spanAttrs(span))
		assert.Equal(t, codes.Unset, span.Status.Code)
	})

	t.Run("GetEvent", func(t *testing.T) {
		exp := tracingtest.Setup(t)
		fake := &tracingFakeStore{event: Event{ID: "event-42", Ledger: 7}}
		ts := NewTracingStore(fake, otel.Tracer("test")).(*TracingStore)

		scope := Scope{}
		event, err := ts.GetEvent(ctx, "event-42", scope)

		require.NoError(t, err)
		assert.Equal(t, fake.event, event)
		assert.Equal(t, []string{"GetEvent"}, fake.calls)
		assert.Equal(t, "event-42", fake.gotID)
		assert.Equal(t, scope, fake.gotScope)

		span := onlySpan(t, exp)
		assert.Equal(t, "store.GetEvent", span.Name)
		assert.Equal(t, map[string]any{"store.event_id": "event-42"}, spanAttrs(span))
		assert.Equal(t, codes.Unset, span.Status.Code)
	})

	t.Run("EventExists", func(t *testing.T) {
		exp := tracingtest.Setup(t)
		fake := &tracingFakeStore{exists: true}
		ts := NewTracingStore(fake, otel.Tracer("test")).(*TracingStore)

		exists, err := ts.EventExists(ctx, "event-9", Scope{})

		require.NoError(t, err)
		assert.True(t, exists)
		assert.Equal(t, []string{"EventExists"}, fake.calls)
		assert.Equal(t, "event-9", fake.gotID)

		span := onlySpan(t, exp)
		assert.Equal(t, "store.EventExists", span.Name)
		assert.Equal(t, map[string]any{"store.event_id": "event-9"}, spanAttrs(span))
		assert.Equal(t, codes.Unset, span.Status.Code)
	})

	t.Run("QueryEvents", func(t *testing.T) {
		exp := tracingtest.Setup(t)
		fake := &tracingFakeStore{
			events: []Event{{ID: "event-1"}, {ID: "event-2"}},
			cursor: "next-page",
		}
		ts := NewTracingStore(fake, otel.Tracer("test")).(*TracingStore)

		filter := EventFilter{ContractID: "CABCDEF", Types: []string{"transfer", "mint"}}
		events, cursor, err := ts.QueryEvents(ctx, filter)

		require.NoError(t, err)
		assert.Equal(t, fake.events, events)
		assert.Equal(t, "next-page", cursor)
		assert.Equal(t, []string{"QueryEvents"}, fake.calls)
		assert.Equal(t, filter, fake.gotFilter)

		span := onlySpan(t, exp)
		assert.Equal(t, "store.QueryEvents", span.Name)
		assert.Equal(t, map[string]any{
			"store.contract_id": "CABCDEF",
			"store.types":       "[transfer mint]",
			"store.event_count": int64(2),
		}, spanAttrs(span))
		assert.Equal(t, codes.Unset, span.Status.Code)
	})

	t.Run("GetIngestionState", func(t *testing.T) {
		exp := tracingtest.Setup(t)
		fake := &tracingFakeStore{ingestion: IngestionState{Network: "default", LastIngestedLedger: 55}}
		ts := NewTracingStore(fake, otel.Tracer("test")).(*TracingStore)

		state, err := ts.GetIngestionState(ctx)

		require.NoError(t, err)
		assert.Equal(t, fake.ingestion, state)
		assert.Equal(t, []string{"GetIngestionState"}, fake.calls)

		span := onlySpan(t, exp)
		assert.Equal(t, "store.GetIngestionState", span.Name)
		assert.Empty(t, spanAttrs(span), "state lookup carries no arguments to record")
		assert.Equal(t, codes.Unset, span.Status.Code)
	})

	t.Run("GetIngestionStateForNetwork", func(t *testing.T) {
		exp := tracingtest.Setup(t)
		fake := &tracingFakeStore{networkState: IngestionState{Network: "testnet", LastIngestedLedger: 77}}
		ts := NewTracingStore(fake, otel.Tracer("test")).(*TracingStore)

		state, err := ts.GetIngestionStateForNetwork(ctx, "testnet")

		require.NoError(t, err)
		assert.Equal(t, fake.networkState, state)
		assert.Equal(t, []string{"GetIngestionStateForNetwork"}, fake.calls)
		assert.Equal(t, "testnet", fake.gotNetwork)

		span := onlySpan(t, exp)
		assert.Equal(t, "store.GetIngestionStateForNetwork", span.Name)
		assert.Equal(t, map[string]any{"store.network": "testnet"}, spanAttrs(span))
		assert.Equal(t, codes.Unset, span.Status.Code)
	})

	t.Run("SaveIngestionState", func(t *testing.T) {
		exp := tracingtest.Setup(t)
		fake := &tracingFakeStore{}
		ts := NewTracingStore(fake, otel.Tracer("test")).(*TracingStore)

		state := IngestionState{Network: "testnet", LastIngestedLedger: 99}
		err := ts.SaveIngestionState(ctx, state)

		require.NoError(t, err)
		assert.Equal(t, []string{"SaveIngestionState"}, fake.calls)
		assert.Equal(t, state, fake.gotState)

		span := onlySpan(t, exp)
		assert.Equal(t, "store.SaveIngestionState", span.Name)
		assert.Equal(t, map[string]any{"store.last_ingested_ledger": int64(99)}, spanAttrs(span))
		assert.Equal(t, codes.Unset, span.Status.Code)
	})
}

func TestTracingStoreRecordsInnerErrors(t *testing.T) {
	wantErr := errors.New("store unavailable")
	ctx := context.Background()

	cases := []struct {
		name string
		call func(*TracingStore) error
	}{
		{"UpsertEvents", func(s *TracingStore) error {
			_, err := s.UpsertEvents(ctx, nil)
			return err
		}},
		{"ReplaceEventsInRange", func(s *TracingStore) error {
			return s.ReplaceEventsInRange(ctx, nil, 1, 2)
		}},
		{"GetEvent", func(s *TracingStore) error {
			_, err := s.GetEvent(ctx, "event-1", Scope{})
			return err
		}},
		{"EventExists", func(s *TracingStore) error {
			_, err := s.EventExists(ctx, "event-1", Scope{})
			return err
		}},
		{"QueryEvents", func(s *TracingStore) error {
			_, _, err := s.QueryEvents(ctx, EventFilter{})
			return err
		}},
		{"GetIngestionState", func(s *TracingStore) error {
			_, err := s.GetIngestionState(ctx)
			return err
		}},
		{"GetIngestionStateForNetwork", func(s *TracingStore) error {
			_, err := s.GetIngestionStateForNetwork(ctx, "testnet")
			return err
		}},
		{"SaveIngestionState", func(s *TracingStore) error {
			return s.SaveIngestionState(ctx, IngestionState{})
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exp := tracingtest.Setup(t)
			fake := &tracingFakeStore{
				upsertErr:    wantErr,
				replaceErr:   wantErr,
				eventErr:     wantErr,
				existsErr:    wantErr,
				queryErr:     wantErr,
				ingestionErr: wantErr,
				saveErr:      wantErr,
				networkErr:   wantErr,
			}
			ts := NewTracingStore(fake, otel.Tracer("test")).(*TracingStore)

			err := tc.call(ts)

			require.Equal(t, wantErr, err, "the inner store's error must be returned unchanged")
			span := onlySpan(t, exp)
			assert.Equal(t, "store."+tc.name, span.Name)
			assert.Equal(t, codes.Error, span.Status.Code)
			assert.Equal(t, wantErr.Error(), span.Status.Description)
			require.Len(t, span.Events, 1, "the error must be recorded as a span event")
			assert.Equal(t, "exception", span.Events[0].Name)
		})
	}
}

func TestTracingStoreNetworkFallback(t *testing.T) {
	ctx := context.Background()

	t.Run("default network uses GetIngestionState", func(t *testing.T) {
		exp := tracingtest.Setup(t)
		fake := &tracingDefaultOnlyStore{
			state: IngestionState{Network: "default", LastIngestedLedger: 42},
		}
		ts := NewTracingStore(fake, otel.Tracer("test")).(*TracingStore)

		state, err := ts.GetIngestionStateForNetwork(ctx, "default")

		require.NoError(t, err)
		assert.Equal(t, fake.state, state)
		assert.Equal(t, 1, fake.calls, "the fallback must call GetIngestionState exactly once")
		assert.True(t, fake.spanInCtx, "the fallback call must receive the span context")

		span := onlySpan(t, exp)
		assert.Equal(t, "store.GetIngestionStateForNetwork", span.Name)
		assert.Equal(t, map[string]any{"store.network": "default"}, spanAttrs(span))
		assert.Equal(t, codes.Unset, span.Status.Code)
	})

	t.Run("unknown network is not found", func(t *testing.T) {
		exp := tracingtest.Setup(t)
		fake := &tracingDefaultOnlyStore{}
		ts := NewTracingStore(fake, otel.Tracer("test")).(*TracingStore)

		_, err := ts.GetIngestionStateForNetwork(ctx, "testnet")

		require.ErrorIs(t, err, ErrNotFound)
		assert.Zero(t, fake.calls, "a default-only store cannot answer for another network")

		span := onlySpan(t, exp)
		assert.Equal(t, codes.Error, span.Status.Code)
		assert.Equal(t, ErrNotFound.Error(), span.Status.Description)
		require.Len(t, span.Events, 1, "the lookup failure must be recorded on the span")
		assert.Equal(t, "exception", span.Events[0].Name)
	})

	t.Run("fallback error is recorded", func(t *testing.T) {
		exp := tracingtest.Setup(t)
		wantErr := errors.New("connection reset")
		fake := &tracingDefaultOnlyStore{err: wantErr}
		ts := NewTracingStore(fake, otel.Tracer("test")).(*TracingStore)

		_, err := ts.GetIngestionStateForNetwork(ctx, "")

		require.Equal(t, wantErr, err)
		assert.Equal(t, 1, fake.calls)

		span := onlySpan(t, exp)
		assert.Equal(t, codes.Error, span.Status.Code)
		assert.Equal(t, wantErr.Error(), span.Status.Description)
		require.Len(t, span.Events, 1)
		assert.Equal(t, "exception", span.Events[0].Name)
	})
}

// TestTracingStoreDoesNotRecordRawFilters pins the attribute policy: spans
// may carry bounded, caller-validated values (contract ID, event type names,
// counts, ledgers, network), but never free-text filters, cursors or query
// fragments that can hold arbitrary user input.
func TestTracingStoreDoesNotRecordRawFilters(t *testing.T) {
	exp := tracingtest.Setup(t)
	fake := &tracingFakeStore{events: []Event{{ID: "event-1"}}}
	ts := NewTracingStore(fake, otel.Tracer("test")).(*TracingStore)

	const secret = "user-supplied-sentinel-9d8f"
	filter := EventFilter{
		ContractID:       "CABCDEF",
		Types:            []string{"transfer"},
		TxHash:           secret,
		Cursor:           secret,
		Topic:            []byte(`{"secret":"` + secret + `"}`),
		ContractIDPrefix: secret,
		Order:            secret,
	}

	_, _, err := ts.QueryEvents(context.Background(), filter)
	require.NoError(t, err)

	span := onlySpan(t, exp)
	for _, kv := range span.Attributes {
		assert.NotContains(t, fmt.Sprint(kv.Value.AsInterface()), secret,
			"attribute %q must not carry the raw filter value", kv.Key)
		assert.NotEqual(t, "store.tx_hash", string(kv.Key))
		assert.NotEqual(t, "store.cursor", string(kv.Key))
		assert.NotEqual(t, "store.topic", string(kv.Key))
	}
}

func TestNewTracingStoreWithoutTracer(t *testing.T) {
	fake := &tracingFakeStore{upsertInserted: 1}
	ts := NewTracingStore(fake, nil)
	require.NotNil(t, ts)

	inserted, err := ts.UpsertEvents(context.Background(), []Event{{ID: "event-1"}})

	require.NoError(t, err)
	assert.Equal(t, int64(1), inserted)
	assert.Equal(t, []string{"UpsertEvents"}, fake.calls)
}

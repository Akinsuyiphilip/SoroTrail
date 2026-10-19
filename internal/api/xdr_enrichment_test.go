package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/store"
)

// TestEnrichEventsWithXDR_Present asserts that raw XDR (topics and value) is attached
// unmodified to enriched events when present.
func TestEnrichEventsWithXDR_Present(t *testing.T) {
	topicXDR := []string{"AAAAAQ==", "AAAAAg=="}
	valueXDR := "AAAAAw=="
	ev := store.EnrichedEvent{
		Event: store.Event{
			ID:          "0000000001-0000000001",
			ContractID:  "CAAA",
			Ledger:      100,
			RawTopicXDR: topicXDR,
			RawValueXDR: valueXDR,
		},
		Decoded: true,
		DecodedEvent: &store.DecodedEventResponse{
			Event:  "transfer",
			Fields: map[string]any{"amount": "100"},
		},
	}

	result := enrichEventsWithXDR([]store.EnrichedEvent{ev})
	require.Len(t, result, 1)

	got := result[0]
	assert.Equal(t, topicXDR, got.TopicsXDR, "raw topics XDR must be attached unmodified")
	require.NotNil(t, got.ValueXDR, "value XDR must be present")
	assert.Equal(t, valueXDR, *got.ValueXDR, "raw value XDR must be attached unmodified")
	assert.True(t, got.Decoded)
	assert.Equal(t, "transfer", got.DecodedEvent.Event)
}

// TestEnrichEventsWithXDR_MissingAndMalformed pins the behavior when XDR is missing
// or empty: an empty RawValueXDR produces a nil ValueXDR pointer (omitted in JSON),
// and an absent RawTopicXDR is preserved as nil (null in JSON).
func TestEnrichEventsWithXDR_MissingAndMalformed(t *testing.T) {
	evMissingValue := store.EnrichedEvent{
		Event: store.Event{
			ID:          "0000000001-0000000001",
			RawTopicXDR: []string{"AAAAAQ=="},
			RawValueXDR: "", // missing/empty value XDR
		},
		Decoded: false,
	}

	evMissingAll := store.EnrichedEvent{
		Event: store.Event{
			ID:          "0000000001-0000000002",
			RawTopicXDR: nil,
			RawValueXDR: "",
		},
		Decoded: false,
	}

	result := enrichEventsWithXDR([]store.EnrichedEvent{evMissingValue, evMissingAll})
	require.Len(t, result, 2)

	assert.Nil(t, result[0].ValueXDR, "empty RawValueXDR must result in nil ValueXDR pointer")
	assert.Equal(t, []string{"AAAAAQ=="}, result[0].TopicsXDR)
	assert.False(t, result[0].Decoded)

	assert.Nil(t, result[1].ValueXDR)
	assert.Nil(t, result[1].TopicsXDR)
	assert.False(t, result[1].Decoded)

	// Verify JSON marshaling behavior
	data0, err := json.Marshal(result[0])
	require.NoError(t, err)
	assert.NotContains(t, string(data0), `"value_xdr"`, "nil ValueXDR must be omitted from JSON")
	assert.Contains(t, string(data0), `"topics_xdr":["AAAAAQ=="]`)
}

// TestEnrichEventsWithXDR_PartialFailure asserts that when a page contains a mix of
// successfully decoded events and failing/malformed events, the failure is visible
// on the individual event (Decoded == false) rather than silently failing the whole page
// or masking the failed event as one that merely lacked XDR.
func TestEnrichEventsWithXDR_PartialFailure(t *testing.T) {
	goodEvent1 := store.EnrichedEvent{
		Event: store.Event{
			ID:          "0000000001-0000000001",
			RawTopicXDR: []string{"top1"},
			RawValueXDR: "val1",
		},
		Decoded: true,
		DecodedEvent: &store.DecodedEventResponse{
			Event:  "mint",
			Fields: map[string]any{"to": "GAAA"},
		},
	}

	badEvent := store.EnrichedEvent{
		Event: store.Event{
			ID:          "0000000001-0000000002",
			RawTopicXDR: []string{"corrupted-xdr"},
			RawValueXDR: "bad-payload",
		},
		Decoded:     false,
		DecodeError: "failed to decode topics: invalid base64 xdr",
	}

	goodEvent2 := store.EnrichedEvent{
		Event: store.Event{
			ID:          "0000000001-0000000003",
			RawTopicXDR: []string{"top2"},
			RawValueXDR: "val2",
		},
		Decoded: true,
		DecodedEvent: &store.DecodedEventResponse{
			Event:  "burn",
			Fields: map[string]any{"amount": "50"},
		},
	}

	page := []store.EnrichedEvent{goodEvent1, badEvent, goodEvent2}
	result := enrichEventsWithXDR(page)
	require.Len(t, result, 3)

	// Event 1 succeeded
	assert.True(t, result[0].Decoded)
	assert.Equal(t, "mint", result[0].DecodedEvent.Event)
	assert.Equal(t, []string{"top1"}, result[0].TopicsXDR)
	require.NotNil(t, result[0].ValueXDR)
	assert.Equal(t, "val1", *result[0].ValueXDR)

	// Event 2 failed decode but preserves raw XDR and clearly reports Decoded: false
	assert.False(t, result[1].Decoded, "decode failure must be visible on the item")
	assert.Nil(t, result[1].DecodedEvent, "failed event must not have a DecodedEvent tree")
	assert.Equal(t, []string{"corrupted-xdr"}, result[1].TopicsXDR)
	require.NotNil(t, result[1].ValueXDR)
	assert.Equal(t, "bad-payload", *result[1].ValueXDR)

	// Event 3 succeeded
	assert.True(t, result[2].Decoded)
	assert.Equal(t, "burn", result[2].DecodedEvent.Event)
	assert.Equal(t, []string{"top2"}, result[2].TopicsXDR)
	require.NotNil(t, result[2].ValueXDR)
	assert.Equal(t, "val2", *result[2].ValueXDR)
}

// TestEnrichEventsWithXDR_HTTPOptIn asserts that XDR enrichment is strictly opt-in:
// a query with ?decoded=true does not include XDR fields unless ?include_xdr=true is also passed.
func TestEnrichEventsWithXDR_HTTPOptIn(t *testing.T) {
	st := &stubStore{
		events: []store.Event{
			{
				ID:          "0000000001-0000000001",
				ContractID:  "CAAA",
				Ledger:      100,
				RawTopicXDR: []string{"topic-raw-xdr"},
				RawValueXDR: "value-raw-xdr",
			},
		},
	}

	enricher := &mockEnricher{
		enriched: []store.EnrichedEvent{
			{
				Event:   st.events[0],
				Decoded: true,
				DecodedEvent: &store.DecodedEventResponse{
					Event:  "transfer",
					Fields: map[string]any{"amount": "1000"},
				},
			},
		},
	}
	server := newTestServer(st, nil)
	server.enricher = enricher

	// 1. Without include_xdr, response has decoded_event but no topics_xdr or value_xdr
	resp1, body1 := doGet(t, server, "/events?decoded=true")
	require.Equal(t, http.StatusOK, resp1.StatusCode)
	assert.Contains(t, string(body1), "decoded_event")
	assert.NotContains(t, string(body1), "topics_xdr")
	assert.NotContains(t, string(body1), "value_xdr")

	// 2. With include_xdr=true, response has both decoded_event and raw topics_xdr / value_xdr
	resp2, body2 := doGet(t, server, "/events?decoded=true&include_xdr=true")
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	assert.Contains(t, string(body2), "decoded_event")
	assert.Contains(t, string(body2), "topics_xdr")
	assert.Contains(t, string(body2), "value_xdr")

	// Verify unmarshaling into enrichedEventsWithXDRResponse
	var out struct {
		Events []struct {
			TopicsXDR    []string                    `json:"topics_xdr"`
			ValueXDR     *string                     `json:"value_xdr"`
			DecodedEvent *store.DecodedEventResponse `json:"decoded_event"`
			Decoded      bool                        `json:"decoded"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(body2, &out))
	require.Len(t, out.Events, 1)
	assert.Equal(t, []string{"topic-raw-xdr"}, out.Events[0].TopicsXDR)
	require.NotNil(t, out.Events[0].ValueXDR)
	assert.Equal(t, "value-raw-xdr", *out.Events[0].ValueXDR)
	assert.True(t, out.Events[0].Decoded)
	assert.Equal(t, "transfer", out.Events[0].DecodedEvent.Event)
}

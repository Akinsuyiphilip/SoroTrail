package graphql

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/api"
	"github.com/sorotrail/sorotrail/internal/store"
)

// TestIndexAfter covers the contract-list cursor lookup used to
// resume pagination: it must locate the row matching the cursor's
// id, report a sentinel when the id isn't in the list, and never
// panic on an empty slice.
func TestIndexAfter(t *testing.T) {
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	rows := []store.WatchedContract{
		{ContractID: "CABC", AddedAt: now},
		{ContractID: "CDEF", AddedAt: now},
		{ContractID: "CGHI", AddedAt: now},
	}

	tests := []struct {
		name string
		rows []store.WatchedContract
		id   string
		want int
	}{
		{name: "known id returns the index of the matching row", rows: rows, id: "CDEF", want: 1},
		{name: "last element returns the final index", rows: rows, id: "CGHI", want: 2},
		{name: "unknown id returns the documented sentinel", rows: rows, id: "missing", want: -1},
		{name: "empty id with no matching row returns the sentinel", rows: rows, id: "", want: -1},
		{name: "empty slice is handled without panicking", rows: nil, id: "CABC", want: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, indexAfter(tt.rows, tt.id))
		})
	}
}

func TestContractsResolver_TenantScopeIsolation(t *testing.T) {
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	st := &stubStore{
		watchedList: []store.WatchedContract{
			{ContractID: "CTENANTA1", AddedAt: now},
			{ContractID: "CTENANTA2", AddedAt: now},
			{ContractID: "CTENANTB1", AddedAt: now},
		},
	}
	h := newGraphQLTestServer(t, st)

	tenantACtx := api.WithPrincipal(context.Background(), api.Principal{
		Scope: store.NewScope([]string{"CTENANTA1", "CTENANTA2"}),
	})
	tenantBCtx := api.WithPrincipal(context.Background(), api.Principal{
		Scope: store.NewScope([]string{"CTENANTB1"}),
	})
	unauthenticatedCtx := context.Background()

	t.Run("tenant A sees only its own contracts and totalCount matches", func(t *testing.T) {
		resp := postQueryWithContext(t, h, tenantACtx, `{ contracts { nodes { contractId } totalCount } }`)
		require.Empty(t, errorsOf(t, resp))
		conn := resp["data"].(map[string]any)["contracts"].(map[string]any)
		nodes := conn["nodes"].([]any)
		require.Len(t, nodes, 2)
		assert.Equal(t, "CTENANTA1", nodes[0].(map[string]any)["contractId"])
		assert.Equal(t, "CTENANTA2", nodes[1].(map[string]any)["contractId"])
		assert.Equal(t, float64(2), conn["totalCount"])

		lookupOwn := postQueryWithContext(t, h, tenantACtx, `{ contract(id: "CTENANTA1") { contractId } }`)
		require.Empty(t, errorsOf(t, lookupOwn))
		require.NotNil(t, lookupOwn["data"].(map[string]any)["contract"])

		lookupOther := postQueryWithContext(t, h, tenantACtx, `{ contract(id: "CTENANTB1") { contractId } }`)
		require.Empty(t, errorsOf(t, lookupOther))
		assert.Nil(t, lookupOther["data"].(map[string]any)["contract"])
	})

	t.Run("tenant B sees only its own contracts and totalCount matches", func(t *testing.T) {
		resp := postQueryWithContext(t, h, tenantBCtx, `{ contracts { nodes { contractId } totalCount } }`)
		require.Empty(t, errorsOf(t, resp))
		conn := resp["data"].(map[string]any)["contracts"].(map[string]any)
		nodes := conn["nodes"].([]any)
		require.Len(t, nodes, 1)
		assert.Equal(t, "CTENANTB1", nodes[0].(map[string]any)["contractId"])
		assert.Equal(t, float64(1), conn["totalCount"])
	})

	t.Run("no principal returns empty list and zero totalCount", func(t *testing.T) {
		resp := postQueryWithContext(t, h, unauthenticatedCtx, `{ contracts { nodes { contractId } totalCount } }`)
		require.Empty(t, errorsOf(t, resp))
		conn := resp["data"].(map[string]any)["contracts"].(map[string]any)
		assert.Empty(t, conn["nodes"].([]any))
		assert.Equal(t, float64(0), conn["totalCount"])

		lookup := postQueryWithContext(t, h, unauthenticatedCtx, `{ contract(id: "CTENANTA1") { contractId } }`)
		require.Empty(t, errorsOf(t, lookup))
		assert.Nil(t, lookup["data"].(map[string]any)["contract"])
	})
}


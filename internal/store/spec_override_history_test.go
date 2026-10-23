package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSQLite_ContractSpecOverrideHistory exercises the override history
// through a real store: the append, the listing order, the cursor, and the
// tombstone a delete leaves behind. SQLite is used because it needs no
// server, so the history's behaviour is covered by the unit suite and not
// only by the Postgres integration run.
func TestSQLite_ContractSpecOverrideHistory(t *testing.T) {
	ctx := context.Background()
	st := newSQLiteStore(t)

	const contractID = "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const spec = `{"events":[{"name":"transfer"}]}`

	// The first write appends a history entry carrying the spec.
	require.NoError(t, st.SetContractSpecOverrideWithHistory(ctx, contractID, []byte(spec), "prefix1"))

	versions, cursor, err := st.ListContractSpecOverrideHistory(ctx, contractID, 10, "")
	require.NoError(t, err)
	require.Len(t, versions, 1)
	assert.Equal(t, contractID, versions[0].ContractID)
	assert.JSONEq(t, spec, string(versions[0].Spec))
	assert.Equal(t, "prefix1", versions[0].APIKeyPrefix)
	assert.False(t, versions[0].CreatedAt.IsZero(), "the entry must be timestamped")
	assert.Empty(t, cursor, "a partial-free page carries no cursor")

	// A second write appends a second entry, and the listing is newest first.
	require.NoError(t, st.SetContractSpecOverrideWithHistory(ctx, contractID,
		[]byte(`{"events":[{"name":"other"}]}`), "prefix2"))
	versions, _, err = st.ListContractSpecOverrideHistory(ctx, contractID, 10, "")
	require.NoError(t, err)
	require.Len(t, versions, 2)
	assert.JSONEq(t, `{"events":[{"name":"other"}]}`, string(versions[0].Spec))
	assert.JSONEq(t, spec, string(versions[1].Spec))
	assert.Greater(t, versions[0].ID, versions[1].ID, "the ids increase with each change")

	// A delete appends a tombstone: the history keeps the fact that the
	// override went away.
	require.NoError(t, st.DeleteContractSpecOverrideWithHistory(ctx, contractID, "prefix3"))
	versions, _, err = st.ListContractSpecOverrideHistory(ctx, contractID, 10, "")
	require.NoError(t, err)
	require.Len(t, versions, 3)
	assert.Empty(t, versions[0].Spec, "a tombstone carries no spec")

	// The current-override read path is unaffected by the history: the
	// override is gone, but the history still answers.
	_, err = st.GetContractSpecOverride(ctx, contractID)
	assert.ErrorIs(t, err, ErrNotFound)

	// The cursor pages the history without repeating or skipping an entry.
	versions, cursor, err = st.ListContractSpecOverrideHistory(ctx, contractID, 2, "")
	require.NoError(t, err)
	require.Len(t, versions, 2)
	assert.NotEmpty(t, cursor, "a partial page must carry a cursor")

	older, _, err := st.ListContractSpecOverrideHistory(ctx, contractID, 2, cursor)
	require.NoError(t, err)
	require.Len(t, older, 1, "the older page holds the remaining entry")
	assert.Less(t, older[0].ID, versions[1].ID)

	// A malformed cursor is client input, not a server fault.
	_, _, err = st.ListContractSpecOverrideHistory(ctx, contractID, 2, "not base64!!")
	assert.ErrorIs(t, err, ErrInvalidCursor)
}

// TestSQLite_ContractSpecOverrideHistoryIsPerContract proves one contract's
// history does not leak into another's.
func TestSQLite_ContractSpecOverrideHistoryIsPerContract(t *testing.T) {
	ctx := context.Background()
	st := newSQLiteStore(t)

	const first = "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const second = "CBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

	require.NoError(t, st.SetContractSpecOverrideWithHistory(ctx, first, []byte(`{"events":[]}`), "p1"))
	require.NoError(t, st.SetContractSpecOverrideWithHistory(ctx, second, []byte(`{"events":[]}`), "p2"))

	versions, _, err := st.ListContractSpecOverrideHistory(ctx, first, 10, "")
	require.NoError(t, err)
	require.Len(t, versions, 1)
	assert.Equal(t, first, versions[0].ContractID)
}

// TestSpecOverrideHistoryCursor covers the cursor encoding: it round-trips,
// and anything that is not a version id is rejected.
func TestSpecOverrideHistoryCursor(t *testing.T) {
	cursor := EncodeSpecOverrideHistoryCursor(42)
	id, err := DecodeSpecOverrideHistoryCursor(cursor)
	require.NoError(t, err)
	assert.Equal(t, int64(42), id)

	for _, bad := range []string{"", "not base64!", "!!!!", EncodeSpecOverrideHistoryCursor(0)} {
		_, err := DecodeSpecOverrideHistoryCursor(bad)
		assert.ErrorIsf(t, err, ErrInvalidCursor, "cursor %q must be rejected", bad)
	}
}

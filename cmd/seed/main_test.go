package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPtrU64 covers ptrU64 for zero and non-zero values, ensuring
// a non-nil pointer is returned that dereferences to the input.
func TestPtrU64(t *testing.T) {
	t.Parallel()

	// Zero value: a helper that returned nil for zero would be a subtle bug.
	pZero := ptrU64(0)
	require.NotNil(t, pZero, "ptrU64(0) must return a non-nil pointer")
	assert.Equal(t, uint64(0), *pZero, "ptrU64(0) must dereference to 0")

	// Non-zero value.
	const val = 123456789
	pVal := ptrU64(val)
	require.NotNil(t, pVal, "ptrU64 must return a non-nil pointer for non-zero values")
	assert.Equal(t, uint64(val), *pVal, "ptrU64 must dereference to the input value")
}

package decode

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestXDRDecoder_NumericBoundaries pins the ends of every integer ScVal
// width. The middle of each range is covered by the table in xdr_test.go;
// the edges are where a signed/unsigned mix-up or a hi/lo word swap shows
// up, and where a decoding that went through float64 would quietly lose the
// low bits of a 64-bit value.
func TestXDRDecoder_NumericBoundaries(t *testing.T) {
	t.Parallel()

	u32 := func(n uint32) xdr.ScVal {
		v := xdr.Uint32(n)
		return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &v}
	}
	i32 := func(n int32) xdr.ScVal {
		v := xdr.Int32(n)
		return xdr.ScVal{Type: xdr.ScValTypeScvI32, I32: &v}
	}
	u64 := func(n uint64) xdr.ScVal {
		v := xdr.Uint64(n)
		return xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &v}
	}
	i64 := func(n int64) xdr.ScVal {
		v := xdr.Int64(n)
		return xdr.ScVal{Type: xdr.ScValTypeScvI64, I64: &v}
	}
	u128 := func(hi, lo uint64) xdr.ScVal {
		v := xdr.UInt128Parts{Hi: xdr.Uint64(hi), Lo: xdr.Uint64(lo)}
		return xdr.ScVal{Type: xdr.ScValTypeScvU128, U128: &v}
	}
	i128 := func(hi int64, lo uint64) xdr.ScVal {
		v := xdr.Int128Parts{Hi: xdr.Int64(hi), Lo: xdr.Uint64(lo)}
		return xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &v}
	}

	tests := []struct {
		name string
		val  xdr.ScVal
		want string
	}{
		{"u32 min", u32(0), `{"u32":0}`},
		{"u32 max", u32(4294967295), `{"u32":4294967295}`},
		{"i32 min", i32(-2147483648), `{"i32":-2147483648}`},
		{"i32 max", i32(2147483647), `{"i32":2147483647}`},

		{"u64 min", u64(0), `{"u64":0}`},
		{"u64 max", u64(18446744073709551615), `{"u64":18446744073709551615}`},
		{"i64 min", i64(-9223372036854775808), `{"i64":-9223372036854775808}`},
		{"i64 max", i64(9223372036854775807), `{"i64":9223372036854775807}`},

		{"u128 zero", u128(0, 0), `{"u128":"0"}`},
		{
			"u128 max",
			u128(18446744073709551615, 18446744073709551615),
			`{"u128":"340282366920938463463374607431768211455"}`,
		},
		{
			// Only the low word is set: catches a decoder that reads the
			// parts in the wrong order.
			"u128 low word only",
			u128(0, 18446744073709551615),
			`{"u128":"18446744073709551615"}`,
		},
		{
			"i128 min",
			i128(-9223372036854775808, 0),
			`{"i128":"-170141183460469231731687303715884105728"}`,
		},
		{
			"i128 max",
			i128(9223372036854775807, 18446744073709551615),
			`{"i128":"170141183460469231731687303715884105727"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := XDRDecoder{}.DecodeScVal(mustBase64(t, tt.val))
			require.NoError(t, err)
			// Compared as raw JSON text, not JSONEq: unmarshalling a 64-bit
			// integer into an any lands in a float64 and would hide exactly
			// the precision loss these cases exist to catch.
			assert.Equal(t, tt.want, string(got))
		})
	}
}

// TestXDRDecoder_EmptyCollections covers the empty vec and empty map at the
// top level. Nested empties are covered in xdr_test.go; the top-level ones
// are what a resolver sees for an event with no topics, and they must decode
// to an empty JSON array rather than null.
func TestXDRDecoder_EmptyCollections(t *testing.T) {
	t.Parallel()

	emptyVec := xdr.ScVec{}
	emptyVecPtr := &emptyVec
	emptyMap := xdr.ScMap{}
	emptyMapPtr := &emptyMap

	tests := []struct {
		name string
		val  xdr.ScVal
		want string
	}{
		{"empty vec", xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &emptyVecPtr}, `{"vec":[]}`},
		{"empty map", xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &emptyMapPtr}, `{"map":[]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := XDRDecoder{}.DecodeScVal(mustBase64(t, tt.val))
			require.NoError(t, err)
			assert.JSONEq(t, tt.want, string(got))
		})
	}
}

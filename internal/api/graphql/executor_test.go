package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestExecuteOperation_SingleOperation(t *testing.T) {
	// Register a test route
	RegisterRoute("Query", "testField", func(r *Resolver, ctx context.Context, args json.RawMessage, vars map[string]any) (any, error) {
		return "hello world", nil
	})

	UseResolver(&Resolver{})

	req := &GraphQLRequest{
		Query: "query { testField }",
	}

	env, err := executeOperation(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, env)
	assert.Empty(t, env.Errors)
	assert.Equal(t, "hello world", env.Data["testField"])
}

func TestExecuteOperation_NamedOperationSelection(t *testing.T) {
	RegisterRoute("Query", "opOne", func(r *Resolver, ctx context.Context, args json.RawMessage, vars map[string]any) (any, error) {
		return "first", nil
	})
	RegisterRoute("Query", "opTwo", func(r *Resolver, ctx context.Context, args json.RawMessage, vars map[string]any) (any, error) {
		return "second", nil
	})

	UseResolver(&Resolver{})

	query := `
		query FirstOp { opOne }
		query SecondOp { opTwo }
	`

	// A document with multiple operations is rejected outright, even when
	// an operationName is supplied.
	req := &GraphQLRequest{
		Query:         query,
		OperationName: "SecondOp",
	}

	_, err := executeOperation(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple operations per request not supported")
}

func TestExecuteOperation_AmbiguousSelectionWithoutName(t *testing.T) {
	RegisterRoute(
		"Query",
		"opOne",
		func(r *Resolver, ctx context.Context, args json.RawMessage, vars map[string]any) (any, error) {
			return "first", nil
		},
	)
	RegisterRoute(
		"Query",
		"opTwo",
		func(r *Resolver, ctx context.Context, args json.RawMessage, vars map[string]any) (any, error) {
			return "second", nil
		},
	)

	UseResolver(&Resolver{})

	query := `
		query FirstOp { opOne }
		query SecondOp { opTwo }
	`

	req := &GraphQLRequest{
		Query: query,
	}

	// executeOperation accepts at most one operation per document and
	// rejects a multi-operation request outright.
	_, err := executeOperation(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple operations per request not supported")
}

func TestExecuteOperation_FieldArgumentsReachResolver(t *testing.T) {
	var capturedArgs json.RawMessage
	RegisterRoute("Query", "echo", func(r *Resolver, ctx context.Context, args json.RawMessage, vars map[string]any) (any, error) {
		capturedArgs = args
		return "ok", nil
	})

	UseResolver(&Resolver{})

	req := &GraphQLRequest{
		Query: `{ echo(msg: "hello arguments") }`,
	}

	env, err := executeOperation(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, env)
	assert.Empty(t, env.Errors)
	assert.Contains(t, string(capturedArgs), "hello arguments")
}

func TestExecuteOperation_ResolverErrorSurfacesAsGraphQLError(t *testing.T) {
	RegisterRoute("Query", "fails", func(r *Resolver, ctx context.Context, args json.RawMessage, vars map[string]any) (any, error) {
		return nil, errors.New("database connection failed")
	})

	UseResolver(&Resolver{})

	req := &GraphQLRequest{
		Query: `{ fails }`,
	}

	env, err := executeOperation(context.Background(), req)
	require.NoError(t, err, "resolver errors should be captured in envelope errors, not returned as execution error")
	require.NotNil(t, env)
	require.Len(t, env.Errors, 1)
	assert.Equal(t, "database connection failed", env.Errors[0].Message)
	assert.Nil(t, env.Data["fails"])
}

func TestExecuteOperation_PartialDataWithErrors(t *testing.T) {
	RegisterRoute("Query", "successField", func(r *Resolver, ctx context.Context, args json.RawMessage, vars map[string]any) (any, error) {
		return "all good", nil
	})
	RegisterRoute(
		"Query",
		"errorField",
		func(r *Resolver, ctx context.Context, args json.RawMessage, vars map[string]any) (any, error) {
			return nil, errors.New("partial failure")
		},
	)

	UseResolver(&Resolver{})

	req := &GraphQLRequest{
		Query: `{ successField errorField }`,
	}

	env, err := executeOperation(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, env)
	require.Len(t, env.Errors, 1)
	assert.Equal(t, "partial failure", env.Errors[0].Message)
	assert.Equal(t, "all good", env.Data["successField"])
	assert.Nil(t, env.Data["errorField"])
}

// TestArgumentValue pins how argumentValue coerces a parsed GraphQL
// argument into the Go value that fieldArguments marshals for the
// resolvers. A wrong coercion here does not fail loudly: it silently
// changes what a query filters on, so every kind is asserted exactly.
func TestArgumentValue(t *testing.T) {
	scalar := func(kind ast.ValueKind, raw string) *ast.Value {
		return &ast.Value{Kind: kind, Raw: raw}
	}
	vars := map[string]any{
		"contract": "CABC",
		"limit":    float64(25),
		"cleared":  nil,
	}

	tests := []struct {
		name    string
		value   *ast.Value
		want    any
		wantErr string
	}{
		// Scalars coerce to the Go type encoding/json would produce, so
		// resolvers can unmarshal them into their typed args structs.
		{name: "int", value: scalar(ast.IntValue, "42"), want: int64(42)},
		{name: "negative int", value: scalar(ast.IntValue, "-7"), want: int64(-7)},
		{name: "int beyond int64", value: scalar(ast.IntValue, "9223372036854775808"), wantErr: `invalid int "9223372036854775808"`},
		{name: "float", value: scalar(ast.FloatValue, "1.5"), want: 1.5},
		{name: "float with exponent", value: scalar(ast.FloatValue, "2e3"), want: 2000.0},
		{name: "quoted string is unquoted", value: scalar(ast.StringValue, `"CABC"`), want: "CABC"},
		{name: "escaped string is decoded", value: scalar(ast.StringValue, `"a\"b"`), want: `a"b`},
		// The parser hands over strings already unquoted, so a Raw that is
		// not valid JSON must pass through untouched rather than error.
		{name: "unquoted string passes through", value: scalar(ast.StringValue, "CABC"), want: "CABC"},
		{name: "enum", value: scalar(ast.EnumValue, "DESC"), want: "DESC"},
		{name: "boolean true", value: scalar(ast.BooleanValue, "true"), want: true},
		{name: "boolean false", value: scalar(ast.BooleanValue, "false"), want: false},

		// A wrongly typed literal must surface as an error, not collapse
		// into a zero value that would widen the filter.
		{name: "non-numeric int", value: scalar(ast.IntValue, "ten"), wantErr: `invalid int "ten"`},
		{name: "float literal as int", value: scalar(ast.IntValue, "1.5"), wantErr: `invalid int "1.5"`},
		{name: "non-numeric float", value: scalar(ast.FloatValue, "abc"), wantErr: `invalid float "abc"`},
		{name: "unsupported kind", value: &ast.Value{Kind: ast.ValueKind(99)}, wantErr: "unsupported arg kind"},

		// Null and absent: a nil value and an explicit null both yield nil,
		// but a reference to an undeclared variable is an error so a typo
		// in the request cannot silently drop the filter.
		{name: "nil value", value: nil, want: nil},
		{name: "null literal", value: scalar(ast.NullValue, "null"), want: nil},
		{name: "variable", value: scalar(ast.Variable, "contract"), want: "CABC"},
		{name: "variable keeps its decoded type", value: scalar(ast.Variable, "limit"), want: float64(25)},
		{name: "variable explicitly null", value: scalar(ast.Variable, "cleared"), want: nil},
		{name: "missing variable", value: scalar(ast.Variable, "absent"), wantErr: `missing variable "absent"`},

		// Composite values recurse through Children.
		{
			name: "list",
			value: &ast.Value{Kind: ast.ListValue, Children: ast.ChildValueList{
				{Value: scalar(ast.IntValue, "1")},
				{Value: scalar(ast.StringValue, `"two"`)},
				{Value: scalar(ast.Variable, "contract")},
			}},
			want: []any{int64(1), "two", "CABC"},
		},
		{name: "empty list", value: &ast.Value{Kind: ast.ListValue}, want: []any{}},
		{
			name: "object",
			value: &ast.Value{Kind: ast.ObjectValue, Children: ast.ChildValueList{
				{Name: "contractId", Value: scalar(ast.Variable, "contract")},
				{Name: "first", Value: scalar(ast.IntValue, "10")},
				{Name: "nested", Value: &ast.Value{Kind: ast.ObjectValue, Children: ast.ChildValueList{
					{Name: "asc", Value: scalar(ast.BooleanValue, "true")},
				}}},
			}},
			want: map[string]any{"contractId": "CABC", "first": int64(10), "nested": map[string]any{"asc": true}},
		},
		{name: "empty object", value: &ast.Value{Kind: ast.ObjectValue}, want: map[string]any{}},
		{
			name: "error inside list propagates",
			value: &ast.Value{Kind: ast.ListValue, Children: ast.ChildValueList{
				{Value: scalar(ast.IntValue, "1")},
				{Value: scalar(ast.IntValue, "x")},
			}},
			wantErr: `invalid int "x"`,
		},
		{
			name: "error inside object propagates",
			value: &ast.Value{Kind: ast.ObjectValue, Children: ast.ChildValueList{
				{Name: "id", Value: scalar(ast.Variable, "absent")},
			}},
			wantErr: `missing variable "absent"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := argumentValue(tc.value, vars)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Nil(t, got, "a failed coercion must not return a partial value")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// firstField parses q and returns its first root-level field, so the
// table below exercises the same AST shape executeOperation hands to
// fieldArguments rather than a hand-built approximation of it.
func firstField(t *testing.T, q string) *ast.Field {
	t.Helper()
	f, ok := parseOp(t, q).SelectionSet[0].(*ast.Field)
	require.True(t, ok, "first selection is not a field")
	return f
}

// TestFieldArguments pins how fieldArguments turns a parsed field's
// argument list into the JSON object resolvers unmarshal. A dropped or
// widened argument here silently changes what a query filters on, so
// every rule is asserted exactly.
func TestFieldArguments(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		vars    map[string]any
		want    map[string]any
		wantErr string
	}{
		// Every kind the executor accepts must survive into the JSON
		// object, not only the string/int pair the happy path usually hits.
		{
			name:  "all supplied arguments are collected",
			query: `{ events(contract: "CABC", first: 10, live: true, status: OPEN, tags: ["a", "b"], meta: { page: 2 }) }`,
			want: map[string]any{
				"contract": "CABC",
				"first":    float64(10),
				"live":     true,
				"status":   "OPEN",
				"tags":     []any{"a", "b"},
				"meta":     map[string]any{"page": float64(2)},
			},
		},

		// Resolvers rely on non-nil JSON, so the no-argument case must
		// stay the literal empty object rather than a nil RawMessage.
		{
			name:  "field with no arguments yields an empty map rather than nil",
			query: `{ events }`,
			want:  map[string]any{},
		},

		// Nothing upstream rejects a duplicated argument name before this
		// function, so the map assignment is the whole rule: the last
		// occurrence wins. Pinning that keeps a silent switch to
		// first-wins from changing what queries filter on.
		{
			name:  "duplicate argument names keep the last occurrence",
			query: `{ events(limit: 5, limit: 9) }`,
			want:  map[string]any{"limit": float64(9)},
		},

		// Variables are resolved against the operation's variables, both
		// as whole values and inside composites like lists.
		{
			name:  "variable references are resolved against the operation's variables",
			query: `query Q($c: String!, $n: Int!) { events(contract: $c, first: $n, tags: [$c, "fixed"]) }`,
			vars:  map[string]any{"c": "CABC", "n": float64(10)},
			want: map[string]any{
				"contract": "CABC",
				"first":    float64(10),
				"tags":     []any{"CABC", "fixed"},
			},
		},

		// An unresolvable value must fail the field with the argument
		// named in the error, never collapse into a dropped filter that
		// silently widens the query.
		{
			name:    "unknown variable is reported with the argument's name",
			query:   `{ events(contract: $absent) }`,
			wantErr: `argument "contract": missing variable "absent"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fieldArguments(firstField(t, tc.query), tc.vars)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Nil(t, got, "a failed conversion must not return partial arguments")
				return
			}
			require.NoError(t, err)
			// json.RawMessage("{}") is non-nil; unmarshalling also proves
			// the payload is valid JSON before it reaches a resolver.
			require.NotNil(t, got)
			var decoded map[string]any
			require.NoError(t, json.Unmarshal(got, &decoded))
			assert.Equal(t, tc.want, decoded)
		})
	}
}

func TestFieldArguments_CoercionAndErrors(t *testing.T) {
	// Verify fieldArguments orchestrates argument extraction and validation
	// using the coercion routines.
	// We construct an AST field with arguments and call fieldArguments directly.
	selection := &ast.Field{
		Arguments: ast.ArgumentList{
			{
				Name:  "limit",
				Value: &ast.Value{Kind: ast.IntValue, Raw: "10"},
			},
		},
	}
	args, err := fieldArguments(selection, map[string]any{})
	require.NoError(t, err)
	require.NotNil(t, args)
	var parsed struct {
		Limit int64 `json:"limit"`
	}
	err = json.Unmarshal(args, &parsed)
	require.NoError(t, err)
	assert.Equal(t, int64(10), parsed.Limit)
}

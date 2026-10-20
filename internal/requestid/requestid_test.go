package requestid

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFromContext(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{
			name: "empty context has no id",
			ctx:  context.Background(),
			want: "",
		},
		{
			name: "request id is visible",
			ctx:  WithRequestID(context.Background(), "req-123"),
			want: "req-123",
		},
		{
			name: "job id is visible",
			ctx:  WithJob(context.Background(), JobIngester),
			want: JobIngester,
		},
		{
			name: "empty id does not mask an outer value",
			ctx:  WithRequestID(WithRequestID(context.Background(), "outer"), ""),
			want: "outer",
		},
		{
			name: "innermost request id wins",
			ctx:  WithRequestID(WithRequestID(context.Background(), "outer"), "inner"),
			want: "inner",
		},
		{
			name: "job overrides an earlier request id",
			ctx:  WithJob(WithRequestID(context.Background(), "req-1"), JobPruner),
			want: JobPruner,
		},
		{
			name: "request id overrides an earlier job",
			ctx:  WithRequestID(WithJob(context.Background(), JobAuditor), "req-2"),
			want: "req-2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FromContext(tt.ctx))
		})
	}
}

func TestAttrs(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want []any
	}{
		{
			name: "empty context has no attrs",
			ctx:  context.Background(),
			want: nil,
		},
		{
			name: "request id becomes one key/value pair",
			ctx:  WithRequestID(context.Background(), "req-123"),
			want: []any{"request_id", "req-123"},
		},
		{
			name: "job id uses the same field",
			ctx:  WithJob(context.Background(), JobWebhook),
			want: []any{"request_id", JobWebhook},
		},
		{
			name: "empty id produces no attrs",
			ctx:  WithRequestID(context.Background(), ""),
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Attrs(tt.ctx))
		})
	}
}

func TestFieldIsLowerSnakeCase(t *testing.T) {
	assert.Equal(t, "request_id", Field)
	assert.Regexp(t, `^[a-z][a-z0-9_]*$`, Field,
		"structured-log keys are lower_snake_case so log pipelines can rely on them")
}

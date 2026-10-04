package apiauth

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/logctx"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"gotest.tools/v3/assert"
)

func TestObservabilityInterceptor(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	ctx = WithUser(ctx, &UserInfo{OrgID: 5})

	var got context.Context
	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		got = ctx
		return nil, nil
	}
	// CreateLinkRequest carries a task id via GetTaskId.
	req := connect.NewRequest(&gritzv1.CreateLinkRequest{TaskId: 11})
	_, err := ObservabilityInterceptor()(next)(ctx, req)
	assert.NilError(t, err)
	span.End()

	// Downstream context carries both identifiers.
	org, ok := logctx.OrgID(got)
	assert.Assert(t, ok)
	assert.Equal(t, org, int64(5))
	task, ok := logctx.TaskID(got)
	assert.Assert(t, ok)
	assert.Equal(t, task, int64(11))

	// The span is annotated with both identifiers.
	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range recorder.Ended()[0].Attributes() {
		attrs[kv.Key] = kv.Value
	}
	assert.Equal(t, attrs["org.id"].AsInt64(), int64(5))
	assert.Equal(t, attrs["task.id"].AsInt64(), int64(11))
}

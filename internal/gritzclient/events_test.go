package gritzclient_test

import (
	"context"
	"testing"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
)

func TestListAllEventsByTask(t *testing.T) {
	t.Parallel()
	// Arrange - two pages keyed by the token that requests them: the newest page
	// (empty token) points back to an older one, which exhausts history.
	pages := map[string]*gritzv1.ListEventsByTaskResponse{
		"": {
			Events:        []*gritzv1.Event{{Id: 3}, {Id: 4}},
			PrevPageToken: "older",
			NextPageToken: "tail",
			More:          true,
		},
		"older": {
			Events:        []*gritzv1.Event{{Id: 1}, {Id: 2}},
			NextPageToken: "tail",
		},
	}
	client := &gritzclient.ClientMock{
		ListEventsByTaskFunc: func(ctx context.Context, req *gritzv1.ListEventsByTaskRequest) (*gritzv1.ListEventsByTaskResponse, error) {
			return pages[req.GetPageToken()], nil
		},
	}
	types := []string{"instruction", "external"}

	// Act
	events, err := gritzclient.ListAllEventsByTask(t.Context(), client, 7, types)

	// Assert - older pages are prepended, so the result is oldest-first, and every
	// request carries the task, a non-zero page size, and the type filter.
	assert.NilError(t, err)
	assert.DeepEqual(t, testx.ExtractField(events, "Id"), []int64{1, 2, 3, 4})
	calls := client.ListEventsByTaskCalls()
	assert.Equal(t, len(calls), 2)
	for _, call := range calls {
		assert.Equal(t, call.ListEventsByTaskRequest.GetTaskId(), int64(7))
		assert.Assert(t, call.ListEventsByTaskRequest.GetPageSize() > 0)
		assert.DeepEqual(t, call.ListEventsByTaskRequest.GetTypes(), types)
	}
}

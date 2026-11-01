package gritzclient_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/testx"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
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

func TestListRunnerEvents(t *testing.T) {
	t.Parallel()
	// Arrange - two pages keyed by the token that requests them; the second
	// reports no more.
	pages := map[string]*gritzv1.ListRunnerEventsResponse{
		"start": {
			Events:        []*gritzv1.Event{{Id: 1}, {Id: 2}},
			NextPageToken: "p2",
			More:          true,
		},
		"p2": {
			Events:        []*gritzv1.Event{{Id: 3}},
			NextPageToken: "p3",
		},
	}
	client := &gritzclient.ClientMock{
		ListRunnerEventsFunc: func(ctx context.Context, req *gritzv1.ListRunnerEventsRequest) (*gritzv1.ListRunnerEventsResponse, error) {
			return pages[req.GetPageToken()], nil
		},
	}
	req := &gritzv1.ListRunnerEventsRequest{Runner: "r1", PageToken: "start", PageSize: 2}

	// Act
	var got []*gritzv1.ListRunnerEventsResponse
	for resp, err := range gritzclient.ListRunnerEvents(t.Context(), client, req) {
		assert.NilError(t, err)
		got = append(got, resp)
	}

	// Assert - each page is yielded in order, every request carries the
	// runner and page size, and the caller's request is left unchanged.
	assert.DeepEqual(t,
		got,
		[]*gritzv1.ListRunnerEventsResponse{pages["start"], pages["p2"]},
		protocmp.Transform(),
	)
	var reqs []*gritzv1.ListRunnerEventsRequest
	for _, call := range client.ListRunnerEventsCalls() {
		reqs = append(reqs, call.ListRunnerEventsRequest)
	}
	assert.DeepEqual(t,
		reqs,
		[]*gritzv1.ListRunnerEventsRequest{
			{Runner: "r1", PageToken: "start", PageSize: 2},
			{Runner: "r1", PageToken: "p2", PageSize: 2},
		},
		protocmp.Transform(),
	)
	assert.Equal(t, req.GetPageToken(), "start")
}

func TestListRunnerEvents_Error(t *testing.T) {
	t.Parallel()
	// Arrange
	client := &gritzclient.ClientMock{
		ListRunnerEventsFunc: func(ctx context.Context, req *gritzv1.ListRunnerEventsRequest) (*gritzv1.ListRunnerEventsResponse, error) {
			return nil, errors.New("boom")
		},
	}

	// Act
	var errs []error
	for resp, err := range gritzclient.ListRunnerEvents(t.Context(), client, &gritzv1.ListRunnerEventsRequest{}) {
		assert.Assert(t, resp == nil)
		errs = append(errs, err)
	}

	// Assert - the error is yielded once and the walk stops.
	assert.Assert(t, cmp.Len(errs, 1))
	assert.ErrorContains(t, errs[0], "boom")
	assert.Assert(t, cmp.Len(client.ListRunnerEventsCalls(), 1))
}

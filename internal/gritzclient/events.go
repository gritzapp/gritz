package gritzclient

import (
	"context"
	"iter"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
)

// ListAllEventsByTask returns a task's whole event stream, oldest-first,
// optionally filtered to types (empty = all types). It opens at the newest page
// and follows prev_page_token back through history, prepending each older page,
// until More reports nothing older remains.
func ListAllEventsByTask(ctx context.Context, c Client, taskID int64, types []string) ([]*gritzv1.Event, error) {
	req := &gritzv1.ListEventsByTaskRequest{
		TaskId:   taskID,
		PageSize: 200, // the server's max, for the fewest round trips
		Types:    types,
	}
	var events []*gritzv1.Event
	for {
		resp, err := c.ListEventsByTask(ctx, req)
		if err != nil {
			return nil, err
		}
		events = append(resp.GetEvents(), events...)
		if !resp.GetMore() || resp.GetPrevPageToken() == "" {
			return events, nil
		}
		req.PageToken = resp.GetPrevPageToken()
	}
}

// ListRunnerEvents returns an iterator over the pages of the runner's event
// log, starting from req.GetPageToken() and threading next_page_token forward
// until More reports no further page is available right now. The caller stores
// each page's next_page_token as its cursor.
//
// The caller's req is not mutated. Each successful page is yielded as
// (resp, nil); on an RPC error the iterator yields (nil, err) once and stops.
// The walk also stops early if the caller breaks out of the range loop.
func ListRunnerEvents(ctx context.Context, c Client, req *gritzv1.ListRunnerEventsRequest) iter.Seq2[*gritzv1.ListRunnerEventsResponse, error] {
	return func(yield func(*gritzv1.ListRunnerEventsResponse, error) bool) {
		token := req.GetPageToken()
		for {
			resp, err := c.ListRunnerEvents(ctx, &gritzv1.ListRunnerEventsRequest{
				Runner:    req.GetRunner(),
				PageToken: token,
				PageSize:  req.GetPageSize(),
			})
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(resp, nil) || !resp.GetMore() {
				return
			}
			token = resp.GetNextPageToken()
		}
	}
}

package gritzclient

import (
	"context"
	"iter"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
)

// ListEventsByTask returns an iterator over the pages of a task's event stream.
// It starts from req.GetPageToken() and threads next_page_token forward to the
// tail — More reports whether a further page exists beyond the one just walked,
// so !More marks the end (the live-follow next_page_token is always populated and
// can't signal it). The caller reads each page's next_page_token to track the
// cursor; the last one yielded points past the tail.
//
// The caller's req is not mutated: the iterator walks a shallow copy so it can
// advance PageToken freely (a by-value struct copy is avoided — copying a
// protobuf message trips go vet's copylocks check — so the scalar fields are
// copied explicitly).
//
// Each successful page is yielded as (resp, nil). On an RPC error the iterator
// yields (nil, err) once and stops, so the error reaches the caller through the
// range:
//
//	req := &gritzv1.ListEventsByTaskRequest{TaskId: id, PageSize: n}
//	for resp, err := range gritzclient.ListEventsByTask(ctx, c, req) {
//		if err != nil {
//			return err
//		}
//		token = resp.GetNextPageToken()
//	}
//
// The walk also stops early if the caller breaks out of the range loop.
func ListEventsByTask(ctx context.Context, c Client, req *gritzv1.ListEventsByTaskRequest) iter.Seq2[*gritzv1.ListEventsByTaskResponse, error] {
	return func(yield func(*gritzv1.ListEventsByTaskResponse, error) bool) {
		// Shallow-copy req so PageToken can advance as we walk without mutating the
		// caller's request (and so re-ranging restarts from the original cursor).
		// Types carries forward unchanged on every page so a server-side type
		// filter applies to the whole walk.
		cur := &gritzv1.ListEventsByTaskRequest{
			TaskId:    req.GetTaskId(),
			PageSize:  req.GetPageSize(),
			PageToken: req.GetPageToken(),
			Types:     req.GetTypes(),
		}
		for {
			resp, err := c.ListEventsByTask(ctx, cur)
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(resp, nil) {
				return
			}
			// Stop at the tail. More reports whether a further page exists beyond
			// this one and is exact even at a page_size boundary. See the More
			// contract on ListEventsByTaskResponse in gritz.proto.
			if !resp.GetMore() {
				return
			}
			cur.PageToken = resp.GetNextPageToken()
		}
	}
}

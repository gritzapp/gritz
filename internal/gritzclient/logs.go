package gritzclient

import (
	"context"
	"iter"
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/common"
)

// ListLogChunksByTask returns an iterator over the pages of a task's log,
// walking from req.GetPageToken() toward newer chunks. It threads
// next_page_token — the live-follow cursor — forward to the tail. More reports
// whether a further page exists beyond the one just walked, so !More marks the
// end: next_page_token is always populated (an empty poll echoes the cursor
// back) and so can't signal it, and a page that exactly fills page_size is
// indistinguishable by length from one with rows behind it. The caller reads
// each page's next_page_token to track the cursor; the last one yielded points
// past the tail.
//
// req.GetPageToken() must be a live-follow cursor — a next_page_token from an
// earlier response. An empty token opens at the tail instead (the newest page),
// whose More reports whether older *history* remains rather than newer rows, so
// it is not a meaningful start for this walk; TaskLog.History pages backwards
// from there.
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
//	req := &gritzv1.ListLogChunksByTaskRequest{TaskId: id, PageSize: n, PageToken: cursor}
//	for resp, err := range gritzclient.ListLogChunksByTask(ctx, c, req) {
//		if err != nil {
//			return err
//		}
//		cursor = resp.GetNextPageToken()
//	}
//
// The walk also stops early if the caller breaks out of the range loop.
func ListLogChunksByTask(ctx context.Context, c Client, req *gritzv1.ListLogChunksByTaskRequest) iter.Seq2[*gritzv1.ListLogChunksByTaskResponse, error] {
	return func(yield func(*gritzv1.ListLogChunksByTaskResponse, error) bool) {
		// Shallow-copy req so PageToken can advance as we walk without mutating the
		// caller's request (and so re-ranging restarts from the original cursor).
		cur := &gritzv1.ListLogChunksByTaskRequest{
			TaskId:    req.GetTaskId(),
			PageSize:  req.GetPageSize(),
			PageToken: req.GetPageToken(),
		}
		for {
			resp, err := c.ListLogChunksByTask(ctx, cur)
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(resp, nil) {
				return
			}
			// Stop at the tail. See the More contract on ListLogChunksByTaskResponse
			// in gritz.proto.
			if !resp.GetMore() {
				return
			}
			next := resp.GetNextPageToken()
			if next == "" {
				// Unreachable against the documented contract: a page with More set is
				// non-empty, and a non-empty page always carries a next_page_token.
				// There is nothing to advance onto, and falling back to the empty token
				// would re-open at the tail and replay, so stop instead of spinning.
				return
			}
			cur.PageToken = next
		}
	}
}

// TaskLog reads one task's driver log: the transcript its chunks concatenate
// into, plus the appends that land past the tail while a run is live. It owns
// the log RPC's cursor rules so callers don't have to rediscover them —
// prev_page_token walks toward older history and next_page_token toward newer
// rows, and only the tail page's next_page_token is a valid follow cursor
// (a later page's points back into history, so following one replays the
// transcript).
//
// Chunks are yielded whole rather than as raw bytes: a caller that only wants
// the transcript writes chunk.GetData(), while one that wants to delimit runs
// still has version, id and created_at.
//
// A TaskLog is stateful — History captures the cursor Follow resumes from — and
// is not safe for concurrent use.
type TaskLog struct {
	client   Client
	taskID   int64
	pageSize int32
	// follow is the cursor just past the last chunk yielded, taken from the newest
	// page read so far. It stays empty until the task has shipped a chunk.
	follow string
}

// OpenTaskLog returns a reader for taskID's log, fetching at most pageSize
// chunks per request (0 selects the server's default).
func OpenTaskLog(c Client, taskID int64, pageSize int32) *TaskLog {
	return &TaskLog{client: c, taskID: taskID, pageSize: pageSize}
}

// History returns an iterator over the task's transcript from its beginning to
// its current tail, oldest chunk first, and captures the follow cursor as a side
// effect so Follow resumes exactly where it stopped.
//
// The RPC opens at the tail, so the walk runs in two passes. The first pages
// backwards over prev_page_token until it reaches the oldest page, keeping only
// the cursor it is walking on — buffering the pages to reverse them would cost
// O(transcript bytes), and nothing bounds a transcript's size (log chunks are
// reclaimed only when the task is deleted). The second pass yields that oldest
// page and then walks forward to the tail, re-fetching the pages in transcript
// order: roughly 2x the requests for O(1) memory.
//
// Appends landing mid-walk can't disturb it. The backward pass' pages are pinned
// by their cursors, and the forward pass drains to whatever the tail is when it
// arrives there, leaving the follow cursor past everything it yielded.
//
// Each chunk is yielded as (chunk, nil). On an RPC error the iterator yields
// (nil, err) once and stops, so the error reaches the caller through the range:
//
//	log := gritzclient.OpenTaskLog(c, taskID, 50)
//	for chunk, err := range log.History(ctx) {
//		if err != nil {
//			return err
//		}
//		os.Stdout.Write(chunk.GetData())
//	}
//
// The walk also stops early if the caller breaks out of the range loop, leaving
// the follow cursor past the last page it yielded.
func (l *TaskLog) History(ctx context.Context) iter.Seq2[*gritzv1.LogChunk, error] {
	return func(yield func(*gritzv1.LogChunk, error) bool) {
		// An empty page token opens at the tail — the newest page.
		resp, err := l.list(ctx, "")
		if err != nil {
			yield(nil, err)
			return
		}
		// Pass one: step back to the oldest page, holding on to nothing but the
		// response being walked. more reports whether older rows remain;
		// prev_page_token empties at the same point, but check both so a bad page
		// can't spin the walk.
		var paged bool
		for resp.GetMore() && resp.GetPrevPageToken() != "" {
			paged = true
			if resp, err = l.list(ctx, resp.GetPrevPageToken()); err != nil {
				yield(nil, err)
				return
			}
		}
		// resp is the oldest page. Pages are walked in transcript order from here,
		// and a page's own chunks are oldest-first, so the bytes concatenate
		// directly.
		if !yieldChunks(yield, resp) {
			return
		}
		l.advance(resp)
		if !paged {
			// The tail was the whole transcript: there is nothing newer to replay.
			return
		}
		for resp, err := range ListLogChunksByTask(ctx, l.client, l.request(l.follow)) {
			if err != nil {
				yield(nil, err)
				return
			}
			if !yieldChunks(yield, resp) {
				return
			}
			l.advance(resp)
		}
	}
}

// Follow returns an iterator over the chunks appended after the point History
// reached, polling every interval until ctx is cancelled — which ends the
// iteration without an error, since a cancelled follow is how `gritz logs -f`
// exits.
//
// Each poll drains everything newer than the cursor before sleeping again, so a
// run that logs more than a page's worth between polls can't fall behind. The
// cursor only ever advances onto a non-empty next_page_token: holding the old
// one is what keeps an empty response from re-opening at the tail and replaying
// the transcript.
//
// Follow is meant to run after History, whose walk leaves the cursor at the
// tail. Called on a task that has not shipped a chunk yet there is no cursor to
// poll from, so it re-opens at the tail (yielding the transcript, which is empty
// until the first chunk lands) until one exists.
//
// Each chunk is yielded as (chunk, nil). On an RPC error the iterator yields
// (nil, err) once and stops.
func (l *TaskLog) Follow(ctx context.Context, interval time.Duration) iter.Seq2[*gritzv1.LogChunk, error] {
	return func(yield func(*gritzv1.LogChunk, error) bool) {
		for {
			if !common.SleepContext(ctx, interval) {
				return
			}
			if l.follow == "" {
				// Nothing has been yielded yet (an empty tail page is what leaves the
				// cursor empty), so re-opening at the tail cannot duplicate output.
				for chunk, err := range l.History(ctx) {
					if !yield(chunk, err) || err != nil {
						return
					}
				}
				continue
			}
			for resp, err := range ListLogChunksByTask(ctx, l.client, l.request(l.follow)) {
				if err != nil {
					yield(nil, err)
					return
				}
				if !yieldChunks(yield, resp) {
					return
				}
				l.advance(resp)
			}
		}
	}
}

// advance moves the follow cursor onto a page that has been yielded in full.
// An empty next_page_token leaves it where it was: the token is always
// populated on a non-empty page, and re-opening at the tail on one that came
// back empty would replay chunks the caller has already seen.
func (l *TaskLog) advance(resp *gritzv1.ListLogChunksByTaskResponse) {
	if next := resp.GetNextPageToken(); next != "" {
		l.follow = next
	}
}

// list fetches a single page of the task's log at token.
func (l *TaskLog) list(ctx context.Context, token string) (*gritzv1.ListLogChunksByTaskResponse, error) {
	return l.client.ListLogChunksByTask(ctx, l.request(token))
}

// request builds a page request at token for the task.
func (l *TaskLog) request(token string) *gritzv1.ListLogChunksByTaskRequest {
	return &gritzv1.ListLogChunksByTaskRequest{
		TaskId:    l.taskID,
		PageSize:  l.pageSize,
		PageToken: token,
	}
}

// yieldChunks passes a page's chunks on in transcript order, reporting whether
// the consumer wants more.
func yieldChunks(yield func(*gritzv1.LogChunk, error) bool, resp *gritzv1.ListLogChunksByTaskResponse) bool {
	for _, chunk := range resp.GetChunks() {
		if !yield(chunk, nil) {
			return false
		}
	}
	return true
}

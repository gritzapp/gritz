package apiserver

import (
	"fmt"
	"testing"

	"connectrpc.com/connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// TestUploadLogs_NonReportIgnored verifies that the only log channel left on the
// wire is the agent's report tool (`llm`). The logs table is gone, so non-report
// entries (formerly audit/info/error rows) no longer have a home and are
// silently dropped — they must not become events.
func TestUploadLogs_NonReportIgnored(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)
	taskResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Task with Logs",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act
	_, err = srv.UploadLogs(ctx, &gritzv1.UploadLogsRequest{
		TaskId: taskResp.Task.Id,
		Entries: []*gritzv1.LogEntry{
			{Type: "info", Content: "First log entry"},
			{Type: "error", Content: "Second log entry"},
		},
	})
	assert.NilError(t, err)

	// Assert - no report (or other) events were created from the dropped entries.
	events, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{TaskId: taskResp.Task.Id})
	assert.NilError(t, err)
	for _, e := range events.Events {
		assert.Assert(t, e.GetReport() == nil, "non-report entries must not become report events")
	}
}

func TestUploadLogs_ReportBecomesEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)
	taskResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Task with report",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act - the agent's report tool uploads an `llm` entry.
	_, err = srv.UploadLogs(ctx, &gritzv1.UploadLogsRequest{
		TaskId: taskResp.Task.Id,
		Entries: []*gritzv1.LogEntry{
			{Type: "llm", Content: "Opened PR #952"},
		},
	})
	assert.NilError(t, err)

	// Assert - the report is a from-agent report event.
	events, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{TaskId: taskResp.Task.Id})
	assert.NilError(t, err)
	var reports []*gritzv1.ReportPayload
	for _, e := range events.Events {
		if r := e.GetReport(); r != nil {
			assert.Equal(t, e.Wake, false)
			reports = append(reports, r)
		}
	}
	assert.Equal(t, len(reports), 1)
	assert.Equal(t, reports[0].Content, "Opened PR #952")
}

func TestUploadLogs_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxB := createCtx(t, orgB)
	taskResp, err := srv.CreateTask(ctxA, &gritzv1.CreateTaskRequest{
		Name:      "User A's Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act
	_, err = srv.UploadLogs(ctxB, &gritzv1.UploadLogsRequest{
		TaskId: taskResp.Task.Id,
		Entries: []*gritzv1.LogEntry{
			{Type: "llm", Content: "Sneaky report"},
		},
	})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

// TestAppendLogChunk_RoundTrip is the end-to-end shape of the feature: the
// driver writes chunks with its task token, a user reads them back, and the
// concatenated data is the transcript byte-for-byte — including bytes that are
// not valid UTF-8, since chunks are opaque to the server.
func TestAppendLogChunk_RoundTrip(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)
	agentCtx := scopedCtx(t, org, taskScopes(taskID))

	// Act - ship the transcript in chunks, as the driver's FIFO sender does.
	for _, data := range [][]byte{
		[]byte("==== run version=1 ====\n"),
		{0x00, 0xff, 0x80},
		[]byte("agent stopped\n"),
	} {
		_, err := srv.AppendLogChunk(agentCtx, &gritzv1.AppendLogChunkRequest{
			TaskId: taskID, Version: 1, Data: data,
		})
		assert.NilError(t, err)
	}

	// Assert - a user read returns the chunks oldest-first, so their data
	// concatenates straight into transcript order.
	resp, err := srv.ListLogChunksByTask(ctx, &gritzv1.ListLogChunksByTaskRequest{TaskId: taskID})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(resp.Chunks, 3))
	var transcript []byte
	for _, c := range resp.Chunks {
		assert.Equal(t, c.TaskId, taskID)
		assert.Equal(t, c.Version, int64(1))
		assert.Assert(t, c.CreatedAt != nil)
		transcript = append(transcript, c.Data...)
	}
	assert.DeepEqual(t, transcript, []byte("==== run version=1 ====\n\x00\xff\x80agent stopped\n"))
}

// A chunk must carry bytes and must not exceed the abuse ceiling; both are
// caller errors, not internal ones.
func TestAppendLogChunk_Bounds(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)

	tests := []struct {
		name string
		data []byte
		err  string
	}{
		{"empty", nil, "data is required"},
		{"too large", make([]byte, maxLogChunkSize+1), "at most 1048576 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			_, err := srv.AppendLogChunk(ctx, &gritzv1.AppendLogChunkRequest{
				TaskId: taskID, Version: 1, Data: tt.data,
			})

			// Assert
			assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
			assert.ErrorContains(t, err, tt.err)
		})
	}

	// A chunk exactly at the cap is accepted — the check is an inclusive ceiling.
	_, err := srv.AppendLogChunk(ctx, &gritzv1.AppendLogChunkRequest{
		TaskId: taskID, Version: 1, Data: make([]byte, maxLogChunkSize),
	})
	assert.NilError(t, err)
}

// The version stamp rides through untouched, including the 0 the driver uses
// for the pre-run preamble it buffers before fetching its task.
func TestAppendLogChunk_Version(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)

	// Act
	for _, version := range []int64{0, 2} {
		_, err := srv.AppendLogChunk(ctx, &gritzv1.AppendLogChunkRequest{
			TaskId: taskID, Version: version, Data: []byte("x"),
		})
		assert.NilError(t, err)
	}

	// Assert
	resp, err := srv.ListLogChunksByTask(ctx, &gritzv1.ListLogChunksByTaskRequest{TaskId: taskID})
	assert.NilError(t, err)
	assert.DeepEqual(t, testx.ExtractField(resp.Chunks, "Version"), []int64{0, 2})
}

// Another org's caller can neither append to nor read a task's log, even
// holding the admin wildcard: both handlers are org-scoped.
func TestAppendLogChunk_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := orgWithWorkspace(t, srv)
	ctxA := createCtx(t, orgA)
	taskID := createTestTask(t, srv, ctxA)
	ctxB := createCtx(t, orgWithWorkspace(t, srv))
	_, err := srv.AppendLogChunk(ctxA, &gritzv1.AppendLogChunkRequest{
		TaskId: taskID, Version: 1, Data: []byte("secret\n"),
	})
	assert.NilError(t, err)

	// Act - org B tries to write to, then read, org A's log.
	_, err = srv.AppendLogChunk(ctxB, &gritzv1.AppendLogChunkRequest{
		TaskId: taskID, Version: 1, Data: []byte("sneaky\n"),
	})
	assert.ErrorContains(t, err, "not found")
	resp, err := srv.ListLogChunksByTask(ctxB, &gritzv1.ListLogChunksByTaskRequest{TaskId: taskID})

	// Assert - the read is org-scoped, so it leaks nothing.
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(resp.Chunks, 0))
}

// A task token holds read scope on its own task, so the driver (and the CLI
// running as one) can read back what it shipped.
func TestListLogChunksByTask_TaskToken(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)
	agentCtx := scopedCtx(t, org, taskScopes(taskID))
	_, err := srv.AppendLogChunk(agentCtx, &gritzv1.AppendLogChunkRequest{
		TaskId: taskID, Version: 1, Data: []byte("hello\n"),
	})
	assert.NilError(t, err)

	// Act
	resp, err := srv.ListLogChunksByTask(agentCtx, &gritzv1.ListLogChunksByTaskRequest{TaskId: taskID})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(resp.Chunks, 1))
	assert.DeepEqual(t, resp.Chunks[0].Data, []byte("hello\n"))
}

// An empty token opens at the tail (the end of the log, what a post-mortem
// reader wants first) and prev_page_token walks back through older history,
// each page ascending — so prepending pages reconstructs the transcript.
func TestListLogChunksByTask_Paged(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)
	var want []byte
	for i := range 5 {
		line := fmt.Appendf(nil, "line %d\n", i)
		_, err := srv.AppendLogChunk(ctx, &gritzv1.AppendLogChunkRequest{
			TaskId: taskID, Version: 1, Data: line,
		})
		assert.NilError(t, err)
		want = append(want, line...)
	}

	// Act - the newest page is the last page-size chunks, with both tokens set:
	// older history exists, and the live-follow token is always populated.
	const pageSize = 2
	newest, err := srv.ListLogChunksByTask(ctx, &gritzv1.ListLogChunksByTaskRequest{
		TaskId: taskID, PageSize: pageSize,
	})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(newest.Chunks, pageSize))
	assert.Assert(t, newest.PrevPageToken != "")
	assert.Assert(t, newest.NextPageToken != "")

	// Assert - walking prev to the oldest chunk rebuilds the whole transcript,
	// and More agrees with prev_page_token on this scroll-back walk.
	var got []byte
	for _, c := range newest.Chunks {
		got = append(got, c.Data...)
	}
	resp := newest
	for resp.PrevPageToken != "" {
		assert.Equal(t, resp.More, true)
		resp, err = srv.ListLogChunksByTask(ctx, &gritzv1.ListLogChunksByTaskRequest{
			TaskId: taskID, PageSize: pageSize, PageToken: resp.PrevPageToken,
		})
		assert.NilError(t, err)
		assert.Assert(t, len(resp.Chunks) > 0)
		var older []byte
		for _, c := range resp.Chunks {
			older = append(older, c.Data...)
		}
		got = append(older, got...)
	}
	assert.Equal(t, resp.More, false)
	assert.DeepEqual(t, got, want)
}

// next_page_token walks the other way — toward newer rows — so a reader can
// follow a growing log from a saved position. This is the direction that
// distinguishes the two tokens: swapping them would return old chunks here.
func TestListLogChunksByTask_Follow(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)
	_, err := srv.AppendLogChunk(ctx, &gritzv1.AppendLogChunkRequest{
		TaskId: taskID, Version: 1, Data: []byte("first\n"),
	})
	assert.NilError(t, err)
	tail, err := srv.ListLogChunksByTask(ctx, &gritzv1.ListLogChunksByTaskRequest{TaskId: taskID})
	assert.NilError(t, err)

	// Act - a poll with nothing new echoes the token and returns no chunks.
	poll, err := srv.ListLogChunksByTask(ctx, &gritzv1.ListLogChunksByTaskRequest{
		TaskId: taskID, PageToken: tail.NextPageToken,
	})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(poll.Chunks, 0))
	assert.Equal(t, poll.More, false)

	// Act - once the driver appends, the same token yields only the new bytes.
	_, err = srv.AppendLogChunk(ctx, &gritzv1.AppendLogChunkRequest{
		TaskId: taskID, Version: 1, Data: []byte("second\n"),
	})
	assert.NilError(t, err)
	poll, err = srv.ListLogChunksByTask(ctx, &gritzv1.ListLogChunksByTaskRequest{
		TaskId: taskID, PageToken: poll.NextPageToken,
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(poll.Chunks, 1))
	assert.DeepEqual(t, poll.Chunks[0].Data, []byte("second\n"))
}

// A page size outside the store's bounds, or a token that did not come from the
// server, is a caller error rather than an internal one.
func TestListLogChunksByTask_BadRequest(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)

	tests := []struct {
		name     string
		pageSize int32
		token    string
	}{
		{"page size over max", 201, ""},
		{"negative page size", -1, ""},
		{"undecodable token", 0, "not-a-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			_, err := srv.ListLogChunksByTask(ctx, &gritzv1.ListLogChunksByTaskRequest{
				TaskId: taskID, PageSize: tt.pageSize, PageToken: tt.token,
			})

			// Assert
			assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
		})
	}
}

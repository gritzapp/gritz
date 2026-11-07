package api_test

import (
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/testx"
	"google.golang.org/protobuf/testing/protocmp"
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
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task with Logs", Draft: true})

	// Act
	_, err := h.Org.Client.UploadLogs(t.Context(), &gritzv1.UploadLogsRequest{
		TaskId: task.ID,
		Entries: []*gritzv1.LogEntry{
			{Type: "info", Content: "First log entry"},
			{Type: "error", Content: "Second log entry"},
		},
	})
	assert.NilError(t, err)

	// Assert - no report (or other) events were created from the dropped entries.
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{TaskId: task.ID})
	for _, e := range events {
		assert.Assert(t, e.GetReport() == nil, "non-report entries must not become report events")
	}
}

func TestUploadLogs_ReportBecomesEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task with report", Draft: true})

	// Act - the agent's report tool uploads an `llm` entry.
	_, err := h.Org.Client.UploadLogs(t.Context(), &gritzv1.UploadLogsRequest{
		TaskId: task.ID,
		Entries: []*gritzv1.LogEntry{
			{Type: "llm", Content: "Opened PR #952"},
		},
	})
	assert.NilError(t, err)

	// Assert - the report is a from-agent report event.
	assert.DeepEqual(t,
		h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{TaskId: task.ID, Types: []string{"report"}}),
		[]*gritzv1.Event{{
			TaskId:  task.ID,
			Payload: &gritzv1.Event_Report{Report: &gritzv1.ReportPayload{Content: "Opened PR #952"}},
		}},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.Event{}, "id", "created_at"),
	)
}

func TestUploadLogs_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "User A's Task", Draft: true})
	other := h.Server.CreateOrg()

	// Act
	_, err := other.Client.UploadLogs(t.Context(), &gritzv1.UploadLogsRequest{
		TaskId: task.ID,
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
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	token := h.Org.CreateTaskToken(&gritzv1.CreateTaskTokenRequest{TaskId: task.ID})

	// Act - ship the transcript in chunks, as the driver's FIFO sender does.
	for _, data := range [][]byte{
		[]byte("==== run version=1 ====\n"),
		{0x00, 0xff, 0x80},
		[]byte("agent stopped\n"),
	} {
		_, err := token.Client.AppendLogChunk(t.Context(), &gritzv1.AppendLogChunkRequest{
			TaskId:  task.ID,
			Version: 1,
			Data:    data,
		})
		assert.NilError(t, err)
	}

	// Assert - a user read returns the chunks oldest-first, so their data
	// concatenates straight into transcript order.
	assert.DeepEqual(t,
		h.Org.ListLogChunksByTask(&gritzv1.ListLogChunksByTaskRequest{TaskId: task.ID}),
		[]*gritzv1.LogChunk{
			{TaskId: task.ID, Version: 1, Data: []byte("==== run version=1 ====\n")},
			{TaskId: task.ID, Version: 1, Data: []byte{0x00, 0xff, 0x80}},
			{TaskId: task.ID, Version: 1, Data: []byte("agent stopped\n")},
		},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.LogChunk{}, "id", "created_at"),
	)
}

// A chunk must carry bytes and must not exceed the abuse ceiling; both are
// caller errors, not internal ones.
func TestAppendLogChunk_Bounds(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	const maxLogChunkSize = 1 << 20 // the handler's 1 MiB ceiling

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
			_, err := h.Org.Client.AppendLogChunk(t.Context(), &gritzv1.AppendLogChunkRequest{
				TaskId:  task.ID,
				Version: 1,
				Data:    tt.data,
			})

			// Assert
			assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
			assert.ErrorContains(t, err, tt.err)
		})
	}

	// A chunk exactly at the cap is accepted — the check is an inclusive ceiling.
	_, err := h.Org.Client.AppendLogChunk(t.Context(), &gritzv1.AppendLogChunkRequest{
		TaskId:  task.ID,
		Version: 1,
		Data:    make([]byte, maxLogChunkSize),
	})
	assert.NilError(t, err)
}

// The version stamp rides through untouched, including the 0 the driver uses
// for the pre-run preamble it buffers before fetching its task.
func TestAppendLogChunk_Version(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})

	// Act
	for _, version := range []int64{0, 2} {
		_, err := h.Org.Client.AppendLogChunk(t.Context(), &gritzv1.AppendLogChunkRequest{
			TaskId:  task.ID,
			Version: version,
			Data:    []byte("x"),
		})
		assert.NilError(t, err)
	}

	// Assert
	chunks := h.Org.ListLogChunksByTask(&gritzv1.ListLogChunksByTaskRequest{TaskId: task.ID})
	assert.DeepEqual(t, testx.ExtractField(chunks, "Version"), []int64{0, 2})
}

// Another org's caller can neither append to nor read a task's log, even
// holding the admin wildcard: both handlers are org-scoped.
func TestAppendLogChunk_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.AppendLogChunk(&gritzv1.AppendLogChunkRequest{
		TaskId:  task.ID,
		Version: 1,
		Data:    []byte("secret\n"),
	})
	other := h.Server.CreateOrg()

	// Act & Assert - org B can't write to org A's log.
	_, err := other.Client.AppendLogChunk(t.Context(), &gritzv1.AppendLogChunkRequest{
		TaskId:  task.ID,
		Version: 1,
		Data:    []byte("sneaky\n"),
	})
	assert.ErrorContains(t, err, "not found")

	// Act & Assert - the read is org-scoped, so it leaks nothing.
	resp, err := other.Client.ListLogChunksByTask(t.Context(), &gritzv1.ListLogChunksByTaskRequest{
		TaskId: task.ID,
	})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(resp.Chunks, 0))
}

// A task token holds read scope on its own task, so the driver (and the CLI
// running as one) can read back what it shipped.
func TestListLogChunksByTask_TaskToken(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	token := h.Org.CreateTaskToken(&gritzv1.CreateTaskTokenRequest{TaskId: task.ID})
	h.Org.AppendLogChunk(&gritzv1.AppendLogChunkRequest{
		TaskId:  task.ID,
		Version: 1,
		Data:    []byte("hello\n"),
	})

	// Act
	resp, err := token.Client.ListLogChunksByTask(t.Context(), &gritzv1.ListLogChunksByTaskRequest{
		TaskId: task.ID,
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t,
		resp.Chunks,
		[]*gritzv1.LogChunk{{TaskId: task.ID, Version: 1, Data: []byte("hello\n")}},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.LogChunk{}, "id", "created_at"),
	)
}

// An empty token opens at the tail (the end of the log, what a post-mortem
// reader wants first) and prev_page_token walks back through older history,
// each page ascending — so prepending pages reconstructs the transcript.
func TestListLogChunksByTask_Paged(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	var want []byte
	for i := range 5 {
		line := fmt.Appendf(nil, "line %d\n", i)
		h.Org.AppendLogChunk(&gritzv1.AppendLogChunkRequest{
			TaskId:  task.ID,
			Version: 1,
			Data:    line,
		})
		want = append(want, line...)
	}

	// Act - the newest page is the last page-size chunks, with both tokens set:
	// older history exists, and the live-follow token is always populated.
	const pageSize = 2
	newest, err := h.Org.Client.ListLogChunksByTask(t.Context(), &gritzv1.ListLogChunksByTaskRequest{
		TaskId:   task.ID,
		PageSize: pageSize,
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
		resp, err = h.Org.Client.ListLogChunksByTask(t.Context(), &gritzv1.ListLogChunksByTaskRequest{
			TaskId:    task.ID,
			PageSize:  pageSize,
			PageToken: resp.PrevPageToken,
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
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.AppendLogChunk(&gritzv1.AppendLogChunkRequest{
		TaskId:  task.ID,
		Version: 1,
		Data:    []byte("first\n"),
	})
	tail, err := h.Org.Client.ListLogChunksByTask(t.Context(), &gritzv1.ListLogChunksByTaskRequest{
		TaskId: task.ID,
	})
	assert.NilError(t, err)

	// Act - a poll with nothing new echoes the token and returns no chunks.
	poll, err := h.Org.Client.ListLogChunksByTask(t.Context(), &gritzv1.ListLogChunksByTaskRequest{
		TaskId:    task.ID,
		PageToken: tail.NextPageToken,
	})
	assert.NilError(t, err)
	assert.DeepEqual(t,
		poll,
		&gritzv1.ListLogChunksByTaskResponse{NextPageToken: tail.NextPageToken},
		protocmp.Transform(),
	)

	// Act - once the driver appends, the same token yields only the new bytes.
	h.Org.AppendLogChunk(&gritzv1.AppendLogChunkRequest{
		TaskId:  task.ID,
		Version: 1,
		Data:    []byte("second\n"),
	})
	poll, err = h.Org.Client.ListLogChunksByTask(t.Context(), &gritzv1.ListLogChunksByTaskRequest{
		TaskId:    task.ID,
		PageToken: poll.NextPageToken,
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t,
		poll.Chunks,
		[]*gritzv1.LogChunk{{TaskId: task.ID, Version: 1, Data: []byte("second\n")}},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.LogChunk{}, "id", "created_at"),
	)
}

// A page size outside the store's bounds, or a token that did not come from the
// server, is a caller error rather than an internal one.
func TestListLogChunksByTask_BadRequest(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})

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
			_, err := h.Org.Client.ListLogChunksByTask(t.Context(), &gritzv1.ListLogChunksByTaskRequest{
				TaskId:    task.ID,
				PageSize:  tt.pageSize,
				PageToken: tt.token,
			})

			// Assert
			assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
		})
	}
}

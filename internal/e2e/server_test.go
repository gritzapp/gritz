package e2e_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/e2e"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestUpdateTask(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, nil)
	task := h.Org.CreateTask()

	_, err := h.Org.Client.UpdateTask(t.Context(), &gritzv1.UpdateTaskRequest{
		Id:   task.ID,
		Name: "Updated Name",
	})

	assert.NilError(t, err)
	resp, err := h.Org.Client.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: task.ID})
	assert.NilError(t, err)
	assert.Equal(t, resp.Task.Name, "Updated Name")
}

func TestTaskScope_GetTask_OwnAllowed_OtherDenied(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, nil)
	taskA := h.Org.CreateTask()
	taskB := h.Org.CreateTask()
	tokenResp, err := h.Org.Client.CreateTaskToken(t.Context(), &gritzv1.CreateTaskTokenRequest{TaskId: taskA.ID})
	assert.NilError(t, err)
	own := h.Server.Client(tokenResp.Token)

	_, errA := own.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: taskA.ID})
	_, errB := own.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: taskB.ID})

	assert.NilError(t, errA)
	assert.Equal(t, connect.CodeOf(errB), connect.CodePermissionDenied)
}

func TestGetTask_Permissions(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, nil)
	orgB := h.Server.CreateOrg()
	task := h.Org.CreateTask()

	_, errA := h.Org.Client.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: task.ID})
	_, errB := orgB.Client.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: task.ID})

	assert.NilError(t, errA)
	assert.ErrorContains(t, errB, "not found")
}

func TestCreateTaskToken_Denied(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, nil)
	task := h.Org.CreateTask()
	// A key with no scopes lacks the task_token.create capability.
	scopeless := h.Org.CreateKey(nil)

	_, err := scopeless.CreateTaskToken(t.Context(), &gritzv1.CreateTaskTokenRequest{TaskId: task.ID})

	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

func TestDeleteKey(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, nil)
	createResp, err := h.Org.Client.CreateKey(t.Context(), &gritzv1.CreateKeyRequest{Name: "to-delete"})
	assert.NilError(t, err)
	key := h.Server.Client(createResp.RawToken)
	_, err = key.ListKeys(t.Context(), &gritzv1.ListKeysRequest{})
	assert.NilError(t, err)

	_, err = h.Org.Client.DeleteKey(t.Context(), &gritzv1.DeleteKeyRequest{Id: createResp.Key.Id})
	assert.NilError(t, err)

	listResp, err := h.Org.Client.ListKeys(t.Context(), &gritzv1.ListKeysRequest{})
	assert.NilError(t, err)
	assert.Assert(t, !slices.ContainsFunc(listResp.Keys, func(k *gritzv1.Key) bool { return k.Id == createResp.Key.Id }))
	_, err = key.ListKeys(t.Context(), &gritzv1.ListKeysRequest{})
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnauthenticated)
}

func TestCreateLink_AppendsLinkEvent(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, nil)
	task := h.Org.CreateTask()

	linkResp, err := h.Org.Client.CreateLink(t.Context(), &gritzv1.CreateLinkRequest{
		TaskId:    task.ID,
		Relevance: "Related PR",
		Url:       "https://github.com/example/repo/pull/123",
		Title:     "Fix bug",
		Subscribe: true,
	})
	assert.NilError(t, err)

	// A link event mirroring the task_links row is appended to the task's
	// stream (the timeline source of truth; task_links is the projection).
	event := h.Org.WaitEvent(task.ID, func(e *gritzv1.Event) bool { return e.GetLink() != nil })
	assert.DeepEqual(t,
		event.GetLink(),
		&gritzv1.LinkPayload{
			LinkId:    linkResp.Link.Id,
			Relevance: "Related PR",
			Url:       "https://github.com/example/repo/pull/123",
			Title:     "Fix bug",
			Subscribe: true,
		},
		protocmp.Transform(),
	)
	// The link event is about-task, not to-agent, so it does not wake the task.
	assert.Equal(t, event.Wake, false)
}

func TestMock(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, nil)
	task := h.Org.CreateTask()
	m := &gritzclient.ClientMock{
		UpdateTaskFunc: func(context.Context, *gritzv1.UpdateTaskRequest) (*gritzv1.UpdateTaskResponse, error) {
			return nil, connect.NewError(connect.CodeInternal, errors.New("e2e: rejected"))
		},
	}
	h.Server.Mock(m)

	_, err := h.Org.Client.UpdateTask(t.Context(), &gritzv1.UpdateTaskRequest{Id: task.ID, Name: "mocked"})
	assert.Equal(t, connect.CodeOf(err), connect.CodeInternal)
	calls := m.UpdateTaskCalls()
	assert.Assert(t, cmp.Len(calls, 1))
	assert.Equal(t, calls[0].UpdateTaskRequest.Name, "mocked")
	// Methods left nil go to the real server.
	h.Org.WaitTask(task.ID, func(got *gritzv1.Task) bool { return got.Name == "" })
	// The mock answers only after auth: a bad token is rejected before it.
	bad := h.Server.Client("xat_bad")
	_, err = bad.UpdateTask(t.Context(), &gritzv1.UpdateTaskRequest{Id: task.ID, Name: "bad"})
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnauthenticated)
	assert.Assert(t, cmp.Len(m.UpdateTaskCalls(), 1))

	h.Server.Mock(nil)

	_, err = h.Org.Client.UpdateTask(t.Context(), &gritzv1.UpdateTaskRequest{Id: task.ID, Name: "real"})
	assert.NilError(t, err)
	h.Org.WaitTask(task.ID, func(got *gritzv1.Task) bool { return got.Name == "real" })
}

func TestAddMember(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	task := h.Org.CreateTask()

	member := h.Org.AddMember(other)

	_, err := member.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: task.ID})
	assert.NilError(t, err)
	_, err = other.Client.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: task.ID})
	assert.ErrorContains(t, err, "not found")
}

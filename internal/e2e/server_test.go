package e2e_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/e2e"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestMock(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
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

func TestAddOrgMember(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})

	member := h.Org.AddOrgMember(&gritzv1.AddOrgMemberRequest{Email: other.Owner.Email})

	// The member sees the org's task; its owner, acting in its own org, doesn't.
	_, err := member.Client.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: task.ID})
	assert.NilError(t, err)
	_, err = other.Client.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: task.ID})
	assert.ErrorContains(t, err, "not found")
}

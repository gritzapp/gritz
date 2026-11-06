package api_test

import (
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/atlassian"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestCreateOrg(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	resp, err := h.Org.Client.CreateOrg(t.Context(), &gritzv1.CreateOrgRequest{
		Name: "my-org",
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, resp.Org.Name, "my-org")
	assert.Assert(t, resp.Org.Id != 0)
}

func TestListOrgs(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	h.Org.CreateOrg(&gritzv1.CreateOrgRequest{Name: "org-1"})
	h.Org.CreateOrg(&gritzv1.CreateOrgRequest{Name: "org-2"})

	// Act
	resp, err := h.Org.Client.ListOrgs(t.Context(), &gritzv1.ListOrgsRequest{})

	// Assert - the two created and the owner's default org.
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(resp.Orgs, 3))
}

func TestDeleteOrg(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	created := h.Org.CreateOrg(&gritzv1.CreateOrgRequest{Name: "to-delete"})

	// Act
	_, err := h.Org.Client.DeleteOrg(t.Context(), &gritzv1.DeleteOrgRequest{Id: created.ID})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(h.Org.ListOrgs(&gritzv1.ListOrgsRequest{}), 1))
}

func TestDeleteOrg_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	created := h.Org.CreateOrg(&gritzv1.CreateOrgRequest{Name: "user-a-org"})

	// Act
	_, err := other.Client.DeleteOrg(t.Context(), &gritzv1.DeleteOrgRequest{Id: created.ID})

	// Assert
	assert.ErrorContains(t, err, "only the org owner can delete it")
}

func TestDeleteOrg_DefaultOrg(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	_, err := h.Org.Client.DeleteOrg(t.Context(), &gritzv1.DeleteOrgRequest{Id: h.Org.ID})

	// Assert
	assert.ErrorContains(t, err, "cannot delete your default org")
}

func TestAddAndListOrgMembers(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()

	// Act
	resp, err := h.Org.Client.AddOrgMember(t.Context(), &gritzv1.AddOrgMemberRequest{
		Email: other.Owner.Email,
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, resp.Member.Role, "member")
	assert.Assert(t, cmp.Len(h.Org.ListOrgMembers(&gritzv1.ListOrgMembersRequest{}), 2))
}

func TestDeleteOrg_RefusedWhileTasksUnarchived(t *testing.T) {
	t.Parallel()
	// Arrange - a stopped task, not yet archived.
	h := e2e.New(t, nil)
	created := h.Org.CreateOrg(&gritzv1.CreateOrgRequest{Name: "has-tasks"})
	task := created.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	created.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	created.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: task.ID, Event: "started", Version: 1},
			{TaskId: task.ID, Event: "stopped", Version: 1},
		},
	})

	// Act & Assert - refused while the task is unarchived.
	_, err := h.Org.Client.DeleteOrg(t.Context(), &gritzv1.DeleteOrgRequest{Id: created.ID})
	assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)

	// Act & Assert - allowed once it is archived.
	created.ArchiveTask(&gritzv1.ArchiveTaskRequest{Id: task.ID})
	_, err = h.Org.Client.DeleteOrg(t.Context(), &gritzv1.DeleteOrgRequest{Id: created.ID})
	assert.NilError(t, err)
	for _, o := range h.Org.ListOrgs(&gritzv1.ListOrgsRequest{}) {
		assert.Assert(t, o.Id != created.ID)
	}
}

func TestDeleteOrg_DisablesSchedules(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	created := h.Org.CreateOrg(&gritzv1.CreateOrgRequest{Name: "has-schedules"})
	sched := created.CreateSchedule(&gritzv1.CreateScheduleRequest{CronExpr: "0 9 * * *", Enabled: true})

	// Act
	_, err := h.Org.Client.DeleteOrg(t.Context(), &gritzv1.DeleteOrgRequest{Id: created.ID})

	// Assert - read from the store: the org is archived, so no RPC reaches it.
	assert.NilError(t, err)
	got, err := h.Server.Store.GetSchedule(t.Context(), nil, sched.Id, created.ID)
	assert.NilError(t, err)
	assert.Assert(t, !got.Enabled)
	assert.Assert(t, got.NextRunAt == nil)
}

func TestDeleteOrg_RejectsAtlassianWebhooks(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	created := h.Org.CreateOrg(&gritzv1.CreateOrgRequest{Name: "has-webhook"})
	webhook := created.GenerateAtlassianWebhookSecret(&gritzv1.GenerateAtlassianWebhookSecretRequest{})
	_, err := h.Org.Client.DeleteOrg(t.Context(), &gritzv1.DeleteOrgRequest{Id: created.ID})
	assert.NilError(t, err)

	// Act
	resp := webhook.Send(&atlassian.WebhookPayload{})

	// Assert
	assert.Equal(t, resp.StatusCode, http.StatusForbidden)
	assert.Assert(t, cmp.Contains(resp.Body, "webhook not configured"))
}

func TestDeleteOrg_RejectsKey(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	created := h.Org.CreateOrg(&gritzv1.CreateOrgRequest{Name: "has-key"})
	key := created.CreateKey(&gritzv1.CreateKeyRequest{Name: "k"})
	_, err := h.Org.Client.DeleteOrg(t.Context(), &gritzv1.DeleteOrgRequest{Id: created.ID})
	assert.NilError(t, err)

	// Act
	_, err = key.Client.ListTasks(t.Context(), &gritzv1.ListTasksRequest{})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnauthenticated)
}

func TestGetOrgSettings_GitHubInstallationID(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	// SetOrgGitHubInstallation verifies the installation with GitHub, which the
	// harness has no client for, so the installation is written to the store.
	err := h.Server.Store.SetOrgGitHubInstallation(t.Context(), nil, h.Org.ID, 12345)
	assert.NilError(t, err)

	// Act
	resp, err := h.Org.Client.GetOrgSettings(t.Context(), &gritzv1.GetOrgSettingsRequest{})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, resp.GithubInstallationId, int64(12345))
}

func TestGetOrgSettings_NoGitHubInstallation(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	resp, err := h.Org.Client.GetOrgSettings(t.Context(), &gritzv1.GetOrgSettingsRequest{})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, resp.GithubInstallationId, int64(0))
}

func TestRemoveOrgMember(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	member := h.Org.AddOrgMember(&gritzv1.AddOrgMemberRequest{Email: other.Owner.Email})

	// Act
	_, err := h.Org.Client.RemoveOrgMember(t.Context(), &gritzv1.RemoveOrgMemberRequest{
		UserId: member.UserID,
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(h.Org.ListOrgMembers(&gritzv1.ListOrgMembersRequest{}), 1))
}

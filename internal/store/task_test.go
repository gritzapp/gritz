package store_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"github.com/gritzapp/gritz/internal/x/pagination"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestClearShellSession(t *testing.T) {
	t.Parallel()
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	orgAgent := teststore.CreateAgent(t, s, org, nil)

	task := &model.Task{
		Name:         "shell-task",
		Runner:       "r",
		Workspace:    "w",
		Status:       model.TaskStatusCompleted,
		OrgID:        org.OrgID,
		AgentID:      orgAgent.ID,
		ShellSession: "sess-1",
	}
	assert.NilError(t, s.CreateTask(t.Context(), nil, task))

	// Clearing with the matching session + org empties the field.
	assert.NilError(t, s.ClearShellSession(t.Context(), nil, "sess-1", org.OrgID))
	got, err := s.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, got.ShellSession, "")
}

func TestClearShellSession_WrongOrgNoOp(t *testing.T) {
	t.Parallel()
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	orgAgent := teststore.CreateAgent(t, s, org, nil)
	other := teststore.CreateOrg(t, s, nil)

	task := &model.Task{
		Name:         "shell-task",
		Runner:       "r",
		Workspace:    "w",
		Status:       model.TaskStatusCompleted,
		OrgID:        org.OrgID,
		AgentID:      orgAgent.ID,
		ShellSession: "sess-2",
	}
	assert.NilError(t, s.CreateTask(t.Context(), nil, task))

	// Clearing under a different org must not touch the task.
	assert.NilError(t, s.ClearShellSession(t.Context(), nil, "sess-2", other.OrgID))
	got, err := s.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, got.ShellSession, "sess-2")
}

func TestCreateTask_AgentID(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	agent := teststore.CreateAgent(t, s, org, nil)
	task := &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   agent.ID,
	}

	// Act
	assert.NilError(t, s.CreateTask(t.Context(), nil, task))
	got, err := s.GetTask(t.Context(), nil, task.ID, org.OrgID)

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, got.AgentID, agent.ID)
}

func TestCreateTask_Variables(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	orgAgent := teststore.CreateAgent(t, s, org, nil)
	vars := map[string]string{"JIRA_ISSUE": "APP-142", "EMPTY": ""}

	// Act
	task := &model.Task{
		Runner:    "r",
		Workspace: "w",
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
		Variables: vars,
	}
	assert.NilError(t, s.CreateTask(t.Context(), nil, task))

	// Assert
	got, err := s.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.DeepEqual(t, got.Variables, vars)
}

func TestListTasksPage_ActiveOnly(t *testing.T) {
	t.Parallel()
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	orgAgent := teststore.CreateAgent(t, s, org, nil)

	// Interleave active and archived; keyset order is newest-created first.
	a := &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
	}
	assert.NilError(t, s.CreateTask(t.Context(), nil, a))
	assert.NilError(t, s.CreateTask(t.Context(), nil, &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
		Archived:  true,
	}))
	c := &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
	}
	assert.NilError(t, s.CreateTask(t.Context(), nil, c))
	assert.NilError(t, s.CreateTask(t.Context(), nil, &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
		Archived:  true,
	}))

	page, err := s.ListTasksPage(t.Context(), nil, store.ListTasksPageParams{OrgID: org.OrgID})
	assert.NilError(t, err)
	// Default (archived=false) yields only the active rows, newest first.
	assert.DeepEqual(t, testx.ExtractField(page.Items, "ID"), []int64{c.ID, a.ID})
	assert.Equal(t, page.NextToken, "")
}

func TestListTasksPage_IncludeArchived(t *testing.T) {
	t.Parallel()
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	orgAgent := teststore.CreateAgent(t, s, org, nil)

	a := &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
	}
	assert.NilError(t, s.CreateTask(t.Context(), nil, a))
	b := &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
		Archived:  true,
	}
	assert.NilError(t, s.CreateTask(t.Context(), nil, b))
	c := &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
	}
	assert.NilError(t, s.CreateTask(t.Context(), nil, c))
	d := &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
		Archived:  true,
	}
	assert.NilError(t, s.CreateTask(t.Context(), nil, d))

	page, err := s.ListTasksPage(t.Context(), nil, store.ListTasksPageParams{
		OrgID:    org.OrgID,
		Archived: true,
	})
	assert.NilError(t, err)
	// Active and archived are interleaved by the same created_at DESC, id DESC
	// keyset ordering — newest-created first, regardless of archived state.
	assert.DeepEqual(t, testx.ExtractField(page.Items, "ID"), []int64{d.ID, c.ID, b.ID, a.ID})
}

func TestListTasksPage_ArchivedKeysetPaging(t *testing.T) {
	t.Parallel()
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	orgAgent := teststore.CreateAgent(t, s, org, nil)

	a := &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
	}
	assert.NilError(t, s.CreateTask(t.Context(), nil, a))
	b := &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
		Archived:  true,
	}
	assert.NilError(t, s.CreateTask(t.Context(), nil, b))
	c := &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
	}
	assert.NilError(t, s.CreateTask(t.Context(), nil, c))

	// Page size 1 forces the keyset cursor to walk archived+active together.
	var got []int64
	token := ""
	for {
		page, err := s.ListTasksPage(t.Context(), nil, store.ListTasksPageParams{
			OrgID:     org.OrgID,
			PageSize:  1,
			PageToken: token,
			Archived:  true,
		})
		assert.NilError(t, err)
		got = append(got, testx.ExtractField(page.Items, "ID").([]int64)...)
		if page.NextToken == "" {
			break
		}
		token = page.NextToken
	}
	assert.DeepEqual(t, got, []int64{c.ID, b.ID, a.ID})
}

func TestListTasksPage_TokenBindsFilter(t *testing.T) {
	t.Parallel()
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	orgAgent := teststore.CreateAgent(t, s, org, nil)

	// Two archived-included rows so the first page mints a next token.
	assert.NilError(t, s.CreateTask(t.Context(), nil, &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
		Archived:  true,
	}))
	assert.NilError(t, s.CreateTask(t.Context(), nil, &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
		Archived:  true,
	}))

	archivedPage, err := s.ListTasksPage(t.Context(), nil, store.ListTasksPageParams{
		OrgID:    org.OrgID,
		PageSize: 1,
		Archived: true,
	})
	assert.NilError(t, err)
	assert.Assert(t, archivedPage.NextToken != "")

	// Replaying an archived-minted token under the active-only filter is rejected.
	_, err = s.ListTasksPage(t.Context(), nil, store.ListTasksPageParams{
		OrgID:     org.OrgID,
		PageSize:  1,
		PageToken: archivedPage.NextToken,
		Archived:  false,
	})
	assert.Assert(t, cmp.ErrorIs(err, pagination.ErrInvalidRequest))

	// And the reverse: an active-minted token replayed under archived=true.
	// Two active rows so the active-only first page also mints a next token.
	assert.NilError(t, s.CreateTask(t.Context(), nil, &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
	}))
	assert.NilError(t, s.CreateTask(t.Context(), nil, &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
	}))
	activePage, err := s.ListTasksPage(t.Context(), nil, store.ListTasksPageParams{
		OrgID:    org.OrgID,
		PageSize: 1,
	})
	assert.NilError(t, err)
	assert.Assert(t, activePage.NextToken != "")

	_, err = s.ListTasksPage(t.Context(), nil, store.ListTasksPageParams{
		OrgID:     org.OrgID,
		PageSize:  1,
		PageToken: activePage.NextToken,
		Archived:  true,
	})
	assert.Assert(t, cmp.ErrorIs(err, pagination.ErrInvalidRequest))
}

func TestListTasksPage_DefaultTokenWireCompatible(t *testing.T) {
	t.Parallel()
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	orgAgent := teststore.CreateAgent(t, s, org, nil)

	assert.NilError(t, s.CreateTask(t.Context(), nil, &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
	}))
	assert.NilError(t, s.CreateTask(t.Context(), nil, &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		AgentID:   orgAgent.ID,
	}))

	page, err := s.ListTasksPage(t.Context(), nil, store.ListTasksPageParams{
		OrgID:    org.OrgID,
		PageSize: 1,
	})
	assert.NilError(t, err)
	assert.Assert(t, page.NextToken != "")

	// The default (archived=false) cursor omits the Archived field entirely
	// (json omitempty), so tokens minted before the archived filter existed stay
	// byte-compatible.
	raw, err := base64.URLEncoding.DecodeString(page.NextToken)
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(string(raw), `"a"`), "default token must not carry the archived field: %s", raw)
}

func TestListTasksPage_AgentFilter(t *testing.T) {
	t.Parallel()
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	agentA := teststore.CreateAgent(t, s, org, nil)
	agentB := teststore.CreateAgent(t, s, org, nil)

	// Interleave the two agents' tasks; keyset order is newest-created first.
	var wantA []int64
	for i := range 4 {
		task := &model.Task{
			Runner:    "r",
			Workspace: "w",
			Status:    model.TaskStatusCompleted,
			OrgID:     org.OrgID,
			AgentID:   agentA.ID,
		}
		if i%2 == 1 {
			task.AgentID = agentB.ID
		}
		assert.NilError(t, s.CreateTask(t.Context(), nil, task))
		if task.AgentID == agentA.ID {
			wantA = append([]int64{task.ID}, wantA...)
		}
	}

	// Page through agent A's tasks one at a time.
	var got []int64
	token := ""
	for {
		page, err := s.ListTasksPage(t.Context(), nil, store.ListTasksPageParams{
			OrgID:     org.OrgID,
			PageSize:  1,
			PageToken: token,
			AgentID:   agentA.ID,
		})
		assert.NilError(t, err)
		got = append(got, testx.ExtractField(page.Items, "ID").([]int64)...)
		if page.NextToken == "" {
			break
		}
		token = page.NextToken
	}
	assert.DeepEqual(t, got, wantA)

	all, err := s.ListTasksPage(t.Context(), nil, store.ListTasksPageParams{OrgID: org.OrgID})
	assert.NilError(t, err)
	assert.Equal(t, len(all.Items), 4)
}

func TestListTasksPage_TokenBindsAgentFilter(t *testing.T) {
	t.Parallel()
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	orgAgent := teststore.CreateAgent(t, s, org, nil)

	for range 2 {
		assert.NilError(t, s.CreateTask(t.Context(), nil, &model.Task{
			Runner:    "r",
			Workspace: "w",
			Status:    model.TaskStatusCompleted,
			OrgID:     org.OrgID,
			AgentID:   orgAgent.ID,
		}))
	}

	agentPage, err := s.ListTasksPage(t.Context(), nil, store.ListTasksPageParams{
		OrgID:    org.OrgID,
		PageSize: 1,
		AgentID:  orgAgent.ID,
	})
	assert.NilError(t, err)
	assert.Assert(t, agentPage.NextToken != "")

	// Replaying an agent-minted token for every agent is rejected.
	_, err = s.ListTasksPage(t.Context(), nil, store.ListTasksPageParams{
		OrgID:     org.OrgID,
		PageSize:  1,
		PageToken: agentPage.NextToken,
	})
	assert.Assert(t, cmp.ErrorIs(err, pagination.ErrInvalidRequest))
}

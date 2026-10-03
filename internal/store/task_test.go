package store_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/icholy/gritz/internal/model"
	"github.com/icholy/gritz/internal/store"
	"github.com/icholy/gritz/internal/store/teststore"
	"github.com/icholy/gritz/internal/x/pagination"
	"github.com/icholy/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestClearShellSession(t *testing.T) {
	t.Parallel()
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)

	task := &model.Task{
		Name:         "shell-task",
		Runner:       "r",
		Workspace:    "w",
		Status:       model.TaskStatusCompleted,
		OrgID:        org.OrgID,
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
	other := teststore.CreateOrg(t, s, nil)

	task := &model.Task{
		Name:         "shell-task",
		Runner:       "r",
		Workspace:    "w",
		Status:       model.TaskStatusCompleted,
		OrgID:        org.OrgID,
		ShellSession: "sess-2",
	}
	assert.NilError(t, s.CreateTask(t.Context(), nil, task))

	// Clearing under a different org must not touch the task.
	assert.NilError(t, s.ClearShellSession(t.Context(), nil, "sess-2", other.OrgID))
	got, err := s.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, got.ShellSession, "sess-2")
}

func TestCreateTask_Namespace(t *testing.T) {
	t.Parallel()
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)

	// A task created with a non-default namespace reads it back.
	task := &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
		Namespace: "reviewbot",
	}
	assert.NilError(t, s.CreateTask(t.Context(), nil, task))
	got, err := s.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, got.Namespace, "reviewbot")

	// A task created without a namespace reads back the default (empty string).
	def := &model.Task{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		OrgID:     org.OrgID,
	}
	assert.NilError(t, s.CreateTask(t.Context(), nil, def))
	gotDef, err := s.GetTask(t.Context(), nil, def.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, gotDef.Namespace, "")
}

func TestListTasksPage_ActiveOnly(t *testing.T) {
	t.Parallel()
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)

	// Interleave active and archived; keyset order is newest-created first.
	a := &model.Task{Runner: "r", Workspace: "w", Status: model.TaskStatusCompleted, OrgID: org.OrgID}
	assert.NilError(t, s.CreateTask(t.Context(), nil, a))
	assert.NilError(t, s.CreateTask(t.Context(), nil, &model.Task{Runner: "r", Workspace: "w", Status: model.TaskStatusCompleted, OrgID: org.OrgID, Archived: true}))
	c := &model.Task{Runner: "r", Workspace: "w", Status: model.TaskStatusCompleted, OrgID: org.OrgID}
	assert.NilError(t, s.CreateTask(t.Context(), nil, c))
	assert.NilError(t, s.CreateTask(t.Context(), nil, &model.Task{Runner: "r", Workspace: "w", Status: model.TaskStatusCompleted, OrgID: org.OrgID, Archived: true}))

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

	a := &model.Task{Runner: "r", Workspace: "w", Status: model.TaskStatusCompleted, OrgID: org.OrgID}
	assert.NilError(t, s.CreateTask(t.Context(), nil, a))
	b := &model.Task{Runner: "r", Workspace: "w", Status: model.TaskStatusCompleted, OrgID: org.OrgID, Archived: true}
	assert.NilError(t, s.CreateTask(t.Context(), nil, b))
	c := &model.Task{Runner: "r", Workspace: "w", Status: model.TaskStatusCompleted, OrgID: org.OrgID}
	assert.NilError(t, s.CreateTask(t.Context(), nil, c))
	d := &model.Task{Runner: "r", Workspace: "w", Status: model.TaskStatusCompleted, OrgID: org.OrgID, Archived: true}
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

	a := &model.Task{Runner: "r", Workspace: "w", Status: model.TaskStatusCompleted, OrgID: org.OrgID}
	assert.NilError(t, s.CreateTask(t.Context(), nil, a))
	b := &model.Task{Runner: "r", Workspace: "w", Status: model.TaskStatusCompleted, OrgID: org.OrgID, Archived: true}
	assert.NilError(t, s.CreateTask(t.Context(), nil, b))
	c := &model.Task{Runner: "r", Workspace: "w", Status: model.TaskStatusCompleted, OrgID: org.OrgID}
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

	// Two archived-included rows so the first page mints a next token.
	assert.NilError(t, s.CreateTask(t.Context(), nil, &model.Task{Runner: "r", Workspace: "w", Status: model.TaskStatusCompleted, OrgID: org.OrgID, Archived: true}))
	assert.NilError(t, s.CreateTask(t.Context(), nil, &model.Task{Runner: "r", Workspace: "w", Status: model.TaskStatusCompleted, OrgID: org.OrgID, Archived: true}))

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
	assert.NilError(t, s.CreateTask(t.Context(), nil, &model.Task{Runner: "r", Workspace: "w", Status: model.TaskStatusCompleted, OrgID: org.OrgID}))
	assert.NilError(t, s.CreateTask(t.Context(), nil, &model.Task{Runner: "r", Workspace: "w", Status: model.TaskStatusCompleted, OrgID: org.OrgID}))
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

	assert.NilError(t, s.CreateTask(t.Context(), nil, &model.Task{Runner: "r", Workspace: "w", Status: model.TaskStatusCompleted, OrgID: org.OrgID}))
	assert.NilError(t, s.CreateTask(t.Context(), nil, &model.Task{Runner: "r", Workspace: "w", Status: model.TaskStatusCompleted, OrgID: org.OrgID}))

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

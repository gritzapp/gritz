package store_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestAgentCRUD(t *testing.T) {
	t.Parallel()
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	ctx := t.Context()

	// Create
	agent := &model.Agent{
		OrgID:       org.OrgID,
		Name:        "implementer",
		Workspace:   "w",
		Runner:      "r",
		Prompt:      "Implement the issue and open a pull request.",
		AutoArchive: 24 * time.Hour,
		Variables:   map[string]string{"REPO": "gritz"},
		Triggers: []model.AgentTrigger{{
			Source:     "atlassian",
			Type:       "label_added",
			Conditions: []model.Condition{{Attr: "label", Op: "equals", Value: "build-it"}},
			Public:     true,
			YieldTo:    []string{"pr-owner"},
			Link:       "jira.issue",
		}},
		Links: []model.AgentLink{
			{Type: "jira.issue", Events: []string{"comment_created"}},
			{Type: "github.pr", Kind: "owned-pr", Claim: "pr-owner", Events: []string{"pull_request_review", "pull_request_closed"}, Public: true},
		},
	}
	assert.NilError(t, s.CreateAgent(ctx, nil, agent))
	assert.Assert(t, agent.ID != 0)

	// Get round-trips every field.
	got, err := s.GetAgent(ctx, nil, org.OrgID, agent.ID)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, agent)

	// Update replaces the stored agent.
	agent.Name = "builder"
	agent.Prompt = ""
	agent.AutoArchive = 0
	agent.Variables = nil
	agent.Triggers = nil
	agent.Links = []model.AgentLink{{Type: "github.pr"}}
	assert.NilError(t, s.UpdateAgent(ctx, nil, agent))

	got, err = s.GetAgent(ctx, nil, org.OrgID, agent.ID)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, agent)

	// Delete hides it.
	assert.NilError(t, s.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if err := s.DeleteAgent(ctx, tx, org.OrgID, agent.ID); err != nil {
			return err
		}
		return tx.Commit()
	}))
	_, err = s.GetAgent(ctx, nil, org.OrgID, agent.ID)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestDeleteAgent_Soft(t *testing.T) {
	t.Parallel()
	// Arrange - an agent with an archived task.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	ctx := t.Context()
	agent := teststore.CreateAgent(t, s, org, nil)
	task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{AgentID: agent.ID})
	task.Archived = true
	assert.NilError(t, s.UpdateTask(ctx, nil, task))

	// Act
	err := s.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if err := s.DeleteAgent(ctx, tx, org.OrgID, agent.ID); err != nil {
			return err
		}
		return tx.Commit()
	})

	// Assert - every method sees no agent, and the archived task still loads
	// with its agent's ID.
	assert.NilError(t, err)
	_, err = s.GetAgent(ctx, nil, org.OrgID, agent.ID)
	assert.ErrorIs(t, err, sql.ErrNoRows)
	agents, err := s.ListAgentsForOrgs(ctx, nil, []int64{org.OrgID})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(agents, 0))
	assert.ErrorIs(t, s.UpdateAgent(ctx, nil, agent), sql.ErrNoRows)
	err = s.WithTx(ctx, nil, func(tx *sql.Tx) error {
		return s.LockLiveAgent(ctx, tx, org.OrgID, agent.ID)
	})
	assert.ErrorIs(t, err, sql.ErrNoRows)
	err = s.WithTx(ctx, nil, func(tx *sql.Tx) error {
		return s.DeleteAgent(ctx, tx, org.OrgID, agent.ID)
	})
	assert.ErrorIs(t, err, sql.ErrNoRows)
	got, err := s.GetTask(ctx, nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, got.AgentID, agent.ID)
}

func TestListAgentsForOrgs(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org1 := teststore.CreateOrg(t, s, nil)
	org2 := teststore.CreateOrg(t, s, nil)
	org3 := teststore.CreateOrg(t, s, nil)
	ctx := t.Context()
	b1 := &model.Agent{OrgID: org1.OrgID, Name: "b", Workspace: "w", Runner: "r"}
	a1 := &model.Agent{OrgID: org1.OrgID, Name: "a", Workspace: "w", Runner: "r"}
	a1Twin := &model.Agent{OrgID: org1.OrgID, Name: "a", Workspace: "w", Runner: "r"}
	a2 := &model.Agent{OrgID: org2.OrgID, Name: "a", Workspace: "w", Runner: "r"}
	a3 := &model.Agent{OrgID: org3.OrgID, Name: "a", Workspace: "w", Runner: "r"}
	for _, a := range []*model.Agent{b1, a1, a1Twin, a2, a3} {
		assert.NilError(t, s.CreateAgent(ctx, nil, a))
	}

	// Act
	got, err := s.ListAgentsForOrgs(ctx, nil, []int64{org2.OrgID, org1.OrgID})

	// Assert - only the listed orgs, ordered by org, name and ID
	assert.NilError(t, err)
	assert.DeepEqual(t, got, []*model.Agent{a1, a1Twin, b1, a2})
}

func TestGetAgent_OtherOrg(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	other := teststore.CreateOrg(t, s, nil)
	ctx := t.Context()
	agent := &model.Agent{OrgID: org.OrgID, Name: "a", Workspace: "w", Runner: "r"}
	assert.NilError(t, s.CreateAgent(ctx, nil, agent))

	// Act
	_, err := s.GetAgent(ctx, nil, other.OrgID, agent.ID)

	// Assert
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestUpdateAgent_OtherOrg(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	other := teststore.CreateOrg(t, s, nil)
	ctx := t.Context()
	agent := &model.Agent{OrgID: org.OrgID, Name: "a", Workspace: "w", Runner: "r"}
	assert.NilError(t, s.CreateAgent(ctx, nil, agent))

	// Act
	err := s.UpdateAgent(ctx, nil, &model.Agent{ID: agent.ID, OrgID: other.OrgID, Name: "b", Workspace: "w", Runner: "r"})

	// Assert - the other org can't touch it, and it is unchanged
	assert.ErrorIs(t, err, sql.ErrNoRows)
	got, err := s.GetAgent(ctx, nil, org.OrgID, agent.ID)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, agent)
}

func TestDeleteAgent_OtherOrg(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	other := teststore.CreateOrg(t, s, nil)
	ctx := t.Context()
	agent := &model.Agent{OrgID: org.OrgID, Name: "a", Workspace: "w", Runner: "r"}
	assert.NilError(t, s.CreateAgent(ctx, nil, agent))

	// Act
	err := s.WithTx(ctx, nil, func(tx *sql.Tx) error {
		return s.DeleteAgent(ctx, tx, other.OrgID, agent.ID)
	})

	// Assert
	assert.ErrorIs(t, err, sql.ErrNoRows)
	_, err = s.GetAgent(ctx, nil, org.OrgID, agent.ID)
	assert.NilError(t, err)
}

func TestDeleteAgent_InUse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		using func(t *testing.T, s *store.Store, org *teststore.Org, agentID int64)
		err   error
	}{
		{
			name: "unarchived task",
			using: func(t *testing.T, s *store.Store, org *teststore.Org, agentID int64) {
				teststore.CreateTask(t, s, org, &teststore.TaskOptions{AgentID: agentID})
			},
			err: store.ErrAgentInUse,
		},
		{
			name: "schedule",
			using: func(t *testing.T, s *store.Store, org *teststore.Org, agentID int64) {
				assert.NilError(t, s.CreateSchedule(t.Context(), nil, &model.Schedule{
					OrgID:     org.OrgID,
					CreatedBy: org.UserID,
					AgentID:   agentID,
					CronExpr:  "@daily",
					Timezone:  "UTC",
				}))
			},
			err: store.ErrAgentInUse,
		},
		{
			name: "archived task",
			using: func(t *testing.T, s *store.Store, org *teststore.Org, agentID int64) {
				task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{AgentID: agentID})
				task.Archived = true
				assert.NilError(t, s.UpdateTask(t.Context(), nil, task))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			s := teststore.New(t)
			org := teststore.CreateOrg(t, s, nil)
			agent := teststore.CreateAgent(t, s, org, nil)
			tt.using(t, s, org, agent.ID)

			// Act
			err := s.WithTx(t.Context(), nil, func(tx *sql.Tx) error {
				return s.DeleteAgent(t.Context(), tx, org.OrgID, agent.ID)
			})

			// Assert
			if tt.err != nil {
				assert.ErrorIs(t, err, tt.err)
			} else {
				assert.NilError(t, err)
			}
		})
	}
}

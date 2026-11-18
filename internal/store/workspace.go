package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/sqlc"
	"github.com/gritzapp/gritz/internal/taskvar"
)

func (s *Store) DeleteWorkspacesByRunner(ctx context.Context, tx *sql.Tx, runnerID string, orgID int64) error {
	return s.q(tx).DeleteWorkspacesByRunner(ctx, sqlc.DeleteWorkspacesByRunnerParams{
		RunnerID: runnerID,
		OrgID:    orgID,
	})
}

// CreateWorkspace registers a runner's workspace with the task variables it
// declares.
func (s *Store) CreateWorkspace(ctx context.Context, tx *sql.Tx, runnerID, name, description string, variables []taskvar.Decl, orgID int64) error {
	// workspaces.variables holds an array even when the workspace declares none.
	if variables == nil {
		variables = []taskvar.Decl{}
	}
	data, err := json.Marshal(variables)
	if err != nil {
		return err
	}
	return s.q(tx).CreateWorkspace(ctx, sqlc.CreateWorkspaceParams{
		RunnerID:    runnerID,
		Name:        name,
		Description: description,
		OrgID:       orgID,
		Variables:   data,
	})
}

func (s *Store) ListWorkspaces(ctx context.Context, tx *sql.Tx, orgID int64) ([]*model.Workspace, error) {
	rows, err := s.q(tx).ListWorkspacesByOrgID(ctx, orgID)
	if err != nil {
		return nil, err
	}
	workspaces := make([]*model.Workspace, len(rows))
	for i, row := range rows {
		var variables []taskvar.Decl
		if err := json.Unmarshal(row.Variables, &variables); err != nil {
			return nil, fmt.Errorf("decode workspace %q variables: %w", row.Name, err)
		}
		if len(variables) == 0 {
			variables = nil
		}
		workspaces[i] = &model.Workspace{
			ID:          row.ID,
			RunnerID:    row.RunnerID,
			Name:        row.Name,
			Description: row.Description,
			OrgID:       row.OrgID,
			UpdatedAt:   row.UpdatedAt,
			Variables:   variables,
		}
	}
	return workspaces, nil
}

func (s *Store) HasWorkspace(ctx context.Context, tx *sql.Tx, runnerID, name string, orgID int64) (bool, error) {
	return s.q(tx).HasWorkspace(ctx, sqlc.HasWorkspaceParams{
		RunnerID: runnerID,
		Name:     name,
		OrgID:    orgID,
	})
}

func (s *Store) ClearWorkspaces(ctx context.Context, tx *sql.Tx, orgID int64) error {
	return s.q(tx).ClearWorkspacesByOrgID(ctx, orgID)
}

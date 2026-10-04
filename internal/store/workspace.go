package store

import (
	"context"
	"database/sql"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/sqlc"
)

func (s *Store) DeleteWorkspacesByRunner(ctx context.Context, tx *sql.Tx, runnerID string, orgID int64) error {
	return s.q(tx).DeleteWorkspacesByRunner(ctx, sqlc.DeleteWorkspacesByRunnerParams{
		RunnerID: runnerID,
		OrgID:    orgID,
	})
}

func (s *Store) CreateWorkspace(ctx context.Context, tx *sql.Tx, runnerID, name, description string, orgID int64) error {
	return s.q(tx).CreateWorkspace(ctx, sqlc.CreateWorkspaceParams{
		RunnerID:    runnerID,
		Name:        name,
		Description: description,
		OrgID:       orgID,
	})
}

func (s *Store) ListWorkspaces(ctx context.Context, tx *sql.Tx, orgID int64) ([]*model.Workspace, error) {
	rows, err := s.q(tx).ListWorkspacesByOrgID(ctx, orgID)
	if err != nil {
		return nil, err
	}
	workspaces := make([]*model.Workspace, len(rows))
	for i, row := range rows {
		workspaces[i] = &model.Workspace{
			ID:          row.ID,
			RunnerID:    row.RunnerID,
			Name:        row.Name,
			Description: row.Description,
			OrgID:       row.OrgID,
			UpdatedAt:   row.UpdatedAt,
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

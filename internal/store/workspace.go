package store

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/sqlc"
)

func (s *Store) DeleteWorkspacesByRunner(ctx context.Context, tx *sql.Tx, runnerID string, orgID int64) error {
	return s.q(tx).DeleteWorkspacesByRunner(ctx, sqlc.DeleteWorkspacesByRunnerParams{
		RunnerID: runnerID,
		OrgID:    orgID,
	})
}

// CreateWorkspace registers a runner's workspace with the task variables and
// features it declares. ID and UpdatedAt are ignored.
func (s *Store) CreateWorkspace(ctx context.Context, tx *sql.Tx, ws *model.Workspace) error {
	// json/v2 encodes a nil slice as []: workspaces.variables and
	// workspaces.features hold an array even when the workspace declares none.
	variables, err := json.Marshal(ws.Variables)
	if err != nil {
		return err
	}
	features, err := json.Marshal(ws.Features)
	if err != nil {
		return err
	}
	return s.q(tx).CreateWorkspace(ctx, sqlc.CreateWorkspaceParams{
		RunnerID:    ws.RunnerID,
		Name:        ws.Name,
		Description: ws.Description,
		OrgID:       ws.OrgID,
		Variables:   variables,
		Features:    features,
	})
}

func (s *Store) ListWorkspaces(ctx context.Context, tx *sql.Tx, orgID int64) ([]*model.Workspace, error) {
	rows, err := s.q(tx).ListWorkspacesByOrgID(ctx, orgID)
	if err != nil {
		return nil, err
	}
	workspaces := make([]*model.Workspace, len(rows))
	for i, row := range rows {
		if workspaces[i], err = workspaceFromRow(sqlc.GetWorkspaceRow(row)); err != nil {
			return nil, err
		}
	}
	return workspaces, nil
}

// GetWorkspace returns a runner's registered workspace with its variables and
// features, or sql.ErrNoRows when it isn't registered.
func (s *Store) GetWorkspace(ctx context.Context, tx *sql.Tx, runnerID, name string, orgID int64) (*model.Workspace, error) {
	row, err := s.q(tx).GetWorkspace(ctx, sqlc.GetWorkspaceParams{
		RunnerID: runnerID,
		Name:     name,
		OrgID:    orgID,
	})
	if err != nil {
		return nil, err
	}
	return workspaceFromRow(row)
}

func workspaceFromRow(row sqlc.GetWorkspaceRow) (*model.Workspace, error) {
	ws := &model.Workspace{
		ID:          row.ID,
		RunnerID:    row.RunnerID,
		Name:        row.Name,
		Description: row.Description,
		OrgID:       row.OrgID,
		UpdatedAt:   row.UpdatedAt,
	}
	if err := json.Unmarshal(row.Variables, &ws.Variables); err != nil {
		return nil, fmt.Errorf("decode workspace %q variables: %w", row.Name, err)
	}
	if err := json.Unmarshal(row.Features, &ws.Features); err != nil {
		return nil, fmt.Errorf("decode workspace %q features: %w", row.Name, err)
	}
	if len(ws.Variables) == 0 {
		ws.Variables = nil
	}
	if len(ws.Features) == 0 {
		ws.Features = nil
	}
	return ws, nil
}

func (s *Store) ClearWorkspaces(ctx context.Context, tx *sql.Tx, orgID int64) error {
	return s.q(tx).ClearWorkspacesByOrgID(ctx, orgID)
}

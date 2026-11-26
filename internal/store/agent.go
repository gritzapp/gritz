package store

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/sqlc"
)

// CreateAgent inserts a and sets its ID.
func (s *Store) CreateAgent(ctx context.Context, tx *sql.Tx, a *model.Agent) error {
	cols, err := marshalAgent(a)
	if err != nil {
		return err
	}
	id, err := s.q(tx).CreateAgent(ctx, sqlc.CreateAgentParams{
		OrgID:       a.OrgID,
		Name:        a.Name,
		Workspace:   a.Workspace,
		Runner:      a.Runner,
		Prompt:      a.Prompt,
		AutoArchive: a.AutoArchive.Microseconds(),
		Variables:   cols.variables,
		Triggers:    cols.triggers,
		Links:       cols.links,
	})
	if err != nil {
		return err
	}
	a.ID = id
	return nil
}

// GetAgent returns the org's agent by ID, or sql.ErrNoRows.
func (s *Store) GetAgent(ctx context.Context, tx *sql.Tx, orgID, id int64) (*model.Agent, error) {
	row, err := s.q(tx).GetAgent(ctx, sqlc.GetAgentParams{OrgID: orgID, ID: id})
	if err != nil {
		return nil, err
	}
	return toModelAgent(row)
}

// ListAgentsForOrgs returns the agents of every org in orgIDs, ordered by org,
// name and ID.
func (s *Store) ListAgentsForOrgs(ctx context.Context, tx *sql.Tx, orgIDs []int64) ([]*model.Agent, error) {
	rows, err := s.q(tx).ListAgentsForOrgs(ctx, orgIDs)
	if err != nil {
		return nil, err
	}
	agents := make([]*model.Agent, len(rows))
	for i, row := range rows {
		a, err := toModelAgent(row)
		if err != nil {
			return nil, err
		}
		agents[i] = a
	}
	return agents, nil
}

// UpdateAgent replaces the stored agent with a, matched by a.ID and a.OrgID.
// It returns sql.ErrNoRows when the org has no such agent.
func (s *Store) UpdateAgent(ctx context.Context, tx *sql.Tx, a *model.Agent) error {
	cols, err := marshalAgent(a)
	if err != nil {
		return err
	}
	n, err := s.q(tx).UpdateAgent(ctx, sqlc.UpdateAgentParams{
		Name:        a.Name,
		Workspace:   a.Workspace,
		Runner:      a.Runner,
		Prompt:      a.Prompt,
		AutoArchive: a.AutoArchive.Microseconds(),
		Variables:   cols.variables,
		Triggers:    cols.triggers,
		Links:       cols.links,
		ID:          a.ID,
		OrgID:       a.OrgID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ErrAgentInUse is returned by DeleteAgent for an agent an unarchived task or
// a schedule belongs to.
var ErrAgentInUse = errors.New("agent in use")

// DeleteAgent removes the org's agent. It returns ErrAgentInUse while an
// unarchived task or a schedule belongs to it, and sql.ErrNoRows when the org
// has no such agent. Archived tasks keep the deleted agent's ID.
func (s *Store) DeleteAgent(ctx context.Context, tx *sql.Tx, orgID, id int64) error {
	inUse, err := s.q(tx).AgentInUse(ctx, sqlc.AgentInUseParams{AgentID: id, OrgID: orgID})
	if err != nil {
		return err
	}
	if inUse {
		return ErrAgentInUse
	}
	n, err := s.q(tx).DeleteAgent(ctx, sqlc.DeleteAgentParams{ID: id, OrgID: orgID})
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

type agentColumns struct {
	variables, triggers, links []byte
}

// marshalAgent encodes the agent's JSONB columns. json/v2 encodes a nil map as
// {} and a nil slice as [], so the columns always hold an object or array.
func marshalAgent(a *model.Agent) (agentColumns, error) {
	var cols agentColumns
	var err error
	if cols.variables, err = json.Marshal(a.Variables); err != nil {
		return cols, fmt.Errorf("marshal agent variables: %w", err)
	}
	if cols.triggers, err = json.Marshal(a.Triggers); err != nil {
		return cols, fmt.Errorf("marshal agent triggers: %w", err)
	}
	if cols.links, err = json.Marshal(a.Links); err != nil {
		return cols, fmt.Errorf("marshal agent links: %w", err)
	}
	return cols, nil
}

func toModelAgent(row sqlc.Agent) (*model.Agent, error) {
	a := &model.Agent{
		ID:          row.ID,
		OrgID:       row.OrgID,
		Name:        row.Name,
		Workspace:   row.Workspace,
		Runner:      row.Runner,
		Prompt:      row.Prompt,
		AutoArchive: time.Duration(row.AutoArchive) * time.Microsecond,
	}
	if err := json.Unmarshal(row.Variables, &a.Variables); err != nil {
		return nil, fmt.Errorf("decode agent %d variables: %w", row.ID, err)
	}
	if err := json.Unmarshal(row.Triggers, &a.Triggers); err != nil {
		return nil, fmt.Errorf("decode agent %d triggers: %w", row.ID, err)
	}
	if err := json.Unmarshal(row.Links, &a.Links); err != nil {
		return nil, fmt.Errorf("decode agent %d links: %w", row.ID, err)
	}
	if len(a.Variables) == 0 {
		a.Variables = nil
	}
	if len(a.Triggers) == 0 {
		a.Triggers = nil
	}
	if len(a.Links) == 0 {
		a.Links = nil
	}
	return a, nil
}

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

// CreateAgent inserts a and its schedules, and sets their IDs.
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
		Features:    cols.features,
	})
	if err != nil {
		return err
	}
	a.ID = id
	for i, sched := range a.Schedules {
		if err := s.insertAgentSchedule(ctx, tx, a, i, sched); err != nil {
			return err
		}
	}
	return nil
}

// GetAgent returns the org's agent by ID, with its schedules, or
// sql.ErrNoRows when it is deleted or the org has none.
func (s *Store) GetAgent(ctx context.Context, tx *sql.Tx, orgID, id int64) (*model.Agent, error) {
	row, err := s.q(tx).GetAgent(ctx, sqlc.GetAgentParams{OrgID: orgID, ID: id})
	if err != nil {
		return nil, err
	}
	a, err := toModelAgent(row)
	if err != nil {
		return nil, err
	}
	scheds, err := s.q(tx).ListAgentSchedules(ctx, sqlc.ListAgentSchedulesParams{OrgID: orgID, AgentID: id})
	if err != nil {
		return nil, err
	}
	if len(scheds) > 0 {
		a.Schedules = toModelSchedules(scheds)
	}
	return a, nil
}

// ListAgents returns the org's live agents with their schedules, ordered by
// name and ID.
func (s *Store) ListAgents(ctx context.Context, tx *sql.Tx, orgID int64) ([]*model.Agent, error) {
	rows, err := s.q(tx).ListAgents(ctx, orgID)
	if err != nil {
		return nil, err
	}
	agents := make([]*model.Agent, len(rows))
	byID := make(map[int64]*model.Agent, len(rows))
	for i, row := range rows {
		a, err := toModelAgent(row)
		if err != nil {
			return nil, err
		}
		agents[i] = a
		byID[a.ID] = a
	}
	scheds, err := s.q(tx).ListOrgAgentSchedules(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for _, row := range scheds {
		// Outside a transaction, the agent may have been created after the
		// agents were listed.
		if a := byID[row.AgentID]; a != nil {
			a.Schedules = append(a.Schedules, toModelSchedule(row))
		}
	}
	return agents, nil
}

// LockAgentSchedules locks the org's agent's schedules until tx ends and
// returns them by ID. UpdateAgent and DeleteAgent take these locks before the
// agent's, the order the scheduler takes them in.
func (s *Store) LockAgentSchedules(ctx context.Context, tx *sql.Tx, orgID, agentID int64) (map[int64]*model.Schedule, error) {
	if tx == nil {
		panic("store: LockAgentSchedules requires a transaction")
	}
	rows, err := s.q(tx).LockAgentSchedules(ctx, sqlc.LockAgentSchedulesParams{OrgID: orgID, AgentID: agentID})
	if err != nil {
		return nil, err
	}
	scheds := make(map[int64]*model.Schedule, len(rows))
	for _, row := range rows {
		scheds[row.ID] = toModelSchedule(row)
	}
	return scheds, nil
}

// EventAgent is a live agent of an org an event routes in, and whether the
// routing actor is a member of that org. A non-member org admits only public
// triggers and link kinds.
type EventAgent struct {
	Agent    *model.Agent
	IsMember bool
}

// ListAgentsForEvent returns the live agents of every org an event of the
// actor userID routes in, ordered by org, name and ID. Those orgs are every
// org the user is a member of (IsMember true), and the passed-in orgs the user
// is NOT a member of (IsMember false). Membership wins on overlap. An empty
// orgs slice reduces this to the member orgs, and an empty userID to the
// passed-in ones. An org with no live agent has no rows.
func (s *Store) ListAgentsForEvent(ctx context.Context, tx *sql.Tx, userID string, orgs []int64) ([]EventAgent, error) {
	rows, err := s.q(tx).ListAgentsForEvent(ctx, sqlc.ListAgentsForEventParams{
		UserID: userID,
		OrgIds: orgs,
	})
	if err != nil {
		return nil, err
	}
	var agents []EventAgent
	for _, row := range rows {
		a, err := toModelAgent(sqlc.Agent{
			ID:          row.ID,
			OrgID:       row.OrgID,
			Name:        row.Name,
			Workspace:   row.Workspace,
			Runner:      row.Runner,
			Prompt:      row.Prompt,
			AutoArchive: row.AutoArchive,
			Variables:   row.Variables,
			Triggers:    row.Triggers,
			Links:       row.Links,
			CreatedAt:   row.CreatedAt,
			UpdatedAt:   row.UpdatedAt,
			DeletedAt:   row.DeletedAt,
			Features:    row.Features,
		})
		if err != nil {
			return nil, err
		}
		agents = append(agents, EventAgent{Agent: a, IsMember: row.IsMember})
	}
	return agents, nil
}

// UpdateAgent replaces the stored agent with a, matched by a.ID and a.OrgID.
// Its schedules are updated by ID, inserted without one (setting their IDs),
// and deleted when a leaves them out; each is written as is, run state
// included, at its index in a.Schedules. It returns sql.ErrNoRows when the
// agent is deleted or the org has none, and an error for a schedule ID that
// isn't one of the agent's.
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
		Features:    cols.features,
		ID:          a.ID,
		OrgID:       a.OrgID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	// Non-nil: a NULL array would keep every schedule.
	keep := []int64{}
	for _, sched := range a.Schedules {
		if sched.ID != 0 {
			keep = append(keep, sched.ID)
		}
	}
	if err := s.q(tx).DeleteAgentSchedulesExcept(ctx, sqlc.DeleteAgentSchedulesExceptParams{
		OrgID:   a.OrgID,
		AgentID: a.ID,
		KeepIds: keep,
	}); err != nil {
		return err
	}
	for i, sched := range a.Schedules {
		if sched.ID == 0 {
			if err := s.insertAgentSchedule(ctx, tx, a, i, sched); err != nil {
				return err
			}
			continue
		}
		sched.OrgID, sched.AgentID = a.OrgID, a.ID
		n, err := s.q(tx).UpdateAgentSchedule(ctx, sqlc.UpdateAgentScheduleParams{
			Name:       sched.Name,
			Position:   int32(i),
			CronExpr:   sched.CronExpr,
			Timezone:   sched.Timezone,
			Enabled:    sched.Enabled,
			Prompt:     sched.Prompt,
			NextRunAt:  nullTime(sched.NextRunAt),
			LastRunAt:  nullTime(sched.LastRunAt),
			LastTaskID: nullInt64(sched.LastTaskID),
			ID:         sched.ID,
			AgentID:    a.ID,
			OrgID:      a.OrgID,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("schedule %d is not a schedule of agent %d", sched.ID, a.ID)
		}
	}
	return nil
}

// insertAgentSchedule inserts sched as the schedule at position i of a, and
// sets its ID.
func (s *Store) insertAgentSchedule(ctx context.Context, tx *sql.Tx, a *model.Agent, i int, sched *model.Schedule) error {
	sched.OrgID, sched.AgentID = a.OrgID, a.ID
	id, err := s.q(tx).InsertAgentSchedule(ctx, sqlc.InsertAgentScheduleParams{
		OrgID:      a.OrgID,
		Name:       sched.Name,
		AgentID:    a.ID,
		Position:   int32(i),
		CronExpr:   sched.CronExpr,
		Timezone:   sched.Timezone,
		Enabled:    sched.Enabled,
		Prompt:     sched.Prompt,
		NextRunAt:  nullTime(sched.NextRunAt),
		LastRunAt:  nullTime(sched.LastRunAt),
		LastTaskID: nullInt64(sched.LastTaskID),
	})
	if err != nil {
		return err
	}
	sched.ID = id
	return nil
}

// ErrAgentInUse is returned by DeleteAgent for an agent an unarchived task
// belongs to.
var ErrAgentInUse = errors.New("agent in use")

// DeleteAgent deletes the org's agent's schedules and soft-deletes the agent
// in tx, after which no method sees it: its row stays, so its archived tasks
// keep referencing it. It returns ErrAgentInUse while an unarchived task
// belongs to it, and sql.ErrNoRows when the org has no such live agent. It
// locks the agent's schedules, then the agent, so a fire in flight finishes
// first; a concurrent LockLiveAgent holder likewise finishes creating or
// unarchiving its task before the check, which then sees that task.
func (s *Store) DeleteAgent(ctx context.Context, tx *sql.Tx, orgID, id int64) error {
	if _, err := s.LockAgentSchedules(ctx, tx, orgID, id); err != nil {
		return err
	}
	if _, err := s.q(tx).LockAgentForDelete(ctx, sqlc.LockAgentForDeleteParams{OrgID: orgID, ID: id}); err != nil {
		return err
	}
	inUse, err := s.q(tx).AgentInUse(ctx, sqlc.AgentInUseParams{AgentID: id, OrgID: orgID})
	if err != nil {
		return err
	}
	if inUse {
		return ErrAgentInUse
	}
	// After the agent's lock, so this also deletes a schedule added while
	// the agent was share-locked.
	if err := s.q(tx).DeleteAgentSchedulesExcept(ctx, sqlc.DeleteAgentSchedulesExceptParams{
		OrgID:   orgID,
		AgentID: id,
		KeepIds: []int64{},
	}); err != nil {
		return err
	}
	return s.q(tx).DeleteAgent(ctx, sqlc.DeleteAgentParams{ID: id, OrgID: orgID})
}

// LockLiveAgent share-locks the org's agent until tx ends, and returns
// sql.ErrNoRows when it is deleted or the org has none. Creating or
// unarchiving a task of the agent holds it, so DeleteAgent can't delete an
// agent an unarchived task belongs to.
func (s *Store) LockLiveAgent(ctx context.Context, tx *sql.Tx, orgID, id int64) error {
	if tx == nil {
		panic("store: LockLiveAgent requires a transaction")
	}
	_, err := s.q(tx).LockLiveAgent(ctx, sqlc.LockLiveAgentParams{OrgID: orgID, ID: id})
	return err
}

type agentColumns struct {
	variables, triggers, links, features []byte
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
	if cols.features, err = json.Marshal(a.Features); err != nil {
		return cols, fmt.Errorf("marshal agent features: %w", err)
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
	if err := json.Unmarshal(row.Features, &a.Features); err != nil {
		return nil, fmt.Errorf("decode agent %d features: %w", row.ID, err)
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
	if len(a.Features) == 0 {
		a.Features = nil
	}
	return a, nil
}

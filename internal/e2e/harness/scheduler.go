package harness

import (
	"context"
	"database/sql"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/server/scheduler"
	"github.com/gritzapp/gritz/internal/store"
)

// FireSchedule fires the agent's schedule once through the server's
// scheduler, as a tick does when the schedule is due, whether or not it is:
// no RPC moves a schedule's clock, and the test database is shared, so the
// tick claims only this schedule rather than every due one.
func (o *Org) FireSchedule(agentID, id int64) {
	t := o.server.t
	t.Helper()
	sched := scheduler.New(scheduler.Options{
		Store:     claimOne{Store: o.server.Store, orgID: o.ID, agentID: agentID, id: id},
		Publisher: o.server.publisher,
	})
	if err := sched.Tick(t.Context()); err != nil {
		t.Fatalf("fire schedule %d: %v", id, err)
	}
}

// claimOne is the server's store with the scheduler's claim narrowed to one
// schedule, locked as ClaimDueSchedules locks the rows it claims.
type claimOne struct {
	*store.Store
	orgID, agentID, id int64
}

func (c claimOne) ClaimDueSchedules(ctx context.Context, tx *sql.Tx, limit int) ([]*model.Schedule, error) {
	scheds, err := c.LockAgentSchedules(ctx, tx, c.orgID, c.agentID)
	if err != nil {
		return nil, err
	}
	sched, ok := scheds[c.id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return []*model.Schedule{sched}, nil
}

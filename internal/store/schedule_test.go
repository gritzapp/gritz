package store_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestAdvanceSchedule(t *testing.T) {
	t.Parallel()
	newNext := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Microsecond)
	tests := []struct {
		name    string
		next    *time.Time
		enabled bool
	}{
		{name: "next occurrence", next: &newNext, enabled: true},
		{name: "no next occurrence disables", next: nil, enabled: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - a future next_run_at keeps this row out of the due set
			// that TestClaimDueSchedules_Concurrent owns; relative to now so it
			// can't rot.
			s := teststore.New(t)
			org := teststore.CreateOrg(t, s, nil)
			ctx := t.Context()
			next := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond)
			agent := teststore.CreateAgent(t, s, org, &model.Agent{Schedules: []*model.Schedule{
				{CronExpr: "0 9 * * *", Timezone: "UTC", Enabled: true, NextRunAt: &next},
			}})
			sched := agent.Schedules[0]
			task := teststore.CreateTask(t, s, org, nil)
			firedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)

			// Act
			err := s.AdvanceSchedule(ctx, nil, sched.ID, org.OrgID, store.ScheduleAdvance{
				NextRunAt:  tt.next,
				LastRunAt:  &firedAt,
				LastTaskID: &task.ID,
			})

			// Assert - the fire is recorded: next_run_at, last_run_at,
			// last_task_id, and enabled only with a next occurrence.
			assert.NilError(t, err)
			got, err := s.GetAgent(ctx, nil, org.OrgID, agent.ID)
			assert.NilError(t, err)
			assert.DeepEqual(t, got.Schedules, []*model.Schedule{{
				ID:         sched.ID,
				OrgID:      org.OrgID,
				AgentID:    agent.ID,
				CronExpr:   "0 9 * * *",
				Timezone:   "UTC",
				Enabled:    tt.enabled,
				NextRunAt:  tt.next,
				LastRunAt:  &firedAt,
				LastTaskID: &task.ID,
			}})
		})
	}
}

// TestClaimDueSchedules_Concurrent proves the FOR UPDATE SKIP LOCKED guarantee:
// two callers claiming the due set at the same time partition it instead of both
// claiming the same row. It seeds the entire due set for the store package (the
// other schedule tests keep their rows out of the due set), so the two claims
// must together cover exactly those rows, disjointly.
func TestClaimDueSchedules_Concurrent(t *testing.T) {
	t.Parallel()
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	ctx := t.Context()

	// The claim isn't scoped to an org: it takes the earliest due schedules in
	// the whole database, which other tests and earlier runs share. Seed this
	// test's schedules long before any other due one, so they sort first, and
	// delete them afterwards, so they don't stay due for the next run.
	const n = 6
	past := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	var scheds []*model.Schedule
	for i := 0; i < n; i++ {
		// Distinct next_run_at so ORDER BY next_run_at is deterministic.
		next := past.Add(time.Duration(i) * time.Minute)
		scheds = append(scheds, &model.Schedule{
			CronExpr:  "* * * * *",
			Timezone:  "UTC",
			Enabled:   true,
			NextRunAt: &next,
		})
	}
	agent := teststore.CreateAgent(t, s, org, &model.Agent{Schedules: scheds})
	t.Cleanup(func() {
		assert.NilError(t, s.WithTx(context.Background(), nil, func(tx *sql.Tx) error {
			if err := s.DeleteAgent(context.Background(), tx, org.OrgID, agent.ID); err != nil {
				return err
			}
			return tx.Commit()
		}))
	})
	want := map[int64]bool{}
	for _, sched := range scheds {
		want[sched.ID] = true
	}

	// Force an overlap: caller 2 claims only after caller 1 has claimed and is
	// still holding its row locks. SKIP LOCKED then makes caller 2 skip caller
	// 1's rows and take the rest, rather than blocking on or re-claiming them.
	claimed1 := make(chan struct{})
	claimed2 := make(chan struct{})
	var claim1, claim2 []*model.Schedule
	var err1, err2 error

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		err1 = s.WithTx(ctx, nil, func(tx *sql.Tx) error {
			var e error
			claim1, e = s.ClaimDueSchedules(ctx, tx, n/2)
			close(claimed1)
			<-claimed2 // hold the locks until caller 2 has claimed
			return e
		})
	}()
	go func() {
		defer wg.Done()
		<-claimed1 // claim only once caller 1 holds its locks
		err2 = s.WithTx(ctx, nil, func(tx *sql.Tx) error {
			var e error
			claim2, e = s.ClaimDueSchedules(ctx, tx, n/2)
			close(claimed2)
			return e
		})
	}()
	wg.Wait()

	assert.NilError(t, err1)
	assert.NilError(t, err2)
	assert.Assert(t, cmp.Len(claim1, n/2))
	assert.Assert(t, cmp.Len(claim2, n/2))

	// The two claims partition the seeded due set: every seeded schedule is
	// claimed exactly once, and no row is claimed by both callers.
	seen := map[int64]int{}
	for _, sc := range append(append([]*model.Schedule{}, claim1...), claim2...) {
		assert.Equal(t, sc.OrgID, org.OrgID)
		seen[sc.ID]++
	}
	assert.Assert(t, cmp.Len(seen, n))
	for id := range want {
		assert.Equal(t, seen[id], 1, "schedule %d must be claimed exactly once", id)
	}
}

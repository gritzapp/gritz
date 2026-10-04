package store_test

import (
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestScheduleCRUD(t *testing.T) {
	t.Parallel()
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	ctx := t.Context()

	// Create — a future next_run_at keeps this schedule out of the claim
	// query's due set (see TestClaimDueSchedules_Concurrent, which owns the due
	// set for this package). Relative to now so it can never rot into the past.
	next := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond)
	sched := &model.Schedule{
		OrgID:     org.OrgID,
		CreatedBy: org.UserID,
		Name:      "nightly",
		Workspace: "w",
		Runner:    "r",
		Namespace: "ns",
		Instructions: []model.ScheduleInstruction{
			{Text: "bump deps", URL: "https://example.com/deps"},
			{Text: "groom changelog"},
		},
		AutoArchive: time.Hour,
		CronExpr:    "0 9 * * *",
		Timezone:    "America/Toronto",
		Enabled:     true,
		NextRunAt:   &next,
	}
	assert.NilError(t, s.CreateSchedule(ctx, nil, sched))
	assert.Assert(t, sched.ID != 0)

	// Get round-trips every field.
	got, err := s.GetSchedule(ctx, nil, sched.ID, org.OrgID)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, sched, cmpopts.IgnoreFields(model.Schedule{}, "CreatedAt", "UpdatedAt"))

	// A get from another org does not see it.
	other := teststore.CreateOrg(t, s, nil)
	_, err = s.GetSchedule(ctx, nil, sched.ID, other.OrgID)
	assert.ErrorIs(t, err, sql.ErrNoRows)

	// Update mutates the template and spec.
	sched.Name = "nightly-v2"
	sched.CronExpr = "30 2 * * *"
	sched.Timezone = "UTC"
	sched.Enabled = false
	sched.NextRunAt = nil
	sched.Instructions = []model.ScheduleInstruction{{Text: "only this"}}
	assert.NilError(t, s.UpdateSchedule(ctx, nil, sched))

	got, err = s.GetSchedule(ctx, nil, sched.ID, org.OrgID)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, sched, cmpopts.IgnoreFields(model.Schedule{}, "CreatedAt", "UpdatedAt"))

	// List is org-scoped.
	list, err := s.ListSchedules(ctx, nil, org.OrgID)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(list, 1))
	assert.Equal(t, list[0].ID, sched.ID)

	empty, err := s.ListSchedules(ctx, nil, other.OrgID)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(empty, 0))

	// Delete removes it.
	assert.NilError(t, s.DeleteSchedule(ctx, nil, sched.ID, org.OrgID))
	_, err = s.GetSchedule(ctx, nil, sched.ID, org.OrgID)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestAdvanceSchedule(t *testing.T) {
	t.Parallel()
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	ctx := t.Context()

	// Future next_run_at keeps this row out of the due set that
	// TestClaimDueSchedules_Concurrent owns; relative to now so it can't rot.
	next := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond)
	sched := &model.Schedule{
		OrgID:     org.OrgID,
		CreatedBy: org.UserID,
		Workspace: "w",
		Runner:    "r",
		CronExpr:  "0 9 * * *",
		Timezone:  "UTC",
		Enabled:   true,
		NextRunAt: &next,
	}
	assert.NilError(t, s.CreateSchedule(ctx, nil, sched))

	// Advancing records the fire: new next_run_at, last_run_at, last_task_id.
	task := teststore.CreateTask(t, s, org, nil)
	newNext := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Microsecond)
	firedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	assert.NilError(t, s.AdvanceSchedule(ctx, nil, sched.ID, org.OrgID, store.ScheduleAdvance{
		NextRunAt:  &newNext,
		LastRunAt:  &firedAt,
		LastTaskID: &task.ID,
	}))

	got, err := s.GetSchedule(ctx, nil, sched.ID, org.OrgID)
	assert.NilError(t, err)
	assert.DeepEqual(t, *got.NextRunAt, newNext)
	assert.DeepEqual(t, *got.LastRunAt, firedAt)
	assert.Equal(t, *got.LastTaskID, task.ID)
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

	const n = 6
	past := time.Now().UTC().Add(-time.Hour)
	want := map[int64]bool{}
	for i := 0; i < n; i++ {
		// Distinct next_run_at so ORDER BY next_run_at is deterministic.
		next := past.Add(time.Duration(i) * time.Minute)
		sched := &model.Schedule{
			OrgID:     org.OrgID,
			CreatedBy: org.UserID,
			Workspace: "w",
			Runner:    "r",
			CronExpr:  "* * * * *",
			Timezone:  "UTC",
			Enabled:   true,
			NextRunAt: &next,
		}
		assert.NilError(t, s.CreateSchedule(ctx, nil, sched))
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

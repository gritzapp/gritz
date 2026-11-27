package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/server/pubsub"
	"github.com/gritzapp/gritz/internal/store"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// passthroughTx is the WithTx mock: it runs the callback with a nil *sql.Tx (the
// mocked store methods ignore it), standing in for the real single transaction.
func passthroughTx(_ context.Context, tx *sql.Tx, f func(tx *sql.Tx) error) error {
	return f(tx)
}

// TestScheduler_Tick_FiresDueSchedule is the happy path: a due schedule claimed
// in the tick creates one task of its agent (a created event attributed to
// ScheduleActor, then the agent's prompt as its instruction, or a generated line
// when the agent has none), advances next_run_at to the first occurrence after
// now (skip-only), and the change notification is published after the
// transaction.
func TestScheduler_Tick_FiresDueSchedule(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{"prompt", "Bump outdated dependencies.", "Bump outdated dependencies."},
		{"no prompt", "", `You were created by schedule "nightly" of agent "deps".`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			before := time.Now()
			// A stale next_run_at three days back: the fire must realign to the future
			// rather than to the occurrence after the stored time.
			stale := before.Add(-72 * time.Hour)
			sched := &model.Schedule{
				ID:        1,
				OrgID:     7,
				AgentID:   3,
				Name:      "nightly",
				CronExpr:  "0 9 * * *",
				Timezone:  "UTC",
				Enabled:   true,
				NextRunAt: &stale,
			}
			agent := &model.Agent{
				ID:          3,
				OrgID:       7,
				Name:        "deps",
				Workspace:   "w",
				Runner:      "r",
				Prompt:      tt.prompt,
				AutoArchive: time.Hour,
				Variables:   map[string]string{"JIRA_ISSUE": "APP-142"},
			}
			st := &StoreMock{
				WithTxFunc: passthroughTx,
				ClaimDueSchedulesFunc: func(_ context.Context, _ *sql.Tx, _ int) ([]*model.Schedule, error) {
					return []*model.Schedule{sched}, nil
				},
				LockLiveAgentFunc: func(context.Context, *sql.Tx, int64, int64) error { return nil },
				GetAgentFunc: func(_ context.Context, _ *sql.Tx, orgID, id int64) (*model.Agent, error) {
					return agent, nil
				},
				CreateTaskFunc: func(_ context.Context, _ *sql.Tx, task *model.Task) error {
					task.ID = 42 // the store assigns the id the events and advance then reference
					return nil
				},
				CreateEventFunc:     func(_ context.Context, _ *sql.Tx, _ *model.Event) error { return nil },
				AdvanceScheduleFunc: func(_ context.Context, _ *sql.Tx, _, _ int64, _ store.ScheduleAdvance) error { return nil },
			}
			pub := &pubsub.PublisherMock{
				PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
			}

			sc := New(Options{Store: st, Publisher: pub})
			assert.NilError(t, sc.Tick(ctx))

			// The agent is share-locked and read in the schedule's org.
			lockCalls := st.LockLiveAgentCalls()
			assert.Assert(t, cmp.Len(lockCalls, 1))
			assert.Equal(t, lockCalls[0].OrgID, int64(7))
			assert.Equal(t, lockCalls[0].ID, int64(3))
			agentCalls := st.GetAgentCalls()
			assert.Assert(t, cmp.Len(agentCalls, 1))
			assert.Equal(t, agentCalls[0].OrgID, int64(7))
			assert.Equal(t, agentCalls[0].ID, int64(3))

			// Exactly one pending/start task of the agent, with its template and the
			// schedule's name.
			taskCalls := st.CreateTaskCalls()
			assert.Assert(t, cmp.Len(taskCalls, 1))
			assert.DeepEqual(t, taskCalls[0].Task, &model.Task{
				ID:          42,
				Name:        "nightly",
				Runner:      "r",
				Workspace:   "w",
				Status:      model.TaskStatusPending,
				Command:     model.TaskCommandStart,
				Version:     1,
				OrgID:       7,
				AgentID:     3,
				AutoArchive: time.Hour,
				Variables:   map[string]string{"JIRA_ISSUE": "APP-142"},
			})

			// The created event first (ScheduleActor) and the start command, then one
			// wake instruction event with the agent's prompt or the generated line, all
			// against the assigned task id.
			assert.DeepEqual(t, eventPayloads(st.CreateEventCalls()), []*model.Event{
				{
					TaskID: 42,
					OrgID:  7,
					Payload: &model.LifecyclePayload{
						Kind:     model.LifecycleKindCreated,
						Actor:    model.ScheduleActor,
						ToStatus: model.TaskStatusPending.Label(),
					},
				},
				{
					TaskID: 42,
					OrgID:  7,
					Payload: &model.CommandPayload{
						Command:   model.TaskCommandStart,
						Version:   1,
						Runner:    "r",
						Workspace: "w",
					},
				},
				{
					TaskID:  42,
					OrgID:   7,
					Wake:    true,
					Payload: &model.InstructionPayload{Text: tt.want},
				},
			})

			// The schedule is advanced (not disabled): next_run_at realigns to the future
			// and last_task_id points at the new task.
			advCalls := st.AdvanceScheduleCalls()
			assert.Assert(t, cmp.Len(advCalls, 1))
			assert.Equal(t, advCalls[0].ID, int64(1))
			assert.Equal(t, advCalls[0].OrgID, int64(7))
			assert.Assert(t, advCalls[0].Adv.NextRunAt != nil)
			assert.Assert(t, advCalls[0].Adv.NextRunAt.After(before), "next_run_at must realign to the future (skip-only)")
			assert.Assert(t, advCalls[0].Adv.LastTaskID != nil)
			assert.Equal(t, *advCalls[0].Adv.LastTaskID, int64(42))
			assert.Assert(t, cmp.Len(st.UpdateScheduleCalls(), 0))

			// The change notification is published after the transaction.
			pubCalls := pub.PublishCalls()
			assert.Assert(t, cmp.Len(pubCalls, 1))
			assert.Equal(t, pubCalls[0].N.OrgID, int64(7))
			assert.DeepEqual(t, pubCalls[0].N.Resources, []model.NotificationResource{
				{Action: "created", Type: "task", ID: 42},
				{Action: "appended", Type: "task_events", ID: 42},
			})
		})
	}
}

// TestScheduler_Tick_DisablesInvalidSchedule covers the invalid-at-fire-time edge
// case: when Next fails (a timezone that can no longer be resolved) the due
// occurrence still fires, then the schedule is disabled via UpdateSchedule
// (enabled=false, next_run_at=nil) rather than advanced — and no report event is
// emitted.
func TestScheduler_Tick_DisablesInvalidSchedule(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	past := time.Now().Add(-time.Hour)
	sched := &model.Schedule{
		ID:        5,
		OrgID:     7,
		AgentID:   3,
		Name:      "nightly",
		CronExpr:  "0 9 * * *",
		Timezone:  "Nowhere/Bad",
		Enabled:   true,
		NextRunAt: &past,
	}
	st := &StoreMock{
		WithTxFunc: passthroughTx,
		ClaimDueSchedulesFunc: func(_ context.Context, _ *sql.Tx, _ int) ([]*model.Schedule, error) {
			return []*model.Schedule{sched}, nil
		},
		LockLiveAgentFunc: func(context.Context, *sql.Tx, int64, int64) error { return nil },
		GetAgentFunc: func(context.Context, *sql.Tx, int64, int64) (*model.Agent, error) {
			return &model.Agent{ID: 3, OrgID: 7, Workspace: "w", Runner: "r"}, nil
		},
		CreateTaskFunc:     func(_ context.Context, _ *sql.Tx, task *model.Task) error { task.ID = 99; return nil },
		CreateEventFunc:    func(_ context.Context, _ *sql.Tx, _ *model.Event) error { return nil },
		UpdateScheduleFunc: func(_ context.Context, _ *sql.Tx, _ *model.Schedule) error { return nil },
	}
	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}

	sc := New(Options{Store: st, Publisher: pub})
	assert.NilError(t, sc.Tick(ctx))

	// The due occurrence still fired, and no report event was emitted.
	assert.Assert(t, cmp.Len(st.CreateTaskCalls(), 1))
	for _, c := range st.CreateEventCalls() {
		_, isReport := c.Event.Payload.(*model.ReportPayload)
		assert.Assert(t, !isReport, "the invalid-fire path must not emit a report event")
	}

	// Disabled, not advanced.
	assert.Assert(t, cmp.Len(st.AdvanceScheduleCalls(), 0))
	updCalls := st.UpdateScheduleCalls()
	assert.Assert(t, cmp.Len(updCalls, 1))
	assert.Assert(t, !updCalls[0].Sched.Enabled, "an unresolvable schedule must be disabled")
	assert.Assert(t, updCalls[0].Sched.NextRunAt == nil, "a disabled schedule must have a nil next_run_at")
	assert.Assert(t, updCalls[0].Sched.LastTaskID != nil, "the fire that triggered the disable is still recorded")
}

// TestScheduler_Tick_ErrorPropagatesWithoutPublishing proves publish-after-commit:
// a store error inside the transaction propagates out of Tick and nothing is
// published, since publishing happens only after WithTx returns nil.
func TestScheduler_Tick_ErrorPropagatesWithoutPublishing(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	past := time.Now().Add(-time.Hour)
	sched := &model.Schedule{
		ID:        3,
		OrgID:     7,
		AgentID:   3,
		CronExpr:  "* * * * *",
		Timezone:  "UTC",
		Enabled:   true,
		NextRunAt: &past,
	}
	boom := errors.New("boom")
	st := &StoreMock{
		WithTxFunc: passthroughTx,
		ClaimDueSchedulesFunc: func(_ context.Context, _ *sql.Tx, _ int) ([]*model.Schedule, error) {
			return []*model.Schedule{sched}, nil
		},
		LockLiveAgentFunc: func(context.Context, *sql.Tx, int64, int64) error { return nil },
		GetAgentFunc: func(context.Context, *sql.Tx, int64, int64) (*model.Agent, error) {
			return &model.Agent{ID: 3, OrgID: 7, Workspace: "w", Runner: "r"}, nil
		},
		CreateTaskFunc: func(_ context.Context, _ *sql.Tx, _ *model.Task) error { return boom },
	}
	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}

	sc := New(Options{Store: st, Publisher: pub})
	err := sc.Tick(ctx)
	assert.ErrorIs(t, err, boom)
	assert.Assert(t, cmp.Len(pub.PublishCalls(), 0), "a failed tick must not publish")
}

// eventPayloads pulls the *model.Event from each recorded CreateEvent call, in
// order, so a test can DeepEqual the whole seeded stream at once.
func eventPayloads(calls []struct {
	Ctx   context.Context
	Tx    *sql.Tx
	Event *model.Event
}) []*model.Event {
	events := make([]*model.Event, len(calls))
	for i, c := range calls {
		events[i] = c.Event
	}
	return events
}

func TestValidate(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("db down")
	agent := &model.Agent{ID: 3, OrgID: 7, Workspace: "w", Runner: "r"}
	tests := []struct {
		name    string
		sched   *model.Schedule
		lockErr error
		check   func(t *testing.T, got *model.Agent, err error)
	}{
		{
			name:  "valid",
			sched: &model.Schedule{OrgID: 7, AgentID: 3, CronExpr: "@daily", Timezone: "UTC"},
			check: func(t *testing.T, got *model.Agent, err error) {
				assert.NilError(t, err)
				assert.Equal(t, got, agent)
			},
		},
		{
			name:  "invalid cron",
			sched: &model.Schedule{OrgID: 7, AgentID: 3, CronExpr: "nonsense", Timezone: "UTC"},
			check: func(t *testing.T, _ *model.Agent, err error) { assert.Assert(t, IsInvalidError(err), err) },
		},
		{
			name:  "invalid timezone",
			sched: &model.Schedule{OrgID: 7, AgentID: 3, CronExpr: "@daily", Timezone: "Nowhere/Bad"},
			check: func(t *testing.T, _ *model.Agent, err error) { assert.Assert(t, IsInvalidError(err), err) },
		},
		{
			name:    "unknown agent",
			sched:   &model.Schedule{OrgID: 7, AgentID: 3, CronExpr: "@daily", Timezone: "UTC"},
			lockErr: sql.ErrNoRows,
			check: func(t *testing.T, _ *model.Agent, err error) {
				assert.Assert(t, IsInvalidError(err), err)
				assert.Error(t, err, "agent 3 not found")
			},
		},
		{
			name:    "store error",
			sched:   &model.Schedule{OrgID: 7, AgentID: 3, CronExpr: "@daily", Timezone: "UTC"},
			lockErr: storeErr,
			check: func(t *testing.T, _ *model.Agent, err error) {
				assert.ErrorIs(t, err, storeErr)
				assert.Assert(t, !IsInvalidError(err))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := &StoreMock{
				LockLiveAgentFunc: func(context.Context, *sql.Tx, int64, int64) error {
					return tt.lockErr
				},
				GetAgentFunc: func(context.Context, *sql.Tx, int64, int64) (*model.Agent, error) {
					return agent, nil
				},
			}
			got, err := Validate(t.Context(), nil, st, tt.sched)
			tt.check(t, got, err)
		})
	}
}

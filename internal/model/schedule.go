package model

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
)

// cronParser is the fixed 5-field parser (minute hour day-of-month month
// day-of-week) plus the @daily/@hourly/... descriptors. Pinning the accepted
// fields keeps the grammar explicit and stable: no seconds field, matching what
// users expect from crontab and GitHub Actions. See
// proposals/accepted/scheduled-tasks.md §"Schedule specification".
var cronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// ScheduleInstruction is a single to-agent instruction stored in a schedule's
// template. It is structurally identical to InstructionPayload (the instruction
// event body) but is a distinct type: a template DTO, not an event payload. On
// every occurrence it is seeded onto the new task as an InstructionPayload
// event, so the field names round-trip through the instructions JSONB column as
// [{text, url}].
type ScheduleInstruction struct {
	Text string `json:"text"`
	URL  string `json:"url,omitempty"`
}

// Schedule is a stored task template plus a cron spec. A server-side scheduler
// worker materializes a real task run on each occurrence (see
// proposals/accepted/scheduled-tasks.md). A schedule belongs to an org, not a
// user — CreatedBy is attribution only and carries no permission semantics.
type Schedule struct {
	ID        int64  `json:"id"`
	OrgID     int64  `json:"org_id"`
	CreatedBy string `json:"created_by"`
	Name      string `json:"name"`

	// Task template — the same shape CreateTask takes.
	Workspace    string                `json:"workspace"`
	Runner       string                `json:"runner"`
	Namespace    string                `json:"namespace,omitempty"`
	Instructions []ScheduleInstruction `json:"instructions"`
	AutoArchive  time.Duration         `json:"auto_archive,omitempty"`

	// Schedule spec.
	CronExpr string `json:"cron_expr"`
	Timezone string `json:"timezone"`
	Enabled  bool   `json:"enabled"`

	// Scheduler bookkeeping.
	NextRunAt  *time.Time `json:"next_run_at,omitempty"` // UTC; nil when disabled/paused
	LastRunAt  *time.Time `json:"last_run_at,omitempty"` // UTC
	LastTaskID *int64     `json:"last_task_id,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Task builds the occurrence task from the schedule's template: a fresh
// pending/start task carrying the template's runner/workspace/namespace and
// auto-archive. It is the schedule -> task half of a fire; the scheduler inserts
// it, then seeds Events(task). The result is byte-for-byte the shape CreateTask
// produces, so a scheduled task is indistinguishable from a hand-created one.
func (s *Schedule) Task() *Task {
	return &Task{
		Name:        s.Name,
		Runner:      s.Runner,
		Workspace:   s.Workspace,
		Namespace:   s.Namespace,
		Status:      TaskStatusPending,
		Command:     TaskCommandStart,
		Version:     1,
		OrgID:       s.OrgID,
		AutoArchive: s.AutoArchive,
	}
}

// Events returns the events to seed onto task for one occurrence: a
// LifecycleKindCreated event attributed to ScheduleActor, then one wake-carrying
// InstructionPayload event per template instruction (created event first, so the
// timeline ordered by event id shows "Created" first). Call it after the task has
// been inserted so task.ID is populated.
func (s *Schedule) Events(task *Task) []*Event {
	events := make([]*Event, 0, len(s.Instructions)+1)
	events = append(events, &Event{
		TaskID: task.ID,
		OrgID:  task.OrgID,
		Payload: &LifecyclePayload{
			Kind:     LifecycleKindCreated,
			Actor:    ScheduleActor,
			ToStatus: task.Status.Label(),
		},
	})
	for _, inst := range s.Instructions {
		payload := InstructionPayload(inst)
		events = append(events, &Event{
			TaskID:  task.ID,
			OrgID:   task.OrgID,
			Wake:    true,
			Payload: &payload,
		})
	}
	return events
}

// Next returns the first occurrence strictly after `after`, evaluated in the
// schedule's timezone, as UTC. The location is only used to interpret the cron
// fields; the returned instant is absolute. Evaluating in the location on every
// call (never by adding a fixed 24h) keeps local-time semantics correct across
// DST transitions. Returns the zero value and a user-facing error if the cron
// expression or timezone is invalid.
func (s *Schedule) Next(after time.Time) (time.Time, error) {
	sched, err := cronParser.Parse(s.CronExpr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron expression %q: %w", s.CronExpr, err)
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timezone %q: %w", s.Timezone, err)
	}
	return sched.Next(after.In(loc)).UTC(), nil
}

// Validate parses cron_expr and loads the timezone, returning a user-facing
// error on failure. Callers surface the error as connect.CodeInvalidArgument so
// an invalid schedule can never be stored.
func (s *Schedule) Validate() error {
	if _, err := cronParser.Parse(s.CronExpr); err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", s.CronExpr, err)
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("invalid timezone %q: %w", s.Timezone, err)
	}
	return nil
}

// Proto converts a Schedule to its protobuf representation. A nil next/last-run
// timestamp (disabled or never fired) maps to an unset proto field; a nil
// LastTaskID maps to 0 ("never run").
func (s *Schedule) Proto() *gritzv1.Schedule {
	pb := &gritzv1.Schedule{
		Id:           s.ID,
		Name:         s.Name,
		Workspace:    s.Workspace,
		Runner:       s.Runner,
		Namespace:    s.Namespace,
		Instructions: make([]*gritzv1.Instruction, len(s.Instructions)),
		CronExpr:     s.CronExpr,
		Timezone:     s.Timezone,
		Enabled:      s.Enabled,
		AutoArchive:  durationpb.New(s.AutoArchive),
		CreatedBy:    s.CreatedBy,
		CreatedAt:    timestamppb.New(s.CreatedAt),
		UpdatedAt:    timestamppb.New(s.UpdatedAt),
	}
	for i, inst := range s.Instructions {
		pb.Instructions[i] = &gritzv1.Instruction{Text: inst.Text, Url: inst.URL}
	}
	if s.NextRunAt != nil {
		pb.NextRunAt = timestamppb.New(*s.NextRunAt)
	}
	if s.LastRunAt != nil {
		pb.LastRunAt = timestamppb.New(*s.LastRunAt)
	}
	if s.LastTaskID != nil {
		pb.LastTaskId = *s.LastTaskID
	}
	return pb
}

// ScheduleInstructionsFromProto converts proto Instruction messages to the
// schedule's template DTOs (the [{text, url}] JSONB shape).
func ScheduleInstructionsFromProto(instructions []*gritzv1.Instruction) []ScheduleInstruction {
	out := make([]ScheduleInstruction, len(instructions))
	for i, inst := range instructions {
		out[i] = ScheduleInstruction{Text: inst.Text, URL: inst.Url}
	}
	return out
}

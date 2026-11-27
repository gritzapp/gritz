package model

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
	"google.golang.org/protobuf/types/known/timestamppb"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
)

// cronParser is the fixed 5-field parser (minute hour day-of-month month
// day-of-week) plus the @daily/@hourly/... descriptors. Pinning the accepted
// fields keeps the grammar explicit and stable: no seconds field, matching what
// users expect from crontab and GitHub Actions. See
// docs/src/content/docs/proposals/implemented/scheduled-tasks.md §"Schedule
// specification".
var cronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// Schedule names an agent and a cron spec. A server-side scheduler worker
// creates a task of the agent on each occurrence, starting with the agent's
// prompt (see docs/src/content/docs/proposals/draft/agent-schedules.md).
type Schedule struct {
	ID    int64  `json:"id"`
	OrgID int64  `json:"org_id"`
	Name  string `json:"name"`

	// AgentID is the agent the schedule's tasks belong to; they take its
	// template.
	AgentID int64 `json:"agent_id"`

	// Schedule spec.
	CronExpr string `json:"cron_expr"`
	Timezone string `json:"timezone"`
	Enabled  bool   `json:"enabled"`

	// Scheduler bookkeeping.
	NextRunAt  *time.Time `json:"next_run_at,omitempty"` // UTC; nil when disabled/paused
	LastRunAt  *time.Time `json:"last_run_at,omitempty"` // UTC
	LastTaskID *int64     `json:"last_task_id,omitempty"`
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
		Id:       s.ID,
		Name:     s.Name,
		AgentId:  s.AgentID,
		CronExpr: s.CronExpr,
		Timezone: s.Timezone,
		Enabled:  s.Enabled,
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

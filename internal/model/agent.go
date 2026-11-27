package model

import (
	"cmp"
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Agent is a named task template with the triggers and link kinds that create
// and wake its tasks.
type Agent struct {
	ID        int64
	OrgID     int64
	Name      string
	Workspace string
	Runner    string
	Prompt    string
	// AutoArchive is applied to the auto-archive timeout of the agent's tasks.
	// See Task.AutoArchive for the value semantics.
	AutoArchive time.Duration
	// Variables are the variables of the agent's tasks, by name, as saved.
	Variables map[string]string
	// Features are the features of the agent's tasks, by name, in the order
	// they merge, as saved.
	Features []string
	// Triggers create tasks of the agent; the first one matching an event wins.
	Triggers []AgentTrigger
	// Links are the kinds of subscribed link the agent's tasks may create.
	Links []AgentLink
	// Schedules create tasks of the agent on a cron schedule, in order. Nil
	// on agents loaded for routing.
	Schedules []*Schedule
}

// Link returns the entry whose kind is kind, and false if the agent has none.
func (a *Agent) Link(kind string) (AgentLink, bool) {
	for _, l := range a.Links {
		if l.Name() == kind {
			return l, true
		}
	}
	return AgentLink{}, false
}

// Task builds a pending task of the agent named name, as a schedule or a run
// creates it: the agent's runner, workspace, auto-archive, variables and
// features, as saved; the runner resolves the variables and applies the
// features when it starts it. The caller inserts it, then
// seeds Events(task, ...).
func (a *Agent) Task(name string) *Task {
	return &Task{
		Name:        name,
		Runner:      a.Runner,
		Workspace:   a.Workspace,
		AgentID:     a.ID,
		Status:      TaskStatusPending,
		Command:     TaskCommandStart,
		Version:     1,
		OrgID:       a.OrgID,
		AutoArchive: a.AutoArchive,
		Variables:   a.Variables,
		Features:    a.Features,
	}
}

// Events returns the events seeding task, a task Task built and the caller
// inserted: a created lifecycle event attributed to actor, the task's start
// command event, then a wake-carrying instruction with the agent's prompt and
// another with prompt, each only when set. The created event goes first, so
// the timeline ordered by event id shows "Created" first.
func (a *Agent) Events(task *Task, actor Actor, prompt string) []*Event {
	events := []*Event{{
		TaskID: task.ID,
		OrgID:  task.OrgID,
		Payload: &LifecyclePayload{
			Kind:     LifecycleKindCreated,
			Actor:    actor,
			ToStatus: task.Status.Label(),
		},
	}}
	if ev, ok := task.CommandEvent(nil); ok {
		events = append(events, ev)
	}
	for _, text := range []string{a.Prompt, prompt} {
		if text != "" {
			events = append(events, &Event{
				TaskID:  task.ID,
				OrgID:   task.OrgID,
				Wake:    true,
				Payload: &InstructionPayload{Text: text},
			})
		}
	}
	return events
}

// AgentTrigger creates a task of its agent when a matching event arrives. Source
// and Type select an event type (empty is a wildcard), every Condition must hold,
// and Public admits events from actors who aren't members of the org.
type AgentTrigger struct {
	Source     string      `json:"source,omitempty"`
	Type       string      `json:"type,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
	Public     bool        `json:"public,omitempty"`
	// YieldTo are claim names: the trigger creates no task while an
	// unarchived task holds one of them on the event's resource.
	YieldTo []string `json:"yield_to,omitempty"`
	// Link is a kind of the same agent: the created task gets a subscribed
	// link of that kind to the event's resource. Empty creates no link.
	Link string `json:"link,omitempty"`
	// Prompt is static text the created task gets as an instruction after
	// the agent's prompt. Empty adds none.
	Prompt string `json:"prompt,omitempty"`
}

// AgentLink is an entry of an agent's links: a kind of link its tasks may
// create, and what it delivers.
type AgentLink struct {
	// Type is a registered link type, e.g. "github.pr".
	Type string `json:"type"`
	// Kind names the entry within the agent; empty means Type (see Name).
	Kind string `json:"kind,omitempty"`
	// Claim is stamped on every link of this kind.
	Claim string `json:"claim,omitempty"`
	// Events are the event types a link of this kind delivers, each with
	// the conditions its events must meet. With none, it delivers nothing.
	Events []AgentLinkEvent `json:"events,omitempty"`
	// Public allows delivery of events from actors who aren't org members.
	Public bool `json:"public,omitempty"`
}

// AgentLinkEvent is an event type a link kind delivers, and the conditions,
// all of which must hold, its events must meet.
type AgentLinkEvent struct {
	Type       string      `json:"type"`
	Conditions []Condition `json:"conditions,omitempty"`
}

// Event returns the entry whose type is typ, and false if l lists none.
func (l AgentLink) Event(typ string) (AgentLinkEvent, bool) {
	for _, e := range l.Events {
		if e.Type == typ {
			return e, true
		}
	}
	return AgentLinkEvent{}, false
}

// Name returns Kind, or Type when Kind is empty.
func (l AgentLink) Name() string {
	if l.Kind != "" {
		return l.Kind
	}
	return l.Type
}

// Proto converts a to its protobuf representation.
func (a *Agent) Proto() *gritzv1.Agent {
	pb := &gritzv1.Agent{
		Id:          a.ID,
		Name:        a.Name,
		Workspace:   a.Workspace,
		Runner:      a.Runner,
		Prompt:      a.Prompt,
		AutoArchive: durationpb.New(a.AutoArchive),
		Variables:   a.Variables,
		Features:    a.Features,
	}
	for _, t := range a.Triggers {
		pb.Triggers = append(pb.Triggers, &gritzv1.AgentTrigger{
			Source:     t.Source,
			Type:       t.Type,
			Conditions: ProtoMap(t.Conditions),
			Public:     t.Public,
			YieldTo:    t.YieldTo,
			Link:       t.Link,
			Prompt:     t.Prompt,
		})
	}
	for _, l := range a.Links {
		link := &gritzv1.AgentLink{
			Type:   l.Type,
			Kind:   l.Kind,
			Claim:  l.Claim,
			Public: l.Public,
		}
		for _, e := range l.Events {
			link.Events = append(link.Events, &gritzv1.AgentLinkEvent{
				Type:       e.Type,
				Conditions: ProtoMap(e.Conditions),
			})
		}
		pb.Links = append(pb.Links, link)
	}
	for _, sched := range a.Schedules {
		pb.Schedules = append(pb.Schedules, sched.AgentProto())
	}
	return pb
}

// AgentFromProto converts a protobuf Agent of org orgID to the model type.
func AgentFromProto(orgID int64, pb *gritzv1.Agent) *Agent {
	a := &Agent{
		ID:          pb.GetId(),
		OrgID:       orgID,
		Name:        pb.GetName(),
		Workspace:   pb.GetWorkspace(),
		Runner:      pb.GetRunner(),
		Prompt:      pb.GetPrompt(),
		AutoArchive: pb.GetAutoArchive().AsDuration(),
		Variables:   pb.GetVariables(),
		Features:    pb.GetFeatures(),
	}
	for _, t := range pb.GetTriggers() {
		a.Triggers = append(a.Triggers, AgentTrigger{
			Source:     t.GetSource(),
			Type:       t.GetType(),
			Conditions: conditionsFromProto(t.GetConditions()),
			Public:     t.GetPublic(),
			YieldTo:    t.GetYieldTo(),
			Link:       t.GetLink(),
			Prompt:     t.GetPrompt(),
		})
	}
	for _, l := range pb.GetLinks() {
		link := AgentLink{
			Type:   l.GetType(),
			Kind:   l.GetKind(),
			Claim:  l.GetClaim(),
			Public: l.GetPublic(),
		}
		for _, e := range l.GetEvents() {
			link.Events = append(link.Events, AgentLinkEvent{
				Type:       e.GetType(),
				Conditions: conditionsFromProto(e.GetConditions()),
			})
		}
		a.Links = append(a.Links, link)
	}
	// The run state is output only: Schedule.Reschedule sets it from the
	// stored schedule.
	for _, sched := range pb.GetSchedules() {
		a.Schedules = append(a.Schedules, &Schedule{
			ID:       sched.GetId(),
			OrgID:    orgID,
			AgentID:  a.ID,
			Name:     sched.GetName(),
			CronExpr: sched.GetCronExpr(),
			Timezone: cmp.Or(sched.GetTimezone(), "UTC"),
			Enabled:  sched.GetEnabled(),
			Prompt:   sched.GetPrompt(),
		})
	}
	return a
}

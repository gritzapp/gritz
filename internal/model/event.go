package model

import (
	"fmt"
	"slices"
	"strings"
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Event type discriminators. These are the values of the events.type column —
// a storage detail used to pick the concrete payload on read. They are not a
// field on the Event value; the type is Payload.Type(). The instruction,
// external, report, and link arms are wired today; later increments add their
// own discriminator with each arm.
const (
	EventTypeInstruction = "instruction"
	EventTypeExternal    = "external"
	EventTypeReport      = "report"
	EventTypeLifecycle   = "lifecycle"
	EventTypeLink        = "link"
)

// EventPayload is the sealed set of event bodies — one per arm of the
// gritzv1.Event.payload oneof. Each arm reports its discriminator via Type()
// and maps itself onto the wire via SetPayloadProto, keeping the arm switch off
// Event.Proto(). The set is closed: implementations must live in this package
// because the interface is sealed with the unexported isEventPayload marker.
type EventPayload interface {
	// Type returns the discriminator stored in the events.type column.
	Type() string
	// SetPayloadProto assigns this arm onto pb.Payload. It does the assignment
	// rather than returning the arm because protoc-gen-go's oneof wrapper type
	// is unexported and unnameable from this package.
	SetPayloadProto(pb *gritzv1.Event)
	isEventPayload()
}

// InstructionPayload is the body of an instruction event — a to-agent
// instruction. It is the stream replacement for the old tasks.instructions JSON
// column and carries the same text/url parity (no actor, per #957's trimming).
type InstructionPayload struct {
	Text string `json:"text"`
	URL  string `json:"url,omitempty"`
}

func (*InstructionPayload) Type() string    { return EventTypeInstruction }
func (*InstructionPayload) isEventPayload() {}

func (p *InstructionPayload) SetPayloadProto(pb *gritzv1.Event) {
	pb.Payload = &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{
		Text: p.Text,
		Url:  p.URL,
	}}
}

// ExternalPayload is the body of an external event — a self-contained webhook
// trigger.
type ExternalPayload struct {
	Description string            `json:"description"`
	URL         string            `json:"url"`
	Data        string            `json:"data"`
	Details     map[string]string `json:"details,omitempty"`
	// Source is the coarse origin (e.g. "github", "jira") and EventType the
	// fine-grained event type (e.g. "issue_comment", "pull_request_review"),
	// both copied from the router's InputEvent. Persisting them lets consumers
	// pick an accurate icon/label instead of guessing from the URL. EventType is
	// named to avoid colliding with the Type() discriminator method; it maps to
	// the proto/json `type` field.
	Source    string `json:"source,omitempty"`
	EventType string `json:"type,omitempty"`
}

func (*ExternalPayload) Type() string    { return EventTypeExternal }
func (*ExternalPayload) isEventPayload() {}

func (p *ExternalPayload) SetPayloadProto(pb *gritzv1.Event) {
	pb.Payload = &gritzv1.Event_External{External: &gritzv1.ExternalPayload{
		Description: p.Description,
		Url:         p.URL,
		Data:        p.Data,
		Details:     p.Details,
		Source:      p.Source,
		Type:        p.EventType,
	}}
}

// ReportPayload is the body of a report event — a from-agent message written by
// the agent's report tool. It is the stream replacement for the old logs rows
// with type='llm'. Reports are not to-agent, so they never enter the brief.
type ReportPayload struct {
	Content string `json:"content"`
}

func (*ReportPayload) Type() string    { return EventTypeReport }
func (*ReportPayload) isEventPayload() {}

func (p *ReportPayload) SetPayloadProto(pb *gritzv1.Event) {
	pb.Payload = &gritzv1.Event_Report{Report: &gritzv1.ReportPayload{
		Content: p.Content,
	}}
}

// LinkPayload is the body of a link event — an about-task record of a link the
// task created. It is the timeline source of truth; the task_links row it
// mirrors is the subscription/list projection. LinkID is the task_links row id.
type LinkPayload struct {
	LinkID    int64  `json:"link_id"`
	Relevance string `json:"relevance"`
	URL       string `json:"url"`
	Title     string `json:"title,omitempty"`
	Subscribe bool   `json:"subscribe"`
}

func (*LinkPayload) Type() string    { return EventTypeLink }
func (*LinkPayload) isEventPayload() {}

func (p *LinkPayload) SetPayloadProto(pb *gritzv1.Event) {
	pb.Payload = &gritzv1.Event_Link{Link: &gritzv1.LinkPayload{
		LinkId:    p.LinkID,
		Relevance: p.Relevance,
		Url:       p.URL,
		Title:     p.Title,
		Subscribe: p.Subscribe,
	}}
}

// Actor identifies who caused a lifecycle event. Kind is one of the ActorKind*
// constants; Name is the human-readable name for user actors and empty for the
// server-internal runner/router actors.
type Actor struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

// Actor kinds. user actions carry a Name; the server-internal
// runner/router/schedule actors do not.
const (
	ActorKindUser     = "user"
	ActorKindRunner   = "runner"
	ActorKindRouter   = "router"
	ActorKindSchedule = "schedule"
)

// UserActor returns a user Actor with the given display name.
func UserActor(name string) Actor { return Actor{Kind: ActorKindUser, Name: name} }

// RunnerActor, RouterActor, and ScheduleActor are the server-internal actors for
// container lifecycle transitions, routing-rule-created tasks, and
// scheduler-fired tasks respectively.
var (
	RunnerActor   = Actor{Kind: ActorKindRunner}
	RouterActor   = Actor{Kind: ActorKindRouter}
	ScheduleActor = Actor{Kind: ActorKindSchedule}
)

// Proto converts an Actor to its protobuf representation.
func (a Actor) Proto() *gritzv1.Actor {
	return &gritzv1.Actor{Kind: a.Kind, Name: a.Name}
}

func actorFromProto(pb *gritzv1.Actor) Actor {
	if pb == nil {
		return Actor{}
	}
	return Actor{Kind: pb.Kind, Name: pb.Name}
}

// LifecycleKind is the closed set of lifecycle transitions, mapped 1:1 onto the
// gritzv1.LifecycleKind enum (the TaskChange set, minus Woken).
type LifecycleKind int32

const (
	LifecycleKindUnspecified    = LifecycleKind(gritzv1.LifecycleKind_LIFECYCLE_KIND_UNSPECIFIED)
	LifecycleKindCreated        = LifecycleKind(gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED)
	LifecycleKindUpdated        = LifecycleKind(gritzv1.LifecycleKind_LIFECYCLE_KIND_UPDATED)
	LifecycleKindCancelled      = LifecycleKind(gritzv1.LifecycleKind_LIFECYCLE_KIND_CANCELLED)
	LifecycleKindRestarted      = LifecycleKind(gritzv1.LifecycleKind_LIFECYCLE_KIND_RESTARTED)
	LifecycleKindArchived       = LifecycleKind(gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED)
	LifecycleKindUnarchived     = LifecycleKind(gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED)
	LifecycleKindAutoArchived   = LifecycleKind(gritzv1.LifecycleKind_LIFECYCLE_KIND_AUTO_ARCHIVED)
	LifecycleKindSandboxStarted = LifecycleKind(gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_STARTED)
	LifecycleKindSandboxExited  = LifecycleKind(gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_EXITED)
	LifecycleKindSandboxFailed  = LifecycleKind(gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_FAILED)
)

// LifecyclePayload is the body of a lifecycle event — an about-task record of a
// transition (created, cancelled, sandbox exited, …). It is the stream
// replacement for the old logs rows with type='audit'/'info' and the
// type='error' container-failure rows. Lifecycle events sit beside the existing
// status mutation in the same transaction; status stays a materialized
// projection. Message carries the failure detail for SANDBOX_FAILED and is
// empty for kinds that don't need it.
type LifecyclePayload struct {
	Kind       LifecycleKind `json:"kind"`
	Actor      Actor         `json:"actor"`
	FromStatus string        `json:"from_status,omitempty"`
	ToStatus   string        `json:"to_status,omitempty"`
	Message    string        `json:"message,omitempty"`
	// Fields lists the task fields that changed for LifecycleKindUpdated (e.g.
	// "name", "status") and is empty for other kinds.
	Fields []string `json:"fields,omitempty"`
}

func (*LifecyclePayload) Type() string    { return EventTypeLifecycle }
func (*LifecyclePayload) isEventPayload() {}

func (p *LifecyclePayload) SetPayloadProto(pb *gritzv1.Event) {
	pb.Payload = &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{
		Kind:       gritzv1.LifecycleKind(p.Kind),
		Actor:      p.Actor.Proto(),
		FromStatus: p.FromStatus,
		ToStatus:   p.ToStatus,
		Message:    p.Message,
		Fields:     p.Fields,
	}}
}

// Summary renders a lifecycle event as a human-readable timeline line — e.g.
// "Created by icholy", "Created by routing rule", "Cancelled", "Sandbox exited
// (Running -> Completed)", "Sandbox failed: <message>". It is the Go-side
// renderer (used by the local MCP get_my_task tool); the web UI timeline has a
// parallel renderer in TypeScript.
func (p *LifecyclePayload) Summary() string {
	var s string
	switch p.Kind {
	case LifecycleKindCreated:
		s = "Created"
	case LifecycleKindUpdated:
		s = "Updated"
		if len(p.Fields) > 0 {
			s += " " + strings.Join(p.Fields, ", ")
		}
	case LifecycleKindCancelled:
		s = "Cancelled"
	case LifecycleKindRestarted:
		s = "Restarted"
	case LifecycleKindArchived:
		s = "Archived"
	case LifecycleKindUnarchived:
		s = "Unarchived"
	case LifecycleKindAutoArchived:
		s = "Auto-archived"
	case LifecycleKindSandboxStarted:
		s = "Sandbox started"
	case LifecycleKindSandboxExited:
		s = "Sandbox exited"
		if p.FromStatus != "" && p.ToStatus != "" {
			s += fmt.Sprintf(" (%s -> %s)", p.FromStatus, p.ToStatus)
		}
	case LifecycleKindSandboxFailed:
		s = "Sandbox failed"
		if p.Message != "" {
			s += ": " + p.Message
		}
	default:
		s = "Lifecycle event"
	}
	switch {
	case p.Actor.Kind == ActorKindUser && p.Actor.Name != "":
		s += " by " + p.Actor.Name
	case p.Actor.Kind == ActorKindRouter:
		s += " by routing rule"
	}
	return s
}

// Event is one row of a task's event stream. Its body is a typed, sealed
// payload; the events.type column is materialized from Payload.Type() purely as
// a storage discriminator and is not carried on the value.
type Event struct {
	ID        int64        `json:"id"`
	TaskID    int64        `json:"task_id"`
	OrgID     int64        `json:"org_id"`
	Wake      bool         `json:"wake"`
	Payload   EventPayload `json:"payload"`
	CreatedAt time.Time    `json:"created_at"`
}

// Proto converts an Event to its protobuf representation, delegating the arm
// mapping to the payload.
func (e *Event) Proto() *gritzv1.Event {
	pb := &gritzv1.Event{
		Id:        e.ID,
		TaskId:    e.TaskID,
		Wake:      e.Wake,
		CreatedAt: timestamppb.New(e.CreatedAt),
	}
	e.Payload.SetPayloadProto(pb)
	return pb
}

// EventFromProto converts a protobuf Event to a model Event, delegating the arm
// mapping to EventPayloadFromProto.
func EventFromProto(pb *gritzv1.Event) *Event {
	e := &Event{
		ID:      pb.Id,
		TaskID:  pb.TaskId,
		Wake:    pb.Wake,
		Payload: EventPayloadFromProto(pb),
	}
	if pb.CreatedAt != nil {
		e.CreatedAt = pb.CreatedAt.AsTime()
	}
	return e
}

// FilterPayloads returns the payloads of the given events whose discriminator
// (Payload.Type()) is in types, in order. Events whose type is not in types are
// skipped.
func FilterPayloads(events []*Event, types ...string) []EventPayload {
	var payloads []EventPayload
	for _, e := range events {
		if slices.Contains(types, e.Payload.Type()) {
			payloads = append(payloads, e.Payload)
		}
	}
	return payloads
}

// EventPayloadFromProto maps the set oneof arm of pb to its typed payload. It
// is the only proto→model arm switch; it returns nil when no arm is set.
func EventPayloadFromProto(pb *gritzv1.Event) EventPayload {
	switch arm := pb.Payload.(type) {
	case *gritzv1.Event_Instruction:
		return &InstructionPayload{
			Text: arm.Instruction.Text,
			URL:  arm.Instruction.Url,
		}
	case *gritzv1.Event_External:
		return &ExternalPayload{
			Description: arm.External.Description,
			URL:         arm.External.Url,
			Data:        arm.External.Data,
			Details:     arm.External.Details,
			Source:      arm.External.Source,
			EventType:   arm.External.Type,
		}
	case *gritzv1.Event_Report:
		return &ReportPayload{
			Content: arm.Report.Content,
		}
	case *gritzv1.Event_Lifecycle:
		return &LifecyclePayload{
			Kind:       LifecycleKind(arm.Lifecycle.Kind),
			Actor:      actorFromProto(arm.Lifecycle.Actor),
			FromStatus: arm.Lifecycle.FromStatus,
			ToStatus:   arm.Lifecycle.ToStatus,
			Message:    arm.Lifecycle.Message,
			Fields:     arm.Lifecycle.Fields,
		}
	case *gritzv1.Event_Link:
		return &LinkPayload{
			LinkID:    arm.Link.LinkId,
			Relevance: arm.Link.Relevance,
			URL:       arm.Link.Url,
			Title:     arm.Link.Title,
			Subscribe: arm.Link.Subscribe,
		}
	default:
		return nil
	}
}

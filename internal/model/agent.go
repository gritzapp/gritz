package model

import (
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
	// Triggers create tasks of the agent; the first one matching an event wins.
	Triggers []AgentTrigger
	// Links are the kinds of subscribed link the agent's tasks may create.
	Links []AgentLink
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

// AgentTrigger creates a task of its agent when a matching event arrives. Source,
// Type, Conditions and Public select events as they do on a RoutingRule.
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
	// Events are the event types a link of this kind delivers; empty means
	// every event type that declares the link type.
	Events []string `json:"events,omitempty"`
	// Public allows delivery of events from actors who aren't org members.
	Public bool `json:"public,omitempty"`
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
	}
	for _, t := range a.Triggers {
		pb.Triggers = append(pb.Triggers, &gritzv1.AgentTrigger{
			Source:     t.Source,
			Type:       t.Type,
			Conditions: conditionsProto(t.Conditions),
			Public:     t.Public,
			YieldTo:    t.YieldTo,
			Link:       t.Link,
		})
	}
	for _, l := range a.Links {
		pb.Links = append(pb.Links, &gritzv1.AgentLink{
			Type:   l.Type,
			Kind:   l.Kind,
			Claim:  l.Claim,
			Events: l.Events,
			Public: l.Public,
		})
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
	}
	for _, t := range pb.GetTriggers() {
		a.Triggers = append(a.Triggers, AgentTrigger{
			Source:     t.GetSource(),
			Type:       t.GetType(),
			Conditions: conditionsFromProto(t.GetConditions()),
			Public:     t.GetPublic(),
			YieldTo:    t.GetYieldTo(),
			Link:       t.GetLink(),
		})
	}
	for _, l := range pb.GetLinks() {
		a.Links = append(a.Links, AgentLink{
			Type:   l.GetType(),
			Kind:   l.GetKind(),
			Claim:  l.GetClaim(),
			Events: l.GetEvents(),
			Public: l.GetPublic(),
		})
	}
	return a
}

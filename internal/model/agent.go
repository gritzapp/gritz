package model

import "time"

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
	Triggers []Trigger
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

// Trigger creates a task of its agent when a matching event arrives. Source,
// Type, Conditions and Public select events as they do on a RoutingRule.
type Trigger struct {
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

package model

import (
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/types/known/durationpb"
)

// RoutingRule defines a routing rule that determines whether an event should be
// routed to an org's tasks. A rule selects a (Source, Type) event kind — empty
// Source/Type are wildcards — and constrains it with a list of attribute
// Conditions (ANDed together). This is the canonical conditions-native shape:
// storage is conditions-native and rules decode by a plain JSON unmarshal.
type RoutingRule struct {
	Source     string            `json:"source,omitempty"`
	Type       string            `json:"type,omitempty"`
	Conditions []Condition       `json:"conditions,omitempty"`
	Create     *CreateTaskAction `json:"create,omitempty"`
	Wakeup     bool              `json:"wakeup,omitempty"`
	// Public lets this rule fire for actors who are not members of the org (and
	// need not be oauth-linked). Defaults false — rules are member-only unless
	// explicitly opted in.
	Public bool `json:"public,omitempty"`
	// Namespace partitions subscription matching. The router scopes this rule's
	// wake-vs-create decision to subscribers whose task shares this namespace,
	// and a created task is stamped with it. Empty is the default namespace —
	// the behavior every existing rule already has.
	Namespace string `json:"namespace,omitempty"`
}

// Condition constrains one attribute dimension of an event. Op is one of
// "equals", "prefix", or "contains"; comparisons are literal (exact,
// case-sensitive) string operations. It is the canonical conditions type the
// eventrouter matcher, registry, and store all operate on directly.
type Condition struct {
	Attr  string `json:"attr,omitempty"`
	Op    string `json:"op,omitempty"`
	Value string `json:"value,omitempty"`
}

// CreateTaskAction configures a routing rule to create a new task on
// matching events. Workspace and runner identify where the task runs;
// the optional prompt is appended as a second instruction after the
// boilerplate event preamble.
type CreateTaskAction struct {
	Workspace string `json:"workspace"`
	Runner    string `json:"runner"`
	Prompt    string `json:"prompt,omitempty"`
	// AutoArchive is applied to the created task's auto-archive timeout.
	// See Task.AutoArchive for the value semantics (0 = never, <0 =
	// immediate, >0 = delay).
	AutoArchive time.Duration `json:"auto_archive,omitempty"`
}

// Proto converts a CreateTaskAction to its protobuf representation.
func (a *CreateTaskAction) Proto() *gritzv1.CreateTaskAction {
	return &gritzv1.CreateTaskAction{
		Workspace:   a.Workspace,
		Runner:      a.Runner,
		Prompt:      a.Prompt,
		AutoArchive: durationpb.New(a.AutoArchive),
	}
}

// CreateTaskActionFromProto converts a protobuf CreateTaskAction to the model type.
func CreateTaskActionFromProto(pb *gritzv1.CreateTaskAction) *CreateTaskAction {
	if pb == nil {
		return nil
	}
	return &CreateTaskAction{
		Workspace:   pb.Workspace,
		Runner:      pb.Runner,
		Prompt:      pb.Prompt,
		AutoArchive: pb.AutoArchive.AsDuration(),
	}
}

// Proto converts a RoutingRule to its protobuf representation.
func (r *RoutingRule) Proto() *gritzv1.RoutingRule {
	pb := &gritzv1.RoutingRule{
		Source:    r.Source,
		Type:      r.Type,
		Wakeup:    r.Wakeup,
		Public:    r.Public,
		Namespace: r.Namespace,
	}
	for _, c := range r.Conditions {
		pb.Conditions = append(pb.Conditions, &gritzv1.RuleCondition{
			Attr:  c.Attr,
			Op:    c.Op,
			Value: c.Value,
		})
	}
	if r.Create != nil {
		pb.Create = r.Create.Proto()
	}
	return pb
}

// RoutingRuleFromProto converts a protobuf RoutingRule to the model type.
func RoutingRuleFromProto(pb *gritzv1.RoutingRule) RoutingRule {
	rule := RoutingRule{
		Source:    pb.Source,
		Type:      pb.Type,
		Create:    CreateTaskActionFromProto(pb.Create),
		Wakeup:    pb.Wakeup,
		Public:    pb.Public,
		Namespace: pb.Namespace,
	}
	for _, c := range pb.Conditions {
		rule.Conditions = append(rule.Conditions, Condition{
			Attr:  c.Attr,
			Op:    c.Op,
			Value: c.Value,
		})
	}
	return rule
}

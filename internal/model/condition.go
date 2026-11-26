package model

import gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"

// Condition constrains one attribute dimension of an event. Op is one of
// "equals", "prefix", "contains", or "glob"; comparisons are case-sensitive.
// The first three are literal string operations. "glob" matches the whole value
// against a pattern where * and ? don't cross /, ** spans path segments, and
// [a-z] and {a,b} work as in a shell. It is the canonical conditions type the
// eventrouter matcher, registry, and store all operate on directly.
type Condition struct {
	Attr  string `json:"attr,omitempty"`
	Op    string `json:"op,omitempty"`
	Value string `json:"value,omitempty"`
}

func conditionsProto(conds []Condition) []*gritzv1.RuleCondition {
	var pb []*gritzv1.RuleCondition
	for _, c := range conds {
		pb = append(pb, &gritzv1.RuleCondition{
			Attr:  c.Attr,
			Op:    c.Op,
			Value: c.Value,
		})
	}
	return pb
}

func conditionsFromProto(pb []*gritzv1.RuleCondition) []Condition {
	var conds []Condition
	for _, c := range pb {
		conds = append(conds, Condition{
			Attr:  c.Attr,
			Op:    c.Op,
			Value: c.Value,
		})
	}
	return conds
}

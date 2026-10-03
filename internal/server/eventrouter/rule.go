package eventrouter

import (
	"strings"

	"github.com/icholy/gritz/internal/model"
)

// Match reports whether the rule matches the event. The matcher is generic: it
// never switches on the event source and does no source-specific parsing. Empty
// Source/Type are wildcards. Each condition holds when any value the event
// carries for cond.Attr satisfies (op, value); a condition on an attr the event
// does not carry fails. Conditions AND together.
//
// It operates on model.RoutingRule / model.Condition directly (the canonical
// conditions types) and matches against the InputEvent it is given.
func Match(rule model.RoutingRule, event InputEvent) bool {
	if rule.Source != "" && rule.Source != event.Source {
		return false
	}
	if rule.Type != "" && rule.Type != event.Type {
		return false
	}
	for _, cond := range rule.Conditions {
		if !conditionMatch(cond, event.Attr(cond.Attr)) {
			return false
		}
	}
	return true
}

// conditionMatch reports whether any of the given values (the event's values for
// the condition's attr) satisfies the condition's op against its value. Passing
// an empty slice — an attr the event does not carry — always fails. Unknown ops
// never match.
func conditionMatch(c model.Condition, values []string) bool {
	for _, v := range values {
		switch c.Op {
		case "equals":
			if v == c.Value {
				return true
			}
		case "prefix":
			if strings.HasPrefix(v, c.Value) {
				return true
			}
		case "contains":
			if strings.Contains(v, c.Value) {
				return true
			}
		}
	}
	return false
}

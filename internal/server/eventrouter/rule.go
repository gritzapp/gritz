package eventrouter

import (
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/gritzapp/gritz/internal/model"
)

// Match reports whether the trigger's selector matches the event. The matcher
// is generic: it never switches on the event source and does no
// source-specific parsing. Empty Source/Type are wildcards. Each condition
// holds when any value the event carries for cond.Attr satisfies (op, value);
// a condition on an attr the event does not carry fails. A negated op holds
// exactly when its positive op does not, so it holds when no value satisfies
// the positive op, including when the event lacks the attr. Conditions AND
// together. Public is admission, not matching: the caller checks it.
func Match(trigger model.AgentTrigger, event InputEvent) bool {
	if trigger.Source != "" && trigger.Source != event.Source {
		return false
	}
	if trigger.Type != "" && trigger.Type != event.Type {
		return false
	}
	return conditionsHold(trigger.Conditions, event)
}

// conditionsHold reports whether every one of conds holds for the event: as
// in Match, a positive condition holds when any value the event carries for its
// attr satisfies it, and a negated one when none does.
func conditionsHold(conds []model.Condition, event InputEvent) bool {
	for _, cond := range conds {
		if !conditionMatch(cond, event.Attr(cond.Attr)) {
			return false
		}
	}
	return true
}

// conditionMatch reports whether the condition holds for the given values (the
// event's values for the condition's attr). A positive op holds when any value
// satisfies it against the condition's value, so an empty slice — an attr the
// event does not carry — fails. A negated op ("not_" + a positive op) holds
// exactly when its positive op does not: when no value satisfies it, including
// when the event does not carry the attr. Unknown ops never match.
func conditionMatch(c model.Condition, values []string) bool {
	switch c.Op {
	case "not_equals", "not_prefix", "not_contains", "not_glob":
		return !anyValueMatches(strings.TrimPrefix(c.Op, "not_"), c.Value, values)
	}
	return anyValueMatches(c.Op, c.Value, values)
}

// anyValueMatches reports whether any of values satisfies the positive op
// against want. Unknown ops never match.
func anyValueMatches(op, want string, values []string) bool {
	for _, v := range values {
		switch op {
		case "equals":
			if v == want {
				return true
			}
		case "prefix":
			if strings.HasPrefix(v, want) {
				return true
			}
		case "contains":
			if strings.Contains(v, want) {
				return true
			}
		case "glob":
			// ValidateAgent rejects malformed patterns before a trigger is saved;
			// MatchUnvalidated reports false for one rather than erroring.
			if doublestar.MatchUnvalidated(want, v) {
				return true
			}
		}
	}
	return false
}

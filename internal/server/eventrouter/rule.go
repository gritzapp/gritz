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
// a condition on an attr the event does not carry fails. A negated condition
// holds exactly when it would not without Negate: when no value satisfies (op,
// value), including when the event lacks the attr. Conditions AND
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
// in Match, a condition holds when any value the event carries for its attr
// satisfies it, or with Negate, when none does.
func conditionsHold(conds []model.Condition, event InputEvent) bool {
	for _, cond := range conds {
		if !conditionMatch(cond, event.Attr(cond.Attr)) {
			return false
		}
	}
	return true
}

// conditionMatch reports whether the condition holds for the given values (the
// event's values for the condition's attr): whether any of them satisfies the
// condition's op against its value, inverted when the condition is negated. An
// empty slice — an attr the event does not carry — satisfies no op, so it fails
// a condition and holds for a negated one. An unknown op satisfies no value;
// ValidateAgent rejects one before a trigger is saved.
func conditionMatch(c model.Condition, values []string) bool {
	return anyValueMatches(c, values) != c.Negate
}

// anyValueMatches reports whether any of values satisfies c's op against
// c.Value, ignoring c.Negate.
func anyValueMatches(c model.Condition, values []string) bool {
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
		case "glob":
			// ValidateAgent rejects malformed patterns before a trigger is saved;
			// MatchUnvalidated reports false for one rather than erroring.
			if doublestar.MatchUnvalidated(c.Value, v) {
				return true
			}
		}
	}
	return false
}

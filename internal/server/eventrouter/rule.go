package eventrouter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/taskvar"
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
		case "glob":
			// Validate rejects malformed patterns before a rule is saved;
			// MatchUnvalidated reports false for one rather than erroring.
			if doublestar.MatchUnvalidated(c.Value, v) {
				return true
			}
		}
	}
	return false
}

// invalidError is a rule ValidateRule rejects: one its event-type registry
// rejects, or a create action whose workspace isn't registered on its runner
// or whose variables the workspace doesn't accept.
type invalidError struct {
	err error
}

func (e *invalidError) Error() string { return e.err.Error() }

func (e *invalidError) Unwrap() error { return e.err }

// IsInvalidError reports whether err is ValidateRule rejecting the rule, as
// opposed to failing to check it.
func IsInvalidError(err error) bool {
	_, ok := errors.AsType[*invalidError](err)
	return ok
}

// ValidateRule checks a rule of org orgID before it is saved: against reg, and,
// for a rule that creates tasks, that its workspace is registered and its
// variables against the workspace's declarations. IsInvalidError tells a
// rejection from a store error.
func ValidateRule(ctx context.Context, tx *sql.Tx, st *store.Store, reg *SchemaRegistry, orgID int64, rule model.RoutingRule) error {
	if err := reg.Validate(rule); err != nil {
		return &invalidError{err: err}
	}
	if rule.Create == nil {
		return nil
	}
	decls, err := st.GetWorkspaceVariables(ctx, tx, rule.Create.Runner, rule.Create.Workspace, orgID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &invalidError{err: fmt.Errorf("workspace %q not found on runner %q", rule.Create.Workspace, rule.Create.Runner)}
		}
		return err
	}
	if _, err := taskvar.Resolve(decls, rule.Create.Variables); err != nil {
		return &invalidError{err: fmt.Errorf("workspace %q: %w", rule.Create.Workspace, err)}
	}
	return nil
}

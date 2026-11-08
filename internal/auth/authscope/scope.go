// Package authscope implements a scope-matching engine for authorization: a
// scope is a pattern over an (operation-path, attributes) pair, and a caller
// checks a concrete operation-path and attributes against the scopes it holds.
// The matching engine (Scope, Scopes, Allow) is taxonomy-agnostic; the concrete
// task-caller taxonomy it is used with — operation paths, attribute keys, and
// the WithTask* attribute constructors — lives in task.go.
//
// See docs/src/content/docs/proposals/implemented/scope-based-permissions.md
// for the design.
package authscope

import (
	"strconv"
)

// Scope is a single capability pattern. Op is the operation path; each segment
// is a single token, or "*" to match any one segment.
type Scope struct {
	Op []string
	// Preds are constraints that only ever NARROW the grant: each key pins an
	// attribute to a single allowed value, so adding a predicate can only shrink
	// the set of requests the scope matches, never widen it. A key that is absent
	// is unconstrained (any value of that attribute is allowed), so an empty Preds
	// is the broadest grant for the operation and matches any instance.
	//
	// Matching tests these predicates against the request's attributes — never the
	// reverse. The scope decides what it constrains; attributes the scope does not
	// mention are ignored, and a constrained key the request omits fails to match.
	// There is no predicate wildcard: a "*" value is matched literally.
	Preds map[string]string
}

// Attr is a single concrete attribute of a request: an application attribute key
// and its value, already stringified.
type Attr struct {
	Name  string
	Value string
}

// Int64Attr builds an Attr from an int64 value, centralizing the int->string
// conversion so call sites never repeat strconv.FormatInt.
func Int64Attr(name string, v int64) Attr {
	return Attr{Name: name, Value: strconv.FormatInt(v, 10)}
}

// StringAttr builds an Attr from a string value.
func StringAttr(name, v string) Attr {
	return Attr{Name: name, Value: v}
}

// New builds a Scope from an operation path and its attributes, folding the
// attrs into the Preds map. Preds is left nil when there are no attrs, so a
// built scope is structurally identical to a parsed one.
func New(op []string, attrs ...Attr) Scope {
	if len(attrs) == 0 {
		return Scope{Op: op}
	}
	preds := make(map[string]string, len(attrs))
	for _, a := range attrs {
		preds[a.Name] = a.Value
	}
	return Scope{Op: op, Preds: preds}
}

// Scopes is a caller's held scopes. Allow is an OR across them.
type Scopes []Scope

// AdminScope is the global-admin wildcard grant: any two-segment operation on
// any instance. It is the capability half of today's omnipotent-within-org
// caller. AdminScope is taxonomy-agnostic — it depends only on the operation
// arity (two segments), not on what the segments mean.
const AdminScope = "*.*"

// Admin returns a Scopes containing only AdminScope, the global-admin grant.
func Admin() Scopes {
	s, err := Parse(AdminScope)
	if err != nil {
		// AdminScope is a compile-time constant and always parses.
		panic(err)
	}
	return Scopes{s}
}

// Allow reports whether any held scope permits operation op — the segments of a
// path, such as OpTaskWrite — on a request described by attrs. A scope permits
// it when its operation pattern covers op (same number of segments; each segment
// matches, with "*" matching any) and every predicate the scope constrains
// equals the corresponding attr (AND across keys). An absent predicate key is
// unconstrained; a constrained key whose attr is missing from attrs, or differs,
// denies.
func (scopes Scopes) Allow(op []string, attrs ...Attr) bool {
	for _, s := range scopes {
		if s.allow(op, attrs) {
			return true
		}
	}
	return false
}

// AllowOp reports whether any held scope's operation path covers op, ignoring
// predicates entirely — "do I hold any scope for this capability at all?", with no
// concrete instance to test. A predicated scope like task.write:{"task.id":"5"}
// still satisfies AllowOp(OpTaskWrite): it answers capability presence, not
// instance access. Use it only for genuine no-instance capability questions (e.g.
// OpTaskTokenCreate), never as the pre-half of an instance check — see
// docs/src/content/docs/proposals/implemented/eliminate-runner-socket-proxy.md §5.
func (scopes Scopes) AllowOp(op []string) bool {
	for _, s := range scopes {
		if s.allowOp(op) {
			return true
		}
	}
	return false
}

// allowOp reports whether the single scope's operation path covers op: the same
// number of segments, each matching with "*" matching any one segment. It ignores
// Preds. allow is exactly allowOp plus the predicate loop, so the two cannot drift.
func (s Scope) allowOp(op []string) bool {
	if len(s.Op) != len(op) {
		return false
	}
	for i, seg := range s.Op {
		// "*" matches any single segment.
		if seg != "*" && seg != op[i] {
			return false
		}
	}
	return true
}

// allow reports whether the single scope permits the operation segments and
// attributes.
func (s Scope) allow(op []string, attrs []Attr) bool {
	if !s.allowOp(op) {
		return false
	}
	for key, want := range s.Preds {
		got, ok := attrValue(attrs, key)
		if !ok || got != want {
			return false
		}
	}
	return true
}

// attrValue returns the value of the first attr with the given name. Callers
// build attrs from the typed Attr constructors and never repeat a name, so
// first-match-wins needs no guard.
func attrValue(attrs []Attr, name string) (string, bool) {
	for _, a := range attrs {
		if a.Name == name {
			return a.Value, true
		}
	}
	return "", false
}

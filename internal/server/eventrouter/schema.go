package eventrouter

import (
	"fmt"
	"regexp"
	"slices"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/gritzapp/gritz/internal/model"
)

// SourceCustom is the event source of org-defined custom event types.
const SourceCustom = "custom"

// reCustomName constrains custom event type names and attr keys to the
// style of the built-in names, e.g. "comment_created".
var reCustomName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ValidateCustomEventTypes checks an org's full list of custom event types
// against r, the built-in registry, returning an error naming the first
// offending def, attr or link type. Every def must have Source SourceCustom, a
// Type matching reCustomName that is unique in defs, and a non-empty Label.
// Every attr Key must match reCustomName and be unique within its def, and
// each def must declare the derived "body" and "url" attrs (see
// InputEvent.Attr); they are never added on its behalf. Its LinkTypes must be
// distinct link types registered in r under SourceCustom.
func (r *SchemaRegistry) ValidateCustomEventTypes(defs []model.EventTypeDef) error {
	types := map[string]bool{}
	for i, def := range defs {
		if def.Source != SourceCustom {
			return fmt.Errorf("event type %d: source must be %q, got %q", i+1, SourceCustom, def.Source)
		}
		if !reCustomName.MatchString(def.Type) {
			return fmt.Errorf("event type %d: type %q must match %s", i+1, def.Type, reCustomName)
		}
		if types[def.Type] {
			return fmt.Errorf("event type %d: duplicate type %q", i+1, def.Type)
		}
		types[def.Type] = true
		if def.Label == "" {
			return fmt.Errorf("event type %q: empty label", def.Type)
		}
		keys := map[string]bool{}
		for _, attr := range def.Attrs {
			if !reCustomName.MatchString(attr.Key) {
				return fmt.Errorf("event type %q: attr key %q must match %s", def.Type, attr.Key, reCustomName)
			}
			if keys[attr.Key] {
				return fmt.Errorf("event type %q: duplicate attr key %q", def.Type, attr.Key)
			}
			keys[attr.Key] = true
		}
		for _, key := range []string{"body", "url"} {
			if !keys[key] {
				return fmt.Errorf("event type %q: missing required attr %q", def.Type, key)
			}
		}
		for i, name := range def.LinkTypes {
			if lt, ok := r.LinkType(name); !ok || lt.Source != SourceCustom {
				return fmt.Errorf("event type %q: unknown link type %q", def.Type, name)
			}
			if slices.Contains(def.LinkTypes[:i], name) {
				return fmt.Errorf("event type %q: duplicate link type %q", def.Type, name)
			}
		}
	}
	return nil
}

// SchemaRegistry holds the set of registered event-type schemas and the routing
// state derived from them (the source:type index). It is populated by
// MustRegister rather than a central table. Construct one with
// NewSchemaRegistry, which initializes the index map.
type SchemaRegistry struct {
	// eventTypes holds every registered schema in registration order. It is the
	// machine-readable contract that the router, rule validation, and (later) the
	// UI derive from, replacing hand-synced copies of the (source, type) pairs
	// and their applicable attrs.
	eventTypes []model.EventTypeDef

	// eventTypeByKey indexes eventTypes by "source:type" for O(1) lookup.
	eventTypeByKey map[string]model.EventTypeDef

	// linkTypes holds every registered link type in registration order.
	linkTypes []model.LinkTypeDef

	// linkTypeByName indexes linkTypes by name.
	linkTypeByName map[string]model.LinkTypeDef
}

// NewSchemaRegistry returns an empty registry ready for registration, with its
// index maps initialized.
func NewSchemaRegistry() *SchemaRegistry {
	return &SchemaRegistry{
		eventTypeByKey: map[string]model.EventTypeDef{},
		linkTypeByName: map[string]model.LinkTypeDef{},
	}
}

// MustRegister records def as the schema for its (Source, Type) event kind,
// making it available to EventTypeFor and EventTypes. It panics if a def with
// the same (Source, Type) is already registered, or if one of its LinkTypes
// isn't registered under the same source: an integration registers its link
// types before the event types that name them.
func (r *SchemaRegistry) MustRegister(def model.EventTypeDef) {
	key := def.Source + ":" + def.Type
	if _, dup := r.eventTypeByKey[key]; dup {
		panic(fmt.Sprintf("eventrouter: duplicate schema registration for source=%q type=%q", def.Source, def.Type))
	}
	for _, name := range def.LinkTypes {
		lt, ok := r.linkTypeByName[name]
		if !ok || lt.Source != def.Source {
			panic(fmt.Sprintf("eventrouter: event type source=%q type=%q names link type %q not registered for its source", def.Source, def.Type, name))
		}
	}
	r.eventTypes = append(r.eventTypes, def)
	r.eventTypeByKey[key] = def
}

// EventTypeFor returns the registry entry for a (source, type) pair, and false
// if none is registered.
func (r *SchemaRegistry) EventTypeFor(source, typ string) (model.EventTypeDef, bool) {
	def, ok := r.eventTypeByKey[source+":"+typ]
	return def, ok
}

// EventTypes returns every registered schema in registration order. It backs
// iteration over the registry and the GetEventTypes RPC.
func (r *SchemaRegistry) EventTypes() []model.EventTypeDef {
	return slices.Clone(r.eventTypes)
}

// MustRegisterLinkType records def, making it available to LinkType, LinkTypes
// and the event types that name it. It panics on a duplicate name.
func (r *SchemaRegistry) MustRegisterLinkType(def model.LinkTypeDef) {
	if _, dup := r.linkTypeByName[def.Name]; dup {
		panic(fmt.Sprintf("eventrouter: duplicate link type registration %q", def.Name))
	}
	r.linkTypes = append(r.linkTypes, def)
	r.linkTypeByName[def.Name] = def
}

// LinkType returns the link type named name, and false if none is registered.
func (r *SchemaRegistry) LinkType(name string) (model.LinkTypeDef, bool) {
	def, ok := r.linkTypeByName[name]
	return def, ok
}

// LinkTypes returns every registered link type in registration order.
func (r *SchemaRegistry) LinkTypes() []model.LinkTypeDef {
	return slices.Clone(r.linkTypes)
}

// CanonicalURL returns the canonical URL of an event of type def at raw: that
// of the first of def's link types that accepts raw, and false if none does.
func (r *SchemaRegistry) CanonicalURL(def model.EventTypeDef, raw string) (string, bool) {
	for _, name := range def.LinkTypes {
		if canonical, ok := r.linkTypeByName[name].Canonical(raw); ok {
			return canonical, true
		}
	}
	return "", false
}

// With returns a new registry holding r's link types and schemas followed by
// defs, leaving r unchanged. It panics, like MustRegister, if a def duplicates
// a registered (Source, Type) or another def.
func (r *SchemaRegistry) With(defs ...model.EventTypeDef) *SchemaRegistry {
	reg := NewSchemaRegistry()
	for _, def := range r.linkTypes {
		reg.MustRegisterLinkType(def)
	}
	for _, def := range slices.Concat(r.eventTypes, defs) {
		reg.MustRegister(def)
	}
	return reg
}

// Validate checks the rule against the event-type registry, returning a wrapped
// error naming the first offending selector, op, or attr. Every rule must name
// exactly one registered (Source, Type) event type: an empty Type is rejected,
// and — because the registry is keyed by (Source, Type) and label_added exists
// under multiple sources — an empty Source is rejected too. Each condition's op
// must be equals/prefix/contains/glob, a glob's value must be a well-formed
// pattern, and its attr must be one of the selected event type's valid attrs
// (its Attrs set, which includes the derived body/url).
// A custom rule must also be Public: custom events are ingested without an
// actor, so routing only considers the org's public rules for them.
func (r *SchemaRegistry) Validate(rule model.RoutingRule) error {
	_, err := r.validateSelector("rule", rule.Source, rule.Type, rule.Conditions, rule.Public)
	return err
}

// validateSelector checks an event selector, a rule's or a trigger's, as
// Validate describes, and returns the event type it selects. what names the
// selector in the custom-must-be-public error.
func (r *SchemaRegistry) validateSelector(what, source, typ string, conds []model.Condition, public bool) (model.EventTypeDef, error) {
	if typ == "" {
		return model.EventTypeDef{}, fmt.Errorf("%s must select an event type: empty type", what)
	}
	if source == "" {
		return model.EventTypeDef{}, fmt.Errorf("%s must select an event source: empty source for type %q", what, typ)
	}
	if source == SourceCustom && !public {
		return model.EventTypeDef{}, fmt.Errorf("custom event %ss must be public: custom events have no actor, so a member-only %s would never fire", what, what)
	}
	def, ok := r.EventTypeFor(source, typ)
	if !ok {
		return model.EventTypeDef{}, fmt.Errorf("unknown event type: source=%q type=%q", source, typ)
	}
	for _, cond := range conds {
		switch cond.Op {
		case "equals", "prefix", "contains":
		case "glob":
			if !doublestar.ValidatePattern(cond.Value) {
				return model.EventTypeDef{}, fmt.Errorf("invalid glob %q on attr %q", cond.Value, cond.Attr)
			}
		default:
			return model.EventTypeDef{}, fmt.Errorf("unknown op %q on attr %q", cond.Op, cond.Attr)
		}
		if !def.HasAttr(cond.Attr) {
			return model.EventTypeDef{}, fmt.Errorf("attr %q not valid for event type %q/%q", cond.Attr, source, typ)
		}
	}
	return def, nil
}

// ValidateAgent checks an agent's link kinds and triggers against the
// registry, returning an error naming the first offending entry. Every link
// entry's Type must be a registered link type, its kind (see
// model.AgentLink.Name) unique in the agent, and each of its Events an event
// type of the link type's source that declares the link type. Every trigger's
// selector is checked as Validate checks a rule's, and its Link, when set,
// must be a kind of the agent whose type the trigger's event type declares.
// Claims and YieldTo names aren't checked: they are free-form, org-wide names.
func (r *SchemaRegistry) ValidateAgent(a *model.Agent) error {
	kinds := map[string]bool{}
	for i, l := range a.Links {
		lt, ok := r.LinkType(l.Type)
		if !ok {
			return fmt.Errorf("link %d: unknown link type %q", i+1, l.Type)
		}
		kind := l.Name()
		if kinds[kind] {
			return fmt.Errorf("link %d: duplicate kind %q", i+1, kind)
		}
		kinds[kind] = true
		for _, typ := range l.Events {
			def, ok := r.EventTypeFor(lt.Source, typ)
			if !ok {
				return fmt.Errorf("link kind %q: unknown event type: source=%q type=%q", kind, lt.Source, typ)
			}
			if !slices.Contains(def.LinkTypes, l.Type) {
				return fmt.Errorf("link kind %q: event type %q/%q doesn't declare link type %q", kind, lt.Source, typ, l.Type)
			}
		}
	}
	for i, t := range a.Triggers {
		def, err := r.validateSelector("trigger", t.Source, t.Type, t.Conditions, t.Public)
		if err != nil {
			return fmt.Errorf("trigger %d: %w", i+1, err)
		}
		if t.Link == "" {
			continue
		}
		l, ok := a.Link(t.Link)
		if !ok {
			return fmt.Errorf("trigger %d: unknown link kind %q", i+1, t.Link)
		}
		if !slices.Contains(def.LinkTypes, l.Type) {
			return fmt.Errorf("trigger %d: event type %q/%q doesn't declare link type %q of kind %q", i+1, t.Source, t.Type, l.Type, t.Link)
		}
	}
	return nil
}

// DefaultSchemaRegistry is the process-wide registry of built-in event types.
// The producer packages (githubserver, atlassianserver, customserver) populate
// it, link types included, from their init functions via their
// RegisterSchemas helper. Handlers that validate rules
// or list event types combine it with an org's custom types through With.
var DefaultSchemaRegistry = NewSchemaRegistry()

package eventrouter

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"

	"github.com/gritzapp/gritz/internal/model"
)

// SourceCustom is the event source of org-defined custom event types.
const SourceCustom = "custom"

// reCustomName constrains custom event type names and attr keys to the
// style of the built-in names, e.g. "comment_created".
var reCustomName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ValidateCustomEventTypes checks an org's full list of custom event types,
// returning an error naming the first offending def or attr. Every def must
// have Source SourceCustom, a Type matching reCustomName that is unique
// in defs, and a non-empty Label. Every attr Key must match reCustomName
// and be unique within its def, and each def must declare the derived "body"
// and "url" attrs (see InputEvent.Attr); they are never added on its behalf.
func ValidateCustomEventTypes(defs []model.EventTypeDef) error {
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
}

// NewSchemaRegistry returns an empty registry ready for registration, with its
// source:type index map initialized.
func NewSchemaRegistry() *SchemaRegistry {
	return &SchemaRegistry{
		eventTypeByKey: map[string]model.EventTypeDef{},
	}
}

// MustRegister records def as the schema for its (Source, Type) event kind,
// making it available to EventTypeFor and EventTypes. It panics if a def with
// the same (Source, Type) is already registered.
func (r *SchemaRegistry) MustRegister(def model.EventTypeDef) {
	key := def.Source + ":" + def.Type
	if _, dup := r.eventTypeByKey[key]; dup {
		panic(fmt.Sprintf("eventrouter: duplicate schema registration for source=%q type=%q", def.Source, def.Type))
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

// With returns a new registry holding r's schemas followed by defs, leaving r
// unchanged. It panics, like MustRegister, if a def duplicates a registered
// (Source, Type) or another def.
func (r *SchemaRegistry) With(defs ...model.EventTypeDef) *SchemaRegistry {
	reg := NewSchemaRegistry()
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
// must be equals/prefix/contains, and its attr must be one of the selected event
// type's valid attrs (its Attrs set, which includes the derived body/url).
// A custom rule must also be Public: custom events are ingested without an
// actor, so routing only considers the org's public rules for them.
func (r *SchemaRegistry) Validate(rule model.RoutingRule) error {
	if rule.Type == "" {
		return fmt.Errorf("rule must select an event type: empty type")
	}
	if rule.Source == "" {
		return fmt.Errorf("rule must select an event source: empty source for type %q", rule.Type)
	}
	if rule.Source == SourceCustom && !rule.Public {
		return fmt.Errorf("custom event rules must be public: custom events have no actor, so a member-only rule would never fire")
	}
	def, ok := r.EventTypeFor(rule.Source, rule.Type)
	if !ok {
		return fmt.Errorf("unknown event type: source=%q type=%q", rule.Source, rule.Type)
	}
	for _, cond := range rule.Conditions {
		switch cond.Op {
		case "equals", "prefix", "contains":
		default:
			return fmt.Errorf("unknown op %q on attr %q", cond.Op, cond.Attr)
		}
		if !def.HasAttr(cond.Attr) {
			return fmt.Errorf("attr %q not valid for event type %q/%q", cond.Attr, rule.Source, rule.Type)
		}
	}
	return nil
}

// ValidateEvent checks e against its type's schema: the type must be registered
// for e.Source, URL must be an absolute http or https URL, and every attr
// must be declared by the type. "body" and "url" can't be attrs; they are
// derived from Data and URL. It returns the type's definition.
func (r *SchemaRegistry) ValidateEvent(e InputEvent) (model.EventTypeDef, error) {
	def, ok := r.EventTypeFor(e.Source, e.Type)
	if !ok {
		return model.EventTypeDef{}, fmt.Errorf("unknown event type %q", e.Type)
	}
	if e.URL == "" {
		return model.EventTypeDef{}, errors.New("url is required")
	}
	u, err := url.Parse(e.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return model.EventTypeDef{}, fmt.Errorf("url %q must be an absolute http or https URL", e.URL)
	}
	for _, key := range slices.Sorted(maps.Keys(e.Attrs)) {
		// InputEvent.Attr derives these from Data and URL, so a value sent
		// here would be silently ignored.
		if key == "body" || key == "url" {
			return model.EventTypeDef{}, fmt.Errorf("attr %q must be sent as the envelope's %q field", key, key)
		}
		if !def.HasAttr(key) {
			return model.EventTypeDef{}, fmt.Errorf("attr %q not declared by event type %q", key, def.Type)
		}
	}
	return def, nil
}

// DefaultSchemaRegistry is the process-wide registry of built-in event types.
// The producer packages (githubserver, atlassianserver) populate it from their
// init functions via their RegisterSchemas helper. Handlers that validate rules
// or list event types combine it with an org's custom types through With.
var DefaultSchemaRegistry = NewSchemaRegistry()

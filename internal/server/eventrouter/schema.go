package eventrouter

import (
	"fmt"
	"slices"

	"github.com/icholy/gritz/internal/model"
)

// AttrDef is a self-describing attribute dimension a rule may condition on. Key
// is the machine name conditions reference and that Validate/the matcher check
// against; Label, Help, and Placeholder are display copy the routing-rule editor
// renders straight from the schema (rather than hardcoding it per attr/source).
type AttrDef struct {
	Key         string // machine name, e.g. "body", "mention"
	Label       string // human label for the attr dropdown, e.g. "Mention"
	Help        string // one-line help shown under the condition row
	Placeholder string // example value for the condition's value input
}

// EventTypeDef declares a (Source, Type) event kind and the complete set of
// attribute dimensions a rule may condition on for that kind. Attrs is that full
// set: it always includes the derived views "body" and "url" (see
// InputEvent.Attr) plus the type's own emitted dimensions. Each AttrDef carries
// its own display copy so clients render labels/help/placeholders from the
// schema.
type EventTypeDef struct {
	Source string
	Type   string
	Label  string    // human label, e.g. "GitHub: Issue/PR Comment"
	Attrs  []AttrDef // complete valid attr set, including derived body/url
}

// hasAttr reports whether def declares an attr with the given key.
func (def EventTypeDef) hasAttr(key string) bool {
	return slices.ContainsFunc(def.Attrs, func(a AttrDef) bool { return a.Key == key })
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
	eventTypes []EventTypeDef

	// eventTypeByKey indexes eventTypes by "source:type" for O(1) lookup.
	eventTypeByKey map[string]EventTypeDef
}

// NewSchemaRegistry returns an empty registry ready for registration, with its
// source:type index map initialized.
func NewSchemaRegistry() *SchemaRegistry {
	return &SchemaRegistry{
		eventTypeByKey: map[string]EventTypeDef{},
	}
}

// MustRegister records def as the schema for its (Source, Type) event kind,
// making it available to EventTypeFor and EventTypes. It panics if a def with
// the same (Source, Type) is already registered.
func (r *SchemaRegistry) MustRegister(def EventTypeDef) {
	key := def.Source + ":" + def.Type
	if _, dup := r.eventTypeByKey[key]; dup {
		panic(fmt.Sprintf("eventrouter: duplicate schema registration for source=%q type=%q", def.Source, def.Type))
	}
	r.eventTypes = append(r.eventTypes, def)
	r.eventTypeByKey[key] = def
}

// EventTypeFor returns the registry entry for a (source, type) pair, and false
// if none is registered.
func (r *SchemaRegistry) EventTypeFor(source, typ string) (EventTypeDef, bool) {
	def, ok := r.eventTypeByKey[source+":"+typ]
	return def, ok
}

// EventTypes returns every registered schema in registration order. It backs
// iteration over the registry and the GetEventTypes RPC.
func (r *SchemaRegistry) EventTypes() []EventTypeDef {
	return slices.Clone(r.eventTypes)
}

// Validate checks the rule against the event-type registry, returning a wrapped
// error naming the first offending selector, op, or attr. Every rule must name
// exactly one registered (Source, Type) event type: an empty Type is rejected,
// and — because the registry is keyed by (Source, Type) and label_added exists
// under multiple sources — an empty Source is rejected too. Each condition's op
// must be equals/prefix/contains, and its attr must be one of the selected event
// type's valid attrs (its Attrs set, which includes the derived body/url).
func (r *SchemaRegistry) Validate(rule model.RoutingRule) error {
	if rule.Type == "" {
		return fmt.Errorf("rule must select an event type: empty type")
	}
	if rule.Source == "" {
		return fmt.Errorf("rule must select an event source: empty source for type %q", rule.Type)
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
		if !def.hasAttr(cond.Attr) {
			return fmt.Errorf("attr %q not valid for event type %q/%q", cond.Attr, rule.Source, rule.Type)
		}
	}
	return nil
}

// DefaultSchemaRegistry is the process-wide registry the producer packages
// populate from their init functions (githubserver, atlassianserver) via their
// RegisterSchemas helper. The apiserver GetEventTypes handler reads from it, and
// the server startup wiring hands it to the store as the routing-rule
// translator.
var DefaultSchemaRegistry = NewSchemaRegistry()

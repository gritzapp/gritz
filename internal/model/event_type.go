package model

import (
	"slices"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
)

// AttrDef is a self-describing attribute dimension a routing rule may condition
// on. Key is the machine name conditions reference and that rule validation and
// the matcher check against; Label, Help, and Placeholder are display copy the
// routing-rule editor renders straight from the schema (rather than hardcoding
// it per attr/source).
type AttrDef struct {
	Key         string `json:"key"`                   // machine name, e.g. "body", "mention"
	Label       string `json:"label,omitempty"`       // human label for the attr dropdown, e.g. "Mention"
	Help        string `json:"help,omitempty"`        // one-line help shown under the condition row
	Placeholder string `json:"placeholder,omitempty"` // example value for the condition's value input
}

// EventTypeDef declares a (Source, Type) event kind and the complete set of
// attribute dimensions a rule may condition on for that kind. Attrs is that full
// set: it always includes the derived views "body" and "url" (see
// eventrouter.InputEvent.Attr) plus the type's own emitted dimensions. Each
// AttrDef carries its own display copy so clients render
// labels/help/placeholders from the schema.
type EventTypeDef struct {
	Source string    `json:"source"`
	Type   string    `json:"type"`
	Label  string    `json:"label,omitempty"` // human label, e.g. "GitHub: Issue/PR Comment"
	Attrs  []AttrDef `json:"attrs,omitempty"` // complete valid attr set, including derived body/url
}

// HasAttr reports whether def declares an attr with the given key.
func (def EventTypeDef) HasAttr(key string) bool {
	_, ok := def.Attr(key)
	return ok
}

// Attr returns def's attr with the given key, and false if it has none.
func (def EventTypeDef) Attr(key string) (AttrDef, bool) {
	i := slices.IndexFunc(def.Attrs, func(a AttrDef) bool { return a.Key == key })
	if i < 0 {
		return AttrDef{}, false
	}
	return def.Attrs[i], true
}

// Proto converts def to its protobuf representation.
func (def EventTypeDef) Proto() *gritzv1.EventTypeDef {
	attrs := make([]*gritzv1.AttrDef, len(def.Attrs))
	for i, attr := range def.Attrs {
		attrs[i] = &gritzv1.AttrDef{
			Key:         attr.Key,
			Label:       attr.Label,
			Help:        attr.Help,
			Placeholder: attr.Placeholder,
		}
	}
	return &gritzv1.EventTypeDef{
		Source: def.Source,
		Type:   def.Type,
		Label:  def.Label,
		Attrs:  attrs,
	}
}

// EventTypeDefFromProto converts a protobuf EventTypeDef to a model
// EventTypeDef.
func EventTypeDefFromProto(pb *gritzv1.EventTypeDef) EventTypeDef {
	def := EventTypeDef{
		Source: pb.GetSource(),
		Type:   pb.GetType(),
		Label:  pb.GetLabel(),
	}
	if len(pb.GetAttrs()) > 0 {
		def.Attrs = make([]AttrDef, len(pb.GetAttrs()))
		for i, attr := range pb.GetAttrs() {
			def.Attrs[i] = AttrDef{
				Key:         attr.GetKey(),
				Label:       attr.GetLabel(),
				Help:        attr.GetHelp(),
				Placeholder: attr.GetPlaceholder(),
			}
		}
	}
	return def
}

package eventrouter

import (
	"slices"
	"strings"
	"testing"

	"github.com/gritzapp/gritz/internal/model"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// Clearly-synthetic schema fixtures under the fake "test" source. The package's
// tests register only the ones each case needs on an isolated registry, so they
// stay self-contained rather than importing the producer packages that register
// the real github/atlassian schemas.
var (
	testComment = model.EventTypeDef{
		Source: "test",
		Type:   "comment",
		Label:  "Test: Comment",
		Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "mention"}},
	}
	testLabel = model.EventTypeDef{
		Source: "test",
		Type:   "label",
		Label:  "Test: Label",
		Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "label"}},
	}
	testOpened = model.EventTypeDef{
		Source: "test",
		Type:   "opened",
		Label:  "Test: Opened",
		Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}},
	}
)

func TestSchemaRegistryValidate(t *testing.T) {
	reg := NewSchemaRegistry()
	reg.MustRegister(testComment)
	reg.MustRegister(testLabel)
	reg.MustRegister(testOpened)

	tests := []struct {
		name    string
		rule    model.RoutingRule
		wantErr bool
	}{
		{
			name: "default body-prefix rule",
			rule: model.RoutingRule{
				Source:     "test",
				Type:       "comment",
				Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}},
				Wakeup:     true,
			},
		},
		{
			name: "mention equals on comment",
			rule: model.RoutingRule{
				Source:     "test",
				Type:       "comment",
				Conditions: []model.Condition{{Attr: "mention", Op: "equals", Value: "alice"}},
			},
		},
		{
			name: "label equals on label",
			rule: model.RoutingRule{
				Source:     "test",
				Type:       "label",
				Conditions: []model.Condition{{Attr: "label", Op: "equals", Value: "bug"}},
			},
		},
		{
			name: "empty type is rejected",
			rule: model.RoutingRule{
				Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}},
			},
			wantErr: true,
		},
		{
			name: "empty source is rejected",
			rule: model.RoutingRule{
				Type:       "comment",
				Conditions: []model.Condition{{Attr: "mention", Op: "equals", Value: "alice"}},
			},
			wantErr: true,
		},
		{
			name: "url and body valid on any type",
			rule: model.RoutingRule{
				Source: "test",
				Type:   "opened",
				Conditions: []model.Condition{
					{Attr: "url", Op: "contains", Value: "icholy/gritz"},
					{Attr: "body", Op: "contains", Value: "hi"},
				},
			},
		},
		{
			name: "unknown op",
			rule: model.RoutingRule{
				Source:     "test",
				Type:       "comment",
				Conditions: []model.Condition{{Attr: "body", Op: "regex", Value: "x"}},
			},
			wantErr: true,
		},
		{
			name: "unknown attr",
			rule: model.RoutingRule{
				Source:     "test",
				Type:       "comment",
				Conditions: []model.Condition{{Attr: "reviewer", Op: "equals", Value: "alice"}},
			},
			wantErr: true,
		},
		{
			name: "attr not emitted by selected type",
			rule: model.RoutingRule{
				Source:     "test",
				Type:       "comment",
				Conditions: []model.Condition{{Attr: "label", Op: "equals", Value: "bug"}},
			},
			wantErr: true,
		},
		{
			name: "unknown event type",
			rule: model.RoutingRule{
				Source: "test",
				Type:   "star_added",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reg.Validate(tt.rule)
			if tt.wantErr {
				assert.Assert(t, err != nil, "Validate() = nil, want error")
			} else {
				assert.NilError(t, err)
			}
		})
	}
}

func TestSchemaRegistryEventTypeFor(t *testing.T) {
	reg := NewSchemaRegistry()
	reg.MustRegister(testComment)

	def, ok := reg.EventTypeFor("test", "comment")
	assert.Assert(t, ok, "EventTypeFor(test, comment) = _, false; want hit")
	assert.Equal(t, def.Label, "Test: Comment")

	_, ok = reg.EventTypeFor("test", "nope")
	assert.Assert(t, !ok, "EventTypeFor(test, nope) = _, true; want miss")
}

func TestSchemaRegistryEventTypes(t *testing.T) {
	reg := NewSchemaRegistry()
	reg.MustRegister(testComment)
	reg.MustRegister(testLabel)
	reg.MustRegister(testOpened)

	defs := reg.EventTypes()
	assert.Equal(t, len(defs), 3)
	assert.Equal(t, defs[0].Type, "comment")
	assert.Equal(t, defs[1].Type, "label")
	assert.Equal(t, defs[2].Type, "opened")
}

func TestSchemaRegistryMustRegisterDuplicatePanics(t *testing.T) {
	reg := NewSchemaRegistry()
	reg.MustRegister(testComment)
	// Re-registering the same (source, type) must panic.
	assert.Assert(t, cmp.Panics(func() {
		reg.MustRegister(model.EventTypeDef{
			Source: "test",
			Type:   "comment",
			Attrs:  []model.AttrDef{{Key: "body"}},
		})
	}))
}

func TestSchemaRegistryWith(t *testing.T) {
	reg := NewSchemaRegistry()
	reg.MustRegister(testComment)

	combined := reg.With(testLabel, testOpened)
	assert.DeepEqual(t, combined.EventTypes(), []model.EventTypeDef{testComment, testLabel, testOpened})
	_, ok := combined.EventTypeFor("test", "label")
	assert.Assert(t, ok, "combined.EventTypeFor(test, label) = _, false; want hit")

	// r itself is left untouched.
	assert.DeepEqual(t, reg.EventTypes(), []model.EventTypeDef{testComment})
	_, ok = reg.EventTypeFor("test", "label")
	assert.Assert(t, !ok, "reg.EventTypeFor(test, label) = _, true; want miss")
}

// testCustomProblem is a well-formed custom event type, the base each malformed
// case in TestValidateCustomEventTypes derives from.
var testCustomProblem = model.EventTypeDef{
	Source: SourceCustom,
	Type:   "problem",
	Label:  "Custom: Problem",
	Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
}

func TestValidateCustomEventTypes(t *testing.T) {
	with := func(f func(def *model.EventTypeDef)) model.EventTypeDef {
		def := testCustomProblem
		def.Attrs = slices.Clone(def.Attrs)
		f(&def)
		return def
	}
	tests := []struct {
		name    string
		defs    []model.EventTypeDef
		wantErr string
	}{
		{
			name: "empty list",
		},
		{
			name: "valid types",
			defs: []model.EventTypeDef{
				testCustomProblem,
				with(func(def *model.EventTypeDef) { def.Type = "deploy_finished_2" }),
			},
		},
		{
			name: "64-char type",
			defs: []model.EventTypeDef{
				with(func(def *model.EventTypeDef) { def.Type = "a" + strings.Repeat("b", 63) }),
			},
		},
		{
			name:    "non-custom source",
			defs:    []model.EventTypeDef{with(func(def *model.EventTypeDef) { def.Source = "github" })},
			wantErr: `source must be "custom"`,
		},
		{
			name:    "empty source",
			defs:    []model.EventTypeDef{with(func(def *model.EventTypeDef) { def.Source = "" })},
			wantErr: `source must be "custom"`,
		},
		{
			name:    "empty type",
			defs:    []model.EventTypeDef{with(func(def *model.EventTypeDef) { def.Type = "" })},
			wantErr: `type "" must match`,
		},
		{
			name:    "uppercase type",
			defs:    []model.EventTypeDef{with(func(def *model.EventTypeDef) { def.Type = "Problem" })},
			wantErr: `type "Problem" must match`,
		},
		{
			name:    "type starting with digit",
			defs:    []model.EventTypeDef{with(func(def *model.EventTypeDef) { def.Type = "1problem" })},
			wantErr: `type "1problem" must match`,
		},
		{
			name:    "type with hyphen",
			defs:    []model.EventTypeDef{with(func(def *model.EventTypeDef) { def.Type = "problem-opened" })},
			wantErr: `type "problem-opened" must match`,
		},
		{
			name: "65-char type",
			defs: []model.EventTypeDef{
				with(func(def *model.EventTypeDef) { def.Type = "a" + strings.Repeat("b", 64) }),
			},
			wantErr: "must match",
		},
		{
			name:    "duplicate type",
			defs:    []model.EventTypeDef{testCustomProblem, testCustomProblem},
			wantErr: `duplicate type "problem"`,
		},
		{
			name:    "empty label",
			defs:    []model.EventTypeDef{with(func(def *model.EventTypeDef) { def.Label = "" })},
			wantErr: "empty label",
		},
		{
			name: "empty attr key",
			defs: []model.EventTypeDef{
				with(func(def *model.EventTypeDef) { def.Attrs = append(def.Attrs, model.AttrDef{}) }),
			},
			wantErr: `attr key "" must match`,
		},
		{
			name: "malformed attr key",
			defs: []model.EventTypeDef{
				with(func(def *model.EventTypeDef) { def.Attrs = append(def.Attrs, model.AttrDef{Key: "Host.Name"}) }),
			},
			wantErr: `attr key "Host.Name" must match`,
		},
		{
			name: "duplicate attr key",
			defs: []model.EventTypeDef{
				with(func(def *model.EventTypeDef) { def.Attrs = append(def.Attrs, model.AttrDef{Key: "severity"}) }),
			},
			wantErr: `duplicate attr key "severity"`,
		},
		{
			name: "missing body attr",
			defs: []model.EventTypeDef{
				with(func(def *model.EventTypeDef) { def.Attrs = []model.AttrDef{{Key: "url"}, {Key: "severity"}} }),
			},
			wantErr: `missing required attr "body"`,
		},
		{
			name: "missing url attr",
			defs: []model.EventTypeDef{
				with(func(def *model.EventTypeDef) { def.Attrs = []model.AttrDef{{Key: "body"}, {Key: "severity"}} }),
			},
			wantErr: `missing required attr "url"`,
		},
		{
			name:    "no attrs",
			defs:    []model.EventTypeDef{with(func(def *model.EventTypeDef) { def.Attrs = nil })},
			wantErr: `missing required attr "body"`,
		},
		{
			name: "malformed type after a valid one",
			defs: []model.EventTypeDef{
				testCustomProblem,
				with(func(def *model.EventTypeDef) { def.Type = "alert"; def.Label = "" }),
			},
			wantErr: `event type "alert": empty label`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCustomEventTypes(tt.defs)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			} else {
				assert.NilError(t, err)
			}
		})
	}
}

func TestSchemaRegistryValidateCustom(t *testing.T) {
	builtin := NewSchemaRegistry()
	builtin.MustRegister(testComment)
	reg := builtin.With(testCustomProblem)

	tests := []struct {
		name    string
		reg     *SchemaRegistry
		rule    model.RoutingRule
		wantErr string
	}{
		{
			name: "public rule on custom type",
			reg:  reg,
			rule: model.RoutingRule{
				Source:     SourceCustom,
				Type:       "problem",
				Conditions: []model.Condition{{Attr: "severity", Op: "equals", Value: "critical"}},
				Public:     true,
			},
		},
		{
			name: "derived body and url on custom type",
			reg:  reg,
			rule: model.RoutingRule{
				Source: SourceCustom,
				Type:   "problem",
				Conditions: []model.Condition{
					{Attr: "body", Op: "contains", Value: "disk"},
					{Attr: "url", Op: "prefix", Value: "https://dynatrace.example.com/"},
				},
				Public: true,
			},
		},
		{
			name: "member-only rule on custom type",
			reg:  reg,
			rule: model.RoutingRule{
				Source:     SourceCustom,
				Type:       "problem",
				Conditions: []model.Condition{{Attr: "severity", Op: "equals", Value: "critical"}},
			},
			wantErr: "custom event rules must be public",
		},
		{
			name: "member-only rule on built-in type",
			reg:  reg,
			rule: model.RoutingRule{
				Source:     "test",
				Type:       "comment",
				Conditions: []model.Condition{{Attr: "mention", Op: "equals", Value: "alice"}},
			},
		},
		{
			name: "attr not declared by custom type",
			reg:  reg,
			rule: model.RoutingRule{
				Source:     SourceCustom,
				Type:       "problem",
				Conditions: []model.Condition{{Attr: "host", Op: "equals", Value: "db1"}},
				Public:     true,
			},
			wantErr: `attr "host" not valid for event type "custom"/"problem"`,
		},
		{
			name: "unknown custom type",
			reg:  reg,
			rule: model.RoutingRule{
				Source: SourceCustom,
				Type:   "deploy",
				Public: true,
			},
			wantErr: "unknown event type",
		},
		{
			name: "custom type absent from the built-in registry",
			reg:  builtin,
			rule: model.RoutingRule{
				Source: SourceCustom,
				Type:   "problem",
				Public: true,
			},
			wantErr: "unknown event type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.reg.Validate(tt.rule)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			} else {
				assert.NilError(t, err)
			}
		})
	}
}

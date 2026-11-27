package eventrouter

import (
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

func TestSchemaRegistryValidateAgent_Trigger(t *testing.T) {
	reg := NewSchemaRegistry()
	reg.MustRegister(testComment)
	reg.MustRegister(testLabel)
	reg.MustRegister(testOpened)

	tests := []struct {
		name    string
		trigger model.AgentTrigger
		wantErr bool
	}{
		{
			name: "body-prefix trigger",
			trigger: model.AgentTrigger{
				Source:     "test",
				Type:       "comment",
				Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}},
			},
		},
		{
			name: "mention equals on comment",
			trigger: model.AgentTrigger{
				Source:     "test",
				Type:       "comment",
				Conditions: []model.Condition{{Attr: "mention", Op: "equals", Value: "alice"}},
			},
		},
		{
			name: "label equals on label",
			trigger: model.AgentTrigger{
				Source:     "test",
				Type:       "label",
				Conditions: []model.Condition{{Attr: "label", Op: "equals", Value: "bug"}},
			},
		},
		{
			name: "empty type is rejected",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}},
			},
			wantErr: true,
		},
		{
			name: "empty source is rejected",
			trigger: model.AgentTrigger{
				Type:       "comment",
				Conditions: []model.Condition{{Attr: "mention", Op: "equals", Value: "alice"}},
			},
			wantErr: true,
		},
		{
			name: "url and body valid on any type",
			trigger: model.AgentTrigger{
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
			trigger: model.AgentTrigger{
				Source:     "test",
				Type:       "comment",
				Conditions: []model.Condition{{Attr: "body", Op: "regex", Value: "x"}},
			},
			wantErr: true,
		},
		{
			name: "valid glob",
			trigger: model.AgentTrigger{
				Source:     "test",
				Type:       "label",
				Conditions: []model.Condition{{Attr: "label", Op: "glob", Value: "team-{a,b}/**"}},
			},
		},
		{
			name: "malformed glob",
			trigger: model.AgentTrigger{
				Source:     "test",
				Type:       "label",
				Conditions: []model.Condition{{Attr: "label", Op: "glob", Value: "team-[a"}},
			},
			wantErr: true,
		},
		{
			name: "valid negated conditions",
			trigger: model.AgentTrigger{
				Source: "test",
				Type:   "label",
				Conditions: []model.Condition{
					{Attr: "label", Op: "equals", Value: "wip", Negate: true},
					{Attr: "label", Op: "prefix", Value: "team-", Negate: true},
					{Attr: "label", Op: "contains", Value: "skip", Negate: true},
					{Attr: "label", Op: "glob", Value: "team-{a,b}/**", Negate: true},
				},
			},
		},
		{
			name: "malformed negated glob",
			trigger: model.AgentTrigger{
				Source:     "test",
				Type:       "label",
				Conditions: []model.Condition{{Attr: "label", Op: "glob", Value: "team-[a", Negate: true}},
			},
			wantErr: true,
		},
		{
			name: "negated unknown op",
			trigger: model.AgentTrigger{
				Source:     "test",
				Type:       "comment",
				Conditions: []model.Condition{{Attr: "body", Op: "regex", Value: "x", Negate: true}},
			},
			wantErr: true,
		},
		{
			name: "unknown attr",
			trigger: model.AgentTrigger{
				Source:     "test",
				Type:       "comment",
				Conditions: []model.Condition{{Attr: "reviewer", Op: "equals", Value: "alice"}},
			},
			wantErr: true,
		},
		{
			name: "attr not emitted by selected type",
			trigger: model.AgentTrigger{
				Source:     "test",
				Type:       "comment",
				Conditions: []model.Condition{{Attr: "label", Op: "equals", Value: "bug"}},
			},
			wantErr: true,
		},
		{
			name: "unknown event type",
			trigger: model.AgentTrigger{
				Source: "test",
				Type:   "star_added",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reg.ValidateAgent(&model.Agent{Triggers: []model.AgentTrigger{tt.trigger}})
			if tt.wantErr {
				assert.Assert(t, err != nil, "ValidateAgent() = nil, want error")
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

func TestValidateCustomEventTypes(t *testing.T) {
	reg := NewSchemaRegistry()
	reg.MustRegisterLinkType(model.LinkTypeDef{Name: "custom.url", Source: SourceCustom})
	reg.MustRegisterLinkType(model.LinkTypeDef{Name: "test.issue", Source: "test"})
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
				{
					Source: SourceCustom,
					Type:   "problem",
					Label:  "Custom: Problem",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
				},
				{
					Source: SourceCustom,
					Type:   "deploy_finished_2",
					Label:  "Custom: Deploy Finished",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}},
				},
			},
		},
		{
			name: "64-char type",
			defs: []model.EventTypeDef{
				{
					Source: SourceCustom,
					Type:   "a" + strings.Repeat("b", 63),
					Label:  "Custom: Problem",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
				},
			},
		},
		{
			name: "non-custom source",
			defs: []model.EventTypeDef{
				{
					Source: "github",
					Type:   "problem",
					Label:  "Custom: Problem",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
				},
			},
			wantErr: `source must be "custom"`,
		},
		{
			name: "empty source",
			defs: []model.EventTypeDef{
				{
					Source: "",
					Type:   "problem",
					Label:  "Custom: Problem",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
				},
			},
			wantErr: `source must be "custom"`,
		},
		{
			name: "empty type",
			defs: []model.EventTypeDef{
				{
					Source: SourceCustom,
					Type:   "",
					Label:  "Custom: Problem",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
				},
			},
			wantErr: `type "" must match`,
		},
		{
			name: "uppercase type",
			defs: []model.EventTypeDef{
				{
					Source: SourceCustom,
					Type:   "Problem",
					Label:  "Custom: Problem",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
				},
			},
			wantErr: `type "Problem" must match`,
		},
		{
			name: "type starting with digit",
			defs: []model.EventTypeDef{
				{
					Source: SourceCustom,
					Type:   "1problem",
					Label:  "Custom: Problem",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
				},
			},
			wantErr: `type "1problem" must match`,
		},
		{
			name: "type with hyphen",
			defs: []model.EventTypeDef{
				{
					Source: SourceCustom,
					Type:   "problem-opened",
					Label:  "Custom: Problem",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
				},
			},
			wantErr: `type "problem-opened" must match`,
		},
		{
			name: "65-char type",
			defs: []model.EventTypeDef{
				{
					Source: SourceCustom,
					Type:   "a" + strings.Repeat("b", 64),
					Label:  "Custom: Problem",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
				},
			},
			wantErr: "must match",
		},
		{
			name: "duplicate type",
			defs: []model.EventTypeDef{
				{
					Source: SourceCustom,
					Type:   "problem",
					Label:  "Custom: Problem",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
				},
				{
					Source: SourceCustom,
					Type:   "problem",
					Label:  "Custom: Problem Again",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
				},
			},
			wantErr: `duplicate type "problem"`,
		},
		{
			name: "empty label",
			defs: []model.EventTypeDef{
				{
					Source: SourceCustom,
					Type:   "problem",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
				},
			},
			wantErr: "empty label",
		},
		{
			name: "empty attr key",
			defs: []model.EventTypeDef{
				{
					Source: SourceCustom,
					Type:   "problem",
					Label:  "Custom: Problem",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {}},
				},
			},
			wantErr: `attr key "" must match`,
		},
		{
			name: "malformed attr key",
			defs: []model.EventTypeDef{
				{
					Source: SourceCustom,
					Type:   "problem",
					Label:  "Custom: Problem",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "Host.Name"}},
				},
			},
			wantErr: `attr key "Host.Name" must match`,
		},
		{
			name: "duplicate attr key",
			defs: []model.EventTypeDef{
				{
					Source: SourceCustom,
					Type:   "problem",
					Label:  "Custom: Problem",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}, {Key: "severity"}},
				},
			},
			wantErr: `duplicate attr key "severity"`,
		},
		{
			name: "missing body attr",
			defs: []model.EventTypeDef{
				{
					Source: SourceCustom,
					Type:   "problem",
					Label:  "Custom: Problem",
					Attrs:  []model.AttrDef{{Key: "url"}, {Key: "severity"}},
				},
			},
			wantErr: `missing required attr "body"`,
		},
		{
			name: "missing url attr",
			defs: []model.EventTypeDef{
				{
					Source: SourceCustom,
					Type:   "problem",
					Label:  "Custom: Problem",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "severity"}},
				},
			},
			wantErr: `missing required attr "url"`,
		},
		{
			name: "no attrs",
			defs: []model.EventTypeDef{
				{
					Source: SourceCustom,
					Type:   "problem",
					Label:  "Custom: Problem",
				},
			},
			wantErr: `missing required attr "body"`,
		},
		{
			name: "malformed type after a valid one",
			defs: []model.EventTypeDef{
				{
					Source: SourceCustom,
					Type:   "problem",
					Label:  "Custom: Problem",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
				},
				{
					Source: SourceCustom,
					Type:   "alert",
					Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
				},
			},
			wantErr: `event type "alert": empty label`,
		},
		{
			name: "custom link type",
			defs: []model.EventTypeDef{
				{
					Source:    SourceCustom,
					Type:      "problem",
					Label:     "Custom: Problem",
					Attrs:     []model.AttrDef{{Key: "body"}, {Key: "url"}},
					LinkTypes: []string{"custom.url"},
				},
			},
		},
		{
			name: "unknown link type",
			defs: []model.EventTypeDef{
				{
					Source:    SourceCustom,
					Type:      "problem",
					Label:     "Custom: Problem",
					Attrs:     []model.AttrDef{{Key: "body"}, {Key: "url"}},
					LinkTypes: []string{"custom.issue"},
				},
			},
			wantErr: `event type "problem": unknown link type "custom.issue"`,
		},
		{
			name: "link type of another source",
			defs: []model.EventTypeDef{
				{
					Source:    SourceCustom,
					Type:      "problem",
					Label:     "Custom: Problem",
					Attrs:     []model.AttrDef{{Key: "body"}, {Key: "url"}},
					LinkTypes: []string{"test.issue"},
				},
			},
			wantErr: `event type "problem": unknown link type "test.issue"`,
		},
		{
			name: "duplicate link type",
			defs: []model.EventTypeDef{
				{
					Source:    SourceCustom,
					Type:      "problem",
					Label:     "Custom: Problem",
					Attrs:     []model.AttrDef{{Key: "body"}, {Key: "url"}},
					LinkTypes: []string{"custom.url", "custom.url"},
				},
			},
			wantErr: `event type "problem": duplicate link type "custom.url"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reg.ValidateCustomEventTypes(tt.defs)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			} else {
				assert.NilError(t, err)
			}
		})
	}
}

func TestSchemaRegistryValidateAgent_CustomTrigger(t *testing.T) {
	builtin := NewSchemaRegistry()
	builtin.MustRegister(testComment)
	reg := builtin.With(model.EventTypeDef{
		Source: SourceCustom,
		Type:   "problem",
		Label:  "Custom: Problem",
		Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
	})

	tests := []struct {
		name    string
		reg     *SchemaRegistry
		trigger model.AgentTrigger
		wantErr string
	}{
		{
			name: "public trigger on custom type",
			reg:  reg,
			trigger: model.AgentTrigger{
				Source:     SourceCustom,
				Type:       "problem",
				Conditions: []model.Condition{{Attr: "severity", Op: "equals", Value: "critical"}},
				Public:     true,
			},
		},
		{
			name: "derived body and url on custom type",
			reg:  reg,
			trigger: model.AgentTrigger{
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
			name: "member-only trigger on custom type",
			reg:  reg,
			trigger: model.AgentTrigger{
				Source:     SourceCustom,
				Type:       "problem",
				Conditions: []model.Condition{{Attr: "severity", Op: "equals", Value: "critical"}},
			},
			wantErr: "custom event triggers must be public",
		},
		{
			name: "member-only trigger on built-in type",
			reg:  reg,
			trigger: model.AgentTrigger{
				Source:     "test",
				Type:       "comment",
				Conditions: []model.Condition{{Attr: "mention", Op: "equals", Value: "alice"}},
			},
		},
		{
			name: "attr not declared by custom type",
			reg:  reg,
			trigger: model.AgentTrigger{
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
			trigger: model.AgentTrigger{
				Source: SourceCustom,
				Type:   "deploy",
				Public: true,
			},
			wantErr: "unknown event type",
		},
		{
			name: "custom type absent from the built-in registry",
			reg:  builtin,
			trigger: model.AgentTrigger{
				Source: SourceCustom,
				Type:   "problem",
				Public: true,
			},
			wantErr: "unknown event type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.reg.ValidateAgent(&model.Agent{Triggers: []model.AgentTrigger{tt.trigger}})
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			} else {
				assert.NilError(t, err)
			}
		})
	}
}

func TestSchemaRegistryLinkTypes(t *testing.T) {
	reg := NewSchemaRegistry()
	reg.MustRegisterLinkType(model.LinkTypeDef{Name: "test.issue", Source: "test", Label: "Test: Issue"})
	reg.MustRegisterLinkType(model.LinkTypeDef{Name: "test.pr", Source: "test", Label: "Test: PR"})

	var names []string
	for _, def := range reg.LinkTypes() {
		names = append(names, def.Name)
	}
	pr, ok := reg.LinkType("test.pr")
	_, missing := reg.LinkType("test.run")

	assert.DeepEqual(t, names, []string{"test.issue", "test.pr"})
	assert.Assert(t, ok)
	assert.Equal(t, pr.Label, "Test: PR")
	assert.Assert(t, !missing)
}

func TestSchemaRegistryCanonicalURL(t *testing.T) {
	reg := NewSchemaRegistry()
	reg.MustRegisterLinkType(model.LinkTypeDef{
		Name:   "test.issue",
		Source: "test",
		Canonical: func(raw string) (string, bool) {
			rest, ok := strings.CutPrefix(raw, "https://test.example/issues/")
			if !ok {
				return "", false
			}
			id, _, _ := strings.Cut(rest, "#")
			return "https://test.example/issues/" + id, true
		},
	})
	reg.MustRegisterLinkType(model.LinkTypeDef{
		Name:   "test.pr",
		Source: "test",
		Canonical: func(raw string) (string, bool) {
			return raw, strings.HasPrefix(raw, "https://test.example/pr/")
		},
	})
	comment := model.EventTypeDef{Source: "test", Type: "comment", LinkTypes: []string{"test.issue", "test.pr"}}
	push := model.EventTypeDef{Source: "test", Type: "push"}

	tests := []struct {
		name string
		def  model.EventTypeDef
		raw  string
		want string
		ok   bool
	}{
		{"first link type", comment, "https://test.example/issues/5#c-1", "https://test.example/issues/5", true},
		{"second link type", comment, "https://test.example/pr/5", "https://test.example/pr/5", true},
		{"no link type accepts", comment, "https://test.example/other/5", "", false},
		{"no link types", push, "https://test.example/issues/5", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := reg.CanonicalURL(tt.def, tt.raw)

			assert.Equal(t, got, tt.want)
			assert.Equal(t, ok, tt.ok)
		})
	}
}

func TestSchemaRegistryWith_LinkTypes(t *testing.T) {
	builtin := NewSchemaRegistry()
	builtin.MustRegisterLinkType(model.LinkTypeDef{
		Name:      "custom.url",
		Source:    SourceCustom,
		Canonical: func(raw string) (string, bool) { return raw, true },
	})

	reg := builtin.With(model.EventTypeDef{
		Source:    SourceCustom,
		Type:      "problem",
		Label:     "Custom: Problem",
		Attrs:     []model.AttrDef{{Key: "body"}, {Key: "url"}},
		LinkTypes: []string{"custom.url"},
	})
	def, ok := reg.EventTypeFor(SourceCustom, "problem")
	assert.Assert(t, ok)
	got, ok := reg.CanonicalURL(def, "https://dynatrace.example.com/problems/1")

	assert.Assert(t, ok)
	assert.Equal(t, got, "https://dynatrace.example.com/problems/1")
	assert.Equal(t, len(reg.LinkTypes()), 1)
}

func TestSchemaRegistryValidateAgent(t *testing.T) {
	builtin := NewSchemaRegistry()
	builtin.MustRegisterLinkType(model.LinkTypeDef{Name: "test.issue", Source: "test"})
	builtin.MustRegisterLinkType(model.LinkTypeDef{Name: "test.pr", Source: "test"})
	builtin.MustRegisterLinkType(model.LinkTypeDef{Name: "custom.url", Source: SourceCustom})
	builtin.MustRegister(model.EventTypeDef{
		Source:    "test",
		Type:      "comment",
		Attrs:     []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "mention"}},
		LinkTypes: []string{"test.issue", "test.pr"},
	})
	builtin.MustRegister(model.EventTypeDef{
		Source:    "test",
		Type:      "review",
		Attrs:     []model.AttrDef{{Key: "body"}, {Key: "url"}},
		LinkTypes: []string{"test.pr"},
	})
	builtin.MustRegister(model.EventTypeDef{
		Source: "test",
		Type:   "push",
		Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}},
	})
	reg := builtin.With(model.EventTypeDef{
		Source:    SourceCustom,
		Type:      "problem",
		Attrs:     []model.AttrDef{{Key: "body"}, {Key: "url"}},
		LinkTypes: []string{"custom.url"},
	})

	tests := []struct {
		name    string
		agent   model.Agent
		wantErr string
	}{
		{
			name: "no triggers or links",
		},
		{
			name: "triggers and links",
			agent: model.Agent{
				Triggers: []model.AgentTrigger{
					{
						Source:     "test",
						Type:       "comment",
						Conditions: []model.Condition{{Attr: "mention", Op: "equals", Value: "gritz"}},
						YieldTo:    []string{"pr-owner"},
						Link:       "test.pr",
					},
					{Source: "test", Type: "push"},
					{Source: SourceCustom, Type: "problem", Public: true, Link: "custom.url"},
				},
				Links: []model.AgentLink{
					{Type: "test.pr", Kind: "owned-pr", Claim: "pr-owner", Events: []model.AgentLinkEvent{{Type: "comment"}, {Type: "review"}}},
					{Type: "test.pr", Events: []model.AgentLinkEvent{{
						Type:       "review",
						Conditions: []model.Condition{{Attr: "body", Op: "glob", Value: "*LGTM*"}},
					}}},
					{Type: "test.issue"},
					{Type: "custom.url", Events: []model.AgentLinkEvent{{Type: "problem"}}},
				},
			},
		},
		{
			name: "trigger with an invalid selector",
			agent: model.Agent{
				Triggers: []model.AgentTrigger{
					{Source: "test", Type: "push"},
					{Source: "test", Type: "comment", Conditions: []model.Condition{{Attr: "label", Op: "equals", Value: "bug"}}},
				},
			},
			wantErr: `trigger 2: attr "label" not valid for event type "test"/"comment"`,
		},
		{
			name: "trigger on an unknown event type",
			agent: model.Agent{
				Triggers: []model.AgentTrigger{{Source: "test", Type: "opened"}},
			},
			wantErr: `trigger 1: unknown event type: source="test" type="opened"`,
		},
		{
			name: "member-only custom trigger",
			agent: model.Agent{
				Triggers: []model.AgentTrigger{{Source: SourceCustom, Type: "problem"}},
			},
			wantErr: "trigger 1: custom event triggers must be public: custom events have no actor, so a member-only trigger would never fire",
		},
		{
			name: "trigger link to an unknown kind",
			agent: model.Agent{
				Triggers: []model.AgentTrigger{{Source: "test", Type: "comment", Link: "owned-pr"}},
				Links:    []model.AgentLink{{Type: "test.pr"}},
			},
			wantErr: `trigger 1: unknown link kind "owned-pr"`,
		},
		{
			name: "trigger link to a kind its event type doesn't declare",
			agent: model.Agent{
				Triggers: []model.AgentTrigger{{Source: "test", Type: "review", Link: "test.issue"}},
				Links:    []model.AgentLink{{Type: "test.issue"}},
			},
			wantErr: `trigger 1: event type "test"/"review" doesn't declare link type "test.issue" of kind "test.issue"`,
		},
		{
			name: "trigger link on an event type without link types",
			agent: model.Agent{
				Triggers: []model.AgentTrigger{{Source: "test", Type: "push", Link: "test.pr"}},
				Links:    []model.AgentLink{{Type: "test.pr"}},
			},
			wantErr: `trigger 1: event type "test"/"push" doesn't declare link type "test.pr" of kind "test.pr"`,
		},
		{
			name: "unknown link type",
			agent: model.Agent{
				Links: []model.AgentLink{{Type: "test.issue"}, {Type: "test.run"}},
			},
			wantErr: `link 2: unknown link type "test.run"`,
		},
		{
			name: "duplicate kind",
			agent: model.Agent{
				Links: []model.AgentLink{{Type: "test.pr"}, {Type: "test.pr", Events: []model.AgentLinkEvent{{Type: "review"}}}},
			},
			wantErr: `link 2: duplicate kind "test.pr"`,
		},
		{
			name: "explicit kind duplicating a defaulted one",
			agent: model.Agent{
				Links: []model.AgentLink{{Type: "test.pr"}, {Type: "test.issue", Kind: "test.pr"}},
			},
			wantErr: `link 2: duplicate kind "test.pr"`,
		},
		{
			name: "link event of an unknown type",
			agent: model.Agent{
				Links: []model.AgentLink{{Type: "test.pr", Kind: "owned-pr", Events: []model.AgentLinkEvent{{Type: "merged"}}}},
			},
			wantErr: `link kind "owned-pr": unknown event type: source="test" type="merged"`,
		},
		{
			name: "link event that doesn't declare the link type",
			agent: model.Agent{
				Links: []model.AgentLink{{Type: "test.issue", Events: []model.AgentLinkEvent{{Type: "comment"}, {Type: "review"}}}},
			},
			wantErr: `link kind "test.issue": event type "test"/"review" doesn't declare link type "test.issue"`,
		},
		{
			name: "link event listed twice",
			agent: model.Agent{
				Links: []model.AgentLink{{Type: "test.pr", Events: []model.AgentLinkEvent{{Type: "review"}, {Type: "review"}}}},
			},
			wantErr: `link kind "test.pr": duplicate event type "review"`,
		},
		{
			name: "link event condition on an attr its type lacks",
			agent: model.Agent{
				Links: []model.AgentLink{{Type: "test.pr", Events: []model.AgentLinkEvent{{
					Type:       "review",
					Conditions: []model.Condition{{Attr: "mention", Op: "equals", Value: "gritz"}},
				}}}},
			},
			wantErr: `link kind "test.pr": event type "review": attr "mention" not valid for event type "test"/"review"`,
		},
		{
			name: "link event condition with an unknown op",
			agent: model.Agent{
				Links: []model.AgentLink{{Type: "test.pr", Events: []model.AgentLinkEvent{{
					Type:       "comment",
					Conditions: []model.Condition{{Attr: "mention", Op: "regex", Value: "gritz"}},
				}}}},
			},
			wantErr: `link kind "test.pr": event type "comment": unknown op "regex" on attr "mention"`,
		},
		{
			name: "link event of another source",
			agent: model.Agent{
				Links: []model.AgentLink{{Type: "custom.url", Events: []model.AgentLinkEvent{{Type: "comment"}}}},
			},
			wantErr: `link kind "custom.url": unknown event type: source="custom" type="comment"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reg.ValidateAgent(&tt.agent)
			if tt.wantErr != "" {
				assert.Error(t, err, tt.wantErr)
			} else {
				assert.NilError(t, err)
			}
		})
	}
}

package api_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

func TestGetRoutingRules_Default(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	resp, err := h.Org.Client.GetRoutingRules(t.Context(), &gritzv1.GetRoutingRulesRequest{})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Rules), 0)
}

func TestSetAndGetRoutingRules(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	rules := []*gritzv1.RoutingRule{
		{
			Source: "github",
			Type:   "issue_comment",
			Conditions: []*gritzv1.RuleCondition{
				{Attr: "body", Op: "prefix", Value: "bot:"},
			},
		},
		{
			Source: "github",
			Type:   "issue_comment",
			Conditions: []*gritzv1.RuleCondition{
				{Attr: "mention", Op: "equals", Value: "mybot"},
			},
		},
	}

	// Act
	setResp, err := h.Org.Client.SetRoutingRules(t.Context(), &gritzv1.SetRoutingRulesRequest{
		Rules: rules,
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, setResp.Rules, rules, protocmp.Transform())

	// Act
	getResp, err := h.Org.Client.GetRoutingRules(t.Context(), &gritzv1.GetRoutingRulesRequest{})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, getResp.Rules, rules, protocmp.Transform())
}

func TestSetAndGetRoutingRules_Namespace(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	rules := []*gritzv1.RoutingRule{
		{
			Source: "github",
			Type:   "label_added",
			Conditions: []*gritzv1.RuleCondition{
				{Attr: "label", Op: "equals", Value: "reviewbot"},
			},
			Namespace: "reviewbot",
		},
		{
			Source: "github",
			Type:   "issue_comment",
			Conditions: []*gritzv1.RuleCondition{
				{Attr: "body", Op: "prefix", Value: "bot:"},
			},
		},
	}

	// Act
	setResp, err := h.Org.Client.SetRoutingRules(t.Context(), &gritzv1.SetRoutingRulesRequest{Rules: rules})

	// Assert: the namespace round-trips through set, and the default (empty)
	// namespace is preserved untouched.
	assert.NilError(t, err)
	assert.DeepEqual(t, setResp.Rules, rules, protocmp.Transform())

	// Act
	getResp, err := h.Org.Client.GetRoutingRules(t.Context(), &gritzv1.GetRoutingRulesRequest{})

	// Assert: set -> get preserves each rule's namespace.
	assert.NilError(t, err)
	assert.DeepEqual(t, getResp.Rules, rules, protocmp.Transform())
	assert.Equal(t, getResp.Rules[0].Namespace, "reviewbot")
	assert.Equal(t, getResp.Rules[1].Namespace, "")
}

func TestSetRoutingRules_OrgIsolation(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{
			{
				Source: "github",
				Type:   "issue_comment",
				Conditions: []*gritzv1.RuleCondition{
					{Attr: "body", Op: "prefix", Value: "a:"},
				},
			},
		},
	})

	// Act
	resp, err := other.Client.GetRoutingRules(t.Context(), &gritzv1.GetRoutingRulesRequest{})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Rules), 0)
}

func TestSetRoutingRules_RejectsInvalid(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	cases := map[string]*gritzv1.RoutingRule{
		"empty type":   {Source: "github"},
		"unknown type": {Source: "github", Type: "not_a_type"},
		"unknown attr": {
			Source: "github",
			Type:   "issue_comment",
			Conditions: []*gritzv1.RuleCondition{
				{Attr: "nope", Op: "equals", Value: "x"},
			},
		},
		"unknown op": {
			Source: "github",
			Type:   "issue_comment",
			Conditions: []*gritzv1.RuleCondition{
				{Attr: "body", Op: "regex", Value: "x"},
			},
		},
		"attr wrong type": {
			Source: "github",
			Type:   "issue_comment",
			Conditions: []*gritzv1.RuleCondition{
				{Attr: "assignee", Op: "equals", Value: "x"},
			},
		},
	}
	for name, rule := range cases {
		t.Run(name, func(t *testing.T) {
			// Act
			_, err := h.Org.Client.SetRoutingRules(t.Context(), &gritzv1.SetRoutingRulesRequest{
				Rules: []*gritzv1.RoutingRule{rule},
			})

			// Assert
			assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
		})
	}

	// Assert: the rejected writes never persisted anything.
	assert.Equal(t, len(h.Org.GetRoutingRules(&gritzv1.GetRoutingRulesRequest{})), 0)
}

func TestSetRoutingRules_CustomTypeRequiresDefinition(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	rules := []*gritzv1.RoutingRule{
		{
			Source: "github",
			Type:   "issue_comment",
			Conditions: []*gritzv1.RuleCondition{
				{Attr: "body", Op: "prefix", Value: "bot:"},
			},
		},
		{
			Source: "custom",
			Type:   "problem",
			Conditions: []*gritzv1.RuleCondition{
				{Attr: "severity", Op: "equals", Value: "critical"},
			},
			Wakeup: true,
			Public: true,
		},
	}

	// Act: the org hasn't defined custom/problem yet.
	_, err := h.Org.Client.SetRoutingRules(t.Context(), &gritzv1.SetRoutingRulesRequest{
		Rules: rules,
	})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
	assert.ErrorContains(t, err, `routing rule 2: unknown event type: source="custom" type="problem"`)
	assert.Equal(t, len(h.Org.GetRoutingRules(&gritzv1.GetRoutingRulesRequest{})), 0)

	// Arrange
	_, err = h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{
		EventTypes: []*gritzv1.EventTypeDef{
			{
				Source: "custom",
				Type:   "problem",
				Label:  "Problem",
				Attrs: []*gritzv1.AttrDef{
					{Key: "body"},
					{Key: "url"},
					{Key: "severity"},
				},
			},
		},
	})
	assert.NilError(t, err)

	// Act
	setResp, err := h.Org.Client.SetRoutingRules(t.Context(), &gritzv1.SetRoutingRulesRequest{
		Rules: rules,
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, setResp.Rules, rules, protocmp.Transform())
	assert.DeepEqual(t, h.Org.GetRoutingRules(&gritzv1.GetRoutingRulesRequest{}), rules, protocmp.Transform())
}

func TestSetRoutingRules_RejectsNonPublicCustom(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	_, err := h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{
		EventTypes: []*gritzv1.EventTypeDef{
			{
				Source: "custom",
				Type:   "problem",
				Label:  "Problem",
				Attrs: []*gritzv1.AttrDef{
					{Key: "body"},
					{Key: "url"},
					{Key: "severity"},
				},
			},
		},
	})
	assert.NilError(t, err)

	// Act
	_, err = h.Org.Client.SetRoutingRules(t.Context(), &gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{
			{
				Source: "custom",
				Type:   "problem",
				Conditions: []*gritzv1.RuleCondition{
					{Attr: "severity", Op: "equals", Value: "critical"},
				},
				Wakeup: true,
			},
		},
	})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
	assert.ErrorContains(t, err, "routing rule 1: custom event rules must be public")
	assert.Equal(t, len(h.Org.GetRoutingRules(&gritzv1.GetRoutingRulesRequest{})), 0)
}

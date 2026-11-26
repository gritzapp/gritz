package api_test

import (
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/e2e/harness"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

func TestSetAndGetCustomEventTypes(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	defs := []*gritzv1.EventTypeDef{
		{
			Source: "custom",
			Type:   "problem",
			Label:  "Monitoring: Problem",
			Attrs: []*gritzv1.AttrDef{
				{Key: "body", Label: "Description"},
				{Key: "url", Label: "Problem URL"},
				{Key: "severity", Label: "Severity", Help: "e.g. critical", Placeholder: "critical"},
			},
		},
	}

	// Act
	setResp, err := h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{
		EventTypes: defs,
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, setResp.EventTypes, defs, protocmp.Transform())

	// Act
	getResp, err := h.Org.Client.GetCustomEventTypes(t.Context(), &gritzv1.GetCustomEventTypesRequest{})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t,
		getResp,
		&gritzv1.GetCustomEventTypesResponse{EventTypes: defs},
		protocmp.Transform(),
	)
}

func TestSetCustomEventTypes_RejectsMalformed(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)

	// Act: the def lacks the required "url" attr.
	_, err := h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{
		EventTypes: []*gritzv1.EventTypeDef{
			{
				Source: "custom",
				Type:   "problem",
				Label:  "Problem",
				Attrs:  []*gritzv1.AttrDef{{Key: "body"}},
			},
		},
	})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
	assert.ErrorContains(t, err, `missing required attr "url"`)
	resp, err := h.Org.Client.GetCustomEventTypes(t.Context(), &gritzv1.GetCustomEventTypesRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(resp.EventTypes), 0)
}

func TestSetCustomEventTypes_RejectsDeletingTypeInUse(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	problem := &gritzv1.EventTypeDef{
		Source: "custom",
		Type:   "problem",
		Label:  "Problem",
		Attrs: []*gritzv1.AttrDef{
			{Key: "body"},
			{Key: "url"},
			{Key: "severity"},
		},
	}
	_, err := h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{
		EventTypes: []*gritzv1.EventTypeDef{problem},
	})
	assert.NilError(t, err)
	_, err = h.Org.Client.SetRoutingRules(t.Context(), &gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{
			{
				Source:     "github",
				Type:       "issue_comment",
				Conditions: []*gritzv1.RuleCondition{{Attr: "body", Op: "prefix", Value: "bot:"}},
			},
			{
				Source:     "custom",
				Type:       "problem",
				Conditions: []*gritzv1.RuleCondition{{Attr: "severity", Op: "equals", Value: "critical"}},
				Wakeup:     true,
				Public:     true,
			},
		},
	})
	assert.NilError(t, err)

	// Act
	_, err = h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
	assert.ErrorContains(t, err, `routing rule 2: unknown event type: source="custom" type="problem"`)
	resp, err := h.Org.Client.GetCustomEventTypes(t.Context(), &gritzv1.GetCustomEventTypesRequest{})
	assert.NilError(t, err)
	assert.DeepEqual(t, resp.EventTypes, []*gritzv1.EventTypeDef{problem}, protocmp.Transform())
}

func TestSetCustomEventTypes_RejectsRemovingAttrInUse(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	problem := &gritzv1.EventTypeDef{
		Source: "custom",
		Type:   "problem",
		Label:  "Problem",
		Attrs: []*gritzv1.AttrDef{
			{Key: "body"},
			{Key: "url"},
			{Key: "severity"},
		},
	}
	_, err := h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{
		EventTypes: []*gritzv1.EventTypeDef{problem},
	})
	assert.NilError(t, err)
	_, err = h.Org.Client.SetRoutingRules(t.Context(), &gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{
			{
				Source:     "github",
				Type:       "issue_comment",
				Conditions: []*gritzv1.RuleCondition{{Attr: "body", Op: "prefix", Value: "bot:"}},
			},
			{
				Source:     "custom",
				Type:       "problem",
				Conditions: []*gritzv1.RuleCondition{{Attr: "severity", Op: "equals", Value: "critical"}},
				Wakeup:     true,
				Public:     true,
			},
		},
	})
	assert.NilError(t, err)

	// Act: drop the "severity" attr the rule's condition uses.
	_, err = h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{
		EventTypes: []*gritzv1.EventTypeDef{
			{
				Source: "custom",
				Type:   "problem",
				Label:  "Problem",
				Attrs:  []*gritzv1.AttrDef{{Key: "body"}, {Key: "url"}},
			},
		},
	})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
	assert.ErrorContains(t, err, `routing rule 2: attr "severity" not valid for event type "custom"/"problem"`)
	resp, err := h.Org.Client.GetCustomEventTypes(t.Context(), &gritzv1.GetCustomEventTypesRequest{})
	assert.NilError(t, err)
	assert.DeepEqual(t, resp.EventTypes, []*gritzv1.EventTypeDef{problem}, protocmp.Transform())
}

func TestSetCustomEventTypes_LinkTypes(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	defs := []*gritzv1.EventTypeDef{{
		Source:    "custom",
		Type:      "problem",
		Label:     "Problem",
		Attrs:     []*gritzv1.AttrDef{{Key: "body"}, {Key: "url"}},
		LinkTypes: []string{"custom.url"},
	}}

	// Act
	_, err := h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{EventTypes: defs})

	// Assert
	assert.NilError(t, err)
	getResp, err := h.Org.Client.GetCustomEventTypes(t.Context(), &gritzv1.GetCustomEventTypesRequest{})
	assert.NilError(t, err)
	assert.DeepEqual(t, getResp.EventTypes, defs, protocmp.Transform())
}

func TestSetCustomEventTypes_RejectsBuiltInLinkType(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)

	// Act
	_, err := h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{
		EventTypes: []*gritzv1.EventTypeDef{{
			Source:    "custom",
			Type:      "problem",
			Label:     "Problem",
			Attrs:     []*gritzv1.AttrDef{{Key: "body"}, {Key: "url"}},
			LinkTypes: []string{"github.pr"},
		}},
	})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
	assert.ErrorContains(t, err, `event type "problem": unknown link type "github.pr"`)
}

func TestSetCustomEventTypes_RejectsRemovingLinkTypeInUse(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	problem := &gritzv1.EventTypeDef{
		Source:    "custom",
		Type:      "problem",
		Label:     "Problem",
		Attrs:     []*gritzv1.AttrDef{{Key: "body"}, {Key: "url"}},
		LinkTypes: []string{"custom.url"},
	}
	_, err := h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{
		EventTypes: []*gritzv1.EventTypeDef{problem},
	})
	assert.NilError(t, err)
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "responder",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Links:     []*gritzv1.AgentLink{{Type: "custom.url", Events: []string{"problem"}}},
	}})
	unlinked := proto.CloneOf(problem)
	unlinked.LinkTypes = nil

	// Act
	_, err = h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{
		EventTypes: []*gritzv1.EventTypeDef{unlinked},
	})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
	assert.ErrorContains(t, err, fmt.Sprintf(`agent %d (responder): link kind "custom.url": event type "custom"/"problem" doesn't declare link type "custom.url"`, agent.Id))
	getResp, err := h.Org.Client.GetCustomEventTypes(t.Context(), &gritzv1.GetCustomEventTypesRequest{})
	assert.NilError(t, err)
	assert.DeepEqual(t, getResp.EventTypes, []*gritzv1.EventTypeDef{problem}, protocmp.Transform())
}

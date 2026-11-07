package api_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/e2e"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

func TestSetAndGetCustomEventTypes(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
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
	assert.DeepEqual(t, getResp.EventTypes, defs, protocmp.Transform())
	// The harness server runs without a BaseURL, so only the path shows.
	assert.Equal(t, getResp.WebhookUrl, "/webhook/custom")
}

func TestSetCustomEventTypes_RejectsMalformed(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

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

func TestSetCustomEventTypes_RejectsInUse(t *testing.T) {
	t.Parallel()
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
	cases := map[string]struct {
		eventTypes []*gritzv1.EventTypeDef
		want       string
	}{
		"delete type": {
			eventTypes: nil,
			want:       `routing rule 2: unknown event type: source="custom" type="problem"`,
		},
		"remove attr": {
			eventTypes: []*gritzv1.EventTypeDef{
				{
					Source: "custom",
					Type:   "problem",
					Label:  "Problem",
					Attrs:  []*gritzv1.AttrDef{{Key: "body"}, {Key: "url"}},
				},
			},
			want: `routing rule 2: attr "severity" not valid for event type "custom"/"problem"`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			h := e2e.New(t, nil)
			_, err := h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{
				EventTypes: []*gritzv1.EventTypeDef{problem},
			})
			assert.NilError(t, err)
			// SetRoutingRules doesn't accept custom types yet, so the rule
			// using one is written to the store directly.
			err = h.Server.Store.SetOrgRoutingRules(t.Context(), nil, h.Org.ID, []model.RoutingRule{
				{
					Source:     "github",
					Type:       "issue_comment",
					Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "bot:"}},
				},
				{
					Source:     "custom",
					Type:       "problem",
					Conditions: []model.Condition{{Attr: "severity", Op: "equals", Value: "critical"}},
					Wakeup:     true,
					Public:     true,
				},
			})
			assert.NilError(t, err)

			// Act
			_, err = h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{
				EventTypes: tc.eventTypes,
			})

			// Assert
			assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
			assert.ErrorContains(t, err, tc.want)
			resp, err := h.Org.Client.GetCustomEventTypes(t.Context(), &gritzv1.GetCustomEventTypesRequest{})
			assert.NilError(t, err)
			assert.DeepEqual(t, resp.EventTypes, []*gritzv1.EventTypeDef{problem}, protocmp.Transform())
		})
	}
}

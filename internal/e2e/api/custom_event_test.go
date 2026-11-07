package api_test

import (
	"net/http"
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/server/customserver"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/durationpb"
	"gotest.tools/v3/assert"
)

// TestPostCustomEvent posts a custom event with an API key and asserts it
// creates a task through a public create rule.
func TestPostCustomEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	_, err := h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{
		EventTypes: []*gritzv1.EventTypeDef{
			{
				Source: "custom",
				Type:   "problem",
				Label:  "Dynatrace Problem",
				Attrs: []*gritzv1.AttrDef{
					{Key: "body"},
					{Key: "url"},
					{Key: "severity"},
				},
			},
		},
	})
	assert.NilError(t, err)
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{
			{
				Source:     "custom",
				Type:       "problem",
				Conditions: []*gritzv1.RuleCondition{{Attr: "severity", Op: "equals", Value: "critical"}},
				Create: &gritzv1.CreateTaskAction{
					Workspace: e2e.DefaultWorkspace,
					Runner:    e2e.DefaultRunner,
					Prompt:    "Investigate the problem.",
				},
				Public: true,
			},
		},
	})
	key := h.Org.CreateKey(&gritzv1.CreateKeyRequest{Name: "dynatrace"})
	webhook := h.Org.CustomWebhook(key)
	url := "https://abc123.live.dynatrace.com/#problems/problemdetails;pid=-123_456V2"

	// Act
	resp := webhook.Send(customserver.Envelope{
		Type:    "problem",
		URL:     url,
		Body:    "CPU saturation on host web-1",
		Attrs:   map[string][]string{"severity": {"critical"}},
		Details: map[string]string{"state": "OPEN"},
	})

	// Assert
	assert.DeepEqual(t,
		resp,
		&e2e.WebhookResponse{StatusCode: http.StatusOK, Body: "{\"routed\":1}\n"},
	)
	assert.DeepEqual(t,
		h.Org.ListTasks(&gritzv1.ListTasksRequest{}),
		[]*gritzv1.Task{{
			Workspace:   e2e.DefaultWorkspace,
			Runner:      e2e.DefaultRunner,
			Status:      gritzv1.TaskStatus_PENDING,
			Command:     gritzv1.TaskCommand_START,
			Version:     1,
			Actions:     &gritzv1.TaskActions{Cancel: true},
			AutoArchive: durationpb.New(0),
		}},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.Task{}, "id", "created_at", "updated_at"),
	)
	assert.DeepEqual(t,
		h.Org.ListExternalEvents(&gritzv1.ListExternalEventsRequest{}),
		[]*gritzv1.Event{{
			Payload: &gritzv1.Event_External{External: &gritzv1.ExternalPayload{
				Description: "Dynatrace Problem",
				Url:         url,
				Data:        "CPU saturation on host web-1",
				Details:     map[string]string{"state": "OPEN"},
				Source:      "custom",
				Type:        "problem",
			}},
		}},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.Event{}, "id", "task_id", "created_at"),
	)
}

package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

// TestPostCustomEvent posts a custom event with an API key: the first post
// creates a task through a public create rule, and a second post about the
// same URL wakes it rather than creating another.
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
				Wakeup: true,
				Public: true,
			},
		},
	})
	key := h.Org.CreateKey(&gritzv1.CreateKeyRequest{Name: "dynatrace"})
	url := "https://abc123.live.dynatrace.com/#problems/problemdetails;pid=-123_456V2"
	envelope := `{
		"type": "problem",
		"url": "` + url + `",
		"body": "CPU saturation on host web-1",
		"attrs": {"severity": ["critical"]},
		"details": {"state": "OPEN"}
	}`

	// Act
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.Server.URL+"/webhook/custom", strings.NewReader(envelope))
	assert.NilError(t, err)
	req.Header.Set("Authorization", "Bearer "+key.Resp.RawToken)
	resp, err := http.DefaultClient.Do(req)
	assert.NilError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	assert.NilError(t, err)

	// Assert: one task was created, with the event as its trigger.
	assert.Equal(t, resp.StatusCode, http.StatusOK, string(body))
	assert.Equal(t, string(body), "{\"routed\":1}\n")
	tasks := h.Org.ListTasks(&gritzv1.ListTasksRequest{Archived: true})
	assert.Equal(t, len(tasks), 1)
	task := tasks[0]
	assert.Equal(t, task.Status, gritzv1.TaskStatus_PENDING)
	events := h.Org.ListExternalEvents(&gritzv1.ListExternalEventsRequest{})
	assert.Equal(t, len(events), 1)
	assert.Equal(t, events[0].TaskId, task.Id)
	assert.DeepEqual(t, events[0].GetExternal(), &gritzv1.ExternalPayload{
		Description: "Dynatrace Problem",
		Url:         url,
		Data:        "CPU saturation on host web-1",
		Details:     map[string]string{"state": "OPEN"},
		Source:      "custom",
		Type:        "problem",
	}, protocmp.Transform())

	// Arrange: the task runs to completion.
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: task.Id, Event: string(model.RunnerEventStarted), Version: task.Version},
			{TaskId: task.Id, Event: string(model.RunnerEventStopped), Version: task.Version},
		},
	})
	assert.Equal(t, h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.Id}).Status, gritzv1.TaskStatus_COMPLETED)

	// Act
	req, err = http.NewRequestWithContext(t.Context(), http.MethodPost, h.Server.URL+"/webhook/custom", strings.NewReader(envelope))
	assert.NilError(t, err)
	req.Header.Set("Authorization", "Bearer "+key.Resp.RawToken)
	resp, err = http.DefaultClient.Do(req)
	assert.NilError(t, err)
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	assert.NilError(t, err)

	// Assert: the subscribed task was woken, and no other task was created.
	assert.Equal(t, resp.StatusCode, http.StatusOK, string(body))
	assert.Equal(t, string(body), "{\"routed\":1}\n")
	assert.Equal(t, len(h.Org.ListTasks(&gritzv1.ListTasksRequest{Archived: true})), 1)
	assert.Equal(t, h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.Id}).Status, gritzv1.TaskStatus_PENDING)
	events = h.Org.ListExternalEvents(&gritzv1.ListExternalEventsRequest{})
	assert.Equal(t, len(events), 2)
	for _, event := range events {
		assert.Equal(t, event.TaskId, task.Id)
	}
}

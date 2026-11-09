package mcpserver

import (
	"context"
	"slices"
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/proto/gritz/v1/gritzv1connect"
	"github.com/gritzapp/gritz/internal/x/mcpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addEventTools registers the event, event type, test and custom event tools.
func (h *handlers) addEventTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_events",
		Description: "List the org's newest external events, such as GitHub comments, Jira updates and custom events, without their bodies",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.listEvents)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_event",
		Description: "Get an event in full, including its body and details",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.getEvent)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_event",
		Description: "Delete an event",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: new(true),
		},
	}, h.deleteEvent)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_event_types",
		Description: "List the event types routing rules can match, built-in and custom, with the attributes a rule can have conditions on",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.listEventTypes)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "put_custom_event_type",
		Description: "Add a custom event type, or replace the one with the same type",
	}, h.putCustomEventType)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_custom_event_type",
		Description: "Delete a custom event type. It fails while a routing rule uses the type.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: new(true),
		},
	}, h.deleteCustomEventType)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "test_event",
		Description: "Test the routing rules with an event of any type: report which rule matches and whether it would wake or create a task. With fire, act on the match for real. A built-in event routes as you; a custom event has no actor, so only public rules match it, exactly as from the custom events webhook. Firing a custom event is how to send one.",
	}, h.testEvent)
}

type listEventsInput struct {
	Limit int32 `json:"limit,omitempty" jsonschema:"Max events to return (default 100, max 100)"`
}

func (h *handlers) listEvents(ctx context.Context, req *mcp.CallToolRequest, input listEventsInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	resp, err := service.ListExternalEvents(ctx, &gritzv1.ListExternalEventsRequest{Limit: input.Limit})
	if err != nil {
		return mcpx.ErrorResult("failed to list events: %v", err), nil, nil
	}
	type eventSummary struct {
		ID          int64     `json:"id"`
		TaskID      int64     `json:"task_id"`
		Source      string    `json:"source"`
		Type        string    `json:"type"`
		Description string    `json:"description,omitempty"`
		URL         string    `json:"url,omitempty"`
		CreatedAt   time.Time `json:"created_at,omitzero"`
	}
	result := make([]eventSummary, len(resp.Events))
	for i, e := range resp.Events {
		ext := e.GetExternal()
		result[i] = eventSummary{
			ID:          e.Id,
			TaskID:      e.TaskId,
			Source:      ext.GetSource(),
			Type:        ext.GetType(),
			Description: ext.GetDescription(),
			URL:         ext.GetUrl(),
		}
		if e.CreatedAt != nil {
			result[i].CreatedAt = e.CreatedAt.AsTime()
		}
	}
	return mcpx.JSONResult(result), nil, nil
}

type eventIDInput struct {
	ID int64 `json:"id" jsonschema:"The event ID"`
}

func (h *handlers) getEvent(ctx context.Context, req *mcp.CallToolRequest, input eventIDInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	resp, err := service.GetEvent(ctx, &gritzv1.GetEventRequest{Id: input.ID})
	if err != nil {
		return mcpx.ErrorResult("failed to get event: %v", err), nil, nil
	}
	return mcpx.ProtoJSONResult(resp.Event), nil, nil
}

func (h *handlers) deleteEvent(ctx context.Context, req *mcp.CallToolRequest, input eventIDInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	if _, err := service.DeleteEvent(ctx, &gritzv1.DeleteEventRequest{Id: input.ID}); err != nil {
		return mcpx.ErrorResult("failed to delete event: %v", err), nil, nil
	}
	type deleted struct {
		Deleted int64 `json:"deleted"`
	}
	return mcpx.JSONResult(deleted{Deleted: input.ID}), nil, nil
}

type listEventTypesInput struct{}

func (h *handlers) listEventTypes(ctx context.Context, req *mcp.CallToolRequest, input listEventTypesInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	resp, err := service.GetEventTypes(ctx, &gritzv1.GetEventTypesRequest{})
	if err != nil {
		return mcpx.ErrorResult("failed to list event types: %v", err), nil, nil
	}
	return mcpx.ProtoJSONResult(resp), nil, nil
}

type attrDefInput struct {
	Key         string `json:"key" jsonschema:"The attribute's name, which routing rule conditions refer to"`
	Label       string `json:"label,omitempty" jsonschema:"The attribute's name in the routing rule editor"`
	Help        string `json:"help,omitempty" jsonschema:"Help text for the attribute in the routing rule editor"`
	Placeholder string `json:"placeholder,omitempty" jsonschema:"Placeholder for the attribute's value in the routing rule editor"`
}

type putCustomEventTypeInput struct {
	Type  string         `json:"type" jsonschema:"The type's name, which senders put in the event's type: a lowercase letter, then up to 63 lowercase letters, digits and underscores"`
	Label string         `json:"label" jsonschema:"The type's name in the routing rule editor and on task timelines"`
	Attrs []attrDefInput `json:"attrs" jsonschema:"The attributes routing rules can match on. Must include \"body\" and \"url\"; keys follow the same rules as the type."`
}

func (h *handlers) putCustomEventType(ctx context.Context, req *mcp.CallToolRequest, input putCustomEventTypeInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	resp, err := service.GetCustomEventTypes(ctx, &gritzv1.GetCustomEventTypesRequest{})
	if err != nil {
		return mcpx.ErrorResult("failed to get custom event types: %v", err), nil, nil
	}
	def := &gritzv1.EventTypeDef{
		Source: "custom",
		Type:   input.Type,
		Label:  input.Label,
	}
	for _, a := range input.Attrs {
		def.Attrs = append(def.Attrs, &gritzv1.AttrDef{
			Key:         a.Key,
			Label:       a.Label,
			Help:        a.Help,
			Placeholder: a.Placeholder,
		})
	}
	defs := resp.EventTypes
	if i := slices.IndexFunc(defs, func(d *gritzv1.EventTypeDef) bool { return d.Type == input.Type }); i >= 0 {
		defs[i] = def
	} else {
		defs = append(defs, def)
	}
	return setCustomEventTypes(ctx, service, defs), nil, nil
}

type deleteCustomEventTypeInput struct {
	Type string `json:"type" jsonschema:"The custom event type to delete"`
}

func (h *handlers) deleteCustomEventType(ctx context.Context, req *mcp.CallToolRequest, input deleteCustomEventTypeInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	resp, err := service.GetCustomEventTypes(ctx, &gritzv1.GetCustomEventTypesRequest{})
	if err != nil {
		return mcpx.ErrorResult("failed to get custom event types: %v", err), nil, nil
	}
	i := slices.IndexFunc(resp.EventTypes, func(d *gritzv1.EventTypeDef) bool { return d.Type == input.Type })
	if i < 0 {
		return mcpx.ErrorResult("custom event type %q not found", input.Type), nil, nil
	}
	return setCustomEventTypes(ctx, service, slices.Delete(resp.EventTypes, i, i+1)), nil, nil
}

// setCustomEventTypes writes the org's whole custom event type set and renders
// the set written.
func setCustomEventTypes(ctx context.Context, service gritzv1connect.GritzServiceHandler, defs []*gritzv1.EventTypeDef) *mcp.CallToolResult {
	resp, err := service.SetCustomEventTypes(ctx, &gritzv1.SetCustomEventTypesRequest{EventTypes: defs})
	if err != nil {
		return mcpx.ErrorResult("failed to set custom event types: %v", err)
	}
	return mcpx.ProtoJSONResult(resp)
}

type testEventInput struct {
	Source      string              `json:"source" jsonschema:"The event type's source, such as \"github\", \"jira\" or \"custom\""`
	Type        string              `json:"type" jsonschema:"The event type, such as \"issue_comment\""`
	Body        string              `json:"body,omitempty" jsonschema:"The event's text, matched by rules on the \"body\" attribute"`
	URL         string              `json:"url" jsonschema:"The absolute http or https URL of the resource the event is about. Events with the same url wake the same tasks."`
	Attrs       map[string][]string `json:"attrs,omitempty" jsonschema:"Values of the type's other attributes, each a list of strings. Not \"body\" or \"url\"."`
	Description string              `json:"description,omitempty" jsonschema:"A description shown on the timeline of a task the event reaches; defaults to the event type's label"`
	Details     map[string]string   `json:"details,omitempty" jsonschema:"Extra context shown with the event; routing rules don't match on it"`
	Fire        bool                `json:"fire,omitempty" jsonschema:"Act on the match for real: wake or create tasks. False (the default) is a dry run."`
}

func (h *handlers) testEvent(ctx context.Context, req *mcp.CallToolRequest, input testEventInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	testReq := &gritzv1.TestEventRequest{
		Source:      input.Source,
		Type:        input.Type,
		Body:        input.Body,
		Url:         input.URL,
		Attrs:       make(map[string]*gritzv1.AttrValues, len(input.Attrs)),
		Description: input.Description,
		Details:     input.Details,
		Fire:        input.Fire,
	}
	for key, values := range input.Attrs {
		testReq.Attrs[key] = &gritzv1.AttrValues{Values: values}
	}
	resp, err := service.TestEvent(ctx, testReq)
	if err != nil {
		return mcpx.ErrorResult("failed to test event: %v", err), nil, nil
	}
	return mcpx.ProtoJSONResult(resp), nil, nil
}

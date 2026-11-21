// Package gritzrpc defines the wire types of gritzrpc, the newline-delimited
// JSON-RPC 2.0 protocol the driver speaks to a custom agent over its stdio.
//
// The driver writes one run/start request to the agent's stdin and may later
// send a run/cancel notification. The agent writes log notifications and its
// run/start response to stdout, one compact JSON object per line. The task,
// events and links in run/start are protojson-encoded gritz.v1 messages.
package gritzrpc

import (
	"encoding/json"
	"fmt"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// ProtocolVersion is the version sent in every run/start.
const ProtocolVersion = 1

// Method names.
const (
	// MethodRunStart starts the run; its response ends it.
	MethodRunStart = "run/start"
	// MethodRunCancel asks the agent to stop gracefully.
	MethodRunCancel = "run/cancel"
	// MethodLog carries one log line from the agent.
	MethodLog = "log"
)

// Error codes.
const (
	// CodeUnsupportedVersion is returned by an agent that cannot speak ProtocolVersion.
	CodeUnsupportedVersion = -32000
	// CodeMethodNotFound is returned by gritz for any request from the agent.
	CodeMethodNotFound = -32601
)

// Message is one JSON-RPC 2.0 request, notification or response.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error formats the error as "<message> (code <code>)".
func (e *Error) Error() string {
	return fmt.Sprintf("%s (code %d)", e.Message, e.Code)
}

// StartParams are the params of run/start.
type StartParams struct {
	ProtocolVersion int
	Resume          bool
	Cwd             string
	Task            *gritzv1.Task
	Events          []*gritzv1.Event
	Links           []*gritzv1.TaskLink
	McpServers      map[string]McpServer
}

// startParamsJSON is the wire shape of StartParams, with the proto messages
// already protojson-encoded.
type startParamsJSON struct {
	ProtocolVersion int                  `json:"protocolVersion"`
	Resume          bool                 `json:"resume"`
	Cwd             string               `json:"cwd,omitempty"`
	Task            json.RawMessage      `json:"task,omitempty"`
	Events          []json.RawMessage    `json:"events"`
	Links           []json.RawMessage    `json:"links,omitempty"`
	McpServers      map[string]McpServer `json:"mcpServers,omitempty"`
}

// MarshalJSON encodes p with Task, Events and Links in protojson.
func (p StartParams) MarshalJSON() ([]byte, error) {
	w := startParamsJSON{
		ProtocolVersion: p.ProtocolVersion,
		Resume:          p.Resume,
		Cwd:             p.Cwd,
		// Events is always an array, empty on a run with nothing new.
		Events:     []json.RawMessage{},
		McpServers: p.McpServers,
	}
	if p.Task != nil {
		data, err := protojson.Marshal(p.Task)
		if err != nil {
			return nil, fmt.Errorf("marshal task: %w", err)
		}
		w.Task = data
	}
	for _, ev := range p.Events {
		data, err := protojson.Marshal(ev)
		if err != nil {
			return nil, fmt.Errorf("marshal event %d: %w", ev.GetId(), err)
		}
		w.Events = append(w.Events, data)
	}
	for _, l := range p.Links {
		data, err := protojson.Marshal(l)
		if err != nil {
			return nil, fmt.Errorf("marshal link %d: %w", l.GetId(), err)
		}
		w.Links = append(w.Links, data)
	}
	return json.Marshal(w)
}

// UnmarshalJSON decodes p with Task, Events and Links from protojson.
// Unknown fields are ignored.
func (p *StartParams) UnmarshalJSON(data []byte) error {
	var w startParamsJSON
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	*p = StartParams{
		ProtocolVersion: w.ProtocolVersion,
		Resume:          w.Resume,
		Cwd:             w.Cwd,
		McpServers:      w.McpServers,
	}
	if w.Task != nil {
		p.Task = &gritzv1.Task{}
		if err := opts.Unmarshal(w.Task, p.Task); err != nil {
			return fmt.Errorf("unmarshal task: %w", err)
		}
	}
	for _, raw := range w.Events {
		ev := &gritzv1.Event{}
		if err := opts.Unmarshal(raw, ev); err != nil {
			return fmt.Errorf("unmarshal event: %w", err)
		}
		p.Events = append(p.Events, ev)
	}
	for _, raw := range w.Links {
		l := &gritzv1.TaskLink{}
		if err := opts.Unmarshal(raw, l); err != nil {
			return fmt.Errorf("unmarshal link: %w", err)
		}
		p.Links = append(p.Links, l)
	}
	return nil
}

// McpServer is an MCP server config as sent in run/start.
type McpServer struct {
	Type    string            `json:"type"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// LogParams are the params of log.
type LogParams struct {
	Level string         `json:"level,omitempty"`
	Msg   string         `json:"msg"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

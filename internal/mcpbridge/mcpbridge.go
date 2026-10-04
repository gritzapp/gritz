// Package mcpbridge implements the local stdio MCP bridge: it forwards
// task-change notifications from the server's per-org SSE stream to the
// host Claude Code session as channel events, subject to a per-process,
// per-task mute set.
//
// The mute set is a blocklist: by default (empty set) every
// channel-worthy notification is forwarded, exactly as the bridge did
// before this package existed. An agent mutes specific tasks it no longer
// cares about via the channel_mute / channel_unmute / channel_muted MCP
// tools; muted tasks stay muted until unmuted or until the bridge process
// restarts (the set is in-memory only).
//
// channel_mute(all=true) flips the gate to mute every task. In that mode
// the per-task set inverts to an allowlist of exceptions: unmuting a
// specific task while all-muted keeps that one task delivering while the
// rest stay muted. channel_unmute(all=true) resets back to the
// subscribe-all default.
package mcpbridge

//go:generate go tool moq -pkg mcpbridge -out channel_sender_moq_test.go . ChannelSender

import (
	"context"
	"log/slog"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/x/mcpchannel"
	"github.com/gritzapp/gritz/internal/x/mcpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ChannelSender is the subset of *mcpchannel.Transport the bridge needs to
// push a channel notification. Defined as an interface so the forwarding
// gate can be tested without a live stdio transport.
type ChannelSender interface {
	SendChannel(ctx context.Context, p mcpchannel.Params) error
}

// Channel owns the mute-aware forwarding gate. The mute state itself lives
// in filter. One Channel is created per `gritz mcp --channel` process.
type Channel struct {
	sender ChannelSender
	filter *TaskFilter
}

// NewChannel returns a Channel that forwards through sender with an empty
// mute set (subscribe-all by default).
func NewChannel(sender ChannelSender) *Channel {
	return &Channel{sender: sender, filter: NewTaskFilter()}
}

// primaryTaskID returns the id of the first task resource in the
// notification, if any.
func primaryTaskID(n model.Notification) (int64, bool) {
	for _, r := range n.Resources {
		if r.Type == "task" {
			return r.ID, true
		}
	}
	return 0, false
}

// Forward applies the summary gate and the mute set, then pushes the
// channel notification. It is suitable as the gritzclient
// NotificationClient handler.
//
// With an empty mute set this is identical to the bridge's original inline
// handler: the same ChannelMessage gate and the same SendChannel call. A
// notification naming a muted task is dropped; a notification with no task
// resource is always forwarded (the mute set only ever holds task ids).
func (c *Channel) Forward(ctx context.Context, n model.Notification) {
	if n.ChannelMessage == "" {
		return // summary gate: not channel-worthy
	}
	if id, ok := primaryTaskID(n); ok && c.filter.Muted(id) {
		return // this task has been muted by the agent
	}
	if err := c.sender.SendChannel(ctx, mcpchannel.Params{Content: n.ChannelMessage}); err != nil {
		slog.Warn("gritz channel: failed to send", "err", err)
	}
}

// AddTools registers channel_mute / channel_unmute / channel_muted on
// server. Called by the bridge only when --channel is enabled, so the
// tools never appear when there is no channel stream to mute.
func (c *Channel) AddTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "channel_mute",
		Description: "Stop receiving gritz channel notifications (queued, woken, " +
			"completed, failed, cancelled, archived) for the given tasks. You are " +
			"subscribed to every task by default; pass task_ids to mute tasks you no " +
			"longer care about, or all=true to mute every task at once. After " +
			"all=true you can channel_unmute specific task_ids to keep just those " +
			"delivering while the rest stay muted. Re-enable with channel_unmute. " +
			"Muting is per bridge session and resets when the session restarts. " +
			"Distinct from create_link(subscribe=true), which routes external " +
			"events INTO a task.",
	}, c.muteTool)

	mcp.AddTool(server, &mcp.Tool{
		Name: "channel_unmute",
		Description: "Resume receiving gritz channel notifications for tasks " +
			"previously muted with channel_mute. Pass task_ids to unmute specific " +
			"tasks, or all=true to clear the whole mute set and go back to the " +
			"subscribe-all default. After channel_mute(all=true), unmuting specific " +
			"task_ids keeps those tasks delivering while every other task stays " +
			"muted.",
	}, c.unmuteTool)

	mcp.AddTool(server, &mcp.Tool{
		Name: "channel_muted",
		Description: "Report the current mute state for this session. When " +
			"all=false, the muted list holds the muted task ids and all other tasks " +
			"are delivered. When all=true, every task is muted except the ids in the " +
			"unmuted list.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, c.mutedTool)
}

type muteInput struct {
	TaskIDs []int64 `json:"task_ids,omitempty" jsonschema:"Task IDs to mute"`
	All     bool    `json:"all,omitempty" jsonschema:"Mute every task; channel_unmute specific task_ids afterwards to keep just those delivering"`
}

func (c *Channel) muteTool(_ context.Context, _ *mcp.CallToolRequest, in muteInput) (*mcp.CallToolResult, any, error) {
	if in.All {
		c.filter.MuteAll()
	}
	for _, id := range in.TaskIDs {
		c.filter.Mute(id)
	}
	return c.mutedResult(), nil, nil
}

type unmuteInput struct {
	TaskIDs []int64 `json:"task_ids,omitempty" jsonschema:"Task IDs to unmute (kept delivering even under mute-all)"`
	All     bool    `json:"all,omitempty" jsonschema:"Unmute every task, clearing the whole mute set"`
}

func (c *Channel) unmuteTool(_ context.Context, _ *mcp.CallToolRequest, in unmuteInput) (*mcp.CallToolResult, any, error) {
	if in.All {
		c.filter.Clear()
	}
	for _, id := range in.TaskIDs {
		c.filter.Unmute(id)
	}
	return c.mutedResult(), nil, nil
}

type mutedInput struct{}

func (c *Channel) mutedTool(_ context.Context, _ *mcp.CallToolRequest, _ mutedInput) (*mcp.CallToolResult, any, error) {
	return c.mutedResult(), nil, nil
}

// muteState is the wire representation of the current mute state a tool
// handler returns. Under mute-all the exception set is reported in Unmuted
// (still-delivering tasks); otherwise it is reported in Muted. The omitempty
// tags mean only the field relevant to the active mode is emitted.
type muteState struct {
	All     bool    `json:"all"`
	Muted   []int64 `json:"muted,omitempty"`
	Unmuted []int64 `json:"unmuted,omitempty"`
	Note    string  `json:"note"`
}

// mutedResult renders the current mute state for a tool response. Under
// mute-all the exception set is reported as the unmuted (still-delivering)
// tasks; otherwise it is reported as the muted tasks.
func (c *Channel) mutedResult() *mcp.CallToolResult {
	if c.filter.All() {
		return mcpx.JSONResult(muteState{
			All:     true,
			Unmuted: c.filter.Exceptions(),
			Note: "Every task is muted except those in unmuted. Keep more tasks " +
				"delivering with channel_unmute(task_ids=...), or channel_unmute(all=true) " +
				"to resume everything. Muting is per bridge session and resets on restart.",
		})
	}
	return mcpx.JSONResult(muteState{
		All:   false,
		Muted: c.filter.Exceptions(),
		Note:  "Channel notifications for all tasks except these are delivered. Muting is per bridge session and resets on restart.",
	})
}

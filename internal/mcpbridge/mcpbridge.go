// Package mcpbridge implements the local stdio MCP bridge: it forwards
// task-change notifications from the server's per-org SSE stream to the
// host Claude Code session as channel events, subject to a per-process,
// per-task mute set kept for each org it follows.
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
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/gritzclient"
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

// OrgKey identifies an org across entries and servers. Two entries for the
// same org share one key.
type OrgKey struct {
	Server string
	ID     int64
}

// followedOrg is the channel state of one followed org.
type followedOrg struct {
	name   string // the name of the entry that started the stream
	filter *TaskFilter
}

// Channel owns the mute-aware forwarding gate. The mute state itself lives
// in each followed org's filter. One Channel is created per
// `gritz mcp --channel` process.
type Channel struct {
	sender   ChannelSender
	clientID string

	mu   sync.Mutex
	orgs map[OrgKey]*followedOrg
}

// NewChannel returns a Channel that forwards through sender and follows no
// org yet.
func NewChannel(sender ChannelSender, clientID string) *Channel {
	return &Channel{sender: sender, clientID: clientID, orgs: map[OrgKey]*followedOrg{}}
}

// Follow starts forwarding entry's notifications, labelled with entry.Name
// (none when empty, as for --key), unless its org is already followed. The
// NotificationClient is built from entry.TokenSource(ctx). The stream runs
// until ctx ends.
func (c *Channel) Follow(ctx context.Context, entry *configfile.Entry) {
	key := OrgKey{Server: entry.Server, ID: entry.ID}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.orgs[key]; ok {
		return
	}
	c.orgs[key] = &followedOrg{name: entry.Name, filter: NewTaskFilter()}
	nc := gritzclient.NewNotificationClient(gritzclient.NotificationClientOptions{
		BaseURL:     entry.Server,
		TokenSource: entry.TokenSource(ctx),
		ClientID:    c.clientID,
		Handler:     func(n model.Notification) { c.Forward(ctx, key, n) },
	})
	go func() {
		if err := nc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("gritz channel: stream ended", "org", entry.Name, "err", err)
		}
	}()
}

// org returns the state of a followed org.
func (c *Channel) org(key OrgKey) *followedOrg {
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.orgs[key]
	if !ok {
		panic(fmt.Sprintf("mcpbridge: org %+v is not followed", key))
	}
	return o
}

// namedOrg returns the followed org whose entry is named name, or the only
// followed org when name is empty.
func (c *Channel) namedOrg(name string) (*followedOrg, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if name == "" {
		if len(c.orgs) > 1 {
			return nil, errors.New("following several orgs: pass org, as shown in the channel's org attribute")
		}
		for _, o := range c.orgs {
			return o, nil
		}
		panic("mcpbridge: no org is followed")
	}
	for _, o := range c.orgs {
		if o.name == name {
			return o, nil
		}
	}
	return nil, fmt.Errorf("not following org %q", name)
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

// Forward applies the summary gate and the org's mute set, then pushes the
// channel notification, with the org's entry name as the org attribute.
//
// With an empty mute set this is identical to the bridge's original inline
// handler: the same ChannelMessage gate and the same SendChannel call. A
// notification naming a muted task is dropped; a notification with no task
// resource is always forwarded (the mute set only ever holds task ids).
func (c *Channel) Forward(ctx context.Context, org OrgKey, n model.Notification) {
	if n.ChannelMessage == "" {
		return // summary gate: not channel-worthy
	}
	o := c.org(org)
	if id, ok := primaryTaskID(n); ok && o.filter.Muted(id) {
		return // this task has been muted by the agent
	}
	p := mcpchannel.Params{Content: n.ChannelMessage}
	if o.name != "" {
		p.Meta = map[string]string{"org": o.name}
	}
	if err := c.sender.SendChannel(ctx, p); err != nil {
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
			"Muting is per organisation: pass org, as shown in the channel's org " +
			"attribute, when notifications come from more than one. Muting is per " +
			"bridge session and resets when the session restarts. " +
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
		Description: "Report an organisation's mute state for this session; pass org when " +
			"notifications come from more than one. When " +
			"all=false, the muted list holds the muted task ids and all other tasks " +
			"are delivered. When all=true, every task is muted except the ids in the " +
			"unmuted list.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, c.mutedTool)
}

type muteInput struct {
	Org     string  `json:"org,omitempty" jsonschema:"The organisation, as shown in the channel's org attribute; required when notifications come from more than one"`
	TaskIDs []int64 `json:"task_ids,omitempty" jsonschema:"Task IDs to mute"`
	All     bool    `json:"all,omitempty" jsonschema:"Mute every task; channel_unmute specific task_ids afterwards to keep just those delivering"`
}

func (c *Channel) muteTool(_ context.Context, _ *mcp.CallToolRequest, in muteInput) (*mcp.CallToolResult, any, error) {
	o, err := c.namedOrg(in.Org)
	if err != nil {
		return mcpx.ErrorResult("%s", err), nil, nil
	}
	if in.All {
		o.filter.MuteAll()
	}
	for _, id := range in.TaskIDs {
		o.filter.Mute(id)
	}
	return o.mutedResult(), nil, nil
}

type unmuteInput struct {
	Org     string  `json:"org,omitempty" jsonschema:"The organisation, as shown in the channel's org attribute; required when notifications come from more than one"`
	TaskIDs []int64 `json:"task_ids,omitempty" jsonschema:"Task IDs to unmute (kept delivering even under mute-all)"`
	All     bool    `json:"all,omitempty" jsonschema:"Unmute every task, clearing the whole mute set"`
}

func (c *Channel) unmuteTool(_ context.Context, _ *mcp.CallToolRequest, in unmuteInput) (*mcp.CallToolResult, any, error) {
	o, err := c.namedOrg(in.Org)
	if err != nil {
		return mcpx.ErrorResult("%s", err), nil, nil
	}
	if in.All {
		o.filter.Clear()
	}
	for _, id := range in.TaskIDs {
		o.filter.Unmute(id)
	}
	return o.mutedResult(), nil, nil
}

type mutedInput struct {
	Org string `json:"org,omitempty" jsonschema:"The organisation, as shown in the channel's org attribute; required when notifications come from more than one"`
}

func (c *Channel) mutedTool(_ context.Context, _ *mcp.CallToolRequest, in mutedInput) (*mcp.CallToolResult, any, error) {
	o, err := c.namedOrg(in.Org)
	if err != nil {
		return mcpx.ErrorResult("%s", err), nil, nil
	}
	return o.mutedResult(), nil, nil
}

// muteState is the wire representation of the current mute state a tool
// handler returns. Org names the org's entry and is empty with --key. Under mute-all the exception set is reported in Unmuted
// (still-delivering tasks); otherwise it is reported in Muted. The omitempty
// tags mean only the field relevant to the active mode is emitted.
type muteState struct {
	Org     string  `json:"org,omitempty"`
	All     bool    `json:"all"`
	Muted   []int64 `json:"muted,omitempty"`
	Unmuted []int64 `json:"unmuted,omitempty"`
	Note    string  `json:"note"`
}

// mutedResult renders the current mute state for a tool response. Under
// mute-all the exception set is reported as the unmuted (still-delivering)
// tasks; otherwise it is reported as the muted tasks.
func (o *followedOrg) mutedResult() *mcp.CallToolResult {
	if o.filter.All() {
		return mcpx.JSONResult(muteState{
			Org:     o.name,
			All:     true,
			Unmuted: o.filter.Exceptions(),
			Note: "Every task is muted except those in unmuted. Keep more tasks " +
				"delivering with channel_unmute(task_ids=...), or channel_unmute(all=true) " +
				"to resume everything. Muting is per bridge session and resets on restart.",
		})
	}
	return mcpx.JSONResult(muteState{
		Org:   o.name,
		All:   false,
		Muted: o.filter.Exceptions(),
		Note:  "Channel notifications for all tasks except these are delivered. Muting is per bridge session and resets on restart.",
	})
}

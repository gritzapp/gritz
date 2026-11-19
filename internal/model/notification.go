package model

import "time"

// Notification is a lightweight change notification for one or more resources.
// Type is either "ready" (subscription is live) or "change" (resources changed).
type Notification struct {
	Type      string                 `json:"type"`
	Resources []NotificationResource `json:"resources,omitempty"`
	Time      time.Time              `json:"timestamp"`
	OrgID     int64                  `json:"org_id"`
	UserID    string                 `json:"user_id,omitempty"`
	ClientID  string                 `json:"client_id,omitempty"`
	// Runner is only set if there's pending work to do
	Runner string `json:"for_runner,omitempty"`
	// TaskStatus carries the task's status after a runner-driven
	// transition. It is populated only for terminal transitions
	// (completed, failed, cancelled) so consumers like `gritz notify`
	// can surface task outcomes without re-fetching the task. The zero
	// value (unspecified) means this notification isn't a terminal task
	// transition.
	TaskStatus TaskStatus `json:"task_status,omitzero"`
	// ChannelMessage is the human-readable line forwarded to the agent
	// channel by mcpserver.ForwardNotification. Empty means silent (the
	// channel forwarder gates on this field). Consumed only by the channel
	// bridge; runner and web UI ignore it.
	ChannelMessage string `json:"channel_message,omitempty"`
	// Ignore causes the publisher to skip this notification entirely. Lets
	// callers always build and "publish" a notification and decide inside
	// the transaction whether it actually goes out, instead of gating the
	// publish call with a separate boolean.
	Ignore bool `json:"-"`
}

// NotificationResource describes an affected resource within a "change" Notification.
type NotificationResource struct {
	Action string `json:"action"` // created, updated, appended, deleted
	Type   string `json:"type"`   // task, task_events, task_links, event, schedule
	ID     int64  `json:"id"`
}

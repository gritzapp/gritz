package mcpbridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/x/cmpx"
	"github.com/gritzapp/gritz/internal/x/mcpchannel"
	"github.com/gritzapp/gritz/internal/x/mcptest"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// TestForward_DefaultForwardsEverything is the byte-for-byte default: an
// empty mute set forwards every channel-worthy notification unchanged.
func TestForward_DefaultForwardsEverything(t *testing.T) {
	t.Parallel()
	// Arrange
	sender := &ChannelSenderMock{
		SendChannelFunc: func(context.Context, mcpchannel.Params) error { return nil },
	}
	ch := NewChannel(sender, "")
	ch.filters[""] = NewTaskFilter()

	// Act
	ch.Forward(context.Background(), "", model.Notification{
		Type:           "change",
		Resources:      []model.NotificationResource{{Action: "updated", Type: "task", ID: 42}},
		ChannelMessage: "Task 42 completed.",
	})

	// Assert: exactly one forward carrying the summary and no metadata.
	assert.DeepEqual(t,
		sender.SentChannelParams(),
		[]mcpchannel.Params{{Content: "Task 42 completed."}},
	)
}

// TestForward_EmptyChannelMessage keeps the summary gate: notifications
// with no ChannelMessage are silent.
func TestForward_EmptyChannelMessage(t *testing.T) {
	t.Parallel()
	// Arrange
	sender := &ChannelSenderMock{
		SendChannelFunc: func(context.Context, mcpchannel.Params) error { return nil },
	}
	ch := NewChannel(sender, "")
	ch.filters[""] = NewTaskFilter()

	// Act
	ch.Forward(context.Background(), "", model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "updated", Type: "task", ID: 42}},
	})

	// Assert
	assert.Assert(t, cmp.Len(sender.SentChannelParams(), 0))
}

// TestForward_MutedTaskDropped drops notifications for a muted task while
// still forwarding others.
func TestForward_MutedTaskDropped(t *testing.T) {
	t.Parallel()
	// Arrange
	sender := &ChannelSenderMock{
		SendChannelFunc: func(context.Context, mcpchannel.Params) error { return nil },
	}
	ch := NewChannel(sender, "")
	ch.filters[""] = NewTaskFilter()
	ch.filters[""].Mute(42)

	// Act
	ch.Forward(context.Background(), "", model.Notification{
		Type:           "change",
		Resources:      []model.NotificationResource{{Action: "updated", Type: "task", ID: 42}},
		ChannelMessage: "Task 42 completed.",
	})
	ch.Forward(context.Background(), "", model.Notification{
		Type:           "change",
		Resources:      []model.NotificationResource{{Action: "updated", Type: "task", ID: 7}},
		ChannelMessage: "Task 7 completed.",
	})

	// Assert: only the un-muted task's notification survived.
	assert.DeepEqual(t,
		sender.SentChannelParams(),
		[]mcpchannel.Params{{Content: "Task 7 completed."}},
		cmpx.OnlyFields("Content"),
	)
}

// TestForward_NonTaskScopedAlwaysForwarded confirms a message with no task
// resource is delivered even when the mute set is non-empty (a blocklist
// can only drop ids it holds).
func TestForward_NonTaskScopedAlwaysForwarded(t *testing.T) {
	t.Parallel()
	// Arrange
	sender := &ChannelSenderMock{
		SendChannelFunc: func(context.Context, mcpchannel.Params) error { return nil },
	}
	ch := NewChannel(sender, "")
	ch.filters[""] = NewTaskFilter()
	ch.filters[""].Mute(42)

	// Act
	ch.Forward(context.Background(), "", model.Notification{ChannelMessage: "System notice."})

	// Assert
	assert.DeepEqual(t,
		sender.SentChannelParams(),
		[]mcpchannel.Params{{Content: "System notice."}},
		cmpx.OnlyFields("Content"),
	)
}

// TestForward_Unmute restores delivery for a previously muted task.
func TestForward_Unmute(t *testing.T) {
	t.Parallel()
	// Arrange
	sender := &ChannelSenderMock{
		SendChannelFunc: func(context.Context, mcpchannel.Params) error { return nil },
	}
	ch := NewChannel(sender, "")
	ch.filters[""] = NewTaskFilter()
	ch.filters[""].Mute(42)
	ch.filters[""].Unmute(42)

	// Act
	ch.Forward(context.Background(), "", model.Notification{
		Type:           "change",
		Resources:      []model.NotificationResource{{Action: "updated", Type: "task", ID: 42}},
		ChannelMessage: "Task 42 completed.",
	})

	// Assert
	assert.Assert(t, cmp.Len(sender.SentChannelParams(), 1))
}

// TestForward_MuteAllDropsEveryTask confirms mute-all suppresses
// task-scoped notifications for tasks that were never individually muted.
func TestForward_MuteAllDropsEveryTask(t *testing.T) {
	t.Parallel()
	// Arrange
	sender := &ChannelSenderMock{
		SendChannelFunc: func(context.Context, mcpchannel.Params) error { return nil },
	}
	ch := NewChannel(sender, "")
	ch.filters[""] = NewTaskFilter()
	_, _, err := ch.muteTool(context.Background(), nil, muteInput{All: true})
	assert.NilError(t, err)

	// Act
	ch.Forward(context.Background(), "", model.Notification{
		Type:           "change",
		Resources:      []model.NotificationResource{{Action: "updated", Type: "task", ID: 42}},
		ChannelMessage: "Task 42 completed.",
	})
	ch.Forward(context.Background(), "", model.Notification{
		Type:           "change",
		Resources:      []model.NotificationResource{{Action: "updated", Type: "task", ID: 7}},
		ChannelMessage: "Task 7 completed.",
	})

	// Assert
	assert.Assert(t, cmp.Len(sender.SentChannelParams(), 0))
}

// TestForward_MuteAllStillForwardsNonTaskScoped confirms mute-all only
// gates task-scoped notifications; a message with no task resource is not a
// channel and is still delivered.
func TestForward_MuteAllStillForwardsNonTaskScoped(t *testing.T) {
	t.Parallel()
	// Arrange
	sender := &ChannelSenderMock{
		SendChannelFunc: func(context.Context, mcpchannel.Params) error { return nil },
	}
	ch := NewChannel(sender, "")
	ch.filters[""] = NewTaskFilter()
	_, _, err := ch.muteTool(context.Background(), nil, muteInput{All: true})
	assert.NilError(t, err)

	// Act
	ch.Forward(context.Background(), "", model.Notification{ChannelMessage: "System notice."})

	// Assert
	assert.DeepEqual(t,
		sender.SentChannelParams(),
		[]mcpchannel.Params{{Content: "System notice."}},
		cmpx.OnlyFields("Content"),
	)
}

// TestForward_MuteAllThenUnmuteAll confirms unmute-all lifts a mute-all and
// restores the subscribe-all default.
func TestForward_MuteAllThenUnmuteAll(t *testing.T) {
	t.Parallel()
	// Arrange
	sender := &ChannelSenderMock{
		SendChannelFunc: func(context.Context, mcpchannel.Params) error { return nil },
	}
	ch := NewChannel(sender, "")
	ch.filters[""] = NewTaskFilter()
	_, _, err := ch.muteTool(context.Background(), nil, muteInput{All: true})
	assert.NilError(t, err)
	_, _, err = ch.unmuteTool(context.Background(), nil, unmuteInput{All: true})
	assert.NilError(t, err)

	// Act
	ch.Forward(context.Background(), "", model.Notification{
		Type:           "change",
		Resources:      []model.NotificationResource{{Action: "updated", Type: "task", ID: 42}},
		ChannelMessage: "Task 42 completed.",
	})

	// Assert
	assert.Assert(t, cmp.Len(sender.SentChannelParams(), 1))
}

func TestMuteTool_All(t *testing.T) {
	t.Parallel()
	// Arrange
	ch := NewChannel(&ChannelSenderMock{}, "")
	ch.filters[""] = NewTaskFilter()
	ch.filters[""].Mute(7)

	// Act
	res, _, err := ch.muteTool(context.Background(), nil, muteInput{All: true})

	// Assert
	assert.NilError(t, err)
	var got muteState
	mcptest.UnmarshalCallToolResult(t, res, &got)
	assert.Assert(t, got.All)
	// mute-all subsumes and clears the exception set: nothing is delivered.
	assert.Equal(t, len(got.Unmuted), 0)
}

// TestForward_MuteAllThenUnmuteOne is the maintainer-requested workflow:
// channel_mute(all=true) followed by channel_unmute(task_ids=[123]) mutes
// every task except 123, which keeps delivering.
func TestForward_MuteAllThenUnmuteOne(t *testing.T) {
	t.Parallel()
	// Arrange
	sender := &ChannelSenderMock{
		SendChannelFunc: func(context.Context, mcpchannel.Params) error { return nil },
	}
	ch := NewChannel(sender, "")
	ch.filters[""] = NewTaskFilter()
	_, _, err := ch.muteTool(context.Background(), nil, muteInput{All: true})
	assert.NilError(t, err)
	_, _, err = ch.unmuteTool(context.Background(), nil, unmuteInput{TaskIDs: []int64{123}})
	assert.NilError(t, err)

	// Act
	ch.Forward(context.Background(), "", model.Notification{
		Type:           "change",
		Resources:      []model.NotificationResource{{Action: "updated", Type: "task", ID: 123}},
		ChannelMessage: "Task 123 completed.",
	})
	ch.Forward(context.Background(), "", model.Notification{
		Type:           "change",
		Resources:      []model.NotificationResource{{Action: "updated", Type: "task", ID: 7}},
		ChannelMessage: "Task 7 completed.",
	})

	// Assert: only the un-muted exception (123) kept delivering.
	assert.DeepEqual(t,
		sender.SentChannelParams(),
		[]mcpchannel.Params{{Content: "Task 123 completed."}},
		cmpx.OnlyFields("Content"),
	)
}

// TestUnmuteTool_AfterMuteAll reports the unmuted exceptions under mute-all.
func TestUnmuteTool_AfterMuteAll(t *testing.T) {
	t.Parallel()
	// Arrange
	ch := NewChannel(&ChannelSenderMock{}, "")
	ch.filters[""] = NewTaskFilter()
	_, _, err := ch.muteTool(context.Background(), nil, muteInput{All: true})
	assert.NilError(t, err)

	// Act
	res, _, err := ch.unmuteTool(context.Background(), nil, unmuteInput{TaskIDs: []int64{123, 7}})

	// Assert
	assert.NilError(t, err)
	var got muteState
	mcptest.UnmarshalCallToolResult(t, res, &got)
	assert.Assert(t, got.All)
	assert.DeepEqual(t, got.Unmuted, []int64{7, 123})
}

// TestMuteTool_RemuteUnderMuteAll confirms muting a task that was unmuted
// under mute-all removes the exception, so it is muted with the rest again.
func TestMuteTool_RemuteUnderMuteAll(t *testing.T) {
	t.Parallel()
	// Arrange
	sender := &ChannelSenderMock{
		SendChannelFunc: func(context.Context, mcpchannel.Params) error { return nil },
	}
	ch := NewChannel(sender, "")
	ch.filters[""] = NewTaskFilter()
	_, _, err := ch.muteTool(context.Background(), nil, muteInput{All: true})
	assert.NilError(t, err)
	_, _, err = ch.unmuteTool(context.Background(), nil, unmuteInput{TaskIDs: []int64{123}})
	assert.NilError(t, err)
	res, _, err := ch.muteTool(context.Background(), nil, muteInput{TaskIDs: []int64{123}})
	assert.NilError(t, err)

	// Assert: the exception is gone, so 123 is muted again.
	var got muteState
	mcptest.UnmarshalCallToolResult(t, res, &got)
	assert.Assert(t, got.All)
	assert.Equal(t, len(got.Unmuted), 0)
	ch.Forward(context.Background(), "", model.Notification{
		Type:           "change",
		Resources:      []model.NotificationResource{{Action: "updated", Type: "task", ID: 123}},
		ChannelMessage: "Task 123 completed.",
	})
	assert.Assert(t, cmp.Len(sender.SentChannelParams(), 0))
}

func TestMuteTool(t *testing.T) {
	t.Parallel()
	// Arrange
	ch := NewChannel(&ChannelSenderMock{}, "")
	ch.filters[""] = NewTaskFilter()

	// Act
	res, _, err := ch.muteTool(context.Background(), nil, muteInput{TaskIDs: []int64{9, 3, 3}})

	// Assert
	assert.NilError(t, err)
	var got muteState
	mcptest.UnmarshalCallToolResult(t, res, &got)
	assert.DeepEqual(t, got.Muted, []int64{3, 9})
}

func TestUnmuteTool(t *testing.T) {
	t.Parallel()
	// Arrange
	ch := NewChannel(&ChannelSenderMock{}, "")
	ch.filters[""] = NewTaskFilter()
	ch.filters[""].Mute(1)
	ch.filters[""].Mute(2)
	ch.filters[""].Mute(3)

	// Act
	res, _, err := ch.unmuteTool(context.Background(), nil, unmuteInput{TaskIDs: []int64{2}})

	// Assert
	assert.NilError(t, err)
	var got muteState
	mcptest.UnmarshalCallToolResult(t, res, &got)
	assert.DeepEqual(t, got.Muted, []int64{1, 3})
}

func TestUnmuteTool_All(t *testing.T) {
	t.Parallel()
	// Arrange
	ch := NewChannel(&ChannelSenderMock{}, "")
	ch.filters[""] = NewTaskFilter()
	ch.filters[""].Mute(1)
	ch.filters[""].Mute(2)

	// Act
	res, _, err := ch.unmuteTool(context.Background(), nil, unmuteInput{All: true})

	// Assert
	assert.NilError(t, err)
	var got muteState
	mcptest.UnmarshalCallToolResult(t, res, &got)
	assert.Equal(t, len(got.Muted), 0)
}

func TestMutedTool(t *testing.T) {
	t.Parallel()
	// Arrange
	ch := NewChannel(&ChannelSenderMock{}, "")
	ch.filters[""] = NewTaskFilter()
	ch.filters[""].Mute(5)
	ch.filters[""].Mute(1)

	// Act
	res, _, err := ch.mutedTool(context.Background(), nil, mutedInput{})

	// Assert
	assert.NilError(t, err)
	var got muteState
	mcptest.UnmarshalCallToolResult(t, res, &got)
	assert.DeepEqual(t, got.Muted, []int64{1, 5})
}

func TestForward_OrgMeta(t *testing.T) {
	t.Parallel()
	// Arrange
	sender := &ChannelSenderMock{
		SendChannelFunc: func(context.Context, mcpchannel.Params) error { return nil },
	}
	ch := NewChannel(sender, "")
	ch.filters["acme"] = NewTaskFilter()

	// Act
	ch.Forward(context.Background(), "acme", model.Notification{ChannelMessage: "Task 42 completed."})

	// Assert
	assert.DeepEqual(t,
		sender.SentChannelParams(),
		[]mcpchannel.Params{{Content: "Task 42 completed.", Meta: map[string]string{"org": "acme"}}},
	)
}

// TestForward_PerOrgMuteSet checks that a task muted in one org is still
// delivered from another org's stream.
func TestForward_PerOrgMuteSet(t *testing.T) {
	t.Parallel()
	// Arrange
	sender := &ChannelSenderMock{
		SendChannelFunc: func(context.Context, mcpchannel.Params) error { return nil },
	}
	ch := NewChannel(sender, "")
	ch.filters["a"] = NewTaskFilter()
	ch.filters["b"] = NewTaskFilter()
	ch.filters["a"].Mute(42)
	n := model.Notification{
		Resources:      []model.NotificationResource{{Action: "updated", Type: "task", ID: 42}},
		ChannelMessage: "Task 42 completed.",
	}

	// Act
	ch.Forward(context.Background(), "a", n)
	ch.Forward(context.Background(), "b", n)

	// Assert
	assert.DeepEqual(t,
		sender.SentChannelParams(),
		[]mcpchannel.Params{{Content: "Task 42 completed.", Meta: map[string]string{"org": "b"}}},
	)
}

// TestMuteTool_NamedOrg checks that the mute tools edit the set of the org
// named by org.
func TestMuteTool_NamedOrg(t *testing.T) {
	t.Parallel()
	// Arrange
	ch := NewChannel(&ChannelSenderMock{}, "")
	ch.filters["a"] = NewTaskFilter()
	ch.filters["b"] = NewTaskFilter()

	// Act
	res, _, err := ch.muteTool(context.Background(), nil, muteInput{Org: "b", TaskIDs: []int64{42}})

	// Assert
	assert.NilError(t, err)
	var got muteState
	mcptest.UnmarshalCallToolResult(t, res, &got)
	assert.DeepEqual(t, got, muteState{Org: "b", Muted: []int64{42}}, cmpopts.IgnoreFields(muteState{}, "Note"))
	assert.Assert(t, ch.filters["b"].Muted(42))
	assert.Assert(t, !ch.filters["a"].Muted(42))
}

// TestMuteTool_UnknownOrg checks that org must name a followed entry.
func TestMuteTool_UnknownOrg(t *testing.T) {
	t.Parallel()
	// Arrange
	ch := NewChannel(&ChannelSenderMock{}, "")
	ch.filters["a"] = NewTaskFilter()

	// Act
	res, _, err := ch.muteTool(context.Background(), nil, muteInput{Org: "x", TaskIDs: []int64{42}})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, res.IsError)
	assert.Assert(t, cmp.Contains(res.Content[0].(*mcp.TextContent).Text, `not following org "x"`))
}

// TestFollow_Once checks that following an already followed entry again
// starts no stream.
func TestFollow_Once(t *testing.T) {
	t.Parallel()
	// Arrange
	auths := make(chan string, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	ch := NewChannel(&ChannelSenderMock{}, "")

	// Act
	ch.Follow(ctx, &configfile.Entry{Name: "acme", ID: 1, Key: "k1", Server: srv.URL})
	ch.Follow(ctx, &configfile.Entry{Name: "acme", ID: 1, Key: "k2", Server: srv.URL})
	ch.Follow(ctx, &configfile.Entry{Name: "other", ID: 2, Key: "k3", Server: srv.URL})
	got := []string{<-auths, <-auths}
	cancel()
	srv.Close()

	// Assert
	slices.Sort(got)
	assert.DeepEqual(t, got, []string{"Bearer k1", "Bearer k3"})
	assert.Assert(t, cmp.Len(auths, 0))
}

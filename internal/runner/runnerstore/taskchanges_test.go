package runnerstore_test

import (
	"testing"

	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
)

func TestTaskChanges(t *testing.T) {
	tests := []struct {
		name   string
		events []*gritzv1.Event
		want   []runnerstore.PageTask
	}{
		{
			name: "tasks in first-event order, last command wins",
			events: []*gritzv1.Event{
				{TaskId: 20, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}}},
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
				{TaskId: 20, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_STOP, Version: 2}}},
			},
			want: []runnerstore.PageTask{
				{ID: 20, Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_STOP, Version: 2}},
				{ID: 10, Archived: new(true)},
			},
		},
		{
			name: "no command or archive event",
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED}}},
				{TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
			},
		},
		{
			name: "archived",
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
			},
			want: []runnerstore.PageTask{{ID: 10, Archived: new(true)}},
		},
		{
			name: "auto-archived",
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_AUTO_ARCHIVED}}},
			},
			want: []runnerstore.PageTask{{ID: 10, Archived: new(true)}},
		},
		{
			name: "unarchived",
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED}}},
			},
			want: []runnerstore.PageTask{{ID: 10, Archived: new(false)}},
		},
		{
			name: "new and archived in one page",
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED}}},
				{TaskId: 10, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}}},
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
			},
			want: []runnerstore.PageTask{
				{ID: 10, Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}, Archived: new(true)},
			},
		},
		{
			name: "archived and unarchived in one page",
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED}}},
			},
			want: []runnerstore.PageTask{{ID: 10, Archived: new(false)}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runnerstore.TaskChanges(tt.events)

			assert.DeepEqual(t, got, tt.want, protocmp.Transform())
		})
	}
}

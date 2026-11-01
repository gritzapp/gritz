package runnerstore_test

import (
	"testing"

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
			name: "tasks in first-event order",
			events: []*gritzv1.Event{
				{TaskId: 20, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED}}},
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED}}},
				{TaskId: 20, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
			},
			want: []runnerstore.PageTask{{ID: 20}, {ID: 10}},
		},
		{
			name: "no archive event",
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
			},
			want: []runnerstore.PageTask{{ID: 10}},
		},
		{
			name: "archived",
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
			},
			want: []runnerstore.PageTask{{ID: 10, HasArchiveEvent: true, Archived: true}},
		},
		{
			name: "auto-archived",
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_AUTO_ARCHIVED}}},
			},
			want: []runnerstore.PageTask{{ID: 10, HasArchiveEvent: true, Archived: true}},
		},
		{
			name: "unarchived",
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED}}},
			},
			want: []runnerstore.PageTask{{ID: 10, HasArchiveEvent: true}},
		},
		{
			name: "new and archived in one page",
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED}}},
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
			},
			want: []runnerstore.PageTask{{ID: 10, HasArchiveEvent: true, Archived: true}},
		},
		{
			name: "archived and unarchived in one page",
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED}}},
			},
			want: []runnerstore.PageTask{{ID: 10, HasArchiveEvent: true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changes := runnerstore.NewTaskChanges(tt.events)

			assert.DeepEqual(t, changes.Tasks(), tt.want)
		})
	}
}

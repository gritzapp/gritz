package runnerstore

import (
	"testing"

	"gotest.tools/v3/assert"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
)

func TestTaskChanges(t *testing.T) {
	tests := []struct {
		name        string
		stored      map[int64]bool
		events      []*gritzv1.Event
		wantRows    []TaskChange
		wantChanges []TaskChange
	}{
		{
			name:   "new tasks in first-event order",
			stored: map[int64]bool{},
			events: []*gritzv1.Event{
				{TaskId: 20, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED}}},
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED}}},
				{TaskId: 20, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
			},
			wantRows:    []TaskChange{{ID: 20}, {ID: 10}},
			wantChanges: []TaskChange{{ID: 20}, {ID: 10}},
		},
		{
			name:   "known task unchanged",
			stored: map[int64]bool{10: false},
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
			},
		},
		{
			name:   "archived",
			stored: map[int64]bool{10: false},
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
			},
			wantRows:    []TaskChange{{ID: 10, Archived: true}},
			wantChanges: []TaskChange{{ID: 10, Archived: true}},
		},
		{
			name:   "auto-archived",
			stored: map[int64]bool{10: false},
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_AUTO_ARCHIVED}}},
			},
			wantRows:    []TaskChange{{ID: 10, Archived: true}},
			wantChanges: []TaskChange{{ID: 10, Archived: true}},
		},
		{
			name:   "unarchived",
			stored: map[int64]bool{10: true},
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED}}},
			},
			wantRows:    []TaskChange{{ID: 10}},
			wantChanges: []TaskChange{{ID: 10}},
		},
		{
			name:   "new and archived in one page",
			stored: map[int64]bool{},
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED}}},
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
			},
			wantRows: []TaskChange{{ID: 10, Archived: true}},
		},
		{
			name:   "archived and unarchived in one page",
			stored: map[int64]bool{10: false},
			events: []*gritzv1.Event{
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
				{TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED}}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changes := newTaskChanges(tt.stored)

			for _, e := range tt.events {
				changes.Apply(e)
			}

			assert.DeepEqual(t, changes.Rows(), tt.wantRows)
			assert.DeepEqual(t, changes.Changes(), tt.wantChanges)
		})
	}
}

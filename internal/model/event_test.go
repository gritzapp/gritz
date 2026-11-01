package model

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestFilterPayloads(t *testing.T) {
	t.Parallel()
	events := []*Event{
		{Payload: &InstructionPayload{Text: "first"}},
		{Payload: &LinkPayload{URL: "https://example.com"}},
		{Payload: &InstructionPayload{Text: "second"}},
		{Payload: &ExternalPayload{Description: "webhook"}},
	}

	// Payloads whose type is in the filter come back in stream order; the
	// LinkPayload, whose type isn't requested, is dropped.
	assert.DeepEqual(t,
		FilterPayloads(events, EventTypeInstruction, EventTypeExternal),
		[]EventPayload{
			&InstructionPayload{Text: "first"},
			&InstructionPayload{Text: "second"},
			&ExternalPayload{Description: "webhook"},
		},
	)
}

func TestLifecyclePayload_ProtoRoundTrip(t *testing.T) {
	t.Parallel()
	want := &LifecyclePayload{
		Kind:       LifecycleKindSandboxFailed,
		Actor:      RunnerActor,
		FromStatus: "Running",
		ToStatus:   "Failed",
		Message:    "container failed",
	}
	ev := &Event{TaskID: 7, OrgID: 3, Payload: want}

	// model -> proto -> model preserves the arm and its fields.
	got := EventFromProto(ev.Proto())
	payload, ok := got.Payload.(*LifecyclePayload)
	assert.Assert(t, ok)
	assert.DeepEqual(t, payload, want)
	assert.Equal(t, payload.Type(), EventTypeLifecycle)
}

func TestLifecyclePayload_Summary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload LifecyclePayload
		want    string
	}{
		{
			name:    "created by user",
			payload: LifecyclePayload{Kind: LifecycleKindCreated, Actor: UserActor("icholy")},
			want:    "Created by icholy",
		},
		{
			name:    "created by routing rule",
			payload: LifecyclePayload{Kind: LifecycleKindCreated, Actor: RouterActor},
			want:    "Created by routing rule",
		},
		{
			name:    "cancelled no actor name",
			payload: LifecyclePayload{Kind: LifecycleKindCancelled, Actor: RunnerActor},
			want:    "Cancelled",
		},
		{
			name: "sandbox exited with statuses",
			payload: LifecyclePayload{
				Kind:       LifecycleKindSandboxExited,
				Actor:      RunnerActor,
				FromStatus: "Running",
				ToStatus:   "Completed",
			},
			want: "Sandbox exited (Running -> Completed)",
		},
		{
			name: "sandbox failed with message",
			payload: LifecyclePayload{
				Kind:    LifecycleKindSandboxFailed,
				Actor:   RunnerActor,
				Message: "boom",
			},
			want: "Sandbox failed: boom",
		},
		{
			name: "updated with fields by user",
			payload: LifecyclePayload{
				Kind:   LifecycleKindUpdated,
				Actor:  UserActor("icholy"),
				Fields: []string{"name", "status"},
			},
			want: "Updated name, status by icholy",
		},
		{
			name:    "updated without fields",
			payload: LifecyclePayload{Kind: LifecycleKindUpdated, Actor: RunnerActor},
			want:    "Updated",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.payload.Summary(), tt.want)
		})
	}
}

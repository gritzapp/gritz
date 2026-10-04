package agentprompt

import (
	"testing"
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/golden"
)

// TestRenderGolden snapshots the whole rendered bootstrap prompt across its
// branches: the first-run brief with a nil task (rendered nil-safely), the
// field-complete first-run brief, a wake that renders the pending events as
// markdown blocks, the bare fallback when a wake has nothing pending, and a
// first-run brief with a workspace prompt in the how-to-work section. Both the
// brief and the wake render through the same flat renderEvent stream (no section
// headers), with links appended at the end on init only and the workspace prompt
// rendered inside the how-to-work section on init only; the wake header stays
// thin (id · name only), and a wake never renders the workspace prompt even when
// one is set.
// Regenerate the goldens with: go test ./internal/runner/agent/agentprompt/ -run TestRenderGolden -update
func TestRenderGolden(t *testing.T) {
	t.Parallel()
	// Fixed timestamps keep the rendered createdAt fields stable across runs.
	events := []*gritzv1.Event{
		{
			Id:        42,
			CreatedAt: timestamppb.New(time.Unix(1_700_000_000, 0).UTC()),
			Payload: &gritzv1.Event_External{External: &gritzv1.ExternalPayload{
				Source:      "github",
				Type:        "review_requested",
				Description: "PR review requested",
				Url:         "https://github.com/icholy/gritz/pull/1394",
			}},
		},
		{
			Id:        43,
			CreatedAt: timestamppb.New(time.Unix(1_700_000_100, 0).UTC()),
			Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{
				Text: "keep going",
				Url:  "https://github.com/icholy/gritz/issues/2",
			}},
		},
	}
	// The task the wake header renders (`# Task {id} · {name}`) — the same task the
	// driver already fetched at the top of the run.
	task := &gritzv1.Task{
		Id:        1302,
		Name:      "first-run-brief L2",
		Status:    gritzv1.TaskStatus_RUNNING,
		Workspace: "gritz",
		Namespace: "team-core",
		Url:       "https://gritz.dev/ui/tasks/1302",
	}
	// A field-complete brief: named task with url/namespace, an instruction event,
	// an external event, and a link. Exercises every field the first-run brief
	// renders (Task, Events, Links). The task reuses `task` above.
	briefEvents := []*gritzv1.Event{
		{
			Id:        43,
			CreatedAt: timestamppb.New(time.Unix(1_700_000_100, 0).UTC()),
			Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{
				Text: "Implement the first-run brief.",
				Url:  "https://github.com/icholy/gritz/issues/1398",
			}},
		},
		{
			Id:        42,
			CreatedAt: timestamppb.New(time.Unix(1_700_000_000, 0).UTC()),
			Payload: &gritzv1.Event_External{External: &gritzv1.ExternalPayload{
				Source:      "github",
				Type:        "review_requested",
				Description: "PR review requested",
				Url:         "https://github.com/icholy/gritz/pull/1394",
			}},
		},
	}
	briefLinks := []*gritzv1.TaskLink{
		{
			Id:        7,
			TaskId:    1302,
			Relevance: "the PR this task opened",
			Url:       "https://github.com/icholy/gritz/pull/1394",
			Title:     "feat(agent): first-run brief",
			Subscribe: true,
			CreatedAt: timestamppb.New(time.Unix(1_700_000_050, 0).UTC()),
		},
	}
	tests := []struct {
		name   string
		opts   Options
		golden string
	}{
		{
			// A first run with a nil task: the brief renders nil-safely via the
			// proto getters (zero-value header, no events, no links).
			name:   "first run with a nil task renders the brief",
			opts:   Options{},
			golden: "prompt-first-run-nil-task.golden",
		},
		{
			name:   "first run renders the task brief",
			opts:   Options{Task: task, Events: briefEvents, Links: briefLinks},
			golden: "prompt-first-run-brief.golden",
		},
		{
			// A first run with a workspace prompt: the prompt renders inside the
			// how-to-work section, before the context dump. The prompt is init-only.
			name:   "first run renders the task brief with a workspace prompt",
			opts:   Options{Task: task, Events: briefEvents, Links: briefLinks, Prompt: "Custom workspace instructions."},
			golden: "prompt-first-run-brief-workspace.golden",
		},
		{
			name:   "wake injects pending events",
			opts:   Options{Started: true, Events: events, Task: task},
			golden: "prompt-wake-events.golden",
		},
		{
			name:   "wake with nothing pending falls back",
			opts:   Options{Started: true},
			golden: "prompt-wake-empty.golden",
		},
		{
			// A wake carrying only an instruction event: a flat single-block stream.
			name:   "wake with only an instruction",
			opts:   Options{Started: true, Task: task, Events: events[1:]},
			golden: "prompt-wake-instruction-only.golden",
		},
		{
			// A wake carrying only a context (external) event: a flat single-block stream.
			name:   "wake with only a context event",
			opts:   Options{Started: true, Task: task, Events: events[:1]},
			golden: "prompt-wake-context-only.golden",
		},
		{
			// A wake never renders the workspace prompt: even with a Prompt set, the
			// output is byte-identical to the plain wake (prompt-wake-events.golden).
			name:   "wake does not render the workspace prompt",
			opts:   Options{Started: true, Prompt: "Custom workspace instructions.", Events: events, Task: task},
			golden: "prompt-wake-events.golden",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Render(tt.opts)
			assert.NilError(t, err)
			golden.Assert(t, got, tt.golden)
		})
	}
}

package agentprompt

import (
	"testing"
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gotest.tools/v3/golden"
)

// TestRenderEvent snapshots renderEvent across all five payload arms, one golden
// per arm. The external arm is exercised both with data/details (the worked
// example in the proposal) and without; both carry source and type, as every
// external event does in practice.
// Regenerate the goldens with: go test ./internal/runner/agent/agentprompt/ -run TestRenderEvent -update
func TestRenderEvent(t *testing.T) {
	t.Parallel()
	// Fixed timestamps: 1_700_000_000 == 2023-11-14 22:13:20 UTC. The formatter
	// drops seconds, so each renders to the minute.
	at := func(offset int64) *timestamppb.Timestamp {
		return timestamppb.New(time.Unix(1_700_000_000+offset, 0).UTC())
	}
	tests := []struct {
		name   string
		event  *gritzv1.Event
		golden string
	}{
		{
			name: "instruction",
			event: &gritzv1.Event{
				Id:        43,
				CreatedAt: at(100),
				Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{
					Text: "keep going",
					Url:  "https://github.com/icholy/gritz/issues/2",
				}},
			},
			golden: "event-instruction.golden",
		},
		{
			name: "external without data or details",
			event: &gritzv1.Event{
				Id:        42,
				CreatedAt: at(0),
				Payload: &gritzv1.Event_External{External: &gritzv1.ExternalPayload{
					Source:      "github",
					Type:        "review_requested",
					Description: "PR review requested",
					Url:         "https://github.com/icholy/gritz/pull/1394",
				}},
			},
			golden: "event-external-plain.golden",
		},
		{
			name: "external with source, type, content, and details",
			event: &gritzv1.Event{
				Id:        51,
				CreatedAt: at(400),
				Payload: &gritzv1.Event_External{External: &gritzv1.ExternalPayload{
					Description: "icholy commented on driver.go",
					Source:      "github",
					Type:        "pull_request_review_comment",
					Url:         "https://github.com/icholy/gritz/pull/1394#discussion_r512",
					Data:        "This nil check needs a test before we merge — can you add one that covers the wake path?",
					Details: map[string]string{
						"path":      "internal/agent/driver.go",
						"line":      "218",
						"side":      "RIGHT",
						"diff_hunk": "@@ -215,7 +215,7 @@ func Render(opts Options) {\n-\tTaskDetails: brief,\n+\tTaskDetails: details, // nil on wake",
					},
				}},
			},
			golden: "event-external.golden",
		},
		{
			name: "lifecycle",
			event: &gritzv1.Event{
				Id:        60,
				CreatedAt: at(0),
				Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{
					Kind:       gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_EXITED,
					FromStatus: "Running",
					ToStatus:   "Completed",
				}},
			},
			golden: "event-lifecycle.golden",
		},
		{
			name: "link",
			event: &gritzv1.Event{
				Id:        7,
				CreatedAt: at(50),
				Payload: &gritzv1.Event_Link{Link: &gritzv1.LinkPayload{
					LinkId:    7,
					Title:     "feat(agent): first-run brief",
					Relevance: "the PR this task opened",
					Url:       "https://github.com/icholy/gritz/pull/1394",
					Subscribe: true,
				}},
			},
			golden: "event-link.golden",
		},
		{
			name: "report",
			event: &gritzv1.Event{
				Id:        70,
				CreatedAt: at(100),
				Payload: &gritzv1.Event_Report{Report: &gritzv1.ReportPayload{
					Content: "Looked into the failing test; root cause is a nil cursor.",
				}},
			},
			golden: "event-report.golden",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			golden.Assert(t, renderEvent(tt.event), tt.golden)
		})
	}
}

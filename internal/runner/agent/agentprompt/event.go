package agentprompt

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/icholy/gritz/internal/model"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// renderEvent renders a single event as a prose-framed markdown block. It
// switches on the set payload arm and emits a header line (`### … — {time}`)
// plus an arm-specific body/footer, per the mapping in
// proposals/implemented/hybrid-prompt-rendering.md. It is the hybrid renderer: the
// envelope (header, labeled Type:/Description:/URL: fields) and the external
// content body are prose markdown, while the opaque external details map is
// emitted verbatim as an indented-JSON block. The returned block has no trailing
// newline; callers join blocks with blank lines. An event with no set arm renders empty.
func renderEvent(event *gritzv1.Event) string {
	ts := formatEventTime(event.GetCreatedAt())
	var b strings.Builder
	switch arm := event.GetPayload().(type) {
	case *gritzv1.Event_Instruction:
		p := arm.Instruction
		b.WriteString("### Instruction — " + ts + "\n")
		b.WriteString(p.GetText())
		if p.GetUrl() != "" {
			b.WriteString("\nSource: " + p.GetUrl())
		}
	case *gritzv1.Event_External:
		p := arm.External
		b.WriteString("### External Event — " + ts + "\n")
		fmt.Fprintf(&b, "\nType: %s - %s", p.GetSource(), p.GetType())
		if p.GetDescription() != "" {
			b.WriteString("\nDescription: " + p.GetDescription())
		}
		if p.GetUrl() != "" {
			b.WriteString("\nURL: " + p.GetUrl())
		}
		if p.GetData() != "" {
			b.WriteString("\n\n" + p.GetData())
		}
		if len(p.GetDetails()) > 0 {
			b.WriteString("\n\n```json\n" + renderDetails(p.GetDetails()) + "\n```")
		}
	case *gritzv1.Event_Lifecycle:
		b.WriteString("### " + lifecycleSummary(event) + " — " + ts)
	case *gritzv1.Event_Link:
		p := arm.Link
		b.WriteString(linkBlock(p.GetTitle(), ts, p.GetRelevance(), p.GetUrl(), p.GetSubscribe()))
	case *gritzv1.Event_Report:
		b.WriteString("### Report — " + ts + "\n")
		b.WriteString(arm.Report.GetContent())
	default:
		return ""
	}
	return b.String()
}

// linkBlock formats a link as the shared `### Link: {title} — {time}` /
// relevance / url · (subscribed) block. It is the one formatter behind both the
// renderEvent link arm (over a LinkPayload) and the task's standing links (over
// *TaskLink) appended at the end of the first-run brief, so the two emit
// byte-identical blocks. The returned block has no trailing newline; callers
// join blocks with blank lines.
func linkBlock(title, ts, relevance, url string, subscribe bool) string {
	var b strings.Builder
	b.WriteString("### Link: " + title + " — " + ts + "\n")
	b.WriteString(relevance + "\n")
	b.WriteString(url)
	if subscribe {
		b.WriteString(" · (subscribed)")
	}
	return b.String()
}

// formatEventTime renders an event timestamp as a deterministic, human-readable
// UTC line — e.g. "2023-11-14 22:15 UTC" — rather than RFC3339. It formats in
// UTC ourselves so the output is stable across processes, and the web UI
// timeline already speaks this format. It is nil-safe (a nil timestamp renders
// the Unix epoch).
func formatEventTime(ts *timestamppb.Timestamp) string {
	return ts.AsTime().UTC().Format("2006-01-02 15:04") + " UTC"
}

// renderDetails marshals an external event's opaque details map as one
// indented-JSON block. The map is source-defined and opaque, so it is rendered
// untouched — json.MarshalIndent sorts the keys and the renderer promotes none
// of them. map[string]string never fails to marshal.
func renderDetails(details map[string]string) string {
	out, err := json.MarshalIndent(details, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(out)
}

// lifecycleSummary renders a lifecycle event's human-readable summary line
// (e.g. "Sandbox exited (Running -> Completed)"). It reuses
// model.LifecyclePayload.Summary — the same line the timeline shows — so the
// model needn't decode the lifecycle enum.
func lifecycleSummary(event *gritzv1.Event) string {
	if p, ok := model.EventPayloadFromProto(event).(*model.LifecyclePayload); ok {
		return p.Summary()
	}
	return ""
}

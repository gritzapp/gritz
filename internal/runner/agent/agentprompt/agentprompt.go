// Package agentprompt renders the bootstrap prompt sent to the agent. It takes
// its inputs as parameters so it depends only on the proto package and not on
// internal/agent (avoiding an import cycle).
package agentprompt

import (
	_ "embed"
	"strconv"
	"strings"
	"text/template"

	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
)

// renderHeader renders the task header block: the `# Task {id} · {name}` title
// plus the workspace/namespace and task url lines. It deliberately omits status
// — a task reading this prompt is by definition running, so status is noise (see
// proposals/implemented/hybrid-prompt-rendering.md). The returned block has no
// trailing newline; callers join blocks with blank lines. Nil-safe via the
// proto getters.
func renderHeader(task *gritzv1.Task) string {
	var b strings.Builder
	b.WriteString("# Task " + strconv.FormatInt(task.GetId(), 10) + " · " + task.GetName() + "\n\n")
	b.WriteString("- Workspace: " + task.GetWorkspace() + " · Namespace: " + task.GetNamespace() + "\n")
	b.WriteString("- Task: " + task.GetUrl())
	return b.String()
}

// renderLink renders a task's standing link through the shared linkBlock
// formatter, so a link listed under the first-run brief is byte-identical to the
// same link rendered inline as an event's link arm. Registered as the renderLink
// template func; the init branch loops over Options.Links. Nil-safe via the
// proto getters.
func renderLink(link *gritzv1.TaskLink) string {
	return linkBlock(
		link.GetTitle(), formatEventTime(link.GetCreatedAt()),
		link.GetRelevance(), link.GetUrl(), link.GetSubscribe())
}

//go:embed PROMPT.md
var promptText string

var promptTemplate = template.Must(
	template.New("prompt").Funcs(template.FuncMap{
		"renderEvent":  renderEvent,
		"renderHeader": renderHeader,
		"renderLink":   renderLink,
	}).Parse(promptText),
)

// Options are the inputs to Render.
type Options struct {
	// Started reports whether the task has run before. The first run renders the
	// task brief; a subsequent run renders the wake branch.
	Started bool
	// Prompt is the workspace prompt appended at the end, if any.
	Prompt string

	// Task carries the id and name rendered into the wake header line
	// (`# Task {id} · {name}`) and the full first-run header (renderHeader). It is
	// the task the driver already fetched at the top of the run, so neither path
	// needs an extra fetch. The proto getters are nil-safe, so a nil Task renders
	// zero values rather than panicking.
	Task *gritzv1.Task

	// Events is the task's event stream, looped once by the shared template loop and
	// rendered as markdown blocks via the renderEvent func. On a wake it is the
	// instruction + external events drained since the saved cursor; on the first run
	// it is the brief's full event stream. It is empty on a wake with nothing
	// pending, in which case nothing is injected.
	Events []*gritzv1.Event

	// Links are the task's standing links, rendered at the end of the first-run
	// brief via the renderLink func. They are init-only: a wake resumes the same
	// session, which already saw the links on its first turn.
	Links []*gritzv1.TaskLink
}

// Render builds the bootstrap prompt sent to the agent from opts.
func Render(opts Options) (string, error) {
	var b strings.Builder
	if err := promptTemplate.Execute(&b, opts); err != nil {
		return "", err
	}
	return b.String(), nil
}

package runnerstore

import gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"

// TaskChanges accumulates a page's effect on the tasks projection.
type TaskChanges struct {
	stored map[int64]bool
	ids    []int64 // in order of each task's first event in the page
	tasks  map[int64]*taskState
}

// taskState is one task's archive state over a page.
type taskState struct {
	known    bool // the task had a row before the page
	before   bool // its archived state before the page, when known
	archived bool // its archived state after the events applied so far
}

// NewTaskChanges starts from the stored archive state of the page's tasks
// that are already known.
func NewTaskChanges(stored map[int64]bool) *TaskChanges {
	return &TaskChanges{stored: stored, tasks: map[int64]*taskState{}}
}

// Apply folds one event into its task's state.
func (c *TaskChanges) Apply(e *gritzv1.Event) {
	t, ok := c.tasks[e.GetTaskId()]
	if !ok {
		before, known := c.stored[e.GetTaskId()]
		t = &taskState{known: known, before: before, archived: before}
		c.tasks[e.GetTaskId()] = t
		c.ids = append(c.ids, e.GetTaskId())
	}
	t.archived = foldArchived(t.archived, e)
}

// Rows returns the tasks rows to write: new tasks, and known ones whose
// archived state changed.
func (c *TaskChanges) Rows() []TaskChange {
	var rows []TaskChange
	for _, id := range c.ids {
		t := c.tasks[id]
		if t.known && t.before == t.archived {
			continue
		}
		rows = append(rows, TaskChange{ID: id, Archived: t.archived})
	}
	return rows
}

// Changes returns what to report: Rows minus tasks that were new and ended
// the page archived, since they were never active.
func (c *TaskChanges) Changes() []TaskChange {
	var changes []TaskChange
	for _, row := range c.Rows() {
		if !c.tasks[row.ID].known && row.Archived {
			continue
		}
		changes = append(changes, row)
	}
	return changes
}

// foldArchived returns whether a task is archived after e, given whether it
// was before. Archived and AutoArchived set it, Unarchived clears it, and any
// other event, including a payload arm this runner doesn't know, leaves it.
func foldArchived(archived bool, e *gritzv1.Event) bool {
	switch e.GetLifecycle().GetKind() {
	case gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED, gritzv1.LifecycleKind_LIFECYCLE_KIND_AUTO_ARCHIVED:
		return true
	case gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED:
		return false
	default:
		return archived
	}
}

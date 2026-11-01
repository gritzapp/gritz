package runnerstore

import gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"

// TaskChanges computes a page's effect on the tasks projection.
type TaskChanges struct {
	events []*gritzv1.Event
	ids    []int64        // distinct, in order of each task's first event
	stored map[int64]bool // archive state from before the page, for known tasks
}

// NewTaskChanges records a page's events and its distinct task ids in order
// of each task's first event.
func NewTaskChanges(events []*gritzv1.Event) *TaskChanges {
	c := &TaskChanges{events: events, stored: map[int64]bool{}}
	seen := map[int64]bool{}
	for _, e := range events {
		if !seen[e.GetTaskId()] {
			seen[e.GetTaskId()] = true
			c.ids = append(c.ids, e.GetTaskId())
		}
	}
	return c
}

// TaskIDs returns the page's distinct task ids, for loading their stored state.
func (c *TaskChanges) TaskIDs() []int64 {
	return c.ids
}

// Stored records a task's archive state from before the page. Tasks never
// passed to Stored are new.
func (c *TaskChanges) Stored(id int64, archived bool) {
	c.stored[id] = archived
}

// Rows returns the tasks rows to write: new tasks, and known ones whose
// archived state changed.
func (c *TaskChanges) Rows() []TaskChange {
	archived := map[int64]bool{}
	for id, a := range c.stored {
		archived[id] = a
	}
	for _, e := range c.events {
		archived[e.GetTaskId()] = foldArchived(archived[e.GetTaskId()], e)
	}
	var rows []TaskChange
	for _, id := range c.ids {
		if before, known := c.stored[id]; known && before == archived[id] {
			continue
		}
		rows = append(rows, TaskChange{ID: id, Archived: archived[id]})
	}
	return rows
}

// Changes returns what to report: Rows minus tasks that were new and ended
// the page archived, since they were never active.
func (c *TaskChanges) Changes() []TaskChange {
	var changes []TaskChange
	for _, row := range c.Rows() {
		if _, known := c.stored[row.ID]; !known && row.Archived {
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

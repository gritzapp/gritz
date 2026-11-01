package runnerstore

import gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"

// TaskChanges records, for each task in a page, the archive state the page's
// events leave it in.
type TaskChanges struct {
	tasks []PageTask // in order of each task's first event
}

// PageTask is one task in a page.
type PageTask struct {
	ID int64
	// HasArchiveEvent is whether the page has an Archived, AutoArchived or
	// Unarchived event for the task. Archived is the last one's result, and
	// is false when there is none.
	HasArchiveEvent bool
	Archived        bool
}

// NewTaskChanges folds a page's events into each task's archive state.
func NewTaskChanges(events []*gritzv1.Event) *TaskChanges {
	c := &TaskChanges{}
	index := map[int64]int{}
	for _, e := range events {
		i, ok := index[e.GetTaskId()]
		if !ok {
			i = len(c.tasks)
			index[e.GetTaskId()] = i
			c.tasks = append(c.tasks, PageTask{ID: e.GetTaskId()})
		}
		if archived, ok := archiveState(e); ok {
			c.tasks[i].HasArchiveEvent = true
			c.tasks[i].Archived = archived
		}
	}
	return c
}

// Tasks returns the page's tasks in order of each task's first event.
func (c *TaskChanges) Tasks() []PageTask {
	return c.tasks
}

// archiveState returns the archive state e sets, and false when e is not an
// archive event. Archived and AutoArchived set it, Unarchived clears it, and
// any other event, including a payload arm this runner doesn't know, leaves
// it alone.
func archiveState(e *gritzv1.Event) (archived, ok bool) {
	switch e.GetLifecycle().GetKind() {
	case gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED, gritzv1.LifecycleKind_LIFECYCLE_KIND_AUTO_ARCHIVED:
		return true, true
	case gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED:
		return false, true
	default:
		return false, false
	}
}

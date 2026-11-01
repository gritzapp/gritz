package runnerstore

import gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"

// PageTask is one task in a page.
type PageTask struct {
	ID int64
	// HasArchiveEvent is whether the page has an Archived, AutoArchived or
	// Unarchived event for the task. Archived is the last one's result, and
	// is false when there is none.
	HasArchiveEvent bool
	Archived        bool
}

// TaskChanges folds a page's events into one PageTask per task, in order of
// each task's first event.
func TaskChanges(events []*gritzv1.Event) []PageTask {
	var tasks []PageTask
	index := map[int64]int{}
	for _, e := range events {
		i, ok := index[e.GetTaskId()]
		if !ok {
			i = len(tasks)
			index[e.GetTaskId()] = i
			tasks = append(tasks, PageTask{ID: e.GetTaskId()})
		}
		if archived, ok := archiveState(e); ok {
			tasks[i].HasArchiveEvent = true
			tasks[i].Archived = archived
		}
	}
	return tasks
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

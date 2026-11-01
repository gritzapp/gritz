package runnerstore

import gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"

// PageTask is one task's command and archive events in a page.
type PageTask struct {
	ID      int64
	Command *gritzv1.CommandPayload // last command event in the page, or nil
	// CommandEventID is the id of the page's last command event, set with
	// Command. It is temporary scaffolding for the runner's followers, and
	// goes along with the tasks' handled position once the driver acts on
	// commands (L8).
	CommandEventID int64
	Archived       *bool // last archive event's result, or nil
}

// TaskChanges folds a page's events into one PageTask per task with a command
// or archive event in it, in order of each task's first such event. Other
// events don't affect the tasks table.
func TaskChanges(events []*gritzv1.Event) []PageTask {
	var tasks []PageTask
	index := map[int64]int{}
	for _, e := range events {
		command := e.GetCommand()
		archived, isArchive := archiveState(e)
		if command == nil && !isArchive {
			continue
		}
		i, ok := index[e.GetTaskId()]
		if !ok {
			i = len(tasks)
			index[e.GetTaskId()] = i
			tasks = append(tasks, PageTask{ID: e.GetTaskId()})
		}
		if command != nil {
			tasks[i].Command = command
			tasks[i].CommandEventID = e.GetId()
		}
		if isArchive {
			tasks[i].Archived = &archived
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

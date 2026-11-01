package runner

// Runner returns the task's registered TaskRunner, or nil once its RunLoop has
// returned for the archived task, or if it never had one.
func (m *TaskManager) Runner(taskID int64) *TaskRunner {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tasks[taskID]
}

// NewTaskRunner returns a TaskRunner for the task built as the manager builds
// them, but not registered: no RunLoop runs it, so a test can call its methods
// directly.
func (m *TaskManager) NewTaskRunner(taskID int64) *TaskRunner {
	return m.newTaskRunner(taskID)
}

// Wait waits for the Run goroutines tr has started to return. It must not be
// called while tr can start another.
func (tr *TaskRunner) Wait() { tr.wait() }

package mcpbridge

import (
	"slices"
	"sync"
)

// TaskFilter is the per-process, concurrency-safe mute state for channel
// notifications: it decides whether a given task's notifications should be
// delivered.
//
// By default (all=false, no exceptions) every task is delivered. Muting
// specific tasks turns the exception set into a blocklist. MuteAll flips
// the default so every task is muted and the exception set inverts into an
// allowlist: unmuting a task under mute-all keeps just that task
// delivering while the rest stay muted. Clear resets to the
// deliver-everything default. The state is in-memory only.
type TaskFilter struct {
	mu  sync.Mutex
	all bool // when true every task is muted by default (mute-all)
	// except holds the task ids whose delivery differs from the default:
	// when all is false these are the muted ids (a blocklist); when all is
	// true these are the explicitly-unmuted ids that keep delivering (an
	// allowlist of exceptions).
	except map[int64]struct{}
}

// NewTaskFilter returns a filter that delivers every task (empty mute set).
func NewTaskFilter() *TaskFilter {
	return &TaskFilter{except: map[int64]struct{}{}}
}

// Mute stops delivering the given task. Under mute-all it drops any unmute
// exception so the task is muted along with the rest again.
func (f *TaskFilter) Mute(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.all {
		delete(f.except, id)
		return
	}
	f.except[id] = struct{}{}
}

// MuteAll mutes every task. The exception set is cleared so nothing is
// delivered until a task is individually unmuted.
func (f *TaskFilter) MuteAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.all = true
	clear(f.except)
}

// Unmute resumes delivering the given task. Under mute-all it records the
// task as an exception so it keeps delivering while the rest stay muted.
func (f *TaskFilter) Unmute(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.all {
		f.except[id] = struct{}{}
		return
	}
	delete(f.except, id)
}

// Clear resets the filter to the deliver-everything default.
func (f *TaskFilter) Clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.all = false
	clear(f.except)
}

// Muted reports whether the given task's notifications are currently
// suppressed.
func (f *TaskFilter) Muted(id int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.except[id]
	if f.all {
		return !ok // muted unless this task is an explicit exception
	}
	return ok // muted only if in the blocklist
}

// All reports whether every task is muted by default.
func (f *TaskFilter) All() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.all
}

// Exceptions returns the exception task ids, sorted ascending. When All is
// false these are the muted ids; when All is true these are the
// explicitly-unmuted ids that keep delivering.
func (f *TaskFilter) Exceptions() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []int64
	for id := range f.except {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

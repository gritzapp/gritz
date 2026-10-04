package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/sqlc"
	"github.com/gritzapp/gritz/internal/x/pagination"
)

func (s *Store) CreateTask(ctx context.Context, tx *sql.Tx, task *model.Task) error {
	now := time.Now().UTC()
	id, err := s.q(tx).CreateTask(ctx, sqlc.CreateTaskParams{
		Name:         task.Name,
		Runner:       task.Runner,
		Workspace:    task.Workspace,
		Status:       int32(task.Status),
		Command:      int32(task.Command),
		Version:      task.Version,
		OrgID:        task.OrgID,
		CreatedAt:    now,
		UpdatedAt:    now,
		Archived:     task.Archived,
		AutoArchive:  task.AutoArchive.Microseconds(),
		ShellSession: task.ShellSession,
		Namespace:    task.Namespace,
	})
	if err != nil {
		return err
	}
	task.ID = id
	task.CreatedAt = now
	task.UpdatedAt = now
	return nil
}

func (s *Store) GetTask(ctx context.Context, tx *sql.Tx, id int64, orgID int64) (*model.Task, error) {
	row, err := s.q(tx).GetTask(ctx, sqlc.GetTaskParams{
		ID:    id,
		OrgID: orgID,
	})
	if err != nil {
		return nil, err
	}
	return toModelTask(row)
}

func (s *Store) GetTaskForUpdate(ctx context.Context, tx *sql.Tx, id int64, orgID int64) (*model.Task, error) {
	row, err := s.q(tx).GetTaskForUpdate(ctx, sqlc.GetTaskForUpdateParams{
		ID:    id,
		OrgID: orgID,
	})
	if err != nil {
		return nil, err
	}
	return toModelTask(row)
}

func (s *Store) ListTasks(ctx context.Context, tx *sql.Tx, orgID int64) ([]*model.Task, error) {
	rows, err := s.q(tx).ListTasks(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return toModelTasks(rows)
}

// taskCursor is the keyset a task page token encodes. created_at is not
// unique, so id is the tiebreaker. Archived binds the token to the filter it
// was minted under so a cursor can't be silently replayed across filters; the
// omitempty tag keeps the common (false) token byte-compatible with tokens
// minted before the archived filter existed.
type taskCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        int64     `json:"i"`
	Archived  bool      `json:"a,omitempty"`
}

// ListTasksPageParams mirrors the RPC's pagination fields as plain values so
// the handler can pass them through untouched.
type ListTasksPageParams struct {
	OrgID     int64
	PageSize  int32  // 0 means the default (50); max 100
	PageToken string // opaque token from a previous page; empty for the first page
	Archived  bool   // include archived tasks alongside active ones
}

// taskSource implements pagination.Source for the tasks table.
type taskSource struct {
	store  *Store
	tx     *sql.Tx
	params ListTasksPageParams
}

// Query serves the task list's single forward (descending, newest-first) walk.
// The task list is not followed, so backward is unsupported: List never asks for
// it (the RPC exposes no newer token to resubmit), and the guard is only reached
// if a client hand-crafts a backward token.
func (src taskSource) Query(ctx context.Context, token pagination.Token[taskCursor], limit int) ([]*model.Task, error) {
	if token.Backward {
		return nil, pagination.ErrUnsupportedDirection
	}
	// The token binds the filter it was minted under: a cursor whose Archived
	// stamp disagrees with this request is a cross-filter replay, rejected as a
	// bad request (→ CodeInvalidArgument) rather than silently resumed.
	if token.Cursor != nil && token.Cursor.Archived != src.params.Archived {
		return nil, fmt.Errorf("%w: page token does not match archived filter", pagination.ErrInvalidRequest)
	}
	args := sqlc.ListTasksPageParams{
		OrgID:     src.params.OrgID,
		Archived:  src.params.Archived,
		UseCursor: token.Cursor != nil,
		PageLimit: int32(limit), // int32 only at the sqlc boundary
	}
	if token.Cursor != nil {
		args.CursorCreatedAt = token.Cursor.CreatedAt
		args.CursorID = token.Cursor.ID
	}
	rows, err := src.store.q(src.tx).ListTasksPage(ctx, args)
	if err != nil {
		return nil, err
	}
	return toModelTasks(rows)
}

func (src taskSource) Cursor(t *model.Task) taskCursor {
	return taskCursor{CreatedAt: t.CreatedAt, ID: t.ID, Archived: src.params.Archived}
}

// ListTasksPage returns a keyset-paginated page of tasks for the org, newest
// first, plus the token for the next page. By default only active tasks are
// returned; p.Archived includes archived tasks interleaved by the same
// created_at DESC, id DESC ordering. It owns everything pagination-related: the
// cursor keyset (created_at, id), the page-size bounds, and the opaque token
// format. A bad PageSize, an undecodable PageToken, or a token whose archived
// filter disagrees with the request surfaces as a wrapped
// pagination.ErrInvalidRequest; query failures surface as-is.
func (s *Store) ListTasksPage(ctx context.Context, tx *sql.Tx, p ListTasksPageParams) (*pagination.Page[*model.Task], error) {
	return pagination.List(ctx, pagination.Options[*model.Task, taskCursor]{
		DefaultPageSize: 50,
		MaxPageSize:     100,
		Reverse:         false,
		PageSize:        int(p.PageSize),
		PageToken:       p.PageToken,
		Source:          taskSource{store: s, tx: tx, params: p},
	})
}

func (s *Store) ListTasksForRunner(ctx context.Context, tx *sql.Tx, runner string, orgID int64) ([]*model.Task, error) {
	rows, err := s.q(tx).ListTasksForRunner(ctx, sqlc.ListTasksForRunnerParams{
		Runner: runner,
		OrgID:  orgID,
	})
	if err != nil {
		return nil, err
	}
	return toModelTasks(rows)
}

func (s *Store) UpdateTask(ctx context.Context, tx *sql.Tx, task *model.Task) error {
	task.UpdatedAt = time.Now().UTC()
	return s.q(tx).UpdateTask(ctx, sqlc.UpdateTaskParams{
		Name:         task.Name,
		Runner:       task.Runner,
		Workspace:    task.Workspace,
		Status:       int32(task.Status),
		Command:      int32(task.Command),
		Version:      task.Version,
		UpdatedAt:    task.UpdatedAt,
		Archived:     task.Archived,
		AutoArchive:  task.AutoArchive.Microseconds(),
		ShellSession: task.ShellSession,
		ID:           task.ID,
		OrgID:        task.OrgID,
	})
}

// TaskDueForArchive identifies a task whose auto_archive deadline has
// elapsed. The version is for an optimistic concurrency check; org_id lets
// the caller follow up with GetTaskForUpdate / UpdateTask without an extra
// resolve step.
type TaskDueForArchive struct {
	ID      int64
	Version int64
	OrgID   int64
}

func (s *Store) ListTasksDueForArchive(ctx context.Context, tx *sql.Tx, limit int) ([]TaskDueForArchive, error) {
	rows, err := s.q(tx).ListTasksDueForArchive(ctx, int32(limit))
	if err != nil {
		return nil, err
	}
	out := make([]TaskDueForArchive, len(rows))
	for i, row := range rows {
		out[i] = TaskDueForArchive{ID: row.ID, Version: row.Version, OrgID: row.OrgID}
	}
	return out, nil
}

func (s *Store) DeleteTask(ctx context.Context, tx *sql.Tx, id int64, orgID int64) error {
	return s.q(tx).DeleteTask(ctx, sqlc.DeleteTaskParams{
		ID:    id,
		OrgID: orgID,
	})
}

// ClearShellSession resets shell_session to empty for the task in orgID that
// currently holds session. Called when a debug-shell rendezvous tears down so a
// later restart of that task is a normal agent run, not another shell. A no-op
// if no task holds the session.
func (s *Store) ClearShellSession(ctx context.Context, tx *sql.Tx, session string, orgID int64) error {
	return s.q(tx).ClearShellSession(ctx, sqlc.ClearShellSessionParams{
		ShellSession: session,
		OrgID:        orgID,
	})
}

func toModelTask(row sqlc.Task) (*model.Task, error) {
	return &model.Task{
		ID:           row.ID,
		Name:         row.Name,
		Runner:       row.Runner,
		Workspace:    row.Workspace,
		Status:       model.TaskStatus(row.Status),
		Command:      model.TaskCommand(row.Command),
		Version:      row.Version,
		OrgID:        row.OrgID,
		Archived:     row.Archived,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
		AutoArchive:  time.Duration(row.AutoArchive) * time.Microsecond,
		ShellSession: row.ShellSession,
		Namespace:    row.Namespace,
	}, nil
}

func toModelTasks(rows []sqlc.Task) ([]*model.Task, error) {
	tasks := make([]*model.Task, len(rows))
	for i, row := range rows {
		task, err := toModelTask(row)
		if err != nil {
			return nil, err
		}
		tasks[i] = task
	}
	return tasks, nil
}

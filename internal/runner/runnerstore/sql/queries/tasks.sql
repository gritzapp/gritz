-- name: UpsertTask :one
-- Fields the page didn't touch are passed as NULL and keep their stored value.
INSERT INTO tasks (id, command, version, workspace, command_event_id, archived)
VALUES (sqlc.arg(id), sqlc.narg(command), sqlc.narg(version), sqlc.narg(workspace), sqlc.narg(command_event_id), sqlc.narg(archived))
ON CONFLICT (id) DO UPDATE SET
    command   = coalesce(excluded.command, command),
    version   = coalesce(excluded.version, version),
    workspace = coalesce(excluded.workspace, workspace),
    command_event_id = coalesce(excluded.command_event_id, command_event_id),
    archived  = coalesce(excluded.archived, archived)
RETURNING id, command, version, workspace, command_event_id, archived;

-- name: ListUnarchivedTasks :many
SELECT id, command, version, workspace, command_event_id, archived FROM tasks WHERE archived IS NOT 1 ORDER BY id;

-- name: GetTaskHandled :one
SELECT handled FROM tasks WHERE id = ?;

-- name: SetTaskHandled :exec
UPDATE tasks SET handled = sqlc.arg(handled) WHERE id = sqlc.arg(id);

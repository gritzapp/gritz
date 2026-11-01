-- name: UpsertTask :one
-- Fields the page didn't touch are passed as NULL and keep their stored value.
INSERT INTO tasks (id, command, version, workspace, archived)
VALUES (sqlc.arg(id), sqlc.narg(command), sqlc.narg(version), sqlc.narg(workspace), sqlc.narg(archived))
ON CONFLICT (id) DO UPDATE SET
    command   = coalesce(excluded.command, command),
    version   = coalesce(excluded.version, version),
    workspace = coalesce(excluded.workspace, workspace),
    archived  = coalesce(excluded.archived, archived)
RETURNING id, command, version, workspace, archived;

-- name: ListUnarchivedTasks :many
SELECT id, command, version, workspace, archived FROM tasks WHERE archived IS NOT 1 ORDER BY id;

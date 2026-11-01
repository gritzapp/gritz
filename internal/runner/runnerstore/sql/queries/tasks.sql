-- name: UpsertTask :one
-- Fields the page didn't touch are passed as NULL and keep their stored value.
INSERT INTO tasks (id, command, version, archived)
VALUES (sqlc.arg(id), sqlc.narg(command), sqlc.narg(version), sqlc.narg(archived))
ON CONFLICT (id) DO UPDATE SET
    command  = coalesce(excluded.command, command),
    version  = coalesce(excluded.version, version),
    archived = coalesce(excluded.archived, archived)
RETURNING id, command, version, archived;

-- name: ListUnarchivedTasks :many
SELECT id, command, version, archived FROM tasks WHERE archived IS NOT 1 ORDER BY id;

-- name: UpsertTask :one
-- Fields the page didn't touch are passed as NULL and keep their stored value.
-- The update reuses ?4 for archived: sqlc doesn't rewrite sqlc.narg inside
-- DO UPDATE SET.
INSERT INTO tasks (id, command, version, archived)
VALUES (
    sqlc.arg(id),
    sqlc.narg(command),
    sqlc.narg(version),
    coalesce(CAST(sqlc.narg(archived) AS BOOLEAN), 0)
)
ON CONFLICT (id) DO UPDATE SET
    command  = coalesce(excluded.command, command),
    version  = coalesce(excluded.version, version),
    archived = coalesce(?4, archived)
RETURNING id, command, version, archived;

-- name: ListUnarchivedTaskIDs :many
SELECT id FROM tasks WHERE NOT archived ORDER BY id;

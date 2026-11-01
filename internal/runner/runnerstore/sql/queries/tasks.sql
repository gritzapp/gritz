-- name: GetTaskArchived :one
SELECT archived FROM tasks WHERE id = ?;

-- name: SetTaskArchived :exec
INSERT INTO tasks (id, archived) VALUES (?, ?)
ON CONFLICT (id) DO UPDATE SET archived = excluded.archived;

-- name: ListUnarchivedTaskIDs :many
SELECT id FROM tasks WHERE NOT archived ORDER BY id;

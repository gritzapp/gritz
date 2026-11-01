-- name: ListTaskArchived :many
SELECT id, archived FROM tasks WHERE id IN (sqlc.slice(ids));

-- name: SetTaskArchived :exec
INSERT INTO tasks (id, archived) VALUES (?, ?)
ON CONFLICT (id) DO UPDATE SET archived = excluded.archived;

-- name: ListUnarchivedTaskIDs :many
SELECT id FROM tasks WHERE NOT archived ORDER BY id;

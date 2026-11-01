-- name: SetTaskArchived :exec
INSERT INTO tasks (id, archived) VALUES (?, ?)
ON CONFLICT (id) DO UPDATE SET archived = excluded.archived;

-- name: InsertTaskIfNew :many
INSERT INTO tasks (id, archived) VALUES (?, 0)
ON CONFLICT (id) DO NOTHING
RETURNING id;

-- name: ListUnarchivedTaskIDs :many
SELECT id FROM tasks WHERE NOT archived ORDER BY id;

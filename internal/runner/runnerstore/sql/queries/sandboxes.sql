-- name: UpsertSandbox :exec
INSERT INTO sandboxes (task_id, version, type, id, data) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (task_id) DO UPDATE SET
    version = excluded.version,
    type    = excluded.type,
    id      = excluded.id,
    data    = excluded.data;

-- name: GetSandbox :one
SELECT task_id, version, type, id, data FROM sandboxes WHERE task_id = ?;

-- name: DeleteSandbox :exec
DELETE FROM sandboxes WHERE task_id = ?;

-- name: ListSandboxes :many
SELECT task_id, version, type, id, data FROM sandboxes ORDER BY task_id;

-- name: InsertEvent :exec
INSERT INTO events (id, task_id, type, event) VALUES (?, ?, ?, ?);

-- name: GetEventSeq :one
SELECT seq FROM events WHERE id = ?;

-- name: ListTaskEventsAfter :many
SELECT event FROM events
WHERE task_id = ? AND seq > ?
ORDER BY seq;

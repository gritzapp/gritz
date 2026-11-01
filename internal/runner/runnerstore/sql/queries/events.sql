-- name: InsertEvent :exec
INSERT INTO events (id, task_id, event) VALUES (?, ?, ?);

-- name: ListTaskEventsAfter :many
SELECT e.event FROM events e
WHERE e.task_id = sqlc.arg(task_id)
  AND (CAST(sqlc.arg(after_id) AS INTEGER) = 0
       OR e.seq > (SELECT a.seq FROM events a WHERE a.id = sqlc.arg(after_id)))
ORDER BY e.seq;

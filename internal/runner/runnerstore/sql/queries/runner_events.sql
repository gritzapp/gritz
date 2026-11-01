-- name: InsertRunnerEvent :exec
INSERT INTO runner_events (task_id, event, version, reason) VALUES (?, ?, ?, ?);

-- name: GetOldestRunnerEvent :one
SELECT seq, task_id, event, version, reason FROM runner_events
WHERE dead = 0 ORDER BY seq LIMIT 1;

-- name: DeleteRunnerEvent :exec
DELETE FROM runner_events WHERE seq = ?;

-- name: MarkRunnerEventDead :exec
UPDATE runner_events SET dead = 1 WHERE seq = ?;

-- name: CountRunnerEvents :one
SELECT COUNT(*) FROM runner_events WHERE dead = 0;

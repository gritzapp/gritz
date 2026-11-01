-- name: CreateLogChunk :one
-- Inserts one log chunk. One chunk per statement is what makes id order write
-- order: the driver ships with a single in-flight sender, so the bigserial is
-- assigned in the order the bytes were written, by construction rather than by
-- any assumption about how a multi-row insert orders its rows.
INSERT INTO log_chunks (org_id, task_id, version, data, created_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING id;

-- name: ListLogChunksByTaskDesc :many
-- Newest-first slice: the newest page (no cursor) and scroll-back (id < cursor).
-- Backs pagination.History's Older walk, which reverses pages to ascending.
-- Covered by idx_log_chunks_task_id_id (no new migration).
SELECT id, org_id, task_id, version, data, created_at
FROM log_chunks
WHERE task_id = sqlc.arg(task_id)
  AND org_id = sqlc.arg(org_id)
  AND (NOT sqlc.arg(use_cursor)::bool OR id < sqlc.arg(cursor_id)::bigint)
ORDER BY id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListLogChunksByTaskAsc :many
-- Live-follow slice (id > cursor), ascending. Backs pagination.History's Newer
-- walk; a cursor of 0 follows from the start. Covered by
-- idx_log_chunks_task_id_id (no new migration).
SELECT id, org_id, task_id, version, data, created_at
FROM log_chunks
WHERE task_id = sqlc.arg(task_id)
  AND org_id = sqlc.arg(org_id)
  AND id > sqlc.arg(cursor_id)::bigint
ORDER BY id ASC
LIMIT sqlc.arg(page_limit);

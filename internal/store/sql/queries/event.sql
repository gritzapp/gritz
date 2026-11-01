-- name: CreateEvent :one
INSERT INTO events (task_id, org_id, type, wake, payload, created_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, xid;

-- name: GetEvent :one
SELECT id, org_id, created_at, task_id, type, wake, payload, xid
FROM events
WHERE id = $1 AND org_id = $2;

-- name: ListEvents :many
-- The org event feed. The optional types filter ($3) narrows to specific arms —
-- an empty/nil array matches all types — mirroring ListEventsByTask. The org
-- feed is external-only, so its handler passes types = ['external'] to filter
-- out non-external arms (instruction/link/...) that also carry an org_id but are
-- not org-feed rows.
SELECT id, org_id, created_at, task_id, type, wake, payload, xid
FROM events
WHERE org_id = $1
  AND (cardinality(sqlc.arg(types)::text[]) = 0 OR type = ANY(sqlc.arg(types)::text[]))
ORDER BY id DESC
LIMIT $2;

-- name: DeleteEvent :exec
DELETE FROM events WHERE id = $1 AND org_id = $2;

-- name: ListEventsByTask :many
-- A task's events in chronological stream order (ORDER BY id). The optional
-- types filter ($3) narrows to specific arms — an empty/nil array matches all
-- types — so the same query serves both the full timeline and the agent's brief
-- (instruction + external; see proposals/draft/unified-task-event-stream.md
-- "The agent's brief").
SELECT id, org_id, created_at, task_id, type, wake, payload, xid
FROM events
WHERE task_id = $1 AND org_id = $2
  AND (cardinality(sqlc.arg(types)::text[]) = 0 OR type = ANY(sqlc.arg(types)::text[]))
ORDER BY id;

-- name: ListEventsByTaskDesc :many
-- Newest-first slice: the newest page (no cursor) and scroll-back (id < cursor).
-- Backs the pagination-forward (primary) walk; List reverses to ascending for
-- display. The optional types filter matches ListEventsByTask; covered by
-- idx_events_task_id_id (no new migration).
SELECT id, org_id, created_at, task_id, type, wake, payload, xid
FROM events
WHERE task_id = sqlc.arg(task_id)
  AND org_id = sqlc.arg(org_id)
  AND (cardinality(sqlc.arg(types)::text[]) = 0 OR type = ANY(sqlc.arg(types)::text[]))
  AND (NOT sqlc.arg(use_cursor)::bool OR id < sqlc.arg(cursor_id)::bigint)
ORDER BY id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListEventsByTaskAsc :many
-- Live-follow slice (id > cursor), ascending. Backs the pagination-backward
-- walk. The optional types filter matches ListEventsByTask; covered by
-- idx_events_task_id_id (no new migration).
SELECT id, org_id, created_at, task_id, type, wake, payload, xid
FROM events
WHERE task_id = sqlc.arg(task_id)
  AND org_id = sqlc.arg(org_id)
  AND (cardinality(sqlc.arg(types)::text[]) = 0 OR type = ANY(sqlc.arg(types)::text[]))
  AND id > sqlc.arg(cursor_id)::bigint
ORDER BY id ASC
LIMIT sqlc.arg(page_limit);

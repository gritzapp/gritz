-- name: CreateLink :one
INSERT INTO task_links (task_id, relevance, url, title, kind, claim, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id;

-- name: ListLinksByTask :many
SELECT l.id, l.task_id, l.relevance, l.url, l.title, l.created_at, l.kind, l.claim
FROM task_links l
JOIN tasks t ON l.task_id = t.id
WHERE l.task_id = $1 AND t.org_id = $2
ORDER BY l.created_at ASC;

-- name: DeleteLink :exec
DELETE FROM task_links WHERE id = $1;

-- name: FindSubscribedLinks :many
SELECT l.id, l.task_id, l.relevance, l.url, l.title, l.created_at, l.kind, l.claim, t.org_id, t.agent_id
FROM task_links l
JOIN tasks t ON l.task_id = t.id
WHERE l.url = sqlc.arg(url) AND l.kind <> '' AND t.archived = FALSE
  AND t.org_id = ANY(sqlc.arg(org_ids)::BIGINT[])
ORDER BY t.org_id, l.task_id, l.id;

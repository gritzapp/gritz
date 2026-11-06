-- name: CreateKey :exec
INSERT INTO keys (id, name, token_hash, expires_at, created_at, org_id, scopes)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetKey :one
SELECT id, name, token_hash, org_id, expires_at, created_at, scopes
FROM keys
WHERE id = $1 AND org_id = $2;

-- name: GetKeyByHash :one
-- A key of an archived org matches nothing, so it fails authentication.
SELECT k.id, k.name, k.token_hash, k.org_id, k.expires_at, k.created_at, k.scopes
FROM keys k
JOIN orgs o ON o.id = k.org_id
WHERE k.token_hash = $1 AND o.archived = FALSE;

-- name: ListKeys :many
SELECT id, name, token_hash, org_id, expires_at, created_at, scopes
FROM keys
WHERE org_id = $1
ORDER BY created_at DESC;

-- name: DeleteKey :exec
DELETE FROM keys WHERE id = $1 AND org_id = $2;

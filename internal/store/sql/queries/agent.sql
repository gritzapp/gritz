-- name: CreateAgent :one
INSERT INTO agents (org_id, name, workspace, runner, prompt, auto_archive, variables, triggers, links)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id;

-- name: GetAgent :one
SELECT id, org_id, name, workspace, runner, prompt, auto_archive, variables, triggers, links, created_at, updated_at
FROM agents
WHERE org_id = $1 AND id = $2;

-- name: ListAgentsForOrgs :many
SELECT id, org_id, name, workspace, runner, prompt, auto_archive, variables, triggers, links, created_at, updated_at
FROM agents
WHERE org_id = ANY(sqlc.arg(org_ids)::BIGINT[])
ORDER BY org_id, name, id;

-- name: UpdateAgent :execrows
UPDATE agents
SET name = $1, workspace = $2, runner = $3, prompt = $4, auto_archive = $5,
    variables = $6, triggers = $7, links = $8, updated_at = now()
WHERE id = $9 AND org_id = $10;

-- name: DeleteAgent :execrows
DELETE FROM agents WHERE id = $1 AND org_id = $2;

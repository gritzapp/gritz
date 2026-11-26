-- name: CreateAgent :one
INSERT INTO agents (org_id, name, workspace, runner, prompt, auto_archive, variables, triggers, links)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id;

-- name: GetAgent :one
SELECT id, org_id, name, workspace, runner, prompt, auto_archive, variables, triggers, links, created_at, updated_at, deleted_at
FROM agents
WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL;

-- name: LockLiveAgent :one
-- Share-locks a live agent until the transaction ends, so DeleteAgent (which
-- locks it for update) can't delete it while a task of it is created or
-- unarchived.
SELECT id FROM agents
WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL
FOR SHARE;

-- name: ListAgentsForOrgs :many
SELECT id, org_id, name, workspace, runner, prompt, auto_archive, variables, triggers, links, created_at, updated_at, deleted_at
FROM agents
WHERE org_id = ANY(sqlc.arg(org_ids)::BIGINT[]) AND deleted_at IS NULL
ORDER BY org_id, name, id;

-- name: UpdateAgent :execrows
UPDATE agents
SET name = $1, workspace = $2, runner = $3, prompt = $4, auto_archive = $5,
    variables = $6, triggers = $7, links = $8, updated_at = now()
WHERE id = $9 AND org_id = $10 AND deleted_at IS NULL;

-- name: LockAgentForDelete :one
SELECT id FROM agents
WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL
FOR UPDATE;

-- name: DeleteAgent :exec
-- Soft-deletes the agent: archived tasks keep referencing its row.
UPDATE agents SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND org_id = $2;

-- name: AgentInUse :one
-- Whether an unarchived task or a schedule references the agent.
SELECT (EXISTS (
    SELECT 1 FROM tasks t
    WHERE t.agent_id = sqlc.arg(agent_id) AND t.org_id = sqlc.arg(org_id) AND t.archived = FALSE
) OR EXISTS (
    SELECT 1 FROM schedules s
    WHERE s.agent_id = sqlc.arg(agent_id) AND s.org_id = sqlc.arg(org_id)
))::boolean AS in_use;

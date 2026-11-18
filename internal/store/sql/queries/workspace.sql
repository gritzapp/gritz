-- name: CreateWorkspace :exec
INSERT INTO workspaces (runner_id, name, description, org_id, variables)
VALUES ($1, $2, $3, $4, $5);

-- name: ListWorkspacesByOrgID :many
SELECT id, runner_id, name, description, updated_at, org_id, variables
FROM workspaces
WHERE org_id = $1
ORDER BY name ASC;

-- name: DeleteWorkspacesByRunner :exec
DELETE FROM workspaces
WHERE runner_id = $1 AND org_id = $2;

-- name: HasWorkspace :one
SELECT EXISTS(
    SELECT 1 FROM workspaces
    WHERE runner_id = $1 AND name = $2 AND org_id = $3
);

-- name: ClearWorkspacesByOrgID :exec
DELETE FROM workspaces
WHERE org_id = $1;

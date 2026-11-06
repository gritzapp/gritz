-- name: CreateOrg :one
INSERT INTO orgs (name, owner, created_at, updated_at)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: GetOrg :one
SELECT id, name, owner, created_at, updated_at, archived, github_installation_id, atlassian_webhook_secret
FROM orgs
WHERE id = $1;

-- name: ListOrgsByMember :many
SELECT o.id, o.name, o.owner, o.created_at, o.updated_at, o.archived, o.github_installation_id
FROM orgs o
JOIN org_members om ON o.id = om.org_id
WHERE om.user_id = $1 AND o.archived = FALSE
ORDER BY o.name;

-- name: UpdateOrg :exec
UPDATE orgs SET
    name = $2,
    updated_at = $3
WHERE id = $1;

-- name: ArchiveOrg :exec
UPDATE orgs SET archived = TRUE, updated_at = CURRENT_TIMESTAMP WHERE id = $1;

-- name: AddOrgMember :exec
INSERT INTO org_members (org_id, user_id, role, created_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (org_id, user_id) DO NOTHING;

-- name: RemoveOrgMember :exec
DELETE FROM org_members
WHERE org_id = $1 AND user_id = $2;

-- name: ListOrgMembers :many
SELECT om.org_id, om.user_id, om.role, om.created_at
FROM org_members om
WHERE om.org_id = $1
ORDER BY om.created_at;

-- name: ListOrgMembersWithUsers :many
SELECT om.org_id, om.user_id, om.role, om.created_at, u.email, u.name
FROM org_members om
JOIN users u ON om.user_id = u.id
WHERE om.org_id = $1
ORDER BY om.created_at;

-- name: GetOrgAtlassianWebhookSecret :one
SELECT atlassian_webhook_secret FROM orgs WHERE id = $1 AND archived = FALSE;

-- name: SetOrgAtlassianWebhookSecret :exec
UPDATE orgs SET atlassian_webhook_secret = $2 WHERE id = $1;

-- name: SetOrgGitHubInstallation :exec
UPDATE orgs SET
    github_installation_id = sqlc.arg(github_installation_id)::BIGINT,
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: ClearGitHubInstallation :exec
UPDATE orgs SET
    github_installation_id = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE github_installation_id = sqlc.arg(github_installation_id)::BIGINT;

-- name: ListOrgIDsByGitHubInstallation :many
-- Returns the ids of the non-archived orgs that share the given GitHub App
-- installation. Used to resolve a webhook's installation to the orgs an event
-- belongs to, independent of the actor's membership.
SELECT id FROM orgs
WHERE github_installation_id = sqlc.arg(github_installation_id)::BIGINT
  AND archived = FALSE;

-- name: DestroyOrg :exec
DELETE FROM orgs WHERE id = $1;

-- name: IsOrgMember :one
SELECT EXISTS(
    SELECT 1 FROM org_members om
    JOIN orgs o ON o.id = om.org_id
    WHERE om.org_id = $1 AND om.user_id = $2 AND o.archived = FALSE
) AS is_member;

-- name: GetOrgRoutingRules :one
SELECT routing_rules FROM orgs WHERE id = $1;

-- name: SetOrgRoutingRules :exec
UPDATE orgs SET
    routing_rules = $2,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: GetRoutingRulesByOrgs :many
SELECT id, routing_rules FROM orgs WHERE id = ANY($1::BIGINT[]);

-- name: ListRoutingRulesForEvent :many
-- Member orgs (joined via org_members) are returned with is_member = TRUE and
-- all their rules. The orgs in org_ids (the event's orgs) that the actor is NOT
-- a member of are returned with is_member = FALSE; the caller keeps only their
-- public rules. Membership wins on overlap (the NOT EXISTS drops a passed org
-- the actor already belongs to). An empty org_ids reduces this to the
-- member-only behavior; an empty user_id yields just the non-member branch.
SELECT o.id, o.routing_rules, TRUE::boolean AS is_member
FROM orgs o
JOIN org_members m ON m.org_id = o.id
WHERE m.user_id = sqlc.arg(user_id) AND o.archived = FALSE
UNION
SELECT o.id, o.routing_rules, FALSE::boolean AS is_member
FROM orgs o
WHERE o.id = ANY(sqlc.arg(org_ids)::BIGINT[])
  AND o.archived = FALSE
  AND NOT EXISTS (
      SELECT 1 FROM org_members m
      WHERE m.org_id = o.id AND m.user_id = sqlc.arg(user_id)
  );


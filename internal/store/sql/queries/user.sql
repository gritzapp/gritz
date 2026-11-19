-- name: UpsertUser :one
INSERT INTO users (id, email, name)
VALUES ($1, $2, $3)
ON CONFLICT (id) DO UPDATE SET
    email = EXCLUDED.email,
    name = EXCLUDED.name,
    updated_at = CURRENT_TIMESTAMP
RETURNING id, email, name, github_user_id, github_username, atlassian_account_id, atlassian_username, default_org_id, created_at, updated_at;

-- name: CreateUser :one
INSERT INTO users (id, email, name, github_user_id, github_username, default_org_id)
VALUES ($1, $2, $3, sqlc.narg('github_user_id'), sqlc.narg('github_username'), sqlc.narg('default_org_id'))
RETURNING id, email, name, github_user_id, github_username, atlassian_account_id, atlassian_username, default_org_id, created_at, updated_at;

-- name: GetUser :one
SELECT id, email, name, github_user_id, github_username, atlassian_account_id, atlassian_username, default_org_id, created_at, updated_at
FROM users
WHERE id = $1;

-- name: GetUserByEmail :one
SELECT id, email, name, github_user_id, github_username, atlassian_account_id, atlassian_username, default_org_id, created_at, updated_at
FROM users
WHERE email = $1;

-- name: GetUserByGitHubUserID :one
SELECT id, email, name, github_user_id, github_username, atlassian_account_id, atlassian_username, default_org_id, created_at, updated_at
FROM users
WHERE github_user_id = $1;

-- name: GetUserByAtlassianAccountID :one
SELECT id, email, name, github_user_id, github_username, atlassian_account_id, atlassian_username, default_org_id, created_at, updated_at
FROM users
WHERE atlassian_account_id = $1;

-- name: LinkGitHubAccount :exec
UPDATE users SET
    github_user_id = $2,
    github_username = $3,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: UnlinkGitHubAccount :exec
UPDATE users SET
    github_user_id = NULL,
    github_username = '',
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: UpdateGitHubUsername :exec
UPDATE users SET
    github_username = $1,
    updated_at = CURRENT_TIMESTAMP
WHERE github_user_id = $2;

-- name: UpdateDefaultOrgID :exec
UPDATE users SET
    default_org_id = $2,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: LinkAtlassianAccount :exec
UPDATE users SET
    atlassian_account_id = $2,
    atlassian_username = $3,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: UnlinkAtlassianAccount :exec
UPDATE users SET
    atlassian_account_id = NULL,
    atlassian_username = '',
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: CreateSchedule :one
INSERT INTO schedules (org_id, created_by, name, agent_id, instructions, cron_expr, timezone, enabled, next_run_at, last_run_at, last_task_id, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING id;

-- name: GetSchedule :one
SELECT id, org_id, created_by, name, instructions, cron_expr, timezone, enabled, next_run_at, last_run_at, last_task_id, created_at, updated_at, agent_id
FROM schedules
WHERE id = $1 AND org_id = $2;

-- name: GetScheduleForUpdate :one
SELECT id, org_id, created_by, name, instructions, cron_expr, timezone, enabled, next_run_at, last_run_at, last_task_id, created_at, updated_at, agent_id
FROM schedules
WHERE id = $1 AND org_id = $2
FOR UPDATE;

-- name: ListSchedules :many
SELECT id, org_id, created_by, name, instructions, cron_expr, timezone, enabled, next_run_at, last_run_at, last_task_id, created_at, updated_at, agent_id
FROM schedules
WHERE org_id = $1
ORDER BY created_at DESC;

-- name: UpdateSchedule :exec
UPDATE schedules
SET name = $1, agent_id = $2, instructions = $3, cron_expr = $4, timezone = $5, enabled = $6, next_run_at = $7, last_run_at = $8, last_task_id = $9, updated_at = $10
WHERE id = $11 AND org_id = $12;

-- name: DeleteSchedule :exec
DELETE FROM schedules WHERE id = $1 AND org_id = $2;

-- name: DisableSchedules :exec
UPDATE schedules
SET enabled = FALSE, next_run_at = NULL, updated_at = (NOW() AT TIME ZONE 'UTC')
WHERE org_id = $1;

-- name: ClaimDueSchedules :many
-- Lock and return up to $1 due, enabled schedules, skipping any row another server
-- instance already holds. FOR UPDATE SKIP LOCKED makes the claim atomic across instances:
-- two schedulers ticking at the same instant partition the due set instead of both firing
-- the same schedule. The locks are held until the caller's transaction commits, by which
-- point next_run_at has been advanced past now, so the row is no longer due.
SELECT id, org_id, created_by, name, instructions,
       cron_expr, timezone, enabled,
       next_run_at, last_run_at, last_task_id, created_at, updated_at, agent_id
FROM schedules
WHERE enabled = TRUE
  AND next_run_at IS NOT NULL
  AND next_run_at <= (NOW() AT TIME ZONE 'UTC')
ORDER BY next_run_at
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: AdvanceSchedule :exec
UPDATE schedules
SET next_run_at = $1, last_run_at = $2, last_task_id = $3,
    updated_at = (NOW() AT TIME ZONE 'UTC')
WHERE id = $4 AND org_id = $5;

-- name: CreateSchedule :one
INSERT INTO schedules (org_id, created_by, name, workspace, runner, namespace, instructions, auto_archive, cron_expr, timezone, enabled, next_run_at, last_run_at, last_task_id, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
RETURNING id;

-- name: GetSchedule :one
SELECT id, org_id, created_by, name, workspace, runner, namespace, instructions, auto_archive, cron_expr, timezone, enabled, next_run_at, last_run_at, last_task_id, created_at, updated_at
FROM schedules
WHERE id = $1 AND org_id = $2;

-- name: GetScheduleForUpdate :one
SELECT id, org_id, created_by, name, workspace, runner, namespace, instructions, auto_archive, cron_expr, timezone, enabled, next_run_at, last_run_at, last_task_id, created_at, updated_at
FROM schedules
WHERE id = $1 AND org_id = $2
FOR UPDATE;

-- name: ListSchedules :many
SELECT id, org_id, created_by, name, workspace, runner, namespace, instructions, auto_archive, cron_expr, timezone, enabled, next_run_at, last_run_at, last_task_id, created_at, updated_at
FROM schedules
WHERE org_id = $1
ORDER BY created_at DESC;

-- name: UpdateSchedule :exec
UPDATE schedules
SET name = $1, workspace = $2, runner = $3, namespace = $4, instructions = $5, auto_archive = $6, cron_expr = $7, timezone = $8, enabled = $9, next_run_at = $10, last_run_at = $11, last_task_id = $12, updated_at = $13
WHERE id = $14 AND org_id = $15;

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
SELECT id, org_id, created_by, name, workspace, runner, namespace, instructions,
       auto_archive, cron_expr, timezone, enabled,
       next_run_at, last_run_at, last_task_id, created_at, updated_at
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

-- name: CreateSchedule :one
-- A new schedule goes last in its agent.
INSERT INTO schedules (org_id, name, agent_id, position, cron_expr, timezone, enabled, next_run_at, last_run_at, last_task_id)
VALUES (@org_id, @name, @agent_id,
        (SELECT COALESCE(MAX(position) + 1, 0) FROM schedules WHERE agent_id = @agent_id),
        @cron_expr, @timezone, @enabled, @next_run_at, @last_run_at, @last_task_id)
RETURNING id;

-- name: GetSchedule :one
SELECT id, org_id, name, cron_expr, timezone, enabled, next_run_at, last_run_at, last_task_id, agent_id, position
FROM schedules
WHERE id = $1 AND org_id = $2;

-- name: GetScheduleForUpdate :one
SELECT id, org_id, name, cron_expr, timezone, enabled, next_run_at, last_run_at, last_task_id, agent_id, position
FROM schedules
WHERE id = $1 AND org_id = $2
FOR UPDATE;

-- name: ListSchedules :many
SELECT id, org_id, name, cron_expr, timezone, enabled, next_run_at, last_run_at, last_task_id, agent_id, position
FROM schedules
WHERE org_id = $1
ORDER BY id DESC;

-- name: UpdateSchedule :exec
-- A schedule moved to another agent goes last in it.
UPDATE schedules u
SET name = @name,
    position = CASE WHEN u.agent_id = @agent_id THEN u.position
                    ELSE (SELECT COALESCE(MAX(s.position) + 1, 0) FROM schedules s WHERE s.agent_id = @agent_id) END,
    agent_id = @agent_id, cron_expr = @cron_expr, timezone = @timezone, enabled = @enabled,
    next_run_at = @next_run_at, last_run_at = @last_run_at, last_task_id = @last_task_id
WHERE u.id = @id AND u.org_id = @org_id;

-- name: DeleteSchedule :exec
DELETE FROM schedules WHERE id = $1 AND org_id = $2;

-- name: DisableSchedules :exec
UPDATE schedules
SET enabled = FALSE, next_run_at = NULL
WHERE org_id = $1;

-- name: ClaimDueSchedules :many
-- Lock and return up to $1 due, enabled schedules, skipping any row another server
-- instance already holds. FOR UPDATE SKIP LOCKED makes the claim atomic across instances:
-- two schedulers ticking at the same instant partition the due set instead of both firing
-- the same schedule. The locks are held until the caller's transaction commits, by which
-- point next_run_at has been advanced past now, so the row is no longer due.
SELECT id, org_id, name,
       cron_expr, timezone, enabled,
       next_run_at, last_run_at, last_task_id, agent_id, position
FROM schedules
WHERE enabled = TRUE
  AND next_run_at IS NOT NULL
  AND next_run_at <= (NOW() AT TIME ZONE 'UTC')
ORDER BY next_run_at
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: AdvanceSchedule :exec
UPDATE schedules
SET next_run_at = $1, last_run_at = $2, last_task_id = $3
WHERE id = $4 AND org_id = $5;

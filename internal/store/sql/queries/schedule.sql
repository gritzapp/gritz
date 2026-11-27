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
SET enabled = @enabled, next_run_at = @next_run_at, last_run_at = @last_run_at, last_task_id = @last_task_id
WHERE id = @id AND org_id = @org_id;

-- name: ListAgentSchedules :many
SELECT id, org_id, name, cron_expr, timezone, enabled, next_run_at, last_run_at, last_task_id, agent_id, position
FROM schedules
WHERE org_id = $1 AND agent_id = $2
ORDER BY position, id;

-- name: LockAgentSchedules :many
-- Locks the agent's schedules until the transaction ends. Saving or deleting
-- an agent takes these locks before the agent's, in the scheduler's order.
SELECT id, org_id, name, cron_expr, timezone, enabled, next_run_at, last_run_at, last_task_id, agent_id, position
FROM schedules
WHERE org_id = $1 AND agent_id = $2
FOR UPDATE;

-- name: ListOrgAgentSchedules :many
SELECT id, org_id, name, cron_expr, timezone, enabled, next_run_at, last_run_at, last_task_id, agent_id, position
FROM schedules
WHERE org_id = $1
ORDER BY agent_id, position, id;

-- name: InsertAgentSchedule :one
INSERT INTO schedules (org_id, name, agent_id, position, cron_expr, timezone, enabled, next_run_at, last_run_at, last_task_id)
VALUES (@org_id, @name, @agent_id, @position, @cron_expr, @timezone, @enabled, @next_run_at, @last_run_at, @last_task_id)
RETURNING id;

-- name: UpdateAgentSchedule :execrows
-- Matches the schedule by its agent too, so a save can't take over another
-- agent's schedule.
UPDATE schedules
SET name = @name, position = @position, cron_expr = @cron_expr, timezone = @timezone, enabled = @enabled,
    next_run_at = @next_run_at, last_run_at = @last_run_at, last_task_id = @last_task_id
WHERE id = @id AND agent_id = @agent_id AND org_id = @org_id;

-- name: DeleteAgentSchedulesExcept :exec
DELETE FROM schedules
WHERE org_id = @org_id AND agent_id = @agent_id AND NOT (id = ANY(@keep_ids::BIGINT[]));

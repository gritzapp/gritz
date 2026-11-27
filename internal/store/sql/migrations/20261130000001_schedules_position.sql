-- migrate:up

-- A schedule's tasks start with its agent's prompt, and a schedule is ordered
-- within its agent. See proposals/draft/agent-schedules.md.
ALTER TABLE schedules ADD COLUMN position integer NOT NULL DEFAULT 0;

-- Each agent's schedules keep the order the Schedules page listed them in
-- (newest first).
UPDATE schedules s
SET position = o.position
FROM (
    SELECT id, row_number() OVER (PARTITION BY agent_id ORDER BY created_at DESC, id DESC) - 1 AS position
    FROM schedules
) o
WHERE o.id = s.id;

ALTER TABLE schedules DROP COLUMN instructions,
                      DROP COLUMN created_by,
                      DROP COLUMN created_at,
                      DROP COLUMN updated_at;

DROP INDEX idx_schedules_agent_id;
CREATE INDEX idx_schedules_agent_position ON schedules (agent_id, position);

-- migrate:down

DROP INDEX idx_schedules_agent_position;
CREATE INDEX idx_schedules_agent_id ON schedules (agent_id);

ALTER TABLE schedules ADD COLUMN instructions jsonb NOT NULL DEFAULT '[]',
                      ADD COLUMN created_by text REFERENCES users(id),
                      ADD COLUMN created_at timestamp NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC'),
                      ADD COLUMN updated_at timestamp NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC');

-- The attribution is gone; the org's owner stands in for it.
UPDATE schedules s SET created_by = o.owner FROM orgs o WHERE o.id = s.org_id;
ALTER TABLE schedules ALTER COLUMN created_by SET NOT NULL;

ALTER TABLE schedules DROP COLUMN position;

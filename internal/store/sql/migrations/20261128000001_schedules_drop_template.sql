-- migrate:up

-- A schedule's tasks take their template from its agent. See
-- proposals/draft/agents.md.
ALTER TABLE schedules DROP COLUMN workspace, DROP COLUMN runner,
                      DROP COLUMN auto_archive, DROP COLUMN variables;

-- migrate:down

ALTER TABLE schedules ADD COLUMN workspace text NOT NULL DEFAULT '',
                      ADD COLUMN runner text NOT NULL DEFAULT '',
                      ADD COLUMN auto_archive bigint NOT NULL DEFAULT 0,
                      ADD COLUMN variables jsonb NOT NULL DEFAULT '{}';

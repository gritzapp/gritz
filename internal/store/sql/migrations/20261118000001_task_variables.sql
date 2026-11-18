-- migrate:up

-- Task variables. See proposals/draft/task-variables.md.
-- The task's resolved variables, a JSON object of name to value.
ALTER TABLE tasks ADD COLUMN variables JSONB NOT NULL DEFAULT '{}';
-- The workspace's variable declarations as registered, a JSON array.
ALTER TABLE workspaces ADD COLUMN variables JSONB NOT NULL DEFAULT '[]';
-- The schedule's variables, a JSON object of name to value.
ALTER TABLE schedules ADD COLUMN variables JSONB NOT NULL DEFAULT '{}';

-- migrate:down

ALTER TABLE schedules DROP COLUMN IF EXISTS variables;
ALTER TABLE workspaces DROP COLUMN IF EXISTS variables;
ALTER TABLE tasks DROP COLUMN IF EXISTS variables;

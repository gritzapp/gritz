-- migrate:up

-- Workspace features. See proposals/draft/workspace-features.md.
-- The task's features, a JSON array of names in the order they merge, copied
-- from its agent at creation.
ALTER TABLE tasks ADD COLUMN features jsonb NOT NULL DEFAULT '[]';

-- migrate:down

ALTER TABLE tasks DROP COLUMN IF EXISTS features;

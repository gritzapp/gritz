-- migrate:up

-- Workspace features. See proposals/draft/workspace-features.md.
-- The workspace's feature declarations as registered, a JSON array of
-- {name, description}.
ALTER TABLE workspaces ADD COLUMN features jsonb NOT NULL DEFAULT '[]';

-- migrate:down

ALTER TABLE workspaces DROP COLUMN IF EXISTS features;

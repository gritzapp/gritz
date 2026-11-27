-- migrate:up

-- Workspace features. See proposals/draft/workspace-features.md.
-- The agent's features, a JSON array of names in the order they merge.
ALTER TABLE agents ADD COLUMN features jsonb NOT NULL DEFAULT '[]';

-- migrate:down

ALTER TABLE agents DROP COLUMN IF EXISTS features;

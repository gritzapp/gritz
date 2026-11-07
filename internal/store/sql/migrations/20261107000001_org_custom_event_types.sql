-- migrate:up

-- The org's custom event type schemas, a JSON array of model.EventTypeDef.
-- See proposals/draft/custom-external-events.md.
ALTER TABLE orgs ADD COLUMN custom_event_types JSONB NOT NULL DEFAULT '[]';

-- migrate:down

ALTER TABLE orgs DROP COLUMN custom_event_types;

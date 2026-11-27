-- migrate:up

-- A schedule's prompt: static text its tasks get as an instruction after the
-- agent's prompt. Empty adds none.
ALTER TABLE schedules ADD COLUMN prompt text NOT NULL DEFAULT '';

-- migrate:down

ALTER TABLE schedules DROP COLUMN IF EXISTS prompt;

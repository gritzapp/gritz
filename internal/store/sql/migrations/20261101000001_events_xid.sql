-- migrate:up

-- The inserting transaction's 64-bit xid. Existing rows get the migration's
-- own xid, which is below every later one, so they sort first in (xid, id).
-- See proposals/draft/runner-event-replica.md.
ALTER TABLE events ADD COLUMN xid xid8 NOT NULL DEFAULT pg_current_xact_id();
CREATE INDEX idx_events_xid_id ON events (xid, id);

-- migrate:down

DROP INDEX idx_events_xid_id;
ALTER TABLE events DROP COLUMN xid;

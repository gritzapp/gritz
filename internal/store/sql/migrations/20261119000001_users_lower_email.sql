-- migrate:up

-- Email lookups are case-insensitive. Not unique: existing rows may hold
-- emails that differ only in case, and GetUsersByEmail reports those.
CREATE INDEX idx_users_lower_email ON users (lower(email));

-- migrate:down

DROP INDEX IF EXISTS idx_users_lower_email;

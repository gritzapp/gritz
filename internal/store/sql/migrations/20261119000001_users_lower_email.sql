-- migrate:up

-- Emails are stored lower-cased, so the unique index on email enforces
-- uniqueness regardless of case. Fails if two rows differ only in case.
UPDATE users SET email = lower(email);

-- migrate:down

-- The original casing is not kept, so there is nothing to undo.

CREATE TABLE tasks (
    id       INTEGER PRIMARY KEY,
    archived INTEGER NOT NULL DEFAULT 0  -- the fold of the task's archive events
);

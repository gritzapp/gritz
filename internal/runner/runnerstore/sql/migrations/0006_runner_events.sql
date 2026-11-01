CREATE TABLE runner_events (
    seq     INTEGER PRIMARY KEY AUTOINCREMENT,  -- FIFO order; never reused, even after a delete
    task_id INTEGER NOT NULL,
    event   TEXT    NOT NULL,                   -- model.RunnerEventType
    version INTEGER NOT NULL,
    reason  TEXT    NOT NULL,
    dead    INTEGER NOT NULL DEFAULT 0          -- 1 once dead-lettered
);
CREATE INDEX runner_events_pending ON runner_events (seq) WHERE dead = 0;

CREATE TABLE events (
    seq     INTEGER PRIMARY KEY,  -- local arrival order
    id      INTEGER NOT NULL UNIQUE,
    task_id INTEGER NOT NULL,
    type    TEXT    NOT NULL,
    event   BLOB    NOT NULL      -- gritz.v1.Event, protobuf-encoded
);
CREATE INDEX events_task_seq ON events (task_id, seq);

CREATE TABLE replication (
    id     INTEGER PRIMARY KEY CHECK (id = 1),
    cursor TEXT NOT NULL          -- opaque; ListRunnerEventsResponse.next_page_token
);

CREATE TABLE tasks (
    id       INTEGER PRIMARY KEY,
    archived INTEGER NOT NULL DEFAULT 0  -- the fold of the task's archive events
);

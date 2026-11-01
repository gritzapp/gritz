CREATE TABLE sandboxes (
    task_id INTEGER PRIMARY KEY,
    version INTEGER NOT NULL,  -- task.Version of the run the sandbox is hosting; expected to go once
                               -- the driver owns its runs (runner-event-replica.md slices 7–8)
    type    TEXT    NOT NULL,  -- backend handle type; informational
    id      TEXT    NOT NULL,  -- backend handle id
    data    BLOB               -- opaque backend handle data; never decoded
);

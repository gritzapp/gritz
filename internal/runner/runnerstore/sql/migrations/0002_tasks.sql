CREATE TABLE tasks (
    id       INTEGER PRIMARY KEY,
    command  INTEGER,                    -- gritz.v1.TaskCommand of the last command event
    version  INTEGER,                    -- version of the last command event
    archived INTEGER NOT NULL DEFAULT 0  -- result of the last archive event
);

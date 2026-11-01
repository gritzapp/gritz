CREATE TABLE tasks (
    id       INTEGER PRIMARY KEY,
    command  INTEGER,                    -- gritz.v1.TaskCommand of the last command event
    version  INTEGER,                    -- version of the last command event
    archived INTEGER                     -- result of the last archive event; NULL reads as not archived
);
